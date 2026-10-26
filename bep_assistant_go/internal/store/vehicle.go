// Copyright (c) 2026 alcyez <alcyez9821@gmail.com>
// SPDX-License-Identifier: MIT

// File: vehicle.go
// Purpose: vehicles 表 CRUD —— 车辆信息 + 进京证状态 + 二次办证状态。
// Author: alcyez (AI Agent 辅助生成注释)
// Created: 2026-07-06
//
// Business Context:
//   一个账号可绑定多辆车。每辆车维护三组信息：
//     1. 基础属性：hphm 车牌号、hpzl 号牌种类、ybcs/sycs/syts 额度剩余情况
//     2. 当前进京证 (last_*)：正在生效或最近一次申请的证件状态
//     3. 二次办证 (ec_*)：下一张即将生效的证件（申请后但未到起始日）
//   代办模式还额外维护 default_area / default_xxdz / default_jjmd 三个必填字段，
//   Go 端 ApplyForPermit 直接读这些字段作为提交参数。
//
// Key Logic:
//   1. vehicleCols 列名清单集中定义，避免每个 SELECT 重复拼字符串导致列漂移
//   2. scanVehicle 统一处理 NullTime / NullString，避免每个 CRUD 都写 sql.NullXxx 转换
//   3. UpsertVehicle 用"先 SELECT id 再 INSERT/UPDATE"两段式（不用 SQLite 的 INSERT ...
//      ON CONFLICT），换取更清晰的日志和新旧记录的 last_error 清零逻辑
//   4. UpdateVehicle 列名白名单严格校验，防 SQL 注入
package store

import (
	"database/sql"
	"fmt"
	"time"
)

// Vehicle 车辆模型，对应 vehicles 表一行。
//
// 命名约定：字段名保留了北京交警 API 的拼音短名（hphm / yxqz / blzt 等），与
// JSON tag 和 DB 列一致，避免多套命名之间的翻译成本。中文含义见字段行内注释。
type Vehicle struct {
	ID             int64      `json:"id"`               // 自增主键
	AccountID      int64      `json:"account_id"`       // 归属账号
	VID            string     `json:"v_id"`             // 北京交警侧的车辆 ID（API 主键）
	Hphm           string     `json:"hphm"`             // 号牌号码，例如 "京A12345"
	Hpzl           string     `json:"hpzl"`             // 号牌种类，例如 "02"
	Mode           string     `json:"mode"`             // "reminder"=提醒模式；"agent"=代办模式
	DefaultArea    string     `json:"default_area"`     // 代办模式默认目的地所在区（如"大兴区"）
	DefaultXxdz    string     `json:"default_xxdz"`     // 代办模式默认详细地址
	DefaultJjmd    string     `json:"default_jjmd"`     // 代办模式默认进京目的（如"其它"）
	Jjzzl          string     `json:"jjzzl"`            // 进京证种类：01=六环内，02=六环外
	Ybcs           int        `json:"ybcs"`             // 已办次数
	Sycs           string     `json:"sycs"`             // 剩余次数（API 返回字符串）
	Syts           string     `json:"syts"`             // 剩余天数
	LastStatus     string     `json:"last_status"`      // 当前进京证状态名称（blzt 对应的 blztmc）
	LastJjzzlmc    string     `json:"last_jjzzlmc"`     // 当前进京证种类名称
	LastYxqs       string     `json:"last_yxqs"`        // 有效期起 YYYY-MM-DD
	LastYxqz       string     `json:"last_yxqz"`        // 有效期止 YYYY-MM-DD
	LastJsrxm      string     `json:"last_jsrxm"`       // 驾驶人姓名
	LastApplyID    string     `json:"last_apply_id"`    // 申请单 ID
	LastBlzt       int        `json:"last_blzt"`        // 办理状态码（1=生效中,4=不通过,5=审核中,6=待生效）
	LastShsbyyms   string     `json:"last_shsbyyms"`    // 审核失败原因描述
	LastCheckedAt  *time.Time `json:"last_checked_at"`  // 最近一次调用 API 刷新时间
	LastError      string     `json:"last_error"`       // 最近一次错误信息（缺默认值、申请失败等）
	LastRemindedAt string     `json:"last_reminded_at"` // 老版本提醒去重标记（保留兼容，不再写入）
	// === 二次办证字段（ec_* = ecbzxx 前缀） ===
	// 二次办证 = 已有生效中的证 + 新申请了下一张证的情况；此时 last_* 是当前证，ec_* 是下一张证
	EcNotifiedAt  string    `json:"ec_notified_at"`  // 单字段去重键："" | "到期" | "过期" | "通过" | "不通过"
	EcAutoApplied int       `json:"ec_auto_applied"` // 代办已提交标记；1=已代办本次生命周期内下一张
	EcBlzt        int       `json:"ec_blzt"`         // 二次办证办理状态码
	EcYxqs        string    `json:"ec_yxqs"`         // 二次办证有效期起
	EcYxqz        string    `json:"ec_yxqz"`         // 二次办证有效期止
	EcJjzzlmc     string    `json:"ec_jjzzlmc"`      // 二次办证进京证种类名称
	EcShsbyyms    string    `json:"ec_shsbyyms"`     // 二次办证审核失败原因描述
	CreatedAt     time.Time `json:"created_at"`      // 首次插入时间
	UpdatedAt     *time.Time `json:"updated_at"`     // 最后一次 UpdateVehicle 时间
}

