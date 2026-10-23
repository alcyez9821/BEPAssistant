// Copyright (c) 2026 alcyez <alcyez9821@gmail.com>
// SPDX-License-Identifier: MIT
//
// File: pages/account_detail_page.dart
// Purpose: 账号详情页 —— 车辆列表 + 车辆详情底部弹窗（模式切换、代办默认值设置）。
// Author: alcyez (AI Agent 辅助生成注释)
// Created: 2026-07-06
//
// Business Context:
//   一个账号可绑定多辆车。用户在此页可以：
//   1. 查看每辆车当前进京证状态 + 二次办证状态
//   2. 切换模式（提醒 / 代办）
//   3. 代办模式下设置默认目的地区、详细地址、进京目的
//   4. 保存代办设置时**立即触发一次检查**（若到期则马上申请下一张）
//   5. 删除整个账号（连带车辆级联删除）
//
// Key Logic:
//   1. _refreshAll 用 PermissionBridge.runCheckWithCredentials 走完整加密流程
//   2. _VehicleDetailSheet._runCheckAndNotify 保存代办后触发一次检查；
//      重要：Go 端 ensureToken 已带 3 次登录重试，Flutter 侧不再手动重登（避免 5×3=15 次触发锁号）
//   3. 通知派发规则同 accounts_page.dart：notifications 数组含 vehicle_id + notified_at，
//      发送成功后必须回写 ec_notified_at；messages 数组独立处理去重
import 'package:flutter/material.dart';
import '../models/account.dart';
import '../models/vehicle.dart';
import '../services/go_bridge.dart';
import '../services/permission_bridge.dart';
import '../widgets/vehicle_card.dart';

/// 账号详情页：展示指定账号的车辆列表 + 支持车辆设置和账号删除。
class AccountDetailPage extends StatefulWidget {
  /// 关联账号；由上级页传入，_account 会拷贝一份便于 setState。
  final Account account;

  const AccountDetailPage({super.key, required this.account});

  @override
  State<AccountDetailPage> createState() => _AccountDetailPageState();
}

class _AccountDetailPageState extends State<AccountDetailPage> {
  late Account _account;
  List<Vehicle> _vehicles = [];
  bool _loading = true;

  @override
  void initState() {
    super.initState();
    _account = widget.account;
    _loadAll();
  }

  /// 从 Go 加载此账号的车辆列表。
  Future<void> _loadAll() async {
    setState(() => _loading = true);
    try {
      final list = await GoBridge.listVehicles(_account.id);
      setState(() {
        _vehicles = list.map((e) => Vehicle.fromJson(e as Map<String, dynamic>)).toList();
        _loading = false;
      });
    } catch (e) {
      debugPrint('[AccountDetail] _loadAll error: $e');
      setState(() => _loading = false);
    }
  }

  /// 手动刷新：走完整加密流程 → 重新加载列表。
  ///
  /// 与 initState 的自动 refresh 走同一条 [PermissionBridge.runCheckWithCredentials] 链路，
  /// 差别是这里 UI 上给 SnackBar 反馈。
  Future<void> _refreshAll() async {
    ScaffoldMessenger.of(context).showSnackBar(
      const SnackBar(content: Text('刷新中...')),
    );
    try {
      await PermissionBridge.runCheckWithCredentials();
    } catch (e) {
      debugPrint('[UI.Detail.refreshAll] ERROR: $e');
    }
    await _loadAll();
  }

  @override
  Widget build(BuildContext context) {
    return Scaffold(
      appBar: AppBar(
        title: Text(_account.nickname.isNotEmpty
            ? _account.nickname
            : _account.phone),
        actions: [
          IconButton(icon: const Icon(Icons.refresh), onPressed: _refreshAll),
          IconButton(
            icon: const Icon(Icons.delete_outline),
            onPressed: _confirmDelete,
          ),
        ],
      ),
      body: _loading
          ? const Center(child: CircularProgressIndicator())
          : _vehicles.isEmpty
              ? const Center(child: Text('暂无车辆数据，请刷新'))
              : ListView.builder(
                  padding: const EdgeInsets.symmetric(vertical: 8),
                  itemCount: _vehicles.length,
                  itemBuilder: (ctx, i) => VehicleCard(
                    vehicle: _vehicles[i],
                    onTap: () => _showVehicleDetail(_vehicles[i]),
                  ),
                ),
    );
  }

