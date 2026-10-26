// Copyright (c) 2026 alcyez <alcyez9821@gmail.com>
// SPDX-License-Identifier: MIT

// Package main（作为 c-shared 库编译）为 Flutter / Kotlin 提供 FFI 入口。
//
// File: mobile/bridge.go
// Purpose: 通过 cgo //export 暴露 C ABI 函数（Init / FetchCaptcha / LoginWithCaptcha /
//          AddAccount / RunCheckWithCredentials 等），Flutter 走 dart:ffi，Kotlin
//          走 jni_bridge.c 转发。
// Author: alcyez (AI Agent 辅助生成注释)
// Created: 2026-07-06
//
// Business Context:
//   Go 库负责所有核心业务：北京通登录、北京交警 API、SQLite 持久化、ONNX 验证码识别、
//   定时检查调度。Flutter/Kotlin 是外壳。所有跨语言调用都必须通过此文件的 //export
//   函数，返回 JSON 字符串（*C.char），调用方使用后需调 FreeStr 释放。
//
// Key Logic:
//   1. Init 用 sync.Once 幂等初始化 db / bepClient / captchaSolver
//   2. FetchCaptcha 为每次登录建独立 http.Client（放 loginSessions sync.Map）→
//      LoginWithCaptcha 用同一 sessionID 取回；保证并发登录 cookie 隔离
//   3. RunCheck / RunCheckWithCredentials 共享 runCheckMu 串行锁，前后台并发时排队
//   4. classifyError 把内部错误映射成用户友好文案，避免暴露 SQL / 路径细节
//
// Notes:
//   //export 函数不能返回 Go 分配的字符串（GC 会回收），必须用 C.CString 拷到 C 堆；
//   调用方使用后必须调 FreeStr 释放，否则每次调用都会泄漏 KB 级堆。
package main

/*
#include <stdlib.h>
#include <android/log.h>
static void bridgeLog(const char* msg) {
    __android_log_print(ANDROID_LOG_DEBUG, "Bridge", "%s", msg);
}
*/
import "C"
import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"

	"github.com/alcyez9821/BEPAssistant/bep_assistant_go/internal/bjt"
	"github.com/alcyez9821/BEPAssistant/bep_assistant_go/internal/captcha"
	"github.com/alcyez9821/BEPAssistant/bep_assistant_go/internal/bep"
	"github.com/alcyez9821/BEPAssistant/bep_assistant_go/internal/scheduler"
	"github.com/alcyez9821/BEPAssistant/bep_assistant_go/internal/store"
)

// bridgeLog 把 Go 日志送到 Android logcat 的 "Bridge" tag。
//
// Args:
//   msg: 日志正文（含错误详情、参数等）；不做截断，logcat 自动分行。
func bridgeLog(msg string) {
	cs := C.CString(msg)
	C.bridgeLog(cs)
	C.free(unsafe.Pointer(cs))
}

var (
	// db 全局 SQLite 句柄；Init 内 sync.Once 保护一次初始化，后续读稳定指针。
	db *store.DB
	// bepClient 北京交警 API 客户端；同上一次初始化，多线程读安全（Client 内部 http.Client 线程安全）。
	bepClient *bep.Client
	// captchaSolver 验证码识别器；可能为 nil（模型文件缺失）。
	captchaSolver *captcha.Solver
	// once 保护 Init 幂等：Flutter + Kotlin 可能各调一次，只有第一次真的建库+加模型。
	once sync.Once

	// loginSessions 并发登录时每个 UI 上下文（添加账号 / 手动重登）都拿一个独立 session；
	// sessionID 由 FetchCaptcha 生成后返回给调用方，LoginWithCaptcha 用同一 ID 取回对应 client。
	// 用 sync.Map 而不是普通 map + mutex：读多写少（FetchCaptcha 才写），且 key 生命周期短暂。
	loginSessions  sync.Map // map[string]*http.Client
	sessionCounter uint64

	// runCheckMu 检查任务串行锁：前台 refresh + 后台 AlarmReceiver + 手动 manualCheck
	// 并发时排队执行。避免同一时间两次 ExecuteCheck 争抢 token / bepClient / DB 事务。
	runCheckMu sync.Mutex
)

