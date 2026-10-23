// Copyright (c) 2026 alcyez <alcyez9821@gmail.com>
// SPDX-License-Identifier: MIT

// Package bjt 实现北京通（BJT / 北京市统一身份认证平台）登录流程。
//
// File: captcha.go
// Purpose: 声明验证码识别的抽象接口，让登录流程与具体识别实现（ONNX / 打码平台 / mock）解耦。
// Author: alcyez (AI Agent 辅助生成注释)
// Created: 2026-07-06
//
// Business Context:
//   北京通每次密码登录都要求图片验证码。项目内使用 internal/captcha.Solver（ddddocr ONNX）
//   离线识别，但 bjt 包不直接依赖 captcha 包 —— 避免循环 import、便于单测传 mock。
package bjt

// CaptchaSolver 验证码识别接口。
//
// 实现说明：
//   - 生产实现：internal/captcha.Solver（ONNX 离线识别）
//   - 测试可用 mock：直接内嵌预期字符串
//
// Notes:
//   实现方需保证并发安全 —— 北京通登录本身串行（每次登录一个独立 http.Client + session），
//   但 solver 单例会被前台 UI 与后台 scheduler 同时调用。
type CaptchaSolver interface {
	// Recognize 从原始图片字节识别出验证码文字。
	//
	// Args:
	//   imageBytes: PNG / JPEG 原始字节。
	//
	// Returns:
	//   string: 识别文字（可能为空串，模型置信度极低时）。
	//   error:  解码 / 推理失败。
	Recognize(imageBytes []byte) (string, error)
}
