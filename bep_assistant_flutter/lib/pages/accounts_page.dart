// Copyright (c) 2026 alcyez <alcyez9821@gmail.com>
// SPDX-License-Identifier: MIT
//
// File: pages/accounts_page.dart
// Purpose: 首页 —— 账号列表 + 启动时后台刷新 + 权限引导 + 错过通知横幅。
// Author: alcyez (AI Agent 辅助生成注释)
// Created: 2026-07-06
//
// Business Context:
//   打开 App 首屏。业务上有 4 件事要在 initState 后台并行做：
//   1. 加载本地 SQLite 账号列表并立即展示（离线可用）
//   2. 触发一次带凭据的检查（后台调 API 刷新 + 通知）
//   3. 检查权限状态并按需弹权限引导对话框
//   4. 检查是否有因通知权限未授予而错过的通知，弹 MaterialBanner 提示
//
// Key Logic:
//   1. _refreshOnStart: 调 PermissionBridge.runCheckWithCredentials，Kotlin 完成整套
//      密文流程后回吐结果 → _processNotifications 发通知 + 回写 ec_notified_at
//   2. notifications 与 messages 是独立两组，Go 可能同时返回（例如"过期"+"申请已提交"），
//      本页处理时先派 notifications、再派非重复的 messages，避免同一文案重复通知
//   3. 删完最后一个账号自动 cancelAllScheduled，避免空转前台服务通知继续弹
//   4. 排闹钟不在这里 —— 由 MainActivity.onCreate 负责，避免与 initState 并发排 2 次
import 'package:flutter/material.dart';
import '../models/account.dart';
import '../services/go_bridge.dart';
import '../services/permission_bridge.dart';
import '../services/permission_service.dart';
import '../widgets/permission_guide_dialog.dart';
import 'account_detail_page.dart';
import 'add_account_page.dart';
import 'settings_page.dart';

/// 应用首页，展示账号列表并作为一切业务入口。
class AccountsPage extends StatefulWidget {
  const AccountsPage({super.key});

  @override
  State<AccountsPage> createState() => _AccountsPageState();
}

class _AccountsPageState extends State<AccountsPage> {
  /// 从 Go 读到的账号列表。
  List<Account> _accounts = [];

  /// 首次加载 loading 遮罩；后续刷新时 UI 保留旧数据不 loading。
  bool _loading = true;

  @override
  void initState() {
    super.initState();
    // 并行触发 4 件事：加载 UI、后台刷新、权限引导、miss 计数横幅
    _loadAccounts();
    _refreshOnStart();
    _checkPermissions();
    _checkMissedNotifications();
  }

  /// 启动时后台刷新：先显示本地数据，再调 API 更新。
  ///
  /// 出错静默忽略 —— 用户看到的本地缓存已足够表达"最近一次成功检查"的状态；
  /// 失败信息在下一次手动刷新时由 SnackBar 展示。
  Future<void> _refreshOnStart() async {
    try {
      final result = await PermissionBridge.runCheckWithCredentials();
      await _processNotifications(result);
    } catch (e) {
      debugPrint('[UI.Accounts.refreshOnStart] ERROR: $e');
    }
    if (mounted) _loadAccounts();
    // 通知发送可能被系统拒绝（POST_NOTIFICATIONS 未授权），刷新一次 miss 计数
    _checkMissedNotifications();
  }

