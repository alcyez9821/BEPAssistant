// Copyright (c) 2026 alcyez <alcyez9821@gmail.com>
// SPDX-License-Identifier: MIT
//
// File: widgets/vehicle_card.dart
// Purpose: 车辆卡片 —— 单辆车在账号详情页的展示单元。
// Author: alcyez (AI Agent 辅助生成注释)
// Created: 2026-07-06
//
// Business Context:
//   一辆车最多同时有 3 组信息需要展示：当前证状态、二次办证状态、上次错误。
//   卡片按重要性从上到下排列：号牌 + 状态徽章 → 当前证详情 → 二次办证详情 →
//   缺默认值警告。代办模式下若默认值不全会显示黄色警告条，引导用户点击进入设置。
//
// Key Logic:
//   1. 提醒 / 代办模式徽章：右上角显示对应 mode 的中文名
//   2. StatusBadge 用 Vehicle.statusColor 结合 _statusText 呈现"正常/即将到期/已过期"
//   3. 二次办证只在 ecBlzt != 0 时展示（避免误判 blzt=0 的空数据为待生效）
//   4. _needsDefaults：代办模式但缺 default_area 或 default_xxdz 时显示引导
import 'package:flutter/material.dart';
import '../models/vehicle.dart';
import 'status_badge.dart';

/// 车辆卡片 Widget，展示号牌 / 状态 / 有效期 / 二次办证 / 错误 / 缺默认值等信息。
class VehicleCard extends StatelessWidget {
  final Vehicle vehicle;

  /// 卡片点击回调；一般跳转到 [_VehicleDetailSheet] 底部弹窗。
  final VoidCallback? onTap;

  const VehicleCard({
    super.key,
    required this.vehicle,
    this.onTap,
  });

  @override
  Widget build(BuildContext context) {
    final isAgent = vehicle.mode == 'agent';
    return Card(
      margin: const EdgeInsets.symmetric(horizontal: 16, vertical: 4),
      child: InkWell(
        onTap: onTap,
        borderRadius: BorderRadius.circular(12),
        child: Padding(
          padding: const EdgeInsets.all(16),
          child: Column(
            crossAxisAlignment: CrossAxisAlignment.start,
            children: [
              Row(
                children: [
                  Text(vehicle.hphm,
                      style: const TextStyle(
                          fontSize: 16, fontWeight: FontWeight.bold)),
                  const SizedBox(width: 8),
                  StatusBadge(
                    color: vehicle.statusColor,
                    text: _statusText,
                  ),
                  const Spacer(),
                  // 模式徽章（提醒 / 代办）
                  Container(
                    padding: const EdgeInsets.symmetric(
                        horizontal: 6, vertical: 2),
                    decoration: BoxDecoration(
                      color: isAgent ? Colors.blue.shade50 : Colors.grey.shade200,
                      borderRadius: BorderRadius.circular(8),
                    ),
                    child: Text(
                      isAgent ? '代办' : '提醒',
                      style: TextStyle(
                        color: isAgent
                            ? Colors.blue.shade700
                            : Colors.grey.shade600,
                        fontSize: 11,
                      ),
                    ),
                  ),
                ],
              ),
              if (vehicle.lastStatus.isNotEmpty) ...[
                const SizedBox(height: 8),
                Text('状态: ${vehicle.lastStatus}',
                    style: TextStyle(fontSize: 13, color: _statusTextColor)),
              ],
              if (vehicle.lastYxqs.isNotEmpty && vehicle.lastYxqz.isNotEmpty) ...[
                const SizedBox(height: 4),
                Text('有效期: ${vehicle.lastYxqs} ~ ${vehicle.lastYxqz}',
                    style: const TextStyle(fontSize: 13)),
              ],
              if (vehicle.lastError.isNotEmpty) ...[
                const SizedBox(height: 4),
                Text(vehicle.lastError,
                    style: TextStyle(fontSize: 12, color: Colors.red.shade600)),
              ],
              if (vehicle.ecBlzt != 0) ...[
                const SizedBox(height: 4),
                Text(_ecStatusText,
                    style: TextStyle(fontSize: 12, color: _ecStatusColor)),
              ],
              if (_needsDefaults) ...[
                const SizedBox(height: 8),
                Container(
                  padding: const EdgeInsets.symmetric(horizontal: 8, vertical: 6),
                  decoration: BoxDecoration(
                    color: Colors.orange.shade50,
                    borderRadius: BorderRadius.circular(6),
                  ),
                  child: Row(
                    children: [
                      Icon(Icons.warning_amber, size: 16, color: Colors.orange.shade700),
                      const SizedBox(width: 6),
                      Expanded(
                        child: Text('请设置办理默认值',
                            style: TextStyle(fontSize: 12, color: Colors.orange.shade800)),
                      ),
                      Icon(Icons.chevron_right, size: 16, color: Colors.orange.shade700),
                    ],
                  ),
                ),
              ],
            ],
          ),
        ),
      ),
    );
  }

  /// 主状态徽章文案 —— 由 statusColor 派生的用户可读文本。
  String get _statusText {
    if (vehicle.lastYxqz.isEmpty) return '未查询';
    if (vehicle.isExpired) return '已过期';
    if (vehicle.statusColor == 'red') return '即将到期';
    return '正常';
  }

  /// 二次办证状态文案，按 blzt 码分派。
  String get _ecStatusText {
    switch (vehicle.ecBlzt) {
      case 4:
        return '审核不通过: ${vehicle.ecShsbyyms}';
      case 5:
        return '审核中';
      case 6:
        return '待生效 ${vehicle.ecYxqs}';
      default:
        return '';
    }
  }

  /// 二次办证状态颜色：不通过=红、审核中=橙、待生效=绿。
  Color get _ecStatusColor {
    switch (vehicle.ecBlzt) {
      case 4:
        return Colors.red.shade600;
      case 5:
        return Colors.orange.shade700;
      case 6:
        return Colors.green.shade700;
      default:
        return Colors.grey;
    }
  }

  /// 主状态文字颜色：根据 lastStatus 文案关键字派生（与 API 返回文案耦合）。
  Color get _statusTextColor {
    final s = vehicle.lastStatus;
    if (s.contains('生效中')) return Colors.green.shade700;
    if (s.contains('待生效')) return Colors.green.shade700;
    if (s.contains('审核中')) return Colors.orange.shade700;
    if (s.contains('不通过')) return Colors.red.shade600;
    return Colors.grey;
  }

  /// 是否需要提醒用户"缺默认值"—— 代办模式但 default_area/xxdz 至少缺一个。
  bool get _needsDefaults =>
      vehicle.mode == 'agent' &&
      (vehicle.defaultArea.isEmpty || vehicle.defaultXxdz.isEmpty);
}