// vehicleCols 是所有 SELECT 共用的列清单，与 scanVehicle 顺序严格对应。
//
// 单独抽出常量避免 GetVehicle / ListVehicles / GetVehicleByVID 三处 SELECT 分别写
// 列名导致遗漏或漂移（新增字段时容易漏改一个地方）。
const vehicleCols = `id, account_id, v_id, hphm, hpzl, mode,
	default_area, default_xxdz, default_jjmd, jjzzl,
	ybcs, sycs, syts, last_status, last_jjzzlmc, last_yxqs, last_yxqz,
	last_jsrxm, last_apply_id, last_blzt, last_shsbyyms, last_checked_at, last_error, last_reminded_at,
	ec_notified_at, ec_auto_applied, ec_blzt, ec_yxqs, ec_yxqz, ec_jjzzlmc, ec_shsbyyms,
	created_at, updated_at`

// scanVehicle 把 sql.Row / sql.Rows 扫描到 Vehicle 结构。
//
// 参数用 interface{ Scan(...) error } 是为了让 QueryRow 和 rows.Next() 共用一套扫描逻辑。
// 内部处理三个可空列：last_checked_at / updated_at (NullTime)；last_reminded_at /
// ec_notified_at (NullString)。
//
// Args:
//   row: sql.Row 或 sql.Rows 满足 Scan(...) 签名。
//
// Returns:
//   *Vehicle: 命中的车辆记录。
//   error:    底层 Scan 错误（例如列数不匹配）。
func scanVehicle(row interface{ Scan(dest ...interface{}) error }) (*Vehicle, error) {
	var v Vehicle
	var lastCheckedAt, updatedAt sql.NullTime
	var lastRemindedAt, ecNotifiedAt sql.NullString
	err := row.Scan(
		&v.ID, &v.AccountID, &v.VID, &v.Hphm, &v.Hpzl, &v.Mode,
		&v.DefaultArea, &v.DefaultXxdz, &v.DefaultJjmd, &v.Jjzzl,
		&v.Ybcs, &v.Sycs, &v.Syts, &v.LastStatus, &v.LastJjzzlmc, &v.LastYxqs, &v.LastYxqz,
		&v.LastJsrxm, &v.LastApplyID, &v.LastBlzt, &v.LastShsbyyms, &lastCheckedAt, &v.LastError, &lastRemindedAt,
		&ecNotifiedAt, &v.EcAutoApplied, &v.EcBlzt, &v.EcYxqs, &v.EcYxqz, &v.EcJjzzlmc, &v.EcShsbyyms,
		&v.CreatedAt, &updatedAt,
	)
	if err != nil {
		return nil, err
	}
	if lastCheckedAt.Valid {
		v.LastCheckedAt = &lastCheckedAt.Time
	}
	if updatedAt.Valid {
		v.UpdatedAt = &updatedAt.Time
	}
	if lastRemindedAt.Valid {
		v.LastRemindedAt = lastRemindedAt.String
	}
	if ecNotifiedAt.Valid {
		v.EcNotifiedAt = ecNotifiedAt.String
	}
	return &v, nil
}

// GetVehicle 按主键读取单个车辆。
//
// Args:
//   id: 自增主键。
//
// Returns:
//   *Vehicle: 命中记录。
//   error:    "vehicle N not found" (ErrNoRows) 或底层错误。
func (db *DB) GetVehicle(id int64) (*Vehicle, error) {
	row := db.conn.QueryRow(`SELECT `+vehicleCols+` FROM vehicles WHERE id = ?`, id)
	v, err := scanVehicle(row)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("vehicle %d not found", id)
		}
		return nil, fmt.Errorf("get vehicle: %w", err)
	}
	return v, nil
}

