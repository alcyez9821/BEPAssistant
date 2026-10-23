// Copyright (c) 2026 alcyez <alcyez9821@gmail.com>
// SPDX-License-Identifier: MIT
//
// File: widgets/permission_guide_dialog.dart
// Purpose: 权限引导对话框 —— 通知 / 闹钟和提醒 / 电池优化 / 自启动 / vivo 后台高耗电 的可视化引导。
// Author: alcyez (AI Agent 辅助生成注释)
// Created: 2026-07-06
//
// Business Context:
//   国产 ROM（MIUI / OriginOS / ColorOS 等）后台限制严格，闹钟被系统冻结、通知被拦截都会导致到期提醒
//   失效。本对话框把三个需要用户手动操作的设置项列出来，每项提供"去设置"跳转 + "我已
//   设置"手动确认按钮，让用户能一步步搞定。
//
// Key Logic:
//   1. WidgetsBindingObserver：监听生命周期，用户从系统设置页返回时自动刷新状态显示
//   2. 通知权限有 [Permission.notification.request] 标准 API，"去授权"按钮直接调用
//   3. 电池优化和自启动没有标准查询 API（MIUI 尤其），额外提供"我已设置"手动确认按钮
//      → SharedPreferences 里落"用户已确认"标记，作为软状态兜底
import 'package:flutter/material.dart';
import '../services/permission_bridge.dart';
import '../services/permission_service.dart';

/// 权限引导对话框，展示三个权限项的当前状态和操作按钮。
class PermissionGuideDialog extends StatefulWidget {
  const PermissionGuideDialog({super.key});

  @override
  State<PermissionGuideDialog> createState() => _PermissionGuideDialogState();
}

