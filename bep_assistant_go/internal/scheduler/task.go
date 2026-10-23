// Copyright (c) 2026 alcyez <alcyez9821@gmail.com>
// SPDX-License-Identifier: MIT

// Package scheduler 实现进京证自动检查任务的调度与执行主循环。
//
// File: task.go
// Purpose: 单次检查任务的编排入口。按账号分组车辆 → 拿 token → 调北京交警状态 API →
//          同步 SQLite → 按提醒/代办模式派发通知或自动申请下一张进京证。
// Author: alcyez (AI Agent 辅助生成注释)
// Created: 2026-07-06
//
// Business Context:
//   北京交警对同一号牌有"六环内 5 次/年""六环外无限"的额度限制；进京证有效期最多 7 天；
//   代办模式必须等审核完成才能提交下一张。本文件统一维护"待生效 / 生效中 / 审核中 / 不通过"
//   4 个状态机分支的通知去重和自动申请逻辑。
//
// Key Logic:
//   1. 按 account_id 分组，每组共用 token（token 过期时通过 persister 回调重登）
//   2. 每辆车状态先跑 handleReviewResult 优先出 "通过/不通过" 终态通知
//   3. 剩余分支按 mode 分派：reminder=纯通知，代办=通知+自动 ApplyForPermit
//   4. 通知去重靠 ec_notified_at 单字段（"通过"/"不通过"/"到期"/"过期"/""）
//   5. 明文凭据由外层 Kotlin/Flutter 解密后传入，Go 全程不接触 Keystore
package scheduler

/*
#include <stdlib.h>
#include <android/log.h>
static void goLog(const char* tag, const char* msg) {
    __android_log_print(ANDROID_LOG_DEBUG, tag, "%s", msg);
}
*/
import "C"
import (
	"fmt"
	"time"
	"unsafe"

	"github.com/alcyez9821/BEPAssistant/bep_assistant_go/internal/bjt"
	"github.com/alcyez9821/BEPAssistant/bep_assistant_go/internal/bep"
	"github.com/alcyez9821/BEPAssistant/bep_assistant_go/internal/store"
	"github.com/alcyez9821/BEPAssistant/bep_assistant_go/internal/util"
)

// cLog 把日志送到 Android logcat 的指定 tag（一般固定 "GoTask"）。
//
// 走 C 侧 __android_log_print 而不是 Go 的 log.Printf，因为 modernc.org/sqlite Android
// 上不 attach stdout；主动写 logcat 才能被 adb logcat 看到。
//
// Args:
//   tag: logcat tag。
//   msg: 日志正文。
func cLog(tag, msg string) {
	ct := C.CString(tag)
	cm := C.CString(msg)
	C.goLog(ct, cm)
	C.free(unsafe.Pointer(ct))
	C.free(unsafe.Pointer(cm))
}

// CaptchaProvider 验证码识别接口。
//
// 与 bjt.CaptchaSolver 等价，但在 scheduler 包内单独声明以避免包依赖倒置：
// scheduler 依赖 bjt 用来登录，但让 scheduler 自己声明验证码接口便于测试注入。
type CaptchaProvider interface {
	Recognize(imageBytes []byte) (string, error)
}

// todayStr 返回北京时间的今天日期字符串（YYYY-MM-DD）。
//
// 用于与 vehicle.LastYxqz / EcYxqz 比较是否到期。必须用北京时间：进京证有效期是
// 按北京日历天定义的，不能用 UTC 或用户手机的其他时区。
//
// Returns:
//   YYYY-MM-DD 格式的日期字符串。tzdata 加载失败时用固定 +8 时区兜底。
func todayStr() string {
	loc, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		loc = time.FixedZone("CST", 8*3600)
	}
	return time.Now().In(loc).Format("2006-01-02")
}

// NotificationEntry 单条通知条目，含车辆 ID 和去重标记。
//
// 结构说明：
//   VehicleID:  归属车辆；Kotlin 通知发送成功后回写此 ID 对应车辆的 ec_notified_at
//   NotifiedAt: 去重键值（"到期" / "过期" / "通过" / "不通过"）；写入 ec_notified_at
//   Message:    通知正文（"京A12345 进京证今日到期"等）
type NotificationEntry struct {
	VehicleID  int64  `json:"vehicle_id"`
	NotifiedAt string `json:"notified_at"`
	Message    string `json:"message"`
}