  /// 打开车辆详情底部弹窗；保存时通过 onSaved 触发 _loadAll。
  void _showVehicleDetail(Vehicle v) {
    showModalBottomSheet(
      context: context,
      isScrollControlled: true,
      builder: (ctx) => _VehicleDetailSheet(
        vehicle: v,
        account: _account,
        onSaved: _loadAll,
      ),
    );
  }

  /// 弹删除确认对话框 —— 二次确认后调 GoBridge.deleteAccount 级联删除车辆。
  void _confirmDelete() {
    showDialog(
      context: context,
      builder: (ctx) => AlertDialog(
        title: const Text('删除账户'),
        content: Text('确定删除账户 ${_account.phone}？关联的车辆数据也会被删除。'),
        actions: [
          TextButton(onPressed: () => Navigator.pop(ctx), child: const Text('取消')),
          TextButton(
            onPressed: () async {
              Navigator.pop(ctx);
              await GoBridge.deleteAccount(_account.id);
              if (mounted) Navigator.pop(context, true);
            },
            child: const Text('删除', style: TextStyle(color: Colors.red)),
          ),
        ],
      ),
    );
  }
}

/// 单辆车详情 + 模式设置的底部弹窗（DraggableScrollableSheet）。
class _VehicleDetailSheet extends StatefulWidget {
  final Vehicle vehicle;
  final Account account;

  /// 保存成功后的回调 —— 用于让 AccountDetailPage 重新 _loadAll。
  final VoidCallback onSaved;

  const _VehicleDetailSheet({required this.vehicle, required this.account, required this.onSaved});

  @override
  State<_VehicleDetailSheet> createState() => _VehicleDetailSheetState();
}

class _VehicleDetailSheetState extends State<_VehicleDetailSheet> {
  /// 北京 16 区 + 开发区 —— 服务端字典的完整选项。用户填的中文名会在 Go 端字典查表转编码。
  static const _districts = [
    '东城区', '西城区', '朝阳区', '丰台区', '石景山区', '海淀区',
    '门头沟区', '房山区', '通州区', '顺义区', '昌平区', '大兴区',
    '怀柔区', '平谷区', '密云区', '延庆区', '开发区',
  ];

  late String _mode;
  String? _area;
  late TextEditingController _addrCtrl;
  late String _jjmd;

  @override
  void initState() {
    super.initState();
    _mode = widget.vehicle.mode;
    final saved = widget.vehicle.defaultArea;
    // saved 不在预设区列表中时置 null，避免 Dropdown 报错
    _area = _districts.contains(saved) ? saved : null;
    _addrCtrl = TextEditingController(text: widget.vehicle.defaultXxdz);
    _jjmd = widget.vehicle.defaultJjmd.isEmpty ? '其它' : widget.vehicle.defaultJjmd;
  }

  @override
  void dispose() {
    _addrCtrl.dispose();
    super.dispose();
  }