class _PermissionGuideDialogState extends State<PermissionGuideDialog>
    with WidgetsBindingObserver {
  bool _notifGranted = false;
  bool _batteryExempt = false;
  bool _exactAlarmGranted = true;
  bool _isChineseRom = false;
  bool _autoStartSet = false;
  bool _isVivo = false;
  bool _bgPowerSet = false;
  bool _loading = true;

  @override
  void initState() {
    super.initState();
    // 监听 App 生命周期 —— 用户从系统设置页回来时（resumed）自动刷新权限状态
    WidgetsBinding.instance.addObserver(this);
    _checkPermissions();
  }

  @override
  void dispose() {
    WidgetsBinding.instance.removeObserver(this);
    super.dispose();
  }

  @override
  void didChangeAppLifecycleState(AppLifecycleState state) {
    // 用户从系统设置页回来时 App 进入 resumed 状态；此时刷新权限显示
    if (state == AppLifecycleState.resumed) {
      _checkPermissions();
    }
  }

  /// 拉取最新权限状态并 setState 刷新 UI。
  Future<void> _checkPermissions() async {
    final result = await PermissionService.checkAll();
    final autoStartSet = await PermissionService.isAutoStartSet();
    final isVivo = await PermissionBridge.isVivoDevice();
    final bgPowerSet = await PermissionService.isBackgroundPowerSet();
    if (mounted) {
      setState(() {
        _notifGranted = result.notificationGranted;
        _batteryExempt = result.batteryExempt;
        _exactAlarmGranted = result.exactAlarmGranted;
        _isChineseRom = result.isChineseRom;
        _autoStartSet = autoStartSet;
        _isVivo = isVivo;
        _bgPowerSet = bgPowerSet;
        _loading = false;
      });
    }
  }

  @override
  Widget build(BuildContext context) {
    return AlertDialog(
      title: const Text('应用权限设置'),
      content: _loading
          ? const SizedBox(
              height: 100,
              child: Center(child: CircularProgressIndicator()),
            )
          : SingleChildScrollView(
              child: Column(
                mainAxisSize: MainAxisSize.min,
                crossAxisAlignment: CrossAxisAlignment.start,
                children: [
                  _buildNotifItem(),
                  const SizedBox(height: 12),
                  _buildExactAlarmItem(),
                  const SizedBox(height: 12),
                  _buildBatteryItem(),
                  if (_isChineseRom) ...[
                    const SizedBox(height: 12),
                    _buildAutoStartItem(),
                  ],
                  if (_isVivo) ...[
                    const SizedBox(height: 12),
                    _buildBackgroundPowerItem(),
                  ],
                ],
              ),
            ),
      actions: [
        TextButton(
          onPressed: () => Navigator.pop(context),
          child: const Text('知道了，稍后设置'),
        ),
        TextButton(
          onPressed: () async {
            // "不再提醒" —— 之后启动 App 不再自动弹此对话框（设置页仍能手动打开）
            await PermissionService.markDismissed();
            if (context.mounted) Navigator.pop(context);
          },
          child: const Text('不再提醒'),
        ),
      ],
    );
  }

  /// 通知权限项 —— 有标准 API，只需要"去授权"按钮触发 [Permission.notification.request]。
  Widget _buildNotifItem() {
    return _buildPermissionItem(
      icon: _notifGranted ? Icons.check_circle : Icons.warning_amber,
      iconColor: _notifGranted ? Colors.green : Colors.orange,
      title: '通知权限',
      subtitle: '用于进京证到期提醒和检查结果通知',
      status: _notifGranted ? '已授权' : '未授权',
      statusColor: _notifGranted ? Colors.green : Colors.red,
      actionLabel: _notifGranted ? null : '去授权',
      onPressed: _notifGranted
          ? null
          : () async {
              await PermissionService.requestNotification();
              _checkPermissions();
            },
    );
  }

  /// 精确闹钟项 —— 有标准 API（canScheduleExactAlarms），只需"去授权"跳转系统"闹钟和提醒"页；
  /// 用户返回时 resumed 生命周期自动刷新状态。
  Widget _buildExactAlarmItem() {
    return _buildPermissionItem(
      icon: _exactAlarmGranted ? Icons.check_circle : Icons.warning_amber,
      iconColor: _exactAlarmGranted ? Colors.green : Colors.orange,
      title: '闹钟和提醒',
      subtitle: '允许准时触发每日定时检查，否则系统可能推迟或跳过',
      status: _exactAlarmGranted ? '已授权' : '未授权',
      statusColor: _exactAlarmGranted ? Colors.green : Colors.red,
      actionLabel: _exactAlarmGranted ? null : '去授权',
      onPressed: _exactAlarmGranted
          ? null
          : () {
              PermissionBridge.openExactAlarmSettings();
            },
    );
  }

  /// 电池优化项 —— 无标准 API 查询，双按钮："去设置"跳转 + "我已设置"手动确认。
  Widget _buildBatteryItem() {
    return _buildPermissionItem(
      icon: _batteryExempt ? Icons.check_circle : Icons.warning_amber,
      iconColor: _batteryExempt ? Colors.green : Colors.orange,
      title: '电池优化',
      subtitle: '将应用从系统电池优化列表移除，确保定时检查正常运行',
      status: _batteryExempt ? '已设为无限制' : '未设置',
      statusColor: _batteryExempt ? Colors.green : Colors.red,
      actionLabel: _batteryExempt ? null : '去设置',
      onPressed: _batteryExempt
          ? null
          : () {
              PermissionBridge.openBatteryOptimizationSettings();
            },
      confirmLabel: _batteryExempt ? null : '我已设置',
      onConfirm: _batteryExempt
          ? null
          : () async {
              await PermissionService.markBatterySet();
              _checkPermissions();
            },
    );
  }

  /// 自启动项 —— 国产 ROM 专属，仅在 [_isChineseRom] 为 true 时显示。
  Widget _buildAutoStartItem() {
    return _buildPermissionItem(
      icon: _autoStartSet ? Icons.check_circle : Icons.warning_amber,
      iconColor: _autoStartSet ? Colors.green : Colors.orange,
      title: '自启动',
      subtitle: '需手动开启自启动 / 后台启动，否则 App 关闭后定时检查不会执行',
      status: _autoStartSet ? '已设置' : '未设置',
      statusColor: _autoStartSet ? Colors.green : Colors.red,
      actionLabel: _autoStartSet ? null : '去设置',
      onPressed: _autoStartSet
          ? null
          : () {
              PermissionBridge.openAutoStartSettings();
            },
      confirmLabel: _autoStartSet ? null : '我已设置',
      onConfirm: _autoStartSet
          ? null
          : () async {
              await PermissionService.markAutoStartSet();
              _checkPermissions();
            },
    );
  }

  /// vivo"后台高耗电"项 —— vivo 专属，仅在 [_isVivo] 为 true 时显示。
  ///
  /// 与标准"电池优化"是两套独立开关：vivo 的后台功耗管控会冻结 / 清理后台耗电的应用，
  /// 无公开 API 可查询状态，因此同自启动一样采用"去设置 + 我已设置"双按钮软标记。
  Widget _buildBackgroundPowerItem() {
    return _buildPermissionItem(
      icon: _bgPowerSet ? Icons.check_circle : Icons.warning_amber,
      iconColor: _bgPowerSet ? Colors.green : Colors.orange,
      title: '后台高耗电',
      subtitle: '电池 → 后台耗电管理 → 进京证助手 → 允许后台高耗电，否则后台检查可能被系统冻结',
      status: _bgPowerSet ? '已设置' : '未设置',
      statusColor: _bgPowerSet ? Colors.green : Colors.red,
      actionLabel: _bgPowerSet ? null : '去设置',
      onPressed: _bgPowerSet
          ? null
          : () {
              PermissionBridge.openBackgroundPowerSettings();
            },
      confirmLabel: _bgPowerSet ? null : '我已设置',
      onConfirm: _bgPowerSet
          ? null
          : () async {
              await PermissionService.markBackgroundPowerSet();
              _checkPermissions();
            },
    );
  }

  /// 通用权限项 Widget 构造器。
  ///
  /// Args:
  ///   icon:         左侧大图标（一般 check_circle 或 warning_amber）。
  ///   iconColor:    图标颜色。
  ///   title:        权限名。
  ///   subtitle:     解释文案。
  ///   status:       右上角状态徽章文字。
  ///   statusColor:  状态文字颜色。
  ///   actionLabel:  "去设置"按钮文字（null 时不显示）。
  ///   onPressed:    "去设置"按钮回调。
  ///   confirmLabel: "我已设置"按钮文字（null 时不显示）。
  ///   onConfirm:    "我已设置"按钮回调。
  Widget _buildPermissionItem({
    required IconData icon,
    required Color iconColor,
    required String title,
    required String subtitle,
    required String status,
    required Color statusColor,
    String? actionLabel,
    VoidCallback? onPressed,
    String? confirmLabel,
    VoidCallback? onConfirm,
  }) {
    return Row(
      crossAxisAlignment: CrossAxisAlignment.start,
      children: [
        Icon(icon, color: iconColor, size: 28),
        const SizedBox(width: 12),
        Expanded(
          child: Column(
            crossAxisAlignment: CrossAxisAlignment.start,
            children: [
              Row(
                children: [
                  Text(title,
                      style: const TextStyle(
                          fontSize: 15, fontWeight: FontWeight.w600)),
                  const SizedBox(width: 8),
                  Text(status,
                      style: TextStyle(fontSize: 12, color: statusColor)),
                ],
              ),
              const SizedBox(height: 2),
              Text(subtitle,
                  style: TextStyle(fontSize: 12, color: Colors.grey.shade600)),
              if (actionLabel != null || confirmLabel != null) ...[
                const SizedBox(height: 6),
                Row(
                  children: [
                    if (actionLabel != null)
                      SizedBox(
                        height: 32,
                        child: ElevatedButton(
                          onPressed: onPressed,
                          style: ElevatedButton.styleFrom(
                            padding:
                                const EdgeInsets.symmetric(horizontal: 12),
                            minimumSize: Size.zero,
                            tapTargetSize: MaterialTapTargetSize.shrinkWrap,
                          ),
                          child: Text(actionLabel,
                              style: const TextStyle(fontSize: 13)),
                        ),
                      ),
                    if (actionLabel != null && confirmLabel != null)
                      const SizedBox(width: 8),
                    if (confirmLabel != null)
                      SizedBox(
                        height: 32,
                        child: FilledButton(
                          onPressed: onConfirm,
                          style: FilledButton.styleFrom(
                            padding:
                                const EdgeInsets.symmetric(horizontal: 12),
                            minimumSize: Size.zero,
                            tapTargetSize: MaterialTapTargetSize.shrinkWrap,
                          ),
                          child: Text(confirmLabel,
                              style: const TextStyle(fontSize: 13)),
                        ),
                      ),
                  ],
                ),
              ],
            ],
          ),
        ),
      ],
    );
  }
}
