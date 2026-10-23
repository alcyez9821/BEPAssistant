// Copyright (c) 2026 alcyez <alcyez9821@gmail.com>
// SPDX-License-Identifier: MIT

// Package util 提供跨包共用的小工具函数。
//
// File: util.go
// Purpose: 汇总不属于任何具体业务模块、被多个包引用的通用小函数。
// Author: alcyez (AI Agent 辅助生成注释)
// Created: 2026-07-06
//
// Business Context:
//   业务里许多字段可能是空串（例如 jjmd 进京目的、cllx 车辆类型），但北京交警 API
//   要求必填、且需要落到具体的默认值上。为避免每个调用点都重复一遍 if-else 判空，
//   在此提供 OrDefault 兜底函数。
//
// Key Logic:
//   保持极简：只放纯函数（无状态、无副作用），不引入其他 internal 包依赖。
package util

// OrDefault 返回非空字符串，若 s 为空则回退到 def。
//
// 应用场景：
//   - 进京证申请前，jjmd（进京目的）缺失时用 "其它" 兜底
//   - 车辆 cllx（车辆类型）缺失时用 "01" 兜底
//   - 六环内外描述字段（elzmc / ylzmc）API 未返回时用固定文案兜底
//
// Args:
//   s:   实际值（可能为空串）。
//   def: 备用默认值。
//
// Returns:
//   若 s 非空则返回 s，否则返回 def。
func OrDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}