// TaskResult 一次检查任务的汇总结果，返回给 Kotlin/Flutter 层。
//
// 字段说明：
//   Checked:       实际调过 API 的车辆数（token 失败时也算 Checked）
//   Processed:     处理完通知/申请分支的车辆数
//   Applied:       本轮成功自动提交的申请数（代办模式）
//   Notifications: 结构化通知（含 vehicle_id + notified_at），供 Kotlin 派发 + 回写去重标记
//   Messages:      纯文本消息（保留兼容早期接口）
//   Message:       最后一条 Messages（保留兼容）
type TaskResult struct {
	Checked       int                 `json:"checked"`
	Processed     int                 `json:"processed"`
	Applied       int                 `json:"applied"`
	Notifications []NotificationEntry `json:"notifications"`
	Messages      []string            `json:"messages"`
	Message       string              `json:"message"`
}

// addNotification 追加一条需要去重的通知（业务终态：到期 / 过期 / 通过 / 不通过）。
//
// 同时也追加到 Messages 兼容早期接口。调用方需知道：Go 不直接写 ec_notified_at，
// 由 Kotlin 通知发送成功后回写；如果 Kotlin 侧通知被系统拦截，NotifiedAt 不会落盘，
// 下次检查会再次尝试。
//
// Args:
//   vehicleID:  归属车辆。
//   notifiedAt: 去重标记值。
//   body:       通知正文。
func (r *TaskResult) addNotification(vehicleID int64, notifiedAt, body string) {
	r.Notifications = append(r.Notifications, NotificationEntry{
		VehicleID:  vehicleID,
		NotifiedAt: notifiedAt,
		Message:    body,
	})
	r.Messages = append(r.Messages, body)
	r.Message = body
}

// addMessage 追加一条不需要去重的通知（例如"缺少默认值""申请失败"）。
//
// 仅写入 Messages / Message，Notifications 数组不动，避免 Kotlin 侧误回写 ec_notified_at。
func (r *TaskResult) addMessage(body string) {
	r.Messages = append(r.Messages, body)
	r.Message = body
}

// Credentials 单个账号的解密后凭据。
//
// 由 Kotlin 层从 SQLite 读密文、用 Keystore 解密后传入。明文只在此结构中驻留几秒（一次
// ExecuteCheck 的生命周期）；执行完 Go 内部丢弃引用即由 GC 回收，堆里不会长期残留。
type Credentials struct {
	Password string // 明文密码（token 过期后重登用）
	Token    string // 明文 accessToken（当前有效则直接用）
}

// TokenPersister 保存刷新后的 token 密文。
//
// ExecuteCheck 内部 token 刷新成功后调用此回调，由 Kotlin 层用 Keystore 加密后再写 DB；
// Go 不再直接写明文 token 到 accounts 表。生产实现在 bridge.go 的 filePersister，
// 走"JSONL 短命回调文件"路径。
type TokenPersister interface {
	// SaveRefreshedToken 保存新刷出的 token。
	//
	// Args:
	//   accountID:  刚刷 token 的账号主键。
	//   plainToken: 从北京交警新拿到的明文 accessToken。
	//
	// Returns:
	//   error: 写文件 / 加密 / DB 更新任一失败；scheduler 会记录日志但不中断本次检查。
	SaveRefreshedToken(accountID int64, plainToken string) error
}

// noopTokenPersister 未提供 TokenPersister 时的兜底：直接把明文 token 写 DB。
//
// 仅供开发调试 / 单元测试用，正式路径必须传真正的 filePersister；否则会绕过 Keystore 加密。
type noopTokenPersister struct{ db *store.DB }

// SaveRefreshedToken 直接写明文 token（调试兜底路径）。
func (n *noopTokenPersister) SaveRefreshedToken(accountID int64, plainToken string) error {
	return n.db.UpdateAccount(accountID, map[string]interface{}{"token": plainToken})
}