// nextSessionID 生成新登录会话 ID。
//
// 使用 atomic.AddUint64 保证并发唯一；返回 "s1" / "s2" / ... 格式，方便 logcat 排查。
//
// Returns:
//   string: 全进程唯一的短字符串 ID。
func nextSessionID() string {
	n := atomic.AddUint64(&sessionCounter, 1)
	return fmt.Sprintf("s%d", n)
}

// Init 初始化 Go 核心：打开 SQLite、创建 bep 客户端、加载验证码模型。
//
// 幂等：首次调用真正干活，后续调用直接返回 success。这样 Kotlin/Flutter 各自都能安全调用。
//
// Args:
//   dbPath: SQLite 文件路径（C 字符串，来自 dart:ffi 或 JNI）。
//
// Returns:
//   *C.char: JSON 字符串 {"success":true} 或 {"success":false,"error":"..."}。
//
// Notes:
//   - 验证码模型缺失时 solver 保持 nil；LoginWithCaptcha / RecognizeCaptcha 会明确报错。
//   - store.Logger 被赋值成 bridgeLog，让 store 层日志汇入 Bridge tag。
//
//export Init
func Init(dbPath *C.char) *C.char {
	var initErr error
	once.Do(func() {
		// 让 store 层日志也走 Android logcat 的 Bridge tag
		store.Logger = bridgeLog
		dbPathGo := C.GoString(dbPath)
		var err error
		db, err = store.New(dbPathGo)
		if err != nil {
			initErr = err
			return
		}
		bepClient = bep.New()

		// 初始化验证码识别器（模型文件在 assets/models/ 目录下，由 Kotlin/Flutter 从 APK 提取到磁盘）
		modelDir := filepath.Dir(dbPathGo) + "/assets/models"
		modelPath := modelDir + "/captcha.onnx"
		charsetPath := modelDir + "/charset.json"
		if fi, statErr := os.Stat(modelPath); statErr == nil {
			bridgeLog(fmt.Sprintf("[Init] 模型文件存在 size=%d，开始初始化 solver", fi.Size()))
			captchaSolver, err = captcha.New(modelPath, charsetPath, "")
			if err != nil {
				captchaSolver = nil
				bridgeLog("[Init] 验证码识别器初始化失败: " + err.Error())
			}
		} else {
			bridgeLog(fmt.Sprintf("[Init] 模型文件不存在 path=%s err=%v — solver 保持 nil", modelPath, statErr))
		}
	})
	if initErr != nil {
		return C.CString(errJSON(initErr))
	}
	return C.CString(`{"success":true}`)
}

// FetchCaptcha 获取新验证码图片，并为本次登录会话建立独立 http.Client。
//
// 每次调用生成独立 session 存到 loginSessions；返回 sessionID + base64 图片 + 大小。
// UI 或 Go 侧识别验证码后，调 LoginWithCaptcha 传回同一 sessionID 完成登录。
//
// Returns:
//   *C.char: JSON。成功时 {"success":true,"image":"<b64>","length":N,"session_id":"sN"}；
//            失败时 {"success":false,"error":"..."}。
//
// Notes:
//   - 每次调用都新建 cookie jar：并发登录（前台加账号 + 后台重登）时 cookie 完全隔离。
//   - 请求失败或读 body 失败时会立即从 sync.Map 里删 session，防止悬挂。
//
//export FetchCaptcha
func FetchCaptcha() *C.char {
	// 每次调用生成独立 session，避免并发登录串 cookie
	jar, _ := cookiejar.New(nil)
	client := &http.Client{
		Timeout: 15 * time.Second,
		Jar:     jar,
	}
	sessionID := nextSessionID()
	loginSessions.Store(sessionID, client)

	req, _ := http.NewRequest("GET", "https://bjt.beijing.gov.cn/renzheng/common/generateCaptcha", nil)
	req.Header.Set("User-Agent", "Mozilla/5.0 (iPhone; CPU iPhone OS 16_0 like Mac OS X) AppleWebKit/605.1.15")

	resp, err := client.Do(req)
	if err != nil {
		loginSessions.Delete(sessionID)
		bridgeLog(fmt.Sprintf("[FetchCaptcha] ERROR: %v", err))
		return C.CString(errJSON(fmt.Errorf("获取验证码失败: %w", err)))
	}
	defer resp.Body.Close()

	imgBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		loginSessions.Delete(sessionID)
		bridgeLog(fmt.Sprintf("[FetchCaptcha] read ERROR: %v", err))
		return C.CString(errJSON(fmt.Errorf("读取验证码失败: %w", err)))
	}

	b64 := base64.StdEncoding.EncodeToString(imgBytes)
	result, _ := json.Marshal(map[string]interface{}{
		"success":    true,
		"image":      b64,
		"length":     len(imgBytes),
		"session_id": sessionID,
	})
	return C.CString(string(result))
}

