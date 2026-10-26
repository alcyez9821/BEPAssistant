// Copyright (c) 2026 alcyez <alcyez9821@gmail.com>
// SPDX-License-Identifier: MIT

// File: account.go
// Purpose: accounts 表 CRUD —— 账户增删改查。
// Author: alcyez (AI Agent 辅助生成注释)
// Created: 2026-07-06
//
// Business Context:
//   accounts 表存储用户添加的北京通账号（手机号 + 密文密码 + 密文 accessToken + 备注名）。
//   password / token 是 "ENCv1:base64(...)" 密文，由 Kotlin CryptoManager 加密后传入；
//   Go 端从不接触明文（除了 scheduler.Credentials 短命结构体）。
//
// Key Logic:
//   - CreateAccount 只插基础字段，token 由 AddAccount 后续 UpdateAccount 单独更新
//     （保持一次 INSERT 一次 UPDATE 的原子约束更宽松，方便未来支持无 token 的临时账号）
//   - UpdateAccount 白名单校验列名，防止 map key 走 SQL 注入路径
//   - DeleteAccount 用事务级联删除车辆，避免孤儿数据
package store

import (
	"database/sql"
	"fmt"
	"time"
)

// Account 账户模型，对应 accounts 表一行。
//
// 字段说明：
//   ID:        自增主键
//   Phone:     手机号（11 位，作为凭据 map key 使用，稳定唯一）
//   Nickname:  用户自定义备注名（可空；UI 上显示时空则显示 Phone）
//   Password:  "ENCv1:..." 密文密码（Kotlin Keystore AES-GCM 加密）
//   Token:     "ENCv1:..." 密文 accessToken（同上）
//   CreatedAt: 首次创建时间
//   UpdatedAt: 最后一次 UpdateAccount 时间（可空）
type Account struct {
	ID        int64      `json:"id"`
	Phone     string     `json:"phone"`
	Nickname  string     `json:"nickname"`
	Password  string     `json:"password"`
	Token     string     `json:"token"`
	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt *time.Time `json:"updated_at"`
}

// CreateAccount 新建账户并返回完整对象（含自增 ID + created_at）。
//
// Args:
//   phone:    手机号（11 位）。
//   nickname: 备注名（可空）。
//   password: 密文密码（"ENCv1:..."；Kotlin 侧已加密）。若允许存明文调试，需外部保证不落盘。
//
// Returns:
//   *Account: 新建账户完整对象。
//   error:    INSERT 失败或 GetAccount 反查失败。
//
// Notes:
//   - AddAccount 会先查重（相同手机号已存在则拒绝），此函数不做重复校验。
//   - Token 用另一次 UpdateAccount 更新，与 CreateAccount 解耦；不做多字段 INSERT 是为了
//     未来支持"先建账户再登录拿 token"的分离流程。
func (db *DB) CreateAccount(phone, nickname, password string) (*Account, error) {
	result, err := db.conn.Exec(
		`INSERT INTO accounts (phone, nickname, password) VALUES (?, ?, ?)`,
		phone, nickname, password,
	)
	if err != nil {
		logf("[store.CreateAccount] phone=%s ERROR: %v", phone, err)
		return nil, fmt.Errorf("insert account: %w", err)
	}
	id, _ := result.LastInsertId()
	return db.GetAccount(id)
}

// GetAccount 按主键读取单个账户。
//
// Args:
//   id: 自增主键。
//
// Returns:
//   *Account: 命中记录（包含 password / token 密文）。
//   error:    "account N not found"（ErrNoRows），或底层 IO / scan 失败。
func (db *DB) GetAccount(id int64) (*Account, error) {
	var a Account
	var updatedAt sql.NullTime
	err := db.conn.QueryRow(
		`SELECT id, phone, nickname, password, token, created_at, updated_at FROM accounts WHERE id = ?`, id,
	).Scan(&a.ID, &a.Phone, &a.Nickname, &a.Password, &a.Token, &a.CreatedAt, &updatedAt)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("account %d not found", id)
		}
		return nil, fmt.Errorf("get account: %w", err)
	}
	if updatedAt.Valid {
		a.UpdatedAt = &updatedAt.Time
	}
	return &a, nil
}