// ExecuteCheck 执行一次完整的检查任务（每次都调 API 刷新，不走本地缓存）。
//
// 业务规则：
//   - 一次调用覆盖所有账号所有车辆；串行处理避免 token/DB 并发。
//   - 缺少默认区域/详细地址的代办车辆会被跳过并写 last_error。
//   - 单车业务错误只写 last_error，不影响其他车辆继续处理。
//
// 处理流程：
//   1. 从 vehicles 表拉全表 → 按 account_id 分组
//   2. 每组拿一次 token（credentials[phone] 优先；token 过期则 ensureToken 重登）
//   3. 用 token 调 GetStateList 刷新 → syncVehicles 落盘
//   4. 对每辆车调 processVehicle 处理通知 / 自动申请
//
// Args:
//   db:          数据库句柄，用于 vehicles / accounts 表 CRUD。
//   bepClient:   北京交警 API 客户端。
//   captcha:     验证码识别器，token 过期重登时需要（nil 则重登失败）。
//   credentials: {phone: {password, token}}，明文凭据（Kotlin 侧解密后传入）。
//   persister:   token 刷新后的回调；nil 时降级为直接写 DB（仅调试）。
//
// Returns:
//   *TaskResult: 包含处理数、通知列表、消息列表，交给 Kotlin 层统一发通知。
//   error:       查询车辆失败等致命错误；单车业务错误只写 last_error，不返回。
//
// Notes:
//   与 RunCheck 共享 bridge.go 里的 runCheckMu 锁，前后台并发触发时排队。
func ExecuteCheck(
	db *store.DB,
	bepClient *bep.Client,
	captcha CaptchaProvider,
	credentials map[string]Credentials,
	persister TokenPersister,
) (*TaskResult, error) {
	if persister == nil {
		persister = &noopTokenPersister{db: db}
	}
	today := todayStr()

	vehicles, err := db.ListVehicles()
	if err != nil {
		return nil, fmt.Errorf("查询车辆失败: %w", err)
	}
	if len(vehicles) == 0 {
		return &TaskResult{}, nil
	}

	// 按 account_id 分组：同一账户的车辆共用一次 token 获取
	groups := make(map[int64][]*store.Vehicle)
	for _, v := range vehicles {
		groups[v.AccountID] = append(groups[v.AccountID], v)
	}

	result := &TaskResult{}

	for accountID, groupVehicles := range groups {
		account, err := db.GetAccount(accountID)
		if err != nil {
			continue
		}

		cLog("GoTask", fmt.Sprintf("account %s, getting token...", account.Phone))

		// 获取有效 token（用 phone 而不是 accountID 查，避免删账号+重加错位）
		cred, hasCred := credentials[account.Phone]
		token, err := ensureToken(db, account, cred, hasCred, bepClient, captcha, persister)
		if err != nil {
			cLog("GoTask", fmt.Sprintf("token failed: %v", err))
			for _, v := range groupVehicles {
				db.UpdateVehicle(v.ID, map[string]interface{}{"last_error": err.Error()})
				result.Checked++
			}
			continue
		}
		cLog("GoTask", fmt.Sprintf("token ok, calling API..."))

		// 调 API 刷新
		stateData, err := bepClient.GetStateList(token)
		if err != nil {
			cLog("GoTask", fmt.Sprintf("API failed: %v", err))
			continue
		}
		cLog("GoTask", fmt.Sprintf("API ok, syncing %d vehicles", len(stateData.Bzclxx)))

		syncVehicles(db, account.ID, stateData)
		result.Checked += len(groupVehicles)

		// 重新读取刷新后的车辆数据，让 processVehicle 拿到最新 ec_* 字段
		refreshed, _ := db.ListVehiclesByAccount(accountID)

		for _, v := range refreshed {
			result.Processed++
			processVehicle(db, bepClient, token, account, v, today, result)
		}
	}

	cLog("GoTask", fmt.Sprintf("检查完成: checked=%d processed=%d applied=%d", result.Checked, result.Processed, result.Applied))
	return result, nil
}