// LoginWithCaptcha 用已识别的验证码 + FetchCaptcha 拿到的 sessionID 完成登录。
//
// 调用方必须先 FetchCaptcha 拿 sessionID + 图片；识别后（Go 端 RecognizeCaptcha 或
// 前端人工）用同一 sessionID 传回。sessionID 过期（清理 / 未 FetchCaptcha 就调）时返回
// "会话已过期"。登录成功后立即从 sync.Map 里删 session，防止重放。
//
// Args:
//   sessionID:   FetchCaptcha 返回的会话 ID。
//   phone:       手机号。
//   password:    明文密码（登录后即被丢弃）。
//   captchaText: 已识别的验证码文字。
//
// Returns:
//   *C.char: JSON。成功时 {"success":true,"token":"...","certName":"...",...}；
//            失败时 {"success":false,"error":"验证码错误请重试" 等}。
//
//export LoginWithCaptcha
func LoginWithCaptcha(sessionID *C.char, phone *C.char, password *C.char, captchaText *C.char) *C.char {
	sidGo := C.GoString(sessionID)
	phoneGo := C.GoString(phone)
	passwordGo := C.GoString(password)
	captchaGo := C.GoString(captchaText)

	v, ok := loginSessions.Load(sidGo)
	if !ok {
		bridgeLog(fmt.Sprintf("[LoginWithCaptcha] sessionID=%s NOT FOUND", sidGo))
		return C.CString(errJSON(fmt.Errorf("会话已过期，请重新获取验证码")))
	}
	client := v.(*http.Client)
	// 登录只做一次，无论成功失败都清理 session（防止重放攻击 / 内存泄漏）
	defer loginSessions.Delete(sidGo)

	result, err := bjt.LoginWithSession(phoneGo, passwordGo, captchaGo, client)
	if err != nil {
		bridgeLog(fmt.Sprintf("[LoginWithCaptcha] phone=%s ERROR: %v", phoneGo, err))
		return C.CString(errJSON(err))
	}
	return C.CString(jsonStr(result))
}

// AddAccount 新建账号（登录成功后立即调用）。
//
// 参数中 password / token 已经是 Kotlin CryptoManager 加密后的 "ENCv1:..." 密文
// —— Go 只负责落盘，不接触明文。会先查重（phone 已存在则拒绝）。
//
// Args:
//   phone:    手机号（唯一键）。
//   nickname: 备注名（可空）。
//   password: 加密后的密码密文。
//   token:    加密后的 accessToken 密文；空串表示暂未获得。
//
// Returns:
//   *C.char: JSON。成功时返回完整 Account 对象；失败时 {"success":false,"error":"..."}。
//
//export AddAccount
func AddAccount(phone *C.char, nickname *C.char, password *C.char, token *C.char) *C.char {
	phoneGo := C.GoString(phone)
	nickGo := C.GoString(nickname)
	pwdGo := C.GoString(password)
	tokenGo := C.GoString(token)

	// 检查是否重复添加
	existing, err := db.ListAccounts()
	if err != nil {
		return C.CString(errJSON(fmt.Errorf("查询账号失败: %w", err)))
	}
	for _, acc := range existing {
		if acc.Phone == phoneGo {
			return C.CString(errJSON(fmt.Errorf("该手机号已添加，请勿重复添加")))
		}
	}

	acc, err := db.CreateAccount(phoneGo, nickGo, pwdGo)
	if err != nil {
		bridgeLog(fmt.Sprintf("[AddAccount] CreateAccount ERROR: %v", err))
		return C.CString(errJSON(err))
	}

	// 保存 token（登录成功后才调用此函数，token 一定有值；分开写便于将来支持"先建账户再登录"）
	if tokenGo != "" {
		if err := db.UpdateAccount(acc.ID, map[string]interface{}{"token": tokenGo}); err != nil {
			bridgeLog(fmt.Sprintf("[AddAccount] UpdateAccount token ERROR: %v", err))
		}
		acc.Token = tokenGo
	}

	bridgeLog(fmt.Sprintf("[AddAccount] id=%d phone=%s", acc.ID, phoneGo))
	return C.CString(jsonStr(acc))
}