  @override
  Widget build(BuildContext context) {
    final v = widget.vehicle;
    return DraggableScrollableSheet(
      initialChildSize: 0.7,
      minChildSize: 0.5,
      maxChildSize: 0.95,
      expand: false,
      builder: (ctx, scrollCtrl) => Padding(
        padding: EdgeInsets.only(
          left: 16, right: 16, top: 16,
          bottom: 16 + MediaQuery.of(ctx).viewInsets.bottom,
        ),
        child: ListView(
          controller: scrollCtrl,
          children: [
            Center(
              child: Container(
                width: 40, height: 4,
                decoration: BoxDecoration(
                  color: Colors.grey.shade300,
                  borderRadius: BorderRadius.circular(2),
                ),
              ),
            ),
            const SizedBox(height: 16),
            Text(v.hphm, style: const TextStyle(fontSize: 20, fontWeight: FontWeight.bold)),
            const SizedBox(height: 16),
            _infoRow('状态', v.lastStatus.isEmpty ? '未查询' : v.lastStatus),
            _infoRow('类型', v.lastJjzzlmc),
            _infoRow('有效期', v.lastYxqs.isEmpty ? '-' : '${v.lastYxqs} ~ ${v.lastYxqz}'),
            _infoRow('驾驶人', v.lastJsrxm),
            if (v.lastError.isNotEmpty)
              _infoRow('上次错误', v.lastError, color: Colors.red),
            if (v.ecBlzt != 0) ...[
              const Divider(height: 32),
              const Text('下一次申请', style: TextStyle(fontSize: 16, fontWeight: FontWeight.w600)),
              const SizedBox(height: 8),
              _infoRow('状态', _ecBlztText(v.ecBlzt)),
              if (v.ecYxqs.isNotEmpty)
                _infoRow('有效期', '${v.ecYxqs} ~ ${v.ecYxqz}'),
              if (v.ecJjzzlmc.isNotEmpty)
                _infoRow('类型', v.ecJjzzlmc),
              if (v.ecShsbyyms.isNotEmpty)
                _infoRow('失败原因', v.ecShsbyyms, color: Colors.red),
            ],
            const Divider(height: 32),
            const Text('模式设置', style: TextStyle(fontSize: 16, fontWeight: FontWeight.w600)),
            const SizedBox(height: 8),
            SegmentedButton<String>(
              segments: const [
                ButtonSegment(value: 'reminder', label: Text('提醒'), icon: Icon(Icons.notifications)),
                ButtonSegment(value: 'agent', label: Text('Auto'), icon: Icon(Icons.smart_toy)),
              ],
              selected: {_mode},
              onSelectionChanged: (s) => setState(() => _mode = s.first),
            ),
            if (_mode == 'agent') ...[
              const SizedBox(height: 16),
              const Text('办理默认值', style: TextStyle(fontWeight: FontWeight.w600)),
              const SizedBox(height: 8),
              DropdownButtonFormField<String>(
                value: _area,
                decoration: const InputDecoration(
                  labelText: '目的地所在区',
                  border: OutlineInputBorder(),
                ),
                items: _districts
                    .map((e) => DropdownMenuItem(value: e, child: Text(e)))
                    .toList(),
                onChanged: (v) => setState(() => _area = v),
              ),
              const SizedBox(height: 8),
              TextField(
                controller: _addrCtrl,
                decoration: const InputDecoration(
                  labelText: '详细地址',
                  border: OutlineInputBorder(),
                ),
              ),
              const SizedBox(height: 8),
              DropdownButtonFormField<String>(
                value: _jjmd,
                decoration: const InputDecoration(
                  labelText: '进京目的',
                  border: OutlineInputBorder(),
                ),
                items: ['自驾旅游', '看病就医', '探亲访友', '学习培训', '公务差旅', '其它']
                    .map((e) => DropdownMenuItem(value: e, child: Text(e)))
                    .toList(),
                onChanged: (v) => setState(() => _jjmd = v ?? '其它'),
              ),
            ],
            const SizedBox(height: 24),
            FilledButton(
              onPressed: _save,
              child: const Text('保存设置'),
            ),
          ],
        ),
      ),
    );
  }

  /// 单行信息展示 —— 左侧灰色标签 + 右侧值。
  Widget _infoRow(String label, String value, {Color? color}) {
    return Padding(
      padding: const EdgeInsets.symmetric(vertical: 4),
      child: Row(
        children: [
          SizedBox(width: 80, child: Text(label, style: const TextStyle(color: Colors.grey))),
          Expanded(child: Text(value, style: TextStyle(color: color))),
        ],
      ),
    );
  }