// processVehicle 处理单辆车的检查逻辑：终态通知 → 有效期分支 → 代办自动申请。
//
// 状态机三层分派：
//   1. handleReviewResult：优先派发"通过/不通过"终态通知，命中则直接返回
//   2. reminder 模式：只发到期 / 过期通知，不自动申请
//   3. 代办模式：证有效则重置状态；有二次办证进行中则等审核；否则通知+自动申请下一张
//
// Args:
//   db:        数据库句柄。
//   bepClient: 用于 ApplyForPermit + 申请后再 GetStateList 拿二次办证信息。
//   token:     此账户的有效 accessToken。
//   account:   账户对象（当前实现未直接使用，保留便于未来通知里带账户区分）。
//   v:         处理的车辆。
//   today:     今天日期字符串（避免每辆车重复计算）。
//   result:    汇总结果，通过 addNotification / addMessage 追加通知。
//
// Notes:
//   - "证有效"分支会清 ec_notified_at / ec_auto_applied，让下一轮生命周期能重新通知。
//   - 代办自动申请成功后立刻再调一次 GetStateList，把二次办证信息落盘，UI 立即可见。
func processVehicle(db *store.DB, bepClient *bep.Client, token string, account *store.Account, v *store.Vehicle, today string, result *TaskResult) {
	// === 第 1 步：审核结果通知（提醒/代办共享，覆盖 API 所有状态路径） ===
	//
	// 触发点（以 ec_notified_at 单标记去重）：
	//   ecbzxx.blzt=6 (待生效)  → "通过"（有生效证时的二次办证路径）
	//   ecbzxx.blzt=4 (不通过)  → "不通过"
	//   bzxx.blzt=6   (待生效)  → "通过"（无生效证、直接进入的路径）
	//   bzxx.blzt=4   (不通过)  → "不通过"（无生效证、审核失败的路径）
	//   bzxx.blzt=1 && last_yxqz>today && ec_auto_applied=1
	//                          → "通过"（直接生效、跳过 blzt=6 中间态的兜底）
	// "通过"和"不通过"是终态标记，一次生命周期内只发一次；证过期时会被到期/过期通知覆盖，
	// 用户下次点保存代办自动申请下一张时会被重置为空。
	if handleReviewResult(db, v, result) {
		return
	}

	// === 提醒模式 ===
	if v.Mode == "reminder" {
		if v.LastYxqz != "" && v.LastYxqz > today {
			// 证有效，重置通知状态：下次到期/过期时能再次通知
			db.UpdateVehicle(v.ID, map[string]interface{}{"ec_notified_at": ""})
			return
		}
		if v.LastYxqz == today && v.EcNotifiedAt != "到期" {
			cLog("GoTask", fmt.Sprintf("[提醒] %s 今日到期", v.Hphm))
			result.addNotification(v.ID, "到期", fmt.Sprintf("%s 进京证今日到期，请及时办理", v.Hphm))
		} else if v.LastYxqz != "" && v.LastYxqz < today && v.EcNotifiedAt != "过期" {
			cLog("GoTask", fmt.Sprintf("[提醒] %s 已过期", v.Hphm))
			result.addNotification(v.ID, "过期", fmt.Sprintf("%s 进京证已过期，请尽快办理", v.Hphm))
		}
		return
	}

	// === 代办模式 ===

	// 证有效，重置所有状态（下轮到期时能重新申请）
	if v.LastYxqz != "" && v.LastYxqz > today {
		db.UpdateVehicle(v.ID, map[string]interface{}{
			"ec_notified_at":  "",
			"ec_auto_applied": 0,
		})
		return
	}

	// 有 ecbzxx 数据（二次办证在进行中）→ 不发到期通知；ec_auto_applied 标记为已提交
	// 例外：blzt=4（审核不通过）代办模式下需要落到下方申请分支重试，不能在这里 return。
	if v.EcBlzt != 0 && !(v.Mode == "agent" && v.EcBlzt == 4) {
		db.UpdateVehicle(v.ID, map[string]interface{}{"ec_auto_applied": 1})
		if v.EcBlzt == 5 {
			cLog("GoTask", fmt.Sprintf("[代办] %s 审核中", v.Hphm))
		}
		// blzt=6/4 的通知已在 handleReviewResult 里处理
		return
	}

	// 无二次办证 → 发到期/过期通知，再自动申请

	// 到期通知
	if v.LastYxqz == today && v.EcNotifiedAt != "到期" {
		cLog("GoTask", fmt.Sprintf("[代办] %s 今日到期", v.Hphm))
		result.addNotification(v.ID, "到期", fmt.Sprintf("%s 进京证今日到期", v.Hphm))
	}

	// 过期通知
	if v.LastYxqz != "" && v.LastYxqz < today && v.EcNotifiedAt != "过期" {
		cLog("GoTask", fmt.Sprintf("[代办] %s 已过期", v.Hphm))
		result.addNotification(v.ID, "过期", fmt.Sprintf("%s 进京证已过期", v.Hphm))
	}

	// 已提交，等待审核结果；避免下一轮又提交一次
	if v.EcAutoApplied == 1 {
		cLog("GoTask", fmt.Sprintf("[代办] %s 已提交申请，等待审核结果", v.Hphm))
		return
	}

	// 未提交，自动申请：先检查默认值齐全
	if v.DefaultArea == "" || v.DefaultXxdz == "" {
		cLog("GoTask", fmt.Sprintf("[代办] %s 缺少默认值，跳过", v.Hphm))
		db.UpdateVehicle(v.ID, map[string]interface{}{"last_error": "请设置办理默认值"})
		result.addMessage(fmt.Sprintf("%s 缺少办理默认值", v.Hphm))
		return
	}
	cLog("GoTask", fmt.Sprintf("[代办] %s 自动申请中...", v.Hphm))
	_, err := bepClient.ApplyForPermit(token, bep.ApplyOptions{
		VID:   v.VID,
		Hphm:  v.Hphm,
		Hpzl:  v.Hpzl,
		Jjzzl: "02", // 默认申请六环外（无次数限制）；未来支持用户选择时改这里
		Area:  v.DefaultArea,
		Xxdz:  v.DefaultXxdz,
		Jjmd:  util.OrDefault(v.DefaultJjmd, "其它"),
	})
	if err != nil {
		cLog("GoTask", fmt.Sprintf("[代办] %s 申请失败: %v", v.Hphm, err))
		result.addMessage(fmt.Sprintf("%s 进京证申请失败: %v\n请手动检查", v.Hphm, err))
		db.UpdateVehicle(v.ID, map[string]interface{}{"last_error": "申请失败: " + err.Error()})
	} else {
		cLog("GoTask", fmt.Sprintf("[代办] %s 申请成功", v.Hphm))
		db.UpdateVehicle(v.ID, map[string]interface{}{
			"ec_auto_applied": 1,
			"ec_notified_at":  "", // 重置，允许下次审核结果通知
		})
		result.Applied++
		result.addMessage(fmt.Sprintf("%s 进京证申请已提交", v.Hphm))
		// 刷新数据以获取二次办证信息（UI 立刻可以看到审核中状态）
		if freshState, err := bepClient.GetStateList(token); err == nil {
			syncVehicles(db, v.AccountID, freshState)
		}
	}
}