  /// 处理检查结果中的通知：发送通知 + 更新去重标记。
  ///
  /// 关键设计：notifications 与 messages 是独立两组。
  /// - notifications：含 vehicle_id + notified_at 去重标记（到期/过期/审核通过/不通过）；
  ///   发送成功后必须回写 ec_notified_at，否则下次检查会重复通知。
  /// - messages：不需要去重的一次性提示（申请已提交、申请失败等），可能与 notifications
  ///   同时返回（例如"过期"通知 + "申请已提交"消息），因此不能用 else 分支处理。
  ///
  /// Args:
  ///   result: Go RunCheckWithCredentials 返回的完整 TaskResult JSON。
  static Future<void> _processNotifications(Map<String, dynamic> result) async {
    // notifications 数组：含去重标记的通知（到期/过期/审核通过/不通过）
    final notifications = result['notifications'] as List<dynamic>?;
    if (notifications != null && notifications.isNotEmpty) {
      for (final entry in notifications) {
        if (entry is Map<String, dynamic>) {
          final msg = entry['message'] as String? ?? '';
          final vehicleId = entry['vehicle_id'] as int? ?? 0;
          final notifiedAt = entry['notified_at'] as String? ?? '';
          if (msg.isNotEmpty) {
            await PermissionBridge.sendNotification('进京证提醒', msg);
            if (vehicleId > 0 && notifiedAt.isNotEmpty) {
              try {
                await GoBridge.updateVehicle(vehicleId, {'ec_notified_at': notifiedAt});
              } catch (e) {
                debugPrint('[UI.Accounts.processNotif] updateVehicle ERROR: $e');
              }
            }
          }
        }
      }
    }

    // messages 数组：一次性通知（申请已提交、申请失败等）。与 notifications 独立，
    // Go 端可能同时返回（例如"过期"通知 + "申请已提交"消息），因此不能用 else 分支处理。
    final messages = result['messages'] as List<dynamic>?;
    if (messages != null && messages.isNotEmpty) {
      // 过滤 notifications 里已发的消息，避免重复
      final notifMessages = notifications
              ?.whereType<Map<String, dynamic>>()
              .map((e) => e['message'] as String? ?? '')
              .toSet() ??
          <String>{};
      for (final msg in messages) {
        if (msg is String && msg.isNotEmpty && !notifMessages.contains(msg)) {
          await PermissionBridge.sendNotification('进京证提醒', msg);
        }
      }
    }
  }

  /// 检查权限并按需弹权限引导对话框。
  ///
  /// 延迟 500ms 让首页动画先展开，避免对话框和路由动画抢焦点导致视觉突兀。
  Future<void> _checkPermissions() async {
    await Future.delayed(const Duration(milliseconds: 500));
    if (!mounted) return;
    final shouldShow = await PermissionService.shouldShowDialog();
    if (shouldShow && mounted) {
      showDialog(
        context: context,
        barrierDismissible: true,
        builder: (_) => const PermissionGuideDialog(),
      );
    }
  }

  /// 检查是否有因通知权限未授予而错过的通知，>0 显示 MaterialBanner。
  ///
  /// Banner 提供 "去开启" + "忽略" 两个动作，都会 clearMissedNotifications 清零计数。
  Future<void> _checkMissedNotifications() async {
    final count = await PermissionBridge.getMissedNotifications();
    if (count <= 0 || !mounted) return;
    final messenger = ScaffoldMessenger.of(context);
    messenger.clearMaterialBanners();
    messenger.showMaterialBanner(
      MaterialBanner(
        backgroundColor: Colors.orange.shade50,
        leading: Icon(Icons.notifications_off, color: Colors.orange.shade700),
        content: Text(
          '过去有 $count 条到期提醒未能显示，请开启通知权限',
          style: const TextStyle(fontSize: 13),
        ),
        actions: [
          TextButton(
            onPressed: () async {
              ScaffoldMessenger.of(context).hideCurrentMaterialBanner();
              await PermissionBridge.clearMissedNotifications();
              if (mounted) {
                showDialog(
                  context: context,
                  builder: (_) => const PermissionGuideDialog(),
                );
              }
            },
            child: const Text('去开启'),
          ),
          TextButton(
            onPressed: () async {
              ScaffoldMessenger.of(context).hideCurrentMaterialBanner();
              await PermissionBridge.clearMissedNotifications();
            },
            child: const Text('忽略'),
          ),
        ],
      ),
    );
  }

  /// 加载账号列表。加载完成后如果账号为 0，撤销所有后台定时任务。
  ///
  /// 关于闹钟排期：
  ///   删完最后一个账号 → cancelAllScheduled 避免空转前台服务弹通知。
  ///   重新排闹钟不在这里 —— 由 MainActivity.onCreate 负责（下次打开 app 时纠正），
  ///   避免与 onCreate 竞争导致同一次冷启动重复排 2-3 次。
  Future<void> _loadAccounts() async {
    setState(() => _loading = true);
    try {
      final list = await GoBridge.listAccounts();
      setState(() {
        _accounts = list.map((e) => Account.fromJson(e as Map<String, dynamic>)).toList();
        _loading = false;
      });
      if (_accounts.isEmpty) {
        await PermissionBridge.cancelAllScheduled();
      }
    } catch (e) {
      setState(() => _loading = false);
      debugPrint('[UI.Accounts.loadAccounts] ERROR: $e');
      if (mounted) {
        ScaffoldMessenger.of(context).showSnackBar(
          SnackBar(content: Text('加载失败: $e')),
        );
      }
    }
  }