// ListAccounts 列出所有账号。
//
// Returns:
//   *C.char: JSON。成功时 {"success":true,"accounts":[...]}；失败时 {"success":false,"error":"..."}。
//
//export ListAccounts
func ListAccounts() *C.char {
	accounts, err := db.ListAccounts()
	if err != nil {
		bridgeLog(fmt.Sprintf("[ListAccounts] ERROR: %v", err))
		return C.CString(errJSON(err))
	}
	return C.CString(okJSON("accounts", accounts))
}

// UpdateAccountToken 更新指定账号的 accessToken 密文。
//
// 由 Kotlin CredentialsGateway.applyRefreshedTokens 调用：Go 层重登拿到新明文 token 后，
// 通过 JSONL 回调文件传给 Kotlin，Kotlin 加密后调用本函数写回。
//
// Args:
//   id:    账号主键。
//   token: 加密后的 token 密文（"ENCv1:..."）。
//
// Returns:
//   *C.char: {"success":true} 或 {"success":false,"error":"..."}。
//
//export UpdateAccountToken
func UpdateAccountToken(id C.long, token *C.char) *C.char {
	updates := map[string]interface{}{"token": C.GoString(token)}
	if err := db.UpdateAccount(int64(id), updates); err != nil {
		bridgeLog(fmt.Sprintf("[UpdateAccountToken] id=%d ERROR: %v", int64(id), err))
		return C.CString(errJSON(err))
	}
	return C.CString(`{"success":true}`)
}

// UpdateAccountNickname 修改账号备注名。
//
// Args:
//   id:       账号主键。
//   nickname: 新备注名（可空 → 显示 Phone）。
//
// Returns:
//   *C.char: {"success":true} 或 {"success":false,"error":"..."}。
//
//export UpdateAccountNickname
func UpdateAccountNickname(id C.long, nickname *C.char) *C.char {
	updates := map[string]interface{}{"nickname": C.GoString(nickname)}
	if err := db.UpdateAccount(int64(id), updates); err != nil {
		bridgeLog(fmt.Sprintf("[UpdateAccountNickname] id=%d ERROR: %v", int64(id), err))
		return C.CString(errJSON(err))
	}
	return C.CString(`{"success":true}`)
}

// DeleteAccount 删除账号及其关联车辆（DB 层事务保证原子性）。
//
// Args:
//   id: 要删除的账号主键。
//
// Returns:
//   *C.char: {"success":true} 或 {"success":false,"error":"..."}。
//
//export DeleteAccount
func DeleteAccount(id C.long) *C.char {
	if err := db.DeleteAccount(int64(id)); err != nil {
		bridgeLog(fmt.Sprintf("[DeleteAccount] id=%d ERROR: %v", int64(id), err))
		return C.CString(errJSON(err))
	}
	bridgeLog(fmt.Sprintf("[DeleteAccount] id=%d SUCCESS", int64(id)))
	return C.CString(`{"success":true}`)
}

// ListVehicles 列出指定账号的车辆。
//
// Args:
//   accountID: 账号主键。
//
// Returns:
//   *C.char: {"success":true,"vehicles":[...]} 或 {"success":false,"error":"..."}。
//
//export ListVehicles
func ListVehicles(accountID C.long) *C.char {
	vehicles, err := db.ListVehiclesByAccount(int64(accountID))
	if err != nil {
		bridgeLog(fmt.Sprintf("[ListVehicles] accountID=%d ERROR: %v", int64(accountID), err))
		return C.CString(errJSON(err))
	}
	return C.CString(okJSON("vehicles", vehicles))
}

