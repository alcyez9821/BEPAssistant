// Copyright (c) 2026 alcyez <alcyez9821@gmail.com>
// SPDX-License-Identifier: MIT

// Package captcha 提供离线的图片验证码识别器（ONNX Runtime + ddddocr 兼容模型）。
//
// File: solver.go
// Purpose: 加载 ONNX 模型 + 字符集 → 输入验证码 PNG/JPG → 输出识别文字。
// Author: alcyez (AI Agent 辅助生成注释)
// Created: 2026-07-06
//
// Business Context:
//   北京通登录接口每次都要求带验证码，若走人工必然打断自动化流程。本模块内嵌
//   ddddocr 训练好的通用 CRNN 模型（CTC 解码），能识别 4 位字母数字组合，
//   离线运行，无需请求外部打码服务，也就避免了打码平台的账号泄漏风险。
//
// Key Logic:
//   1. New: 一次性加载 ONNX Runtime 共享库 + 反序列化 charset.json + 建立 DynamicAdvancedSession
//   2. Recognize/RecognizeBase64: 图像 → 灰度化 + 最近邻缩放到高度 64 → float32 [1,1,64,W] tensor
//   3. Run: 用 nil 输出让 ORT 自动推断输出形状（模型 metadata rank 可能失真）
//   4. CTC Greedy Decode: 每一时间步取 argmax → 跳过 blank(0) 和重复索引 → 拼字符
//
// Notes:
//   - 输入 buffer inputBuf 复用以减少 GC；不同尺寸的验证码会触发扩容
//   - Solver 使用者应保证并发调用被外部串行化（Solver 内部无锁；北京交警登录本身串行）
package captcha

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"os"

	ort "github.com/yalue/onnxruntime_go"
)

const (
	// modelHeight 模型输入高度（固定 64；宽度按原图宽高比动态计算）
	modelHeight = 64
)

// Solver 验证码识别器，使用 ONNX Runtime 推理，并维护复用的输入 buffer。
//
// 字段说明：
//   session:     ONNX Runtime 动态 session（输入形状运行时决定）
//   charset:     字符集数组，索引 0 是 CTC blank，其余对应实际字符
//   charsetSize: len(charset)，缓存避免每次调用 len()
//   inputBuf:    复用的 float32 输入 buffer，避免每次识别都重新分配
type Solver struct {
	session     *ort.DynamicAdvancedSession
	charset     []string
	charsetSize int
	inputBuf    []float32
}

// New 创建验证码识别器。
//
// 关键步骤：
//   1. 设置 ONNX Runtime 共享库路径（Android 上是 libonnxruntime.so，与 App 一起打包）
//   2. 若 ORT 环境未初始化则初始化（幂等）
//   3. 读 charset.json 反序列化成字符列表
//   4. 读 modelPath ONNX 二进制数据，创建 DynamicAdvancedSession（输入名 "input1"，输出名 "387"）
//
// Args:
//   modelPath:   ONNX 模型文件绝对路径（通常在 assets/models/captcha.onnx，从 APK 拷贝到磁盘后传入）。
//   charsetPath: 字符集 JSON 文件绝对路径（["", "0", "1", ..., "z"] 之类）。
//   libPath:     onnxruntime.so 路径，空串时默认 "libonnxruntime.so"（依赖 System.loadLibrary 已加载）。
//
// Returns:
//   *Solver: 已初始化的识别器实例。
//   error:   模型/字符集读文件、JSON 解析、ORT 初始化任一失败。
//
// Notes:
//   - 首次调用会全局初始化 ONNX Runtime；ort.IsInitialized 已幂等保护。
func New(modelPath, charsetPath, libPath string) (*Solver, error) {
	if libPath == "" {
		libPath = "libonnxruntime.so"
	}
	ort.SetSharedLibraryPath(libPath)
	if !ort.IsInitialized() {
		if err := ort.InitializeEnvironment(); err != nil {
			return nil, fmt.Errorf("初始化 ONNX Runtime 失败: %w", err)
		}
	}

	// 加载字符集：JSON 数组，index 0 是 CTC blank，用于跳过预测
	charsetData, err := os.ReadFile(charsetPath)
	if err != nil {
		return nil, fmt.Errorf("读取字符集失败: %w", err)
	}
	var charset []string
	if err := json.Unmarshal(charsetData, &charset); err != nil {
		return nil, fmt.Errorf("解析字符集失败: %w", err)
	}

	// 加载模型：一次性读到内存里，避免运行时反复 IO
	modelData, err := os.ReadFile(modelPath)
	if err != nil {
		return nil, fmt.Errorf("读取模型失败: %w", err)
	}

	// 创建动态 session：允许输入形状在运行时变化（不同验证码图片宽度不一样）
	session, err := ort.NewDynamicAdvancedSessionWithONNXData(modelData,
		[]string{"input1"},
		[]string{"387"},
		nil,
	)
	if err != nil {
		return nil, fmt.Errorf("创建 ONNX session 失败: %w", err)
	}

	return &Solver{
		session:     session,
		charset:     charset,
		charsetSize: len(charset),
	}, nil
}

// Recognize 识别验证码图片（原始字节），实现 scheduler.CaptchaProvider 接口。
//
// Args:
//   imageBytes: 验证码图片原始字节（支持 PNG / JPEG，由 image 包自动嗅探）。
//
// Returns:
//   string: 识别出的验证码文字（一般 4-6 位字母数字）。
//   error:  图片解码 / 推理失败。
func (s *Solver) Recognize(imageBytes []byte) (string, error) {
	return s.recognizeBytes(imageBytes)
}