// 通知统一由 Kotlin 层解析 Go 返回的 messages 字段后发送，Go 层不直接发送通知

// handleReviewResult 统一分派"审核通过 / 审核不通过"通知。
//
// 覆盖所有 API 状态路径：ecbzxx 或 bzxx 里出现 blzt=4/6，以及 bzxx.blzt=1 直接生效的兜底。
// 返回 true 表示已发终态通知（"通过"或"不通过"），调用方应尽早 return 避免走到期/过期分支。
//
// 提醒模式和代办模式共享此逻辑。用户在别处（比如北京交警 app）手动办的证也会通知 ——
// 只要审核状态变化到 blzt=4/6，检查任务捕获到就发。
//
// Args:
//   db:     用于审核不通过时清 ec_auto_applied 让用户再次点保存能重新申请。
//   v:      车辆记录。
//   result: 汇总结果。
//
// Returns:
//   bool: true 表示已发终态通知，调用方应尽早 return。
func handleReviewResult(db *store.DB, v *store.Vehicle, result *TaskResult) bool {
	// 优先看 ecbzxx（有生效证时的二次办证）
	switch v.EcBlzt {
	case 6:
		if v.EcNotifiedAt != "通过" {
			cLog("GoTask", fmt.Sprintf("[审核] %s 审核通过（待生效），%s 生效", v.Hphm, v.EcYxqs))
			result.addNotification(v.ID, "通过", fmt.Sprintf("%s 进京证审核通过，%s 生效", v.Hphm, v.EcYxqs))
			return true
		}
	case 4:
		// 代办模式：每次审核不通过都通知（无去重），并 return false 让 processVehicle
		// 继续走到自动申请分支——下次闹钟就会重试提交。
		// 提醒模式：仅去重发一次通知，然后 return true 阻断后续流程。
		if v.Mode == "agent" {
			cLog("GoTask", fmt.Sprintf("[审核] %s 审核不通过（代办将重试）: %s", v.Hphm, v.EcShsbyyms))
			result.addNotification(v.ID, "不通过", fmt.Sprintf("%s 进京证审核不通过: %s", v.Hphm, v.EcShsbyyms))
			// 清 ec_auto_applied 让 processVehicle 下面的分支能重新提交申请
			db.UpdateVehicle(v.ID, map[string]interface{}{"ec_auto_applied": 0})
			v.EcAutoApplied = 0 // 同步内存值，避免下方 `if EcAutoApplied == 1 { return }` 拦截
			return false
		}
		if v.EcNotifiedAt != "不通过" {
			cLog("GoTask", fmt.Sprintf("[审核] %s 审核不通过: %s", v.Hphm, v.EcShsbyyms))
			result.addNotification(v.ID, "不通过", fmt.Sprintf("%s 进京证审核不通过: %s", v.Hphm, v.EcShsbyyms))
			db.UpdateVehicle(v.ID, map[string]interface{}{"ec_auto_applied": 0})
			return true
		}
	}

	// 再看 bzxx（无生效证时的首次/续期路径）
	switch v.LastBlzt {
	case 6:
		if v.EcNotifiedAt != "通过" {
			cLog("GoTask", fmt.Sprintf("[审核] %s 审核通过（待生效），%s 生效", v.Hphm, v.LastYxqs))
			result.addNotification(v.ID, "通过", fmt.Sprintf("%s 进京证审核通过，%s 生效", v.Hphm, v.LastYxqs))
			return true
		}
	case 4:
		// 与 ecbzxx.blzt=4 相同的策略：代办无去重、清标记、return false 允许重试
		if v.Mode == "agent" {
			cLog("GoTask", fmt.Sprintf("[审核] %s 审核不通过（代办将重试）: %s", v.Hphm, v.LastShsbyyms))
			result.addNotification(v.ID, "不通过", fmt.Sprintf("%s 进京证审核不通过: %s", v.Hphm, v.LastShsbyyms))
			db.UpdateVehicle(v.ID, map[string]interface{}{"ec_auto_applied": 0})
			v.EcAutoApplied = 0
			return false
		}
		if v.EcNotifiedAt != "不通过" {
			cLog("GoTask", fmt.Sprintf("[审核] %s 审核不通过: %s", v.Hphm, v.LastShsbyyms))
			result.addNotification(v.ID, "不通过", fmt.Sprintf("%s 进京证审核不通过: %s", v.Hphm, v.LastShsbyyms))
			db.UpdateVehicle(v.ID, map[string]interface{}{"ec_auto_applied": 0})
			return true
		}
	case 1:
		// 直接生效，可能跳过了 blzt=6 中间态。只在"我们代办的证"上补发（避免用户手动办的证被误通知）。
		if v.EcAutoApplied == 1 && v.EcNotifiedAt != "通过" {
			cLog("GoTask", fmt.Sprintf("[审核] %s 直接生效（跳过 blzt=6），%s 生效", v.Hphm, v.LastYxqs))
			result.addNotification(v.ID, "通过", fmt.Sprintf("%s 进京证审核通过，%s 生效", v.Hphm, v.LastYxqs))
			return true
		}
	}
	return false
}