// UpdateVehicle 更新车辆字段（列名白名单校验）。
//
// 常见调用场景：Kotlin/Flutter 通知发送成功后回写 ec_notified_at；UI 上保存"代办模式默认值"。
//
// Args:
//   id:          车辆主键。
//   updatesJSON: JSON 字符串，解析成 map[string]interface{}；列名必须在 store 白名单内。
//
// Returns:
//   *C.char: {"success":true} 或 {"success":false,"error":"..."}。
//
//export UpdateVehicle
func UpdateVehicle(id C.long, updatesJSON *C.char) *C.char {
	var updates map[string]interface{}
	if err := json.Unmarshal([]byte(C.GoString(updatesJSON)), &updates); err != nil {
		bridgeLog(fmt.Sprintf("[UpdateVehicle] id=%d unmarshal ERROR: %v", int64(id), err))
		return C.CString(errJSON(err))
	}
	if err := db.UpdateVehicle(int64(id), updates); err != nil {
		bridgeLog(fmt.Sprintf("[UpdateVehicle] id=%d ERROR: %v", int64(id), err))
		return C.CString(errJSON(err))
	}
	return C.CString(`{"success":true}`)
}

// SyncVehicles 主动同步指定账号的车辆状态（在添加账号后立即刷新时使用）。
//
// 与 RunCheckWithCredentials 的区别：SyncVehicles 只调 stateList + 落 DB，不做通知 / 自动申请。
//
// Args:
//   accountID: 账号主键。
//   token:     明文 accessToken（由添加账号流程直接传入，还未加密）。
//
// Returns:
//   *C.char: {"success":true,"vehicles":[...]} 或错误。
//
//export SyncVehicles
func SyncVehicles(accountID C.long, token *C.char) *C.char {
	tokenGo := C.GoString(token)
	accID := int64(accountID)

	// 调用 API 获取车辆状态
	stateData, err := bepClient.GetStateList(tokenGo)
	if err != nil {
		bridgeLog(fmt.Sprintf("[SyncVehicles] accountID=%d GetStateList ERROR: %v", accID, err))
		return C.CString(errJSON(err))
	}

	// 保存车辆到数据库
	var vehicles []*store.Vehicle
	for _, v := range stateData.Bzclxx {
		// 取最新进京证信息
		var status, jjzzlmc, yxqs, yxqz, jsrxm, applyID, shsbyyms string
		var blzt int
		if len(v.Bzxx) > 0 {
			latest := v.Bzxx[0]
			status = latest.Blztmc
			jjzzlmc = latest.Jjzzlmc
			yxqs = latest.Yxqs
			yxqz = latest.Yxqz
			jsrxm = latest.Jsrxm
			applyID = latest.ApplyID
			blzt = latest.Blzt
			shsbyyms = latest.Shsbyyms
		}
		veh, err := db.UpsertVehicle(accID, v.VID, v.Hphm, v.Hpzl, v.Ybcs, v.Sycs, v.Syts, status, jjzzlmc, yxqs, yxqz, jsrxm, applyID, blzt, shsbyyms)
		if err != nil {
			bridgeLog(fmt.Sprintf("[SyncVehicles] UpsertVehicle vID=%s ERROR: %v", v.VID, err))
			continue
		}
		// 同步二次办证
		if len(v.Ecbzxx) > 0 {
			ec := v.Ecbzxx[0]
			if err := db.UpdateVehicle(veh.ID, map[string]interface{}{
				"ec_blzt":     ec.Blzt,
				"ec_yxqs":     ec.Yxqs,
				"ec_yxqz":     ec.Yxqz,
				"ec_jjzzlmc":  ec.Jjzzlmc,
				"ec_shsbyyms": ec.Shsbyyms,
			}); err != nil {
				bridgeLog(fmt.Sprintf("[SyncVehicles] ec update vehicleID=%d ERROR: %v", veh.ID, err))
			}
		}
		vehicles = append(vehicles, veh)
	}

	return C.CString(okJSON("vehicles", vehicles))
}

