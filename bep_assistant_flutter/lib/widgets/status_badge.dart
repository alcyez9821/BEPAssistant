// Copyright (c) 2026 alcyez <alcyez9821@gmail.com>
// SPDX-License-Identifier: MIT
//
// File: widgets/status_badge.dart
// Purpose: 状态徽章 —— 车辆卡片右上角的圆角标签（"正常"/"即将到期"/"已过期"）。
// Author: alcyez (AI Agent 辅助生成注释)
// Created: 2026-07-06
//
// Business Context:
//   与 Vehicle.statusColor 配合：statusColor 返回 "green"/"yellow"/"red"/"gray" 四种，
//   本 Widget 把颜色名映射到浅色背景 + 深色文字，保证在浅色主题下可读。
import 'package:flutter/material.dart';

/// 圆角状态徽章 Widget。
///
/// 用于车辆卡片的到期状态、审核状态等场景 —— 面积极小、颜色区分度高。
class StatusBadge extends StatelessWidget {
  /// 颜色标识：'green' / 'yellow' / 'red' / 'gray'；其他值会 fallback 到灰色。
  final String color;

  /// 显示文字（一般 2-4 个字）。
  final String text;

  const StatusBadge({super.key, required this.color, required this.text});

  @override
  Widget build(BuildContext context) {
    return Container(
      padding: const EdgeInsets.symmetric(horizontal: 8, vertical: 2),
      decoration: BoxDecoration(
        color: _bgColor,
        borderRadius: BorderRadius.circular(12),
      ),
      child: Text(text, style: TextStyle(color: _textColor, fontSize: 12)),
    );
  }

  /// 背景色：使用 Material 颜色的 shade50（最浅），保证前景文字可读。
  Color get _bgColor {
    switch (color) {
      case 'green':
        return Colors.green.shade50;
      case 'yellow':
        return Colors.orange.shade50;
      case 'red':
        return Colors.red.shade50;
      default:
        return Colors.grey.shade100;
    }
  }

  /// 前景色：使用 Material 颜色的 shade600/shade700，与浅底形成足够对比。
  Color get _textColor {
    switch (color) {
      case 'green':
        return Colors.green.shade700;
      case 'yellow':
        return Colors.orange.shade700;
      case 'red':
        return Colors.red.shade700;
      default:
        return Colors.grey.shade600;
    }
  }
}