// ensureToken 获取有效 token，必要时自动登录刷新。
//
// 决策路径：
//   1. hasCred=false → 直接返回错误（Kotlin 没送来凭据，可能是 map key 错位）
//   2. token 非空 → 试调 GetStateList；成功则复用
//   3. token 无效且有 password → 用 LoginWithRetry(3) 重登，成功后 persister 回调保存
//   4. 无 token 无密码 → 返回错误
//
// Args:
//   db:        数据库句柄（当前实现未直接使用，保留便于未来审计日志用）。
//   account:   账户对象（用于日志和登录时的手机号）。
//   cred:      解密后的凭据。
//   hasCred:   credentials map 里是否有此账户；false 表示 Kotlin 没送。
//   bepClient: 用于校验现有 token 是否有效。
//   captcha:   验证码识别器；LoginWithRetry 需要。
//   persister: token 刷新后的回调。
//
// Returns:
//   string: 有效 accessToken。
//   error:  凭据缺失 / 无验证码识别器 / 重登失败 / 无 token 且无密码。
//
// Notes:
//   绝对不能从 DB 直接读 account.Password 兜底 —— DB 里存的是 ENCv1: 密文，Go 无 Keystore
//   密钥解不了。之前那段兜底代码曾把密文当明文送到北京通登录接口，5 次错误后账号被锁 1 小时
//   （2026-07-02 事故）。map key 已改用 account.Phone，从根本上避免 accountID 错位。
func ensureToken(
	db *store.DB,
	account *store.Account,
	cred Credentials,
	hasCred bool,
	bepClient *bep.Client,
	captcha CaptchaProvider,
	persister TokenPersister,
) (string, error) {
	if !hasCred {
		cLog("GoTask", fmt.Sprintf("[ensureToken] hasCred=false phone=%s — Kotlin 未送来该账号凭据（map key 错位？）", account.Phone))
		return "", fmt.Errorf("凭据缺失 (phone=%s)，Kotlin 未送来解密后的凭据", account.Phone)
	}
	token := cred.Token
	password := cred.Password

	if token != "" {
		if _, err := bepClient.GetStateList(token); err == nil {
			return token, nil
		} else {
			cLog("GoTask", fmt.Sprintf("[ensureToken] GetStateList 失败: %v", err))
		}
		// token 过期，尝试重新登录刷新
	}

	if password != "" {
		if captcha == nil {
			return "", fmt.Errorf("token 过期且无验证码识别器")
		}
		result, err := bjt.LoginWithRetry(account.Phone, password, captcha, 3)
		if err != nil {
			return "", fmt.Errorf("重新登录失败: %w", err)
		}
		// 通过 persister 让上层加密后写 DB；失败也返回新 token 不影响本次检查
		if perr := persister.SaveRefreshedToken(account.ID, result.Token); perr != nil {
			cLog("GoTask", fmt.Sprintf("持久化刷新 token 失败: %v", perr))
		}
		return result.Token, nil
	}

	return "", fmt.Errorf("无 token 且无密码")
}

