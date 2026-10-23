// Copyright (c) 2026 alcyez <alcyez9821@gmail.com>
// SPDX-License-Identifier: MIT

// File: login.go
// Purpose: 北京通 (BJT) 密码登录 6 步流程 + 交换北京交警 JJZ accessToken。
// Author: alcyez (AI Agent 辅助生成注释)
// Created: 2026-07-06
//
// Business Context:
//   北京交警 (jjz.jtgl.beijing.gov.cn) 不接受直接密码登录，必须先通过北京通拿 bjtToken，
//   再用 bjtToken 换取 JJZ accessToken。这是双方约定的 SSO 流程：
//     BJT 用户体系 (bjt.beijing.gov.cn) ─→ portal.bjt.beijing.gov.cn 回调 token ─→
//     北京交警 (jjz.jtgl.beijing.gov.cn:2443) 用这个 token 换自己的 accessToken
//
// Key Logic (6 步流程):
//   Step 1 (getPublicKey):     GET goUserLogin 页拿服务器 RSA 公钥
//   Step 2 (solveCaptcha):     GET generateCaptcha 拿验证码图片 → 识别
//   Step 3 (encryptPassword):  MD5(password) 再用 RSA/PKCS1v15 加密 JSON payload
//   Step 4 (doLogin):          POST doUserLoginByPwd 提交 encryptData + captcha → 拿 redirectUrl
//   Step 5 (extractBjtToken):  GET redirectUrl 不跟随重定向 → Location 头里的 token=xxx
//   Step 6 (exchangeJjzToken): POST 北京交警 loginUserByUserCenter → 拿 JJZ accessToken
//
// Notes:
//   - LoginWithSession：外部已生成 http.Client + 已识别的验证码（配合 Flutter 手动输入路径）
//   - Login：自己新建 client + 自动 solver 识别验证码（内部代码路径）
//   - LoginWithRetry：验证码错误自动重试（最多 3 次；密码错则立即返回）
//   - jjzState 是北京交警的 SSO state 值，是硬编码常量（服务端约定）
//   - source 是北京交警要求的 x-source header 值，用于识别客户端
//
// Anti-Lockout:
//   北京通登录失败 5 次后账号会被锁 1 小时（2026-07-02 事故触发过）。因此：
//     - LoginWithRetry 严格限制 maxRetries≤3 且只对验证码错误重试
//     - Kotlin/Flutter 前端不再叠加额外重试（避免 Go 3 次 × 前端 5 次 = 15 次触发锁定）
package bjt

import (
	"crypto/md5"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"regexp"
	"strings"
	"time"
)

const (
	// jjzState 北京交警 SSO 的 state 参数，服务端约定的固定值
	jjzState = "101000004071"
	// source 北京交警要求的客户端标识 header，用于区分 App 来源；固定值
	source = "99c4g1a438jgf412sa3xvckd43256h7g"
)

// LoginResult 登录结果，字段与前端 JSON 保持一致。
//
// 字段说明：
//   Success:  是否登录成功
//   Token:    JJZ accessToken（登录成功时非空；北京交警 API 需要）
//   CertName: 认证的实名姓名
//   Mobile:   手机号（登录用的号码回显）
//   CertNo:   身份证号
//   Error:    仅在失败时填入的错误消息（当前实现未使用，保留兼容字段）
type LoginResult struct {
	Success  bool   `json:"success"`
	Token    string `json:"token"`
	CertName string `json:"certName"`
	Mobile   string `json:"mobile"`
	CertNo   string `json:"certNo"`
	Error    string `json:"error,omitempty"`
}