  @override
  Widget build(BuildContext context) {
    return Scaffold(
      appBar: AppBar(
        title: const Text('进京证助手'),
        actions: [
          IconButton(
            icon: const Icon(Icons.settings),
            onPressed: () => Navigator.push(
              context,
              MaterialPageRoute(builder: (_) => const SettingsPage()),
            ),
          ),
        ],
      ),
      body: _loading
          ? const Center(child: CircularProgressIndicator())
          : _accounts.isEmpty
              ? Center(
                  child: Column(
                    mainAxisSize: MainAxisSize.min,
                    children: [
                      Icon(Icons.directions_car_outlined,
                          size: 64, color: Colors.grey.shade300),
                      const SizedBox(height: 16),
                      Text('暂无账户', style: TextStyle(color: Colors.grey.shade500)),
                      const SizedBox(height: 8),
                      const Text('点击右下角添加账户',
                          style: TextStyle(color: Colors.grey, fontSize: 13)),
                    ],
                  ),
                )
              : RefreshIndicator(
                  onRefresh: _loadAccounts,
                  child: ListView.builder(
                    padding: const EdgeInsets.symmetric(vertical: 8),
                    itemCount: _accounts.length,
                    itemBuilder: (ctx, i) => _buildAccountCard(_accounts[i]),
                  ),
                ),
      floatingActionButton: FloatingActionButton(
        onPressed: () async {
          final result = await Navigator.push(
            context,
            MaterialPageRoute(builder: (_) => const AddAccountPage()),
          );
          if (result == true) _loadAccounts();
        },
        child: const Icon(Icons.add),
      ),
    );
  }

  /// 构建单个账号卡片，点击进入车辆详情页。
  Widget _buildAccountCard(Account account) {
    return Card(
      margin: const EdgeInsets.symmetric(horizontal: 16, vertical: 4),
      child: ListTile(
        leading: CircleAvatar(
          backgroundColor: Colors.blue.shade50,
          child: Icon(Icons.person, color: Colors.blue.shade600),
        ),
        title: Text(
          account.nickname.isNotEmpty ? account.nickname : account.phone,
          style: const TextStyle(fontWeight: FontWeight.w600),
          overflow: TextOverflow.ellipsis,
        ),
        subtitle: Text(account.phone, style: const TextStyle(fontSize: 13)),
        trailing: Row(
          mainAxisSize: MainAxisSize.min,
          children: [
            IconButton(
              icon: const Icon(Icons.edit_outlined, size: 20),
              tooltip: '修改名称',
              onPressed: () => _editNickname(account),
            ),
            const Icon(Icons.chevron_right),
          ],
        ),
        onTap: () async {
          await Navigator.push(
            context,
            MaterialPageRoute(
              builder: (_) => AccountDetailPage(account: account),
            ),
          );
          _loadAccounts();
        },
      ),
    );
  }

  /// 弹对话框修改账号备注名；空串则 UI 上显示手机号。
  Future<void> _editNickname(Account account) async {
    final controller = TextEditingController(text: account.nickname);
    final newName = await showDialog<String>(
      context: context,
      builder: (ctx) => AlertDialog(
        title: const Text('修改名称'),
        content: TextField(
          controller: controller,
          autofocus: true,
          maxLength: 20,
          decoration: InputDecoration(
            hintText: account.phone,
            border: const OutlineInputBorder(),
            helperText: '留空则显示手机号',
          ),
          onSubmitted: (v) => Navigator.pop(ctx, v.trim()),
        ),
        actions: [
          TextButton(
            onPressed: () => Navigator.pop(ctx),
            child: const Text('取消'),
          ),
          TextButton(
            onPressed: () => Navigator.pop(ctx, controller.text.trim()),
            child: const Text('保存'),
          ),
        ],
      ),
    );
    if (newName == null || newName == account.nickname) return;
    try {
      await GoBridge.updateAccountNickname(account.id, newName);
      if (!mounted) return;
      _loadAccounts();
    } catch (e) {
      debugPrint('[UI.Accounts.editNickname] ERROR: $e');
      if (!mounted) return;
      ScaffoldMessenger.of(context).showSnackBar(
        SnackBar(content: Text('修改失败: $e')),
      );
    }
  }
}
