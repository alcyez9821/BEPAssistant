// Copyright (c) 2026 alcyez <alcyez9821@gmail.com>
// SPDX-License-Identifier: MIT

// Package bep 封装北京交警 (jjz.jtgl.beijing.gov.cn) API 调用。
//
// File: client.go
// Purpose: 通用 HTTP client + 请求/响应封装；Post / PostRaw 供 query.go / apply.go 使用。
// Author: alcyez (AI Agent 辅助生成注释)
// Created: 2026-07-06
//
// Business Context:
//   北京交警所有接口都是同一个前缀（BaseURL）+ Authorization + source header 的 POST，
//   Body 是 JSON，响应也是统一的 {code, msg, data} 包装。抽出 Client 消除各接口的
//   HTTP 样板代码，只暴露业务参数给上层。
//
// Key Logic:
//   1. Post: 解 data 到调用方指定的 result 结构（业务字段直接反序列化）
//   2. PostRaw: 保留完整 APIResponse，用于 ApplyForPermit 需要透传 code/msg/data 的场景
//   3. code != 200 一律转成 error "API error (code=N): msg"
package bep

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

const (
	// BaseURL 北京交警 API 主机名（HTTPS 强制端口 2443）
	BaseURL = "https://jjz.jtgl.beijing.gov.cn:2443"
	// State SSO 交换 accessToken 时的 state 参数（服务端约定；与 bjt 包同值）
	State = "101000004071"
	// Source 客户端标识 header 值（服务端约定；固定）
	Source = "99c4g1a438jgf412sa3xvckd43256h7g"
)

// APIResponse 北京交警 API 通用响应结构。
//
// 字段说明：
//   Code: 200=成功，其他值都是业务错误；msg 里带描述
//   Msg:  错误描述（成功时通常是空串或"成功"）
//   Data: 具体业务数据（各接口结构不同，用 RawMessage 延迟反序列化）
type APIResponse struct {
	Code int             `json:"code"`
	Msg  string          `json:"msg"`
	Data json.RawMessage `json:"data"`
}

// Client 北京交警 API 客户端。
//
// 一个 Client 实例被整个进程共享（bridge.go 里 New 一次），内部 http.Client 15s 超时，
// 复用 TCP 连接。并发调用安全（http.Client 内部本身安全）。
type Client struct {
	httpClient *http.Client
}

// New 创建 Client，默认 15 秒超时。
//
// Returns:
//   *Client: 立即可用的客户端实例。
func New() *Client {
	return &Client{
		httpClient: &http.Client{Timeout: 15 * time.Second},
	}
}

// Post 发送 POST 请求并把 data 字段反序列化到 result。
//
// Args:
//   path:   相对路径（不含 BaseURL），例如 "/pro//applyRecordController/stateList"。
//   body:   请求体（任意可 json.Marshal 的结构），nil 时发送空 Body。
//   token:  JJZ accessToken；非空时会自动加 Authorization + source 两个 header。
//   result: data 字段的目标结构指针；nil 表示不关心响应内容（只判断 code 是否 200）。
//
// Returns:
//   error: 请求 marshal / HTTP / 响应 unmarshal / code != 200 任一失败。
//
// Notes:
//   token 为空时不加 Authorization —— 极少数无认证接口（登录相关）需要此形式，业务大部分需带 token。
func (c *Client) Post(path string, body interface{}, token string, result interface{}) error {
	return c.do(http.MethodPost, path, body, token, result)
}

// do 实际执行 HTTP 请求；method 目前只用 POST，抽出 method 参数便于将来支持 GET。
//
// Args:
//   method: HTTP 方法（当前只用 http.MethodPost）。
//   path:   相对路径。
//   body:   请求体。
//   token:  accessToken。
//   result: data 目标结构。
//
// Returns:
//   error: 各阶段错误；错误消息含足够上下文（例如 status code、body 片段）便于排查。
func (c *Client) do(method, path string, body interface{}, token string, result interface{}) error {
	var bodyReader io.Reader
	if body != nil {
		jsonBytes, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("marshal request body: %w", err)
		}
		bodyReader = bytes.NewReader(jsonBytes)
	}

	req, err := http.NewRequest(method, BaseURL+path, bodyReader)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", token)
		req.Header.Set("source", Source)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("http request: %w", err)
	}
	defer resp.Body.Close()

	respBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read response: %w", err)
	}

	var apiResp APIResponse
	if err := json.Unmarshal(respBytes, &apiResp); err != nil {
		return fmt.Errorf("unmarshal response: %w (body: %s)", err, string(respBytes))
	}

	if apiResp.Code != 200 {
		return fmt.Errorf("API error (code=%d): %s", apiResp.Code, apiResp.Msg)
	}

	if result != nil && len(apiResp.Data) > 0 {
		if err := json.Unmarshal(apiResp.Data, result); err != nil {
			return fmt.Errorf("unmarshal data: %w", err)
		}
	}

	return nil
}

// PostRaw 发送 POST 请求，返回完整 APIResponse（含 code / msg / data 三段）。
//
// 用于调用方需要看到完整响应的场景 —— ApplyForPermit 提交后想把服务端的 msg 传回 UI
// （例如"申请成功，请到 xxx"），不能用 Post 因为 Post 只把 data 部分给调用方。
//
// Args:
//   path:  相对路径。
//   body:  请求体。
//   token: accessToken。
//
// Returns:
//   *APIResponse: 完整响应。
//   error:        网络 / marshal / unmarshal / code != 200 任一失败。
func (c *Client) PostRaw(path string, body interface{}, token string) (*APIResponse, error) {
	var bodyReader io.Reader
	if body != nil {
		jsonBytes, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("marshal request body: %w", err)
		}
		bodyReader = bytes.NewReader(jsonBytes)
	}

	req, err := http.NewRequest(http.MethodPost, BaseURL+path, bodyReader)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", token)
		req.Header.Set("source", Source)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("http request: %w", err)
	}
	defer resp.Body.Close()

	respBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	var apiResp APIResponse
	if err := json.Unmarshal(respBytes, &apiResp); err != nil {
		return nil, fmt.Errorf("unmarshal response: %w (body: %s)", err, string(respBytes))
	}

	if apiResp.Code != 200 {
		return nil, fmt.Errorf("API error (code=%d): %s", apiResp.Code, apiResp.Msg)
	}

	return &apiResp, nil
}