// RecognizeBase64 识别 base64 编码的验证码图片。
//
// 主要用于 Flutter 侧：`FetchCaptcha` 已经把图片 base64 化后返回给 Dart，
// 手动登录页调用此函数直接省一次 base64 解码往返。
//
// Args:
//   base64Image: 标准 base64 编码的图片数据。
//
// Returns:
//   string: 识别文字。
//   error:  base64 解码失败或后续识别失败。
func (s *Solver) RecognizeBase64(base64Image string) (string, error) {
	imgBytes, err := base64.StdEncoding.DecodeString(base64Image)
	if err != nil {
		return "", fmt.Errorf("base64 解码失败: %w", err)
	}
	return s.recognizeBytes(imgBytes)
}

// recognizeBytes 图像预处理 + ONNX 推理 + CTC 解码的核心实现。
//
// 处理步骤：
//   1. 图片解码 → image.Image
//   2. 计算目标宽度 newW = srcW * 64 / srcH（保持宽高比缩到高 64）
//   3. 最近邻缩放 + RGB→灰度（0.299R+0.587G+0.114B）+ 归一化到 [0,1]
//   4. 构建 [1,1,64,newW] float32 tensor，运行 ONNX session
//   5. 输出形状 [seq_len,1,charset_size]，逐时间步 argmax → CTC 去 blank/去重
//
// Args:
//   imageBytes: 图片原始字节。
//
// Returns:
//   string: 识别文字（可能是空串，当模型给出全 blank）。
//   error:  解码 / tensor 创建 / 推理失败。
//
// Notes:
//   - inputBuf 会按需扩容以适配不同图像宽度；用 cap 判断可复用 buffer。
//   - 输出 tensor 用 nil 让 runtime 分配，避免与 model metadata 声明的 rank 冲突。
func (s *Solver) recognizeBytes(imageBytes []byte) (string, error) {
	// 解码图片
	img, _, err := image.Decode(bytes.NewReader(imageBytes))
	if err != nil {
		return "", fmt.Errorf("图片解码失败: %w", err)
	}

	// 缩放到高度 64，保持宽高比
	bounds := img.Bounds()
	srcW := bounds.Dx()
	srcH := bounds.Dy()
	newW := srcW * modelHeight / srcH
	if newW < 1 {
		newW = 1
	}

	// 创建/复用灰度 float32 buffer，避免每次识别都触发一次 make
	dataLen := modelHeight * newW
	if cap(s.inputBuf) < dataLen {
		s.inputBuf = make([]float32, dataLen)
	} else {
		s.inputBuf = s.inputBuf[:dataLen]
	}

	// 最近邻缩放 + 灰度化 + 归一化到 [0, 1]
	for y := 0; y < modelHeight; y++ {
		srcY := bounds.Min.Y + y*srcH/modelHeight
		for x := 0; x < newW; x++ {
			srcX := bounds.Min.X + x*srcW/newW
			r, g, b, _ := img.At(srcX, srcY).RGBA()
			// 标准 ITU-R BT.601 亮度系数；65535 因 image.Color.RGBA 返回值域是 [0, 65535]
			gray := (float64(r)*0.299 + float64(g)*0.587 + float64(b)*0.114) / 65535.0
			s.inputBuf[y*newW+x] = float32(gray)
		}
	}

	// 创建输入 tensor [1, 1, height, width]
	inputShape := ort.NewShape(1, 1, modelHeight, int64(newW))
	inputTensor, err := ort.NewTensor(inputShape, s.inputBuf)
	if err != nil {
		return "", fmt.Errorf("创建输入 tensor 失败: %w", err)
	}
	defer inputTensor.Destroy()

	// 输出传 nil，让 runtime 自动分配正确形状（绕过 metadata rank 声明）
	outputs := []ort.Value{nil}
	if err := s.session.Run([]ort.Value{inputTensor}, outputs); err != nil {
		return "", fmt.Errorf("推理失败: %w", err)
	}
	defer outputs[0].Destroy()

	outputTensor := outputs[0].(*ort.Tensor[float32])
	outputData := outputTensor.GetData()
	outputShape := outputTensor.GetShape()

	// 输出形状: [seq_len, 1, charset_size]
	seqLen := int(outputShape[0])
	charsetSize := int(outputShape[2])
	if seqLen == 0 || charsetSize == 0 {
		return "", fmt.Errorf("无效的输出形状: %v", outputShape)
	}

	// CTC greedy decode
	var result []byte
	prevIdx := -1
	for t := 0; t < seqLen; t++ {
		offset := t * charsetSize
		maxIdx := 0
		maxVal := outputData[offset]
		for c := 1; c < charsetSize; c++ {
			if outputData[offset+c] > maxVal {
				maxVal = outputData[offset+c]
				maxIdx = c
			}
		}
		// 遇到全零行（超出实际输出）时停止：CTC 输出通常在末尾以 0 值填充
		if maxVal == 0 {
			break
		}
		// CTC 规则：跳过 blank(index 0) 和连续重复索引，即可得到去重后的字符序列
		if maxIdx != 0 && maxIdx != prevIdx {
			if maxIdx < len(s.charset) {
				result = append(result, s.charset[maxIdx]...)
			}
		}
		prevIdx = maxIdx
	}

	return string(result), nil
}

// Close 释放 ONNX session 持有的 native 资源。
//
// 一般在应用退出前调用；BEP Assistant 中 Solver 作为进程内单例常驻，
// 目前没有触发 Close 的路径。
func (s *Solver) Close() {
	if s.session != nil {
		s.session.Destroy()
	}
}