// ListAccounts 列出所有账户（单用户应用，返回全表）。
//
// Returns:
//   []*Account: 按 id 升序（即添加顺序）。
//   error:      Query 或 Scan 失败。
//
// Notes:
//   本 app 是单用户产品，账户数一般 ≤ 5；全表扫无性能压力，不引入分页。
func (db *DB) ListAccounts() ([]*Account, error) {
	rows, err := db.conn.Query(
		`SELECT id, phone, nickname, password, token, created_at, updated_at FROM accounts ORDER BY id`,
	)
	if err != nil {
		return nil, fmt.Errorf("list accounts: %w", err)
	}
	defer rows.Close()

	var accounts []*Account
	for rows.Next() {
		var a Account
		var updatedAt sql.NullTime
		if err := rows.Scan(&a.ID, &a.Phone, &a.Nickname, &a.Password, &a.Token, &a.CreatedAt, &updatedAt); err != nil {
			return nil, fmt.Errorf("scan account: %w", err)
		}
		if updatedAt.Valid {
			a.UpdatedAt = &updatedAt.Time
		}
		accounts = append(accounts, &a)
	}
	return accounts, nil
}

// allowedAccountUpdateCols UpdateAccount 允许更新的列名白名单。
//
// SQL 拼接使用 map key 时必须校验白名单，否则调用方可以传任意列名（甚至含子查询）造成
// SQL 注入。此 map 是安全边界。
var allowedAccountUpdateCols = map[string]bool{
	"phone":    true,
	"nickname": true,
	"password": true,
	"token":    true,
}

// UpdateAccount 按主键增量更新账户字段。
//
// Args:
//   id:      主键。
//   updates: 列名 → 新值的 map；列名必须在 allowedAccountUpdateCols 白名单内。
//
// Returns:
//   error: 列名不在白名单（安全拒绝）、SQL 执行失败；updates 为空则直接返回 nil。
//
// Notes:
//   - updated_at 由 CURRENT_TIMESTAMP 自动设置，调用方不需要传。
//   - 白名单校验一旦失败会立即返回错误，不 partial 更新，避免用户看到"部分字段没生效"。
func (db *DB) UpdateAccount(id int64, updates map[string]interface{}) error {
	if len(updates) == 0 {
		return nil
	}
	query := "UPDATE accounts SET updated_at = CURRENT_TIMESTAMP"
	args := []interface{}{}
	cols := []string{}
	for col, val := range updates {
		if !allowedAccountUpdateCols[col] {
			return fmt.Errorf("update account: column %q not allowed", col)
		}
		query += fmt.Sprintf(", %s = ?", col)
		args = append(args, val)
		cols = append(cols, col)
	}
	query += " WHERE id = ?"
	args = append(args, id)

	if _, err := db.conn.Exec(query, args...); err != nil {
		logf("[store.UpdateAccount] id=%d cols=%v ERROR: %v", id, cols, err)
		return fmt.Errorf("update account: %w", err)
	}
	return nil
}

// DeleteAccount 删除账户及其关联车辆（级联删除，事务内完成）。
//
// Args:
//   id: 要删除的账户主键。
//
// Returns:
//   error: 事务开启、DELETE、Commit 任一失败；Rollback 走 defer。
//
// Notes:
//   - vehicles 表没有 FK 约束（modernc.org/sqlite 默认不开启 PRAGMA foreign_keys），
//     因此手工先删 vehicles 再删 accounts；两条 DELETE 包在同一事务里保证原子性，
//     避免删账户成功但车辆残留成孤儿数据。
func (db *DB) DeleteAccount(id int64) error {
	tx, err := db.conn.Begin()
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.Exec(`DELETE FROM vehicles WHERE account_id = ?`, id); err != nil {
		logf("[store.DeleteAccount] id=%d delete vehicles ERROR: %v", id, err)
		return fmt.Errorf("delete vehicles: %w", err)
	}
	if _, err := tx.Exec(`DELETE FROM accounts WHERE id = ?`, id); err != nil {
		logf("[store.DeleteAccount] id=%d delete account ERROR: %v", id, err)
		return fmt.Errorf("delete account: %w", err)
	}
	if err := tx.Commit(); err != nil {
		logf("[store.DeleteAccount] id=%d commit ERROR: %v", id, err)
		return err
	}
	return nil
}