// LoginWithSession 使用外部提供的 http.Client 和已识别的验证码完成登录。
//
// 使用场景：Flutter 手动登录（FetchCaptcha 已经拿了图片、UI 上让用户看/程序自动识别后
// 传回来）。session 必须已经带上 FetchCaptcha 时服务端下发的 cookie，否则验证码会失效。
//
// Args:
//   phone:       手机号。
//   password:    明文密码（登录后立即忘记；不落盘）。
//   captchaText: 已识别的验证码文字（例如 "3fk9"）。
//   session:     承载登录 cookie 的 http.Client；FetchCaptcha 时已在里面写入 sessionID。
//
// Returns:
//   *LoginResult: 登录成功的结果。
//   error:        任一步骤失败（公钥获取 / 加密 / 登录 / redirect token 提取 / JJZ 换 token）。
//
// Notes:
//   Step 4 会新建一个不跟随重定向的临时 client（复用同一 cookie jar），是因为 Go 默认
//   client 会自动跟 302，但北京通的 Location 头里带的 token 是我们需要的 —— 必须手动读取。
func LoginWithSession(phone, password, captchaText string, session *http.Client) (*LoginResult, error) {
	// Step 1: 获取 pubKey（复用 session 的 cookie）
	pubKey, err := getPublicKey(session)
	if err != nil {
		return nil, fmt.Errorf("获取公钥失败: %w", err)
	}

	// Step 2: 加密密码
	encryptedData, err := encryptPassword(phone, password, pubKey)
	if err != nil {
		return nil, fmt.Errorf("加密密码失败: %w", err)
	}

	// Step 3: POST 登录（使用已提供的验证码）
	redirectURL, err := doLogin(session, encryptedData, captchaText)
	if err != nil {
		return nil, fmt.Errorf("登录失败: %w", err)
	}

	// Step 4: 从重定向获取 bjtToken —— 必须不跟随重定向，让 Location 头暴露 token
	jar := session.Jar
	noRedirect := &http.Client{
		Timeout: 15 * time.Second,
		Jar:     jar,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	bjtToken, err := extractBjtToken(noRedirect, redirectURL)
	if err != nil {
		return nil, fmt.Errorf("获取 bjtToken 失败: %w", err)
	}

	// Step 5: 交换 JJZ accessToken
	result, err := exchangeJjzToken(bjtToken)
	if err != nil {
		return nil, fmt.Errorf("交换 JJZ token 失败: %w", err)
	}

	return result, nil
}

// Login 完整登录流程（自动获取并识别验证码），返回 JJZ accessToken。
//
// 使用场景：后台自动重登（token 过期时 scheduler.ensureToken 调用），或 CLI 测试工具。
//
// Args:
//   phone:    手机号。
//   password: 明文密码。
//   solver:   验证码识别器（外部注入以便测试；生产用 captcha.Solver）。
//
// Returns:
//   *LoginResult: 登录结果。
//   error:        任一步骤失败。
//
// Notes:
//   自建 cookie jar + 两个 client（跟随重定向 / 不跟随）；生命周期只在本函数内。
func Login(phone, password string, solver CaptchaSolver) (*LoginResult, error) {
	// 使用 cookie jar 保持会话（Python requests.Session() 的等价物）
	jar, _ := cookiejar.New(nil)
	session := &http.Client{
		Timeout: 15 * time.Second,
		Jar:     jar,
	}
	// 不跟随重定向的客户端（仅用于提取 redirect token）
	noRedirect := &http.Client{
		Timeout: 15 * time.Second,
		Jar:     jar,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	// Step 1: 获取 pubKey
	pubKey, err := getPublicKey(session)
	if err != nil {
		return nil, fmt.Errorf("获取公钥失败: %w", err)
	}

	// Step 2: 获取并识别验证码
	captchaText, err := solveCaptcha(session, solver)
	if err != nil {
		return nil, fmt.Errorf("验证码识别失败: %w", err)
	}

	// Step 3: 加密密码
	encryptedData, err := encryptPassword(phone, password, pubKey)
	if err != nil {
		return nil, fmt.Errorf("加密密码失败: %w", err)
	}

	// Step 4: POST 登录
	redirectURL, err := doLogin(session, encryptedData, captchaText)
	if err != nil {
		return nil, fmt.Errorf("登录失败: %w", err)
	}

	// Step 5: 从重定向获取 bjtToken
	bjtToken, err := extractBjtToken(noRedirect, redirectURL)
	if err != nil {
		return nil, fmt.Errorf("获取 bjtToken 失败: %w", err)
	}

	// Step 6: 交换 JJZ accessToken
	result, err := exchangeJjzToken(bjtToken)
	if err != nil {
		return nil, fmt.Errorf("交换 JJZ token 失败: %w", err)
	}

	return result, nil
}

// LoginWithRetry 带重试的登录，验证码识别错误时自动重试。
//
// Args:
//   phone:       手机号。
//   password:    明文密码。
//   solver:      验证码识别器。
//   maxRetries:  最大重试次数（≤0 时用默认 3）；重试次数包括第一次尝试。
//
// Returns:
//   *LoginResult: 登录成功的结果。
//   error:        非验证码错误立即返回（例如密码错、网络错）；连续 maxRetries 次验证码
//                 错误后返回 "登录失败（已重试 N 次）: ..."。
//
// Notes:
//   - 只对错误消息含"验证码"的失败重试；其他错误（密码错、账号锁定等）立即返回，避免继续尝试
//     被服务端认为是暴力破解从而锁号（2026-07-02 事故根因）。
//   - 上层 Flutter 前端已知晓此限制，不再叠加额外的登录重试。
func LoginWithRetry(phone, password string, solver CaptchaSolver, maxRetries int) (*LoginResult, error) {
	if maxRetries <= 0 {
		maxRetries = 3
	}
	var lastErr error
	for i := 0; i < maxRetries; i++ {
		result, err := Login(phone, password, solver)
		if err == nil {
			return result, nil
		}
		lastErr = err
		// 验证码错误可以重试
		if !strings.Contains(err.Error(), "验证码") {
			return nil, err
		}
	}
	return nil, fmt.Errorf("登录失败（已重试 %d 次）: %w", maxRetries, lastErr)
}

// getPublicKey Step 1：从 goUserLogin 页拿服务器 RSA 公钥。
//
// 北京通把 pubKey 藏在 goUserLogin 的最终 URL query 参数里（redirect 到登录页后带 pubKey=xxx）。
// 极少数情况会藏在 HTML body 里，此时 fallback 用正则从响应体挖出来。
//
// Args:
//   client: HTTP client（要求带 cookie jar，此步骤会种下必要的 session cookie）。
//
// Returns:
//   string: base64 编码的 RSA 公钥（PKIX 格式）。
//   error:  请求失败、pubKey 未找到。
//
// Notes:
//   URL 解码时不用 url.QueryUnescape —— 后者会把 base64 里的 '+' 变成空格；只手工处理
//   %2B / %2F / %3D 三个 base64 合法字符的百分号编码。
func getPublicKey(client *http.Client) (string, error) {
	params := url.Values{
		"client_id":     {"100100001423"},
		"redirect_uri":  {"https://ssp.jtgl.beijing.gov.cn/sso/callback/userauth"},
		"response_type": {"code"},
		"scope":         {"user_info"},
		"state":         {"100009"},
	}
	reqURL := "https://bjt.beijing.gov.cn/renzheng/open/m/login/goUserLogin?" + params.Encode()

	req, err := http.NewRequest("GET", reqURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (iPhone; CPU iPhone OS 16_0 like Mac OS X) AppleWebKit/605.1.15")

	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	// 从最终 URL 中提取 pubKey
	finalURL := resp.Request.URL.String()
	log.Printf("[BJT] finalURL: %s", finalURL)

	re := regexp.MustCompile(`pubKey=([^&]+)`)
	m := re.FindStringSubmatch(finalURL)
	if len(m) < 2 {
		// 尝试从响应 HTML body 中提取
		bodyBytes, _ := io.ReadAll(resp.Body)
		bodyStr := string(bodyBytes)
		re2 := regexp.MustCompile(`pubKey["'\s:=]+([A-Za-z0-9+/=]+)`)
		m2 := re2.FindStringSubmatch(bodyStr)
		if len(m2) >= 2 {
			return m2[1], nil
		}
		return "", fmt.Errorf("无法获取 pubKey")
	}
	// 不用 url.QueryUnescape — 它会把 base64 中的 '+' 转成空格
	// 只处理 %XX 编码，保留 '+' 作为 base64 合法字符
	pubKey := m[1]
	pubKey = strings.ReplaceAll(pubKey, "%2B", "+")
	pubKey = strings.ReplaceAll(pubKey, "%2F", "/")
	pubKey = strings.ReplaceAll(pubKey, "%3D", "=")
	log.Printf("[BJT] pubKey: %s", pubKey[:min(40, len(pubKey))])
	return pubKey, nil
}

// solveCaptcha Step 2：调 generateCaptcha 拿验证码 PNG，交给 solver 识别。
//
// Args:
//   client: HTTP client（cookie jar 里会写入验证码 sessionID）。
//   solver: 验证码识别器。
//
// Returns:
//   string: 识别文字。
//   error:  请求 / 读取 / 识别失败。
func solveCaptcha(client *http.Client, solver CaptchaSolver) (string, error) {
	req, err := http.NewRequest("GET", "https://bjt.beijing.gov.cn/renzheng/common/generateCaptcha", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (iPhone; CPU iPhone OS 16_0 like Mac OS X) AppleWebKit/605.1.15")
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	imgBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

	return solver.Recognize(imgBytes)
}

// encryptPassword Step 3：MD5(password) → RSA/PKCS1v15 加密整个 JSON payload。
//
// 加密流程与北京通前端 JS 完全一致：
//   1. password 先 MD5 → 32 位十六进制字符串
//   2. 组装 {userIdentity, encryptedPwd, resetFlag} JSON
//   3. 用服务端 RSA 公钥 PKCS1v15 加密 JSON 字节 → base64 输出
//
// Args:
//   phone:      手机号（→ userIdentity 字段）。
//   password:   明文密码。
//   pubKeyB64:  base64 编码的服务器公钥（PKIX 格式）。
//
// Returns:
//   string: base64 编码的密文，可以直接作为 form 参数 encryptData 提交。
//   error:  PEM 解析 / 公钥解析 / RSA 加密失败。
func encryptPassword(phone, password, pubKeyB64 string) (string, error) {
	// 构建 PEM
	pemData := "-----BEGIN PUBLIC KEY-----\n" + pubKeyB64 + "\n-----END PUBLIC KEY-----"
	block, _ := pem.Decode([]byte(pemData))
	if block == nil {
		return "", fmt.Errorf("解析 PEM 失败，pubKey 长度: %d", len(pubKeyB64))
	}

	pubKey, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return "", fmt.Errorf("解析公钥失败: %w", err)
	}
	rsaPubKey, ok := pubKey.(*rsa.PublicKey)
	if !ok {
		return "", fmt.Errorf("不是 RSA 公钥")
	}

	// MD5 哈希密码：北京通前端约定，防止明文密码离开客户端
	hash := md5.Sum([]byte(password))
	hashedPwd := hex.EncodeToString(hash[:])

	// 构建 JSON payload：三个字段与前端 JS 一致，resetFlag 表示不是重置密码流程
	payload := map[string]interface{}{
		"userIdentity": phone,
		"encryptedPwd": hashedPwd,
		"resetFlag":    false,
	}
	plaintext, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}

	// RSA PKCS1v15 加密（服务端指定的填充方式；不是 OAEP）
	encrypted, err := rsa.EncryptPKCS1v15(rand.Reader, rsaPubKey, plaintext)
	if err != nil {
		return "", fmt.Errorf("RSA 加密失败: %w", err)
	}

	return base64.StdEncoding.EncodeToString(encrypted), nil
}

// doLogin Step 4：POST doUserLoginByPwd 提交加密后的凭据 + 验证码。
//
// 成功时服务器返回 meta.code=0 + data.redirectUrl；redirectUrl 里带 code=xxx 参数，
// 下一步用来换 bjtToken。失败时 meta.message 会是"验证码错误"/"密码错误"等文案，
// LoginWithRetry 依赖此文案判断是否重试。
//
// Args:
//   client:        HTTP client（要求带 cookie jar，session 已在 goUserLogin 阶段建立）。
//   encryptedData: encryptPassword 返回的 base64 密文。
//   captcha:       识别后的验证码文字。
//
// Returns:
//   string: 服务器给出的 redirectUrl。
//   error:  网络错误 / 响应解析失败 / meta.code != 0。
//
// Notes:
//   Referer / Origin 头必须是 portal.bjt.beijing.gov.cn，否则服务器拒绝请求。
func doLogin(client *http.Client, encryptedData, captcha string) (string, error) {
	form := url.Values{
		"encryptData": {encryptedData},
		"captcha":     {captcha},
	}

	req, err := http.NewRequest("POST",
		"https://bjt.beijing.gov.cn/renzheng/inner/m/login/doUserLoginByPwd",
		strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded;charset=UTF-8")
	req.Header.Set("Origin", "https://portal.bjt.beijing.gov.cn")
	req.Header.Set("Referer", "https://portal.bjt.beijing.gov.cn/")

	startTs := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("请求失败 (cost=%v): %w", time.Since(startTs), err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	cost := time.Since(startTs)

	var result struct {
		Meta struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"meta"`
		Data struct {
			RedirectURL string `json:"redirectUrl"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return "", fmt.Errorf("解析响应失败 (status=%d cost=%v body=%s)", resp.StatusCode, cost, string(body))
	}

	if result.Meta.Code != "0" {
		return "", fmt.Errorf("%s [cost=%v code=%s body=%s]", result.Meta.Message, cost, result.Meta.Code, string(body))
	}

	if result.Data.RedirectURL == "" {
		return "", fmt.Errorf("响应中没有 redirectUrl")
	}

	return result.Data.RedirectURL, nil
}

// extractBjtToken Step 5：不跟随重定向 GET redirectURL，从 Location 头正则提取 token=xxx。
//
// Args:
//   client:      不跟随重定向的 client（CheckRedirect 返回 http.ErrUseLastResponse）。
//   redirectURL: doLogin 返回的 redirect 地址。
//
// Returns:
//   string: bjtToken。
//   error:  请求失败或 Location 头没有 token 字段。
func extractBjtToken(client *http.Client, redirectURL string) (string, error) {
	resp, err := client.Get(redirectURL)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	location := resp.Header.Get("Location")
	re := regexp.MustCompile(`token=([^&]+)`)
	m := re.FindStringSubmatch(location)
	if len(m) < 2 {
		return "", fmt.Errorf("无法从重定向中提取 token: %s", location)
	}
	return m[1], nil
}

// exchangeJjzToken Step 6：用 bjtToken 从北京交警 SSO 换 JJZ accessToken + 实名信息。
//
// 请求 jjz.jtgl.beijing.gov.cn:2443/auth/userController/loginUserByUserCenter，Body 是
// {token, state}；state 是硬编码的 jjzState 常量。成功时 code=200 + data.accessToken。
//
// Args:
//   bjtToken: extractBjtToken 拿到的北京通 SSO token。
//
// Returns:
//   *LoginResult: 含 JJZ accessToken + 实名姓名 + 手机号 + 身份证号。
//   error:        请求失败 / JSON 解析失败 / 服务端 code != 200。
func exchangeJjzToken(bjtToken string) (*LoginResult, error) {
	body := fmt.Sprintf(`{"token":"%s","state":"%s"}`, bjtToken, jjzState)
	req, err := http.NewRequest("POST",
		fmt.Sprintf("https://jjz.jtgl.beijing.gov.cn:2443/auth/userController/loginUserByUserCenter?state=%s", jjzState),
		strings.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("source", source)

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)

	var apiResp struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
		Data struct {
			AccessToken string `json:"accessToken"`
			CertName    string `json:"certName"`
			Mobile      string `json:"mobile"`
			CertNo      string `json:"certNo"`
		} `json:"data"`
	}
	if err := json.Unmarshal(respBody, &apiResp); err != nil {
		return nil, fmt.Errorf("解析 JJZ 响应失败: %s", string(respBody))
	}
	if apiResp.Code != 200 {
		return nil, fmt.Errorf("JJZ 登录失败: %s", apiResp.Msg)
	}

	return &LoginResult{
		Success:  true,
		Token:    apiResp.Data.AccessToken,
		CertName: apiResp.Data.CertName,
		Mobile:   apiResp.Data.Mobile,
		CertNo:   apiResp.Data.CertNo,
	}, nil
}