// ListVehicles 列出所有车辆（单用户应用，返回全表）。
//
// Returns:
//   []*Vehicle: 按 id 升序（近似添加顺序）。
//   error:      Query 失败。
//
// Notes:
//   scheduler.ExecuteCheck 会先调此函数拿全表，然后按 account_id 分组 —— 一次 IO
//   +内存分组比多次按 account_id 查更省事，本 app 车辆总数在个位数。
func (db *DB) ListVehicles() ([]*Vehicle, error) {
	rows, err := db.conn.Query(`SELECT ` + vehicleCols + ` FROM vehicles ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("list vehicles: %w", err)
	}
	defer rows.Close()

	var vehicles []*Vehicle
	for rows.Next() {
		v, err := scanVehicle(rows)
		if err != nil {
			return nil, fmt.Errorf("scan vehicle: %w", err)
		}
		vehicles = append(vehicles, v)
	}
	return vehicles, nil
}

// ListVehiclesByAccount 列出指定账户下的车辆。
//
// Args:
//   accountID: 账户主键。
//
// Returns:
//   []*Vehicle: 该账户下的所有车辆；无匹配返回 nil。
//   error:      Query 或 Scan 失败。
func (db *DB) ListVehiclesByAccount(accountID int64) ([]*Vehicle, error) {
	rows, err := db.conn.Query(`SELECT `+vehicleCols+` FROM vehicles WHERE account_id = ? ORDER BY id`, accountID)
	if err != nil {
		return nil, fmt.Errorf("list vehicles by account: %w", err)
	}
	defer rows.Close()

	var vehicles []*Vehicle
	for rows.Next() {
		v, err := scanVehicle(rows)
		if err != nil {
			return nil, fmt.Errorf("scan vehicle: %w", err)
		}
		vehicles = append(vehicles, v)
	}
	return vehicles, nil
}

// UpsertVehicle 同步车辆数据（存在则更新，不存在则插入）。
//
// 参数很多但都是从北京交警 stateList API 返回结构拆出来的当前进京证信息 (bzxx[0])；
// 二次办证 (ecbzxx[0]) 由调用方另行调 UpdateVehicle 写入，不在此函数内混入。
//
// Args:
//   accountID: 归属账户 ID。
//   vID:       北京交警 API 侧车辆 ID（唯一定位车辆）。
//   hphm:      号牌号码。
//   hpzl:      号牌种类。
//   ybcs:      已办次数。
//   sycs:      剩余次数。
//   syts:      剩余天数。
//   status:    当前进京证状态名称 (blztmc)。
//   jjzzlmc:   进京证种类名称。
//   yxqs:      有效期起。
//   yxqz:      有效期止。
//   jsrxm:     驾驶人姓名。
//   applyID:   申请单 ID。
//   blzt:      办理状态码。
//   shsbyyms:  审核失败原因描述。
//
// Returns:
//   *Vehicle: 插入或更新后的完整记录。
//   error:    SELECT / INSERT / UPDATE / GetVehicle 反查任一失败。
//
// Notes:
//   - 新建时 mode 默认 'reminder'；用户在 UI 上手动改成 'agent' 才切换。
//   - 更新时 last_error 会被清空，让"上次成功刷新的记录不带上一次的错误提示"。
//   - 二次办证字段 (ec_*) 不在此写入 —— 由调用方 (scheduler.syncVehicles) 再调 UpdateVehicle。
func (db *DB) UpsertVehicle(accountID int64, vID, hphm, hpzl string, ybcs int, sycs, syts, status, jjzzlmc, yxqs, yxqz, jsrxm, applyID string, blzt int, shsbyyms string) (*Vehicle, error) {
	// 查找是否已存在
	var existingID int64
	err := db.conn.QueryRow(
		`SELECT id FROM vehicles WHERE v_id = ? AND account_id = ?`, vID, accountID,
	).Scan(&existingID)

	now := time.Now()

	if err == sql.ErrNoRows {
		// 插入新记录：mode / default_* / jjzzl 用建表默认值兜底
		result, err := db.conn.Exec(
			`INSERT INTO vehicles (account_id, v_id, hphm, hpzl, mode, default_area, default_xxdz, default_jjmd, jjzzl,
					ybcs, sycs, syts, last_status, last_jjzzlmc, last_yxqs, last_yxqz, last_jsrxm, last_apply_id,
					last_blzt, last_shsbyyms, last_checked_at, last_error)
				VALUES (?, ?, ?, ?, 'reminder', '', '', '其它', '02', ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, '')`,
			accountID, vID, hphm, hpzl, ybcs, sycs, syts, status, jjzzlmc, yxqs, yxqz, jsrxm, applyID, blzt, shsbyyms, now,
		)
		if err != nil {
			return nil, fmt.Errorf("insert vehicle: %w", err)
		}
		id, _ := result.LastInsertId()
		return db.GetVehicle(id)
	}
	if err != nil {
		return nil, fmt.Errorf("query vehicle: %w", err)
	}

	// 更新已有记录：仅刷 API 数据字段，mode / default_* 由用户设置保持不动
	res, err := db.conn.Exec(
		`UPDATE vehicles SET hphm = ?, hpzl = ?, ybcs = ?, sycs = ?, syts = ?,
				last_status = ?, last_jjzzlmc = ?, last_yxqs = ?, last_yxqz = ?,
				last_jsrxm = ?, last_apply_id = ?, last_blzt = ?, last_shsbyyms = ?,
				last_checked_at = ?, last_error = '', updated_at = ?
			WHERE id = ?`,
		hphm, hpzl, ybcs, sycs, syts, status, jjzzlmc, yxqs, yxqz, jsrxm, applyID, blzt, shsbyyms, now, now, existingID,
	)
	if err != nil {
		return nil, fmt.Errorf("update vehicle: %w", err)
	}
	_ = res
	return db.GetVehicle(existingID)
}

// allowedVehicleUpdateCols UpdateVehicle 允许更新的列名白名单，防止 SQL 注入。
//
// 参数不允许出现在此表中：id / account_id / v_id / hphm / hpzl / created_at / updated_at —
// 前几项是主键或天然属性，updated_at 由 CURRENT_TIMESTAMP 自动填。
var allowedVehicleUpdateCols = map[string]bool{
	"mode":             true,
	"default_area":     true,
	"default_xxdz":     true,
	"default_jjmd":     true,
	"jjzzl":            true,
	"ybcs":             true,
	"sycs":             true,
	"syts":             true,
	"last_status":      true,
	"last_jjzzlmc":     true,
	"last_yxqs":        true,
	"last_yxqz":        true,
	"last_jsrxm":       true,
	"last_apply_id":    true,
	"last_blzt":        true,
	"last_shsbyyms":    true,
	"last_error":       true,
	"last_reminded_at": true,
	"ec_notified_at":   true,
	"ec_auto_applied":  true,
	"ec_blzt":          true,
	"ec_yxqs":          true,
	"ec_yxqz":          true,
	"ec_jjzzlmc":       true,
	"ec_shsbyyms":      true,
}

// UpdateVehicle 按主键增量更新车辆字段。
//
// Args:
//   id:      主键。
//   updates: 列名 → 新值 map；列名必须在 allowedVehicleUpdateCols 白名单内。
//
// Returns:
//   error: 列名不在白名单、SQL 执行失败；updates 为空返回 nil。
//
// Notes:
//   Flutter / Kotlin 层通知发送成功后会调此函数写 ec_notified_at 去重标记。
//   业务上"通过/不通过/到期/过期"四个终态一次生命周期内只发一次，靠此字段保证。
func (db *DB) UpdateVehicle(id int64, updates map[string]interface{}) error {
	if len(updates) == 0 {
		return nil
	}
	query := "UPDATE vehicles SET updated_at = CURRENT_TIMESTAMP"
	args := []interface{}{}
	cols := []string{}
	for col, val := range updates {
		if !allowedVehicleUpdateCols[col] {
			return fmt.Errorf("update vehicle: column %q not allowed", col)
		}
		query += fmt.Sprintf(", %s = ?", col)
		args = append(args, val)
		cols = append(cols, col)
	}
	query += " WHERE id = ?"
	args = append(args, id)

	result, err := db.conn.Exec(query, args...)
	if err != nil {
		logf("[store.UpdateVehicle] id=%d cols=%v ERROR: %v", id, cols, err)
		return fmt.Errorf("update vehicle: %w", err)
	}
	_ = result
	return nil
}

// GetVehicleByVID 按 (v_id, account_id) 二元组查找车辆。
//
// Args:
//   vID:       北京交警 API 车辆 ID。
//   accountID: 归属账户。
//
// Returns:
//   *Vehicle: 命中记录，或 (nil, nil) 表示 ErrNoRows（不当作错误对待，upsert 场景需要）。
//   error:    非 ErrNoRows 的其他 DB 错误。
func (db *DB) GetVehicleByVID(vID string, accountID int64) (*Vehicle, error) {
	row := db.conn.QueryRow(`SELECT `+vehicleCols+` FROM vehicles WHERE v_id = ? AND account_id = ?`, vID, accountID)
	v, err := scanVehicle(row)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("get vehicle by vid: %w", err)
	}
	return v, nil
}