// RecognizeCaptcha 用 ONNX solver 识别 base64 编码的验证码图片。
//
// 前端手动登录页会先调 FetchCaptcha 拿图，再调本函数识别；识别结果回填后再调
// LoginWithCaptcha 完成登录。Go 侧后台 scheduler 走 Login (bjt 包内部) 时不经过此函数。
//
// Args:
//   base64Image: base64 编码的图片。
//
// Returns:
//   *C.char: {"success":true,"text":"3fk9"} 或 {"success":false,"error":"..."}。
//
//export RecognizeCaptcha
func RecognizeCaptcha(base64Image *C.char) *C.char {
	if captchaSolver == nil {
		bridgeLog("[RecognizeCaptcha] solver is nil")
		return C.CString(errJSON(fmt.Errorf("验证码识别器未初始化")))
	}
	text, err := captchaSolver.RecognizeBase64(C.GoString(base64Image))
	if err != nil {
		bridgeLog(fmt.Sprintf("[RecognizeCaptcha] ERROR: %v", err))
		return C.CString(errJSON(err))
	}
	result, _ := json.Marshal(map[string]interface{}{
		"success": true,
		"text":    text,
	})
	return C.CString(string(result))
}

// RunCheckWithCredentials 主入口：由 Kotlin/Flutter 传入所有账号的解密后凭据 + 新 token 回调地址。
//
// credentialsJSON 结构：
//   {
//     "credentials": {"phone1": {"password": "...", "token": "..."}, "phone2": {...}},
//     "refreshedTokenCallbackFile": "/data/user/0/.../refresh_tokens.jsonl"
//   }
//
// refreshedTokenCallbackFile 是一个 JSONL 文件路径：token 过期重登时，Go 把
// {"account_id": N, "token": "..."} 追加写入；Kotlin 在检查返回后读取此文件，
// 用 Keystore 加密后写回 accounts.token，再删除文件。这样明文 token 只经过 Go 内存和这个短命的 JSONL。
//
// Args:
//   credentialsJSON: 上述结构的 JSON 字符串。
//
// Returns:
//   *C.char: 序列化的 TaskResult，或错误 JSON。
//
// Notes:
//   与 runCheckMu 共享同一把锁；前台 + 后台并发触发时排队。
//
//export RunCheckWithCredentials
func RunCheckWithCredentials(credentialsJSON *C.char) *C.char {
	if db == nil {
		return C.CString(errJSON(fmt.Errorf("db is nil")))
	}
	var input struct {
		Credentials                map[string]scheduler.Credentials `json:"credentials"`
		RefreshedTokenCallbackFile string                           `json:"refreshedTokenCallbackFile"`
	}
	if err := json.Unmarshal([]byte(C.GoString(credentialsJSON)), &input); err != nil {
		return C.CString(errJSON(fmt.Errorf("解析 credentials 失败: %w", err)))
	}

	// 串行化：与 RunCheck 共享同一把锁；前台 + 后台并发触发时排队
	runCheckMu.Lock()
	defer runCheckMu.Unlock()

	persister := &filePersister{path: input.RefreshedTokenCallbackFile}
	result, err := scheduler.ExecuteCheck(db, bepClient, captchaSolver, input.Credentials, persister)
	if err != nil {
		bridgeLog(fmt.Sprintf("[RunCheckWithCredentials] ERROR: %v", err))
		return C.CString(errJSON(err))
	}
	return C.CString(jsonStr(result))
}

// filePersister 实现 scheduler.TokenPersister：把刷新后的 token 追加写到 JSONL 文件。
//
// 生命周期极短（几秒）：Go 写入 → 检查执行完 → Kotlin CredentialsGateway 读取加密 → 删除文件。
// 期间明文 token 只在这个受权限保护的应用私有目录里，不会被其他应用访问。
type filePersister struct {
	path string
}

