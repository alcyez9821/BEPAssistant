// Copyright (c) 2026 alcyez <alcyez9821@gmail.com>
// SPDX-License-Identifier: MIT
//
// File: services/permission_service.dart
// Purpose: 权限状态聚合 + SharedPreferences 状态持久化（用户是否已手动设置过、
//          是否已"不再提醒"等）。
// Author: alcyez (AI Agent 辅助生成注释)
// Created: 2026-07-06
//
// Business Context:
//   Android 12+ 起精确闹钟需要运行时权限；MIUI 的"自启动"和"电池优化白名单"没有
//   标准 API 可查，必须靠用户手动开启后自己在 SharedPreferences 里标记"已设"。
//   本文件把三个状态（通知 / 电池 / 自启动）统一到 [PermissionCheckResult] 里方便 UI 判断。
//
// Key Logic:
//   1. 电池优化状态复合判断：优先看系统 API [isBatteryOptimizationExempt]，
//      失败或返回 false 时看 SharedPreferences 里用户是否已点过"我已设置"
//   2. shouldShowDialog: 如果用户已选"不再提醒"，一律不再弹权限引导对话框
//   3. MIUI 设备额外展示"自启动"引导项
import 'package:permission_handler/permission_handler.dart';
import 'package:shared_preferences/shared_preferences.dart';
import 'permission_bridge.dart';

/// 权限检查结果聚合。UI 层根据此对象决定是否展示权限引导。
class PermissionCheckResult {
  /// 通知权限（POST_NOTIFICATIONS on Android 13+ / NotificationManagerCompat 通用）是否授予。
  final bool notificationGranted;

  /// 是否已从电池优化列表移除（可能来自系统 API 或用户手动确认）。
  final bool batteryExempt;

  /// 是否为国产 ROM（当前只判 MIUI）；决定是否额外展示"自启动"引导项。
  final bool isChineseRom;

  const PermissionCheckResult({
    required this.notificationGranted,
    required this.batteryExempt,
    required this.isChineseRom,
  });

  /// 是否所有权限都已到位。isChineseRom 不影响此判断 —— 自启动状态由 UI 独立判断。
  bool get allGood => notificationGranted && batteryExempt;
}

/// 权限相关的静态服务类：集中管理 SharedPreferences key 和查询逻辑。
///
/// 所有方法都是 static —— PermissionService 无实例状态，SharedPreferences 每次异步取。
class PermissionService {
  /// SharedPreferences key：用户是否点过"不再提醒"。
  static const _dismissedKey = 'permission_dialog_dismissed';

  /// SharedPreferences key：用户是否手动设置过电池优化（配合系统 API 判断）。
  static const _batterySetKey = 'permission_battery_set';

  /// SharedPreferences key：用户是否手动设置过 MIUI 自启动。
  static const _autoStartSetKey = 'permission_autostart_set';

  /// 一次性获取三个权限状态。
  ///
  /// 电池优化判断复合：系统 API + 用户手动标记；任一为 true 即认为已授权。
  /// 这样既能自动识别用户在设置里操作，也能兼容 API 报告不准的 ROM。
  ///
  /// Returns:
  ///   [PermissionCheckResult] 快照。
  static Future<PermissionCheckResult> checkAll() async {
    final notifStatus = await Permission.notification.status;
    final notifGranted = notifStatus.isGranted;

    // 电池优化：先查标准 API，再查用户是否已手动设置过
    final batteryExemptByApi =
        await PermissionBridge.isBatteryOptimizationExempt();
    final prefs = await SharedPreferences.getInstance();
    final batterySetByUser = prefs.getBool(_batterySetKey) ?? false;
    final batteryExempt = batteryExemptByApi || batterySetByUser;

    final isMiui = await PermissionBridge.isMiuiDevice();

    return PermissionCheckResult(
      notificationGranted: notifGranted,
      batteryExempt: batteryExempt,
      isChineseRom: isMiui,
    );
  }

  /// 请求通知权限（Android 13+ 才会真的弹出系统对话框，之前版本直接返回已授权状态）。
  static Future<bool> requestNotification() async {
    final status = await Permission.notification.request();
    return status.isGranted;
  }

  /// 用户是否已选择"不再提醒"权限对话框。
  static Future<bool> wasDismissed() async {
    final prefs = await SharedPreferences.getInstance();
    return prefs.getBool(_dismissedKey) ?? false;
  }

  /// 标记用户已选"不再提醒"—— [shouldShowDialog] 将永远返回 false 直到手动 clearDismissed。
  static Future<void> markDismissed() async {
    final prefs = await SharedPreferences.getInstance();
    await prefs.setBool(_dismissedKey, true);
  }

  /// 清除"不再提醒"标记（当前 UI 未提供入口，保留 API 便于未来在设置页恢复引导）。
  static Future<void> clearDismissed() async {
    final prefs = await SharedPreferences.getInstance();
    await prefs.remove(_dismissedKey);
  }

  /// 标记用户已手动设置过电池优化。用户在权限对话框点"我已设置"时调用。
  static Future<void> markBatterySet() async {
    final prefs = await SharedPreferences.getInstance();
    await prefs.setBool(_batterySetKey, true);
  }

  /// 标记用户已手动设置过 MIUI 自启动。用户在权限对话框点"我已设置"时调用。
  static Future<void> markAutoStartSet() async {
    final prefs = await SharedPreferences.getInstance();
    await prefs.setBool(_autoStartSetKey, true);
  }

  /// 用户是否已手动设置过 MIUI 自启动。
  static Future<bool> isAutoStartSet() async {
    final prefs = await SharedPreferences.getInstance();
    return prefs.getBool(_autoStartSetKey) ?? false;
  }

  /// 是否需要在启动时弹权限引导对话框。
  ///
  /// 逻辑：
  ///   - 用户点过"不再提醒"→ 不弹
  ///   - 否则：任一权限未到位，或是 MIUI 设备（需要引导手动开自启动）→ 弹
  ///
  /// Returns:
  ///   true 表示应弹出。
  static Future<bool> shouldShowDialog() async {
    final dismissed = await wasDismissed();
    if (dismissed) return false;

    final status = await checkAll();
    return !status.allGood || status.isChineseRom;
  }
}
