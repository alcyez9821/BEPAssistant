// Copyright (c) 2026 alcyez <alcyez9821@gmail.com>
// SPDX-License-Identifier: MIT
//
// File: services/permission_bridge.dart
// Purpose: Flutter → Kotlin MethodChannel 封装，涵盖权限、通知、Keystore 加解密和
//          带凭据的检查任务触发。
// Author: alcyez (AI Agent 辅助生成注释)
// Created: 2026-07-06
//
// Business Context:
//   dart:ffi (go_bridge.dart) 无法访问 Android Keystore、AlarmManager、通知渠道等
//   平台 API；必须走 Kotlin。本文件是 Flutter 侧的对应 stub，通道名固定为
//   "com.alcyez.bep_assistant/permissions"，与 MainActivity.CHANNEL 保持一致。
//
// Key Logic:
//   1. 所有方法 try/catch 后 fallback 到默认值 —— UI 允许 Kotlin 端未实现的边界情况静默降级。
//   2. encryptSecret 失败时返回明文：避免加密不可用直接阻断保存流程；上层需知晓此兜底行为。
//   3. runCheckWithCredentials 是密文流程的入口，把整条 "Kotlin 读密文 → 解密 → 调 Go →
//      加密回存 token" 链路封装到一个 MethodChannel 调用里。
import 'dart:convert';
import 'package:flutter/services.dart';

/// 权限、通知、Keystore 加密等平台特化操作的 MethodChannel 封装。
///
/// 所有方法都是静态的 —— 无实例状态。所有方法都在异常时静默返回默认值，
/// 避免因 Kotlin 侧未实现或异常影响 UI 主流程。
class PermissionBridge {
  /// MethodChannel 名称，与 [MainActivity.CHANNEL] 保持一致。
  static const _channel =
      MethodChannel('com.alcyez.bep_assistant/permissions');

  /// 判断当前设备是否 MIUI / HyperOS（小米 / Redmi）。
  ///
  /// 用于决定权限引导对话框是否展示"自启动"这个 MIUI 专属项。
  ///
  /// Returns:
  ///   true 表示是小米系设备；异常时返回 false。
  static Future<bool> isMiuiDevice() async {
    try {
      final result = await _channel.invokeMethod<bool>('isMiuiDevice');
      return result ?? false;
    } catch (_) {
      return false;
    }
  }

  /// 跳到系统自启动设置页（MIUI/华为/OPPO/vivo 分别有各自 Intent，兜底应用详情页）。
  ///
  /// Returns:
  ///   true 表示成功跳转；false 表示所有 Intent 都失败。
  static Future<bool> openAutoStartSettings() async {
    try {
      final result =
          await _channel.invokeMethod<bool>('openAutoStartSettings');
      return result ?? false;
    } catch (_) {
      return false;
    }
  }

  /// 跳到系统电池优化设置页（一律用 ACTION_APPLICATION_DETAILS_SETTINGS 兜底）。
  static Future<bool> openBatteryOptimizationSettings() async {
    try {
      final result =
          await _channel.invokeMethod<bool>('openBatteryOptimizationSettings');
      return result ?? false;
    } catch (_) {
      return false;
    }
  }

  /// 查询当前应用是否已从电池优化列表中免除。
  ///
  /// Returns:
  ///   true 表示已免除（后台闹钟稳定）；false 表示仍在优化列表中。
  static Future<bool> isBatteryOptimizationExempt() async {
    try {
      final result =
          await _channel.invokeMethod<bool>('isBatteryOptimizationExempt');
      return result ?? false;
    } catch (_) {
      return false;
    }
  }

  /// 发送一条即时通知（body 会被截断到 100 字符）。
  ///
  /// 由 Flutter 侧的 "手动刷新" 流程调用；后台 CheckService / CheckWorker 走
  /// [NotificationDispatcher] 直接调 [NotificationHelper.notify]，不经过此函数。
  static Future<void> sendNotification(String title, String body) async {
    try {
      await _channel.invokeMethod('sendNotification', {
        'title': title,
        'body': body,
      });
    } catch (_) {}
  }

  /// 注册 WorkManager 申请后轮询任务（每 20 分钟一次，最长 1 小时自动取消）。
  ///
  /// 在代办模式手动保存后 [applied>0] 时触发。
  static Future<void> schedulePolling() async {
    try {
      await _channel.invokeMethod('schedulePolling');
    } catch (_) {}
  }

  /// 撤销所有后台定时任务（AlarmManager 每日闹钟 + WorkManager 轮询）。
  ///
  /// 删完最后一个账号时调用，避免空转前台服务通知继续弹出。
  static Future<void> cancelAllScheduled() async {
    try {
      await _channel.invokeMethod('cancelAllScheduled');
    } catch (_) {}
  }

  /// 读取"因通知权限未授予而错过的通知数"，用于启动时横幅提示。
  ///
  /// Returns:
  ///   ≥0 的错过条数；无错过时返回 0。
  static Future<int> getMissedNotifications() async {
    try {
      final result = await _channel.invokeMethod<int>('getMissedNotifications');
      return result ?? 0;
    } catch (_) {
      return 0;
    }
  }

  /// 清零错过的通知数（用户授权成功或点"忽略"后调用）。
  static Future<void> clearMissedNotifications() async {
    try {
      await _channel.invokeMethod('clearMissedNotifications');
    } catch (_) {}
  }

  /// 用 Android Keystore + AES-GCM 加密敏感字段（密码 / token）。
  ///
  /// Args:
  ///   plain: 明文；空串直接返回空串。
  ///
  /// Returns:
  ///   "ENCv1:base64(...)" 密文；Kotlin Keystore 不可用时**退化为返回原文**，避免因加密
  ///   故障阻断保存 —— 上层调用者需要知晓此兜底行为并接受可能的明文落盘（当前策略：
  ///   加密故障是极小概率事件，业务可用性优先）。
  static Future<String> encryptSecret(String plain) async {
    if (plain.isEmpty) return '';
    try {
      final result = await _channel.invokeMethod<String>('encryptSecret', {'value': plain});
      return result ?? '';
    } catch (_) {
      return plain;
    }
  }

  /// 触发一次带凭据的检查。
  ///
  /// Kotlin 层从 SQLite 读密文 → 解密 → 组装 credentials JSON → 调 Go
  /// RunCheckWithCredentials → 执行完把新 token 加密回存 accounts.token。
  ///
  /// Returns:
  ///   Go 返回的 TaskResult JSON（已解析成 Map）；异常时返回空 Map。
  ///   典型键：checked / processed / applied / notifications / messages。
  static Future<Map<String, dynamic>> runCheckWithCredentials() async {
    try {
      final json = await _channel.invokeMethod<String>('runCheckWithCredentials');
      if (json == null || json.isEmpty) return {};
      final decoded = const JsonDecoder().convert(json);
      return decoded is Map<String, dynamic> ? decoded : {};
    } catch (_) {
      return {};
    }
  }
}