// SaveRefreshedToken 把 (accountID, plainToken) 追加为一行 JSON 写入回调文件。
//
// Args:
//   accountID:  刚刷 token 的账号主键。
//   plainToken: 新明文 accessToken。
//
// Returns:
//   error: 路径未指定 / 打开文件 / 写入失败。
//
// Notes:
//   文件权限 0600 —— 只有应用自身能读写，Android 系统会额外用 UID 隔离。
func (p *filePersister) SaveRefreshedToken(accountID int64, plainToken string) error {
	if p.path == "" {
		return fmt.Errorf("未指定 refreshedTokenCallbackFile")
	}
	entry := map[string]interface{}{"account_id": accountID, "token": plainToken}
	line, _ := json.Marshal(entry)
	f, err := os.OpenFile(p.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		bridgeLog(fmt.Sprintf("[filePersister] open %s ERROR: %v", p.path, err))
		return fmt.Errorf("open %s: %w", p.path, err)
	}
	defer f.Close()
	if _, err := f.Write(append(line, '\n')); err != nil {
		bridgeLog(fmt.Sprintf("[filePersister] write %s ERROR: %v", p.path, err))
		return fmt.Errorf("write %s: %w", p.path, err)
	}
	return nil
}

// FreeStr 释放由 //export 函数返回的 *C.char 字符串。
//
// 调用方（Dart / Kotlin JNI）拿到返回值后必须调此函数释放，否则每次调用都会泄漏 KB 级堆。
// 内存归属：C.CString 分配在 C 堆（malloc），Go 侧无法直接 free —— 走 C.free。
//
// Args:
//   s: //export 函数返回的字符串指针。
//
//export FreeStr
func FreeStr(s *C.char) {
	C.free(unsafe.Pointer(s))
}

// errJSON 把 Go error 序列化成前端可见的 JSON 错误对象。
//
// 双写：内部错误全文写日志（含堆栈/路径/SQL 片段），只对外返回分类过的用户友好消息，
// 避免暴露内部实现细节。
//
// Args:
//   err: 底层错误。
//
// Returns:
//   string: {"success":false,"error":"用户可见文案"}。
func errJSON(err error) string {
	bridgeLog("ERROR: " + err.Error())
	b, _ := json.Marshal(map[string]interface{}{"success": false, "error": classifyError(err)})
	return string(b)
}

// classifyError 把内部错误分类成用户可见的简短提示。
//
// 分类规则按业务优先级：验证码/密码/重复/参数缺失等错误用户需要看到具体消息；
// 网络/DB/超时类错误统一显示"网络连接失败"等无泄漏文案。
//
// Args:
//   err: 底层错误。
//
// Returns:
//   string: 分类后的用户友好文案。err 为 nil 返回空串。
func classifyError(err error) string {
	if err == nil {
		return ""
	}
	msg := err.Error()
	switch {
	case strings.Contains(msg, "验证码"):
		return "验证码错误，请重试"
	case strings.Contains(msg, "密码") || strings.Contains(msg, "用户名"):
		return "手机号或密码错误"
	case strings.Contains(msg, "已添加") || strings.Contains(msg, "重复"):
		return msg // 添加账号重复类信息用户需要看到
	case strings.Contains(msg, "缺少"):
		return msg // 参数缺失类信息用户需要看到
	case strings.Contains(msg, "timeout") || strings.Contains(msg, "deadline") ||
		strings.Contains(msg, "network") || strings.Contains(msg, "connection"):
		return "网络连接失败，请检查网络"
	case strings.Contains(msg, "未找到") || strings.Contains(msg, "not found"):
		return "记录不存在"
	case strings.Contains(msg, "not allowed"):
		return "内部错误：不允许的操作"
	default:
		return "操作失败，请稍后重试"
	}
}

// jsonStr 把任意结构 marshal 成 JSON 字符串。
//
// 忽略错误：Go marshal 一个非循环结构不会失败，忽略 err 简化调用。
//
// Args:
//   v: 待序列化的对象。
//
// Returns:
//   string: JSON 字符串。
func jsonStr(v interface{}) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// okJSON 组装 {"success":true, <key>: <data>} 形式的成功响应。
//
// Args:
//   key:  内层字段名（"accounts" / "vehicles" 等）。
//   data: 该字段对应的数据。
//
// Returns:
//   string: JSON 字符串。
func okJSON(key string, data interface{}) string {
	b, _ := json.Marshal(map[string]interface{}{"success": true, key: data})
	return string(b)
}

// main 是 cgo c-shared 编译模式必需的空 main —— 生成的是共享库而不是可执行文件。
func main() {}