// syncVehicles 从 API 数据同步车辆到数据库（包括二次办证）。
//
// 处理规则：
//   - 每辆车调 UpsertVehicle 落"当前进京证" (bzxx[0]) 字段
//   - 如果 ecbzxx 有数据 → 写入 ec_* 字段
//   - 如果 ecbzxx 为空 → 清零 ec_* 字段（防止老数据残留导致误判为二次办证进行中）
//
// Args:
//   db:        数据库句柄。
//   accountID: 归属账户。
//   stateData: stateList API 返回。
func syncVehicles(db *store.DB, accountID int64, stateData *bep.StateData) {
	for _, v := range stateData.Bzclxx {
		// 同步当前进京证
		permit := bep.PermitInfo{}
		if len(v.Bzxx) > 0 {
			permit = v.Bzxx[0]
		}
		db.UpsertVehicle(accountID, v.VID, v.Hphm, v.Hpzl,
			v.Ybcs, v.Sycs, v.Syts,
			permit.Blztmc, permit.Jjzzlmc, permit.Yxqs, permit.Yxqz,
			permit.Jsrxm, permit.ApplyID,
			permit.Blzt, permit.Shsbyyms)

		// 同步二次办证：需要先拿 vehicle.ID
		veh, _ := db.GetVehicleByVID(v.VID, accountID)
		if veh == nil {
			continue
		}
		if len(v.Ecbzxx) > 0 {
			ec := v.Ecbzxx[0]
			db.UpdateVehicle(veh.ID, map[string]interface{}{
				"ec_blzt":     ec.Blzt,
				"ec_yxqs":     ec.Yxqs,
				"ec_yxqz":     ec.Yxqz,
				"ec_jjzzlmc":  ec.Jjzzlmc,
				"ec_shsbyyms": ec.Shsbyyms,
			})
		} else {
			// 二次办证信息消失 → 可能是证已生效或不通过，清零字段避免下轮误判
			db.UpdateVehicle(veh.ID, map[string]interface{}{
				"ec_blzt":     0,
				"ec_yxqs":     "",
				"ec_yxqz":     "",
				"ec_jjzzlmc":  "",
				"ec_shsbyyms": "",
			})
		}
	}
}