  /// 把 blzt 状态码翻译成中文文案（与 Go / API 状态码对应）。
  String _ecBlztText(int blzt) {
    switch (blzt) {
      case 4: return '审核不通过';
      case 5: return '审核中';
      case 6: return '待生效';
      default: return '未知';
    }
  }

  /// 保存设置。代办模式下会立即触发一次检查（若到期会马上申请下一张）。
  Future<void> _save() async {
    // 代办模式下检查默认值：必须选区 + 填详细地址
    if (_mode == 'agent' && (_area == null || _addrCtrl.text.trim().isEmpty)) {
      showDialog(
        context: context,
        builder: (ctx) => AlertDialog(
          title: const Text('缺少默认值'),
          content: const Text('Auto模式需要选择目的地所在区和详细地址，请填写后再保存。'),
          actions: [
            TextButton(onPressed: () => Navigator.pop(ctx), child: const Text('确定')),
          ],
        ),
      );
      return;
    }

    final updates = <String, dynamic>{
      'mode': _mode,
      'default_area': _area ?? '',
      'default_xxdz': _addrCtrl.text,
      'default_jjmd': _jjmd,
    };
    await GoBridge.updateVehicle(widget.vehicle.id, updates);

    // 代办模式下保存时立即触发检查 —— 让用户看到即时反馈：如果证已到期会马上申请
    if (_mode == 'agent') {
      _runCheckAndNotify();
    }

    if (mounted) {
      Navigator.pop(context);
      widget.onSaved();
      ScaffoldMessenger.of(context).showSnackBar(
        const SnackBar(content: Text('已保存')),
      );
    }
  }

  /// 触发一次带凭据检查 + 发送通知 + 派轮询任务。
  ///
  /// 与 [AccountsPage._processNotifications] 逻辑相同，两处独立以便 UI 侧标题差异化
  /// ("办理成功" vs "进京证提醒")。
  Future<void> _runCheckAndNotify() async {
    try {
      // Go 端 ensureToken 内部已带 bjt.LoginWithRetry(3) 处理 token 过期 + 验证码识别重试，
      // Flutter 侧不再手动重登（历史上曾经手动 fetchCaptcha + loginWithCaptcha 循环 5 次，
      // 与 Go 3 次相乘可放大到 15 次登录请求，触发服务端连续错误锁定）。
      final result = await PermissionBridge.runCheckWithCredentials();
      final applied = result['applied'] ?? 0;

      // notifications 数组：含 vehicle_id + notified_at 去重标记的通知（到期/过期/审核通过/不通过）
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
                await GoBridge.updateVehicle(vehicleId, {'ec_notified_at': notifiedAt});
              }
            }
          }
        }
      }

      // messages 数组：不需要去重的一次性通知（申请已提交、申请失败、缺默认值等）
      // 注意 messages 和 notifications 是独立的两组，Go 端可能同时返回（例如过期+申请成功）。
      final messages = result['messages'] as List<dynamic>?;
      if (messages != null && messages.isNotEmpty) {
        // 过滤掉已在 notifications 里发过的消息，避免重复
        final notifMessages = notifications
                ?.whereType<Map<String, dynamic>>()
                .map((e) => e['message'] as String? ?? '')
                .toSet() ??
            <String>{};
        for (final msg in messages) {
          if (msg is String && msg.isNotEmpty && !notifMessages.contains(msg)) {
            // 有申请提交成功的情况下用"办理成功"标题；否则用通用文案
            final title = applied > 0 ? '办理成功' : '进京证提醒';
            await PermissionBridge.sendNotification(title, msg);
          }
        }
      }

      // 申请提交后启动轮询，跟踪审核结果
      if (applied > 0) {
        await PermissionBridge.schedulePolling();
      }
    } catch (e) {
      debugPrint('[Check] manualCheck error: $e');
    }
  }
}
