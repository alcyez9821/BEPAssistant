// Copyright (c) 2026 alcyez <alcyez9821@gmail.com>
// SPDX-License-Identifier: MIT
//
// File: pages/add_account_page.dart
// Purpose: 添加账号页 —— 手机号 + 密码 + 验证码登录北京通，拿到 token 后建库。
// Author: alcyez (AI Agent 辅助生成注释)
// Created: 2026-07-06
//
// Business Context:
//   北京通登录必须带验证码，因此本页不显示验证码输入框 —— 走 Go 端 ONNX 自动识别，
//   识别失败自动换一张再试（最多 5 次）。登录成功后立即：
//   1. 用 Kotlin CryptoManager 加密密码 / token；
//   2. 调 GoBridge.addAccount 落 SQLite；
//   3. 用明文 token 调 SyncVehicles 立即刷新车辆列表（首次同步走明文快通道，
//      后续检查走加密流程）。
//
// Key Logic:
//   1. 循环 5 次：fetchCaptcha → recognizeCaptcha → loginWithCaptcha，验证码识别错
//      自动换图；密码错立即跳出（避免因错误重试触发账号锁定 —— 2026-07-02 事故复盘）
//   2. 加密走 PermissionBridge.encryptSecret，加密失败时会 fallback 到明文（避免阻断保存）
//   3. addAccount 成功后 syncVehicles 用明文 token 直接调，不用重复走加密链路
import 'package:flutter/material.dart';
import '../services/go_bridge.dart';
import '../services/permission_bridge.dart';

/// 添加账号页：手机号 + 密码 + 备注名。
class AddAccountPage extends StatefulWidget {
  const AddAccountPage({super.key});

  @override
  State<AddAccountPage> createState() => _AddAccountPageState();
}

class _AddAccountPageState extends State<AddAccountPage> {
  final _phoneCtrl = TextEditingController();
  final _nicknameCtrl = TextEditingController();
  final _passwordCtrl = TextEditingController();

  /// 是否正在登录 —— 按钮 loading 状态。
  bool _loading = false;

  @override
  void dispose() {
    _phoneCtrl.dispose();
    _nicknameCtrl.dispose();
    _passwordCtrl.dispose();
    super.dispose();
  }

  @override
  Widget build(BuildContext context) {
    return Scaffold(
      appBar: AppBar(title: const Text('添加账户')),
      body: Center(
        child: ConstrainedBox(
          constraints: const BoxConstraints(maxWidth: 400),
          child: SingleChildScrollView(
            padding: const EdgeInsets.symmetric(horizontal: 32, vertical: 24),
            child: Column(
              mainAxisSize: MainAxisSize.min,
              children: [
                Icon(Icons.directions_car_filled,
                    size: 64, color: Theme.of(context).colorScheme.primary),
                const SizedBox(height: 8),
                Text('北京通账号登录',
                    style: Theme.of(context).textTheme.titleMedium?.copyWith(
                        color: Theme.of(context).colorScheme.onSurfaceVariant)),
                const SizedBox(height: 32),
                TextField(
                  controller: _phoneCtrl,
                  keyboardType: TextInputType.phone,
                  decoration: const InputDecoration(
                    labelText: '手机号',
                    prefixIcon: Icon(Icons.phone_outlined),
                    border: OutlineInputBorder(),
                  ),
                ),
                const SizedBox(height: 16),
                TextField(
                  controller: _passwordCtrl,
                  obscureText: true,
                  decoration: const InputDecoration(
                    labelText: '密码',
                    prefixIcon: Icon(Icons.lock_outline),
                    border: OutlineInputBorder(),
                  ),
                ),
                const SizedBox(height: 16),
                TextField(
                  controller: _nicknameCtrl,
                  decoration: const InputDecoration(
                    labelText: '备注名（可选）',
                    prefixIcon: Icon(Icons.label_outline),
                    hintText: '如：我的车',
                    border: OutlineInputBorder(),
                  ),
                ),
                const SizedBox(height: 28),
                SizedBox(
                  width: double.infinity,
                  height: 48,
                  child: FilledButton(
                    onPressed: _loading ? null : _submit,
                    child: _loading
                        ? const SizedBox(
                            height: 20,
                            width: 20,
                            child: CircularProgressIndicator(
                                strokeWidth: 2, color: Colors.white))
                        : const Text('登录并添加', style: TextStyle(fontSize: 16)),
                  ),
                ),
              ],
            ),
          ),
        ),
      ),
    );
  }

  /// 登录并添加账号的主流程。
  ///
  /// 步骤：
  ///   1. 输入校验：手机号不能为空
  ///   2. 查重：调 GoBridge.listAccounts 判断此手机号是否已添加
  ///   3. 登录循环（最多 5 次）：fetchCaptcha → recognizeCaptcha → loginWithCaptcha
  ///      - 验证码识别失败自动重试
  ///      - 密码错误立即跳出（避免触发账号锁定）
  ///   4. 加密密码 / token，调 GoBridge.addAccount 落库
  ///   5. 用明文 token 调 syncVehicles 立即刷新车辆列表
  ///   6. Navigator.pop(true) 返回上级页并触发刷新
  Future<void> _submit() async {
    final phone = _phoneCtrl.text.trim();
    if (phone.isEmpty) {
      ScaffoldMessenger.of(context).showSnackBar(
        const SnackBar(content: Text('请输入手机号')),
      );
      return;
    }

    setState(() => _loading = true);
    try {
      // 先检查是否已添加（前端提前给友好提示；Go 端 AddAccount 也会兜底校验）
      final existing = await GoBridge.listAccounts();
      final duplicate = existing.any((a) => a['phone'] == phone);
      if (duplicate) {
        if (mounted) {
          ScaffoldMessenger.of(context).showSnackBar(
            const SnackBar(content: Text('该手机号已添加，请勿重复添加')),
          );
        }
        setState(() => _loading = false);
        return;
      }

      // 获取验证码 → 识别 → 登录（自动重试最多 5 次）
      // 5 次仅是识别失败或验证码错误的重试次数；密码错误立即跳出，避免叠加错误触发账号锁定。
      const maxRetries = 5;
      Map<String, dynamic>? loginResult;
      for (int attempt = 1; attempt <= maxRetries; attempt++) {
        final captchaResult = await GoBridge.fetchCaptcha();
        if (captchaResult['success'] != true) {
          if (mounted) {
            ScaffoldMessenger.of(context).showSnackBar(
              SnackBar(content: Text('获取验证码失败: ${captchaResult['error']}')),
            );
          }
          return;
        }
        final sessionId = captchaResult['session_id']?.toString() ?? '';

        final recognizeResult = await GoBridge.recognizeCaptcha(captchaResult['image']);
        final captchaText = recognizeResult['text'] ?? '';
        if (captchaText.isEmpty) {
          debugPrint('[Login] 第 $attempt 次: 验证码识别失败，重试...');
          continue;
        }

        debugPrint('[Login] 第 $attempt 次: 验证码=$captchaText');
        loginResult = await GoBridge.loginWithCaptcha(
            sessionId, phone, _passwordCtrl.text, captchaText);
        debugPrint('[Login] 第 $attempt 次: 结果=$loginResult');

        if (loginResult['success'] == true) break;

        final error = loginResult['error']?.toString() ?? '';
        if (error.contains('密码')) break; // 密码错误，不重试（避免连续错误被服务器锁号）
      }

      if (loginResult == null || loginResult['success'] != true) {
        if (mounted) {
          ScaffoldMessenger.of(context).showSnackBar(
            SnackBar(content: Text('登录失败: ${loginResult?['error'] ?? '验证码识别失败'}')),
          );
        }
        return;
      }

      final token = loginResult['token'] ?? '';

      // 敏感字段（密码 / token）用 Keystore AES-GCM 加密后才落盘
      final encPassword = await PermissionBridge.encryptSecret(_passwordCtrl.text);
      final encToken = await PermissionBridge.encryptSecret(token);

      // 创建账号（token 一起传入，原子操作）
      final accResult = await GoBridge.addAccount(
        phone,
        _nicknameCtrl.text.trim(),
        encPassword,
        encToken,
      );

      if (accResult['id'] == null || accResult['id'] == 0) {
        if (mounted) {
          ScaffoldMessenger.of(context).showSnackBar(
            SnackBar(content: Text('添加失败: ${accResult['error'] ?? '未知错误'}')),
          );
        }
        return;
      }

      // 账号已创建成功，后续步骤（首次同步车辆）失败不影响结果：账号已在 DB 里，
      // 用户能在列表页看到；下次检查会自动补上车辆信息。
      final accId = accResult['id'] ?? 0;
      try {
        if (accId > 0 && token.isNotEmpty) {
          await GoBridge.syncVehicles(token, accId);
        }
      } catch (e) {
        debugPrint('[Login] 同步车辆异常: $e');
      }

      if (mounted) {
        Navigator.pop(context, true);
        ScaffoldMessenger.of(context).showSnackBar(
          const SnackBar(content: Text('添加成功')),
        );
      }
    } catch (e) {
      if (mounted) {
        ScaffoldMessenger.of(context).showSnackBar(
          SnackBar(content: Text('错误: $e')),
        );
      }
    } finally {
      if (mounted) setState(() => _loading = false);
    }
  }
}
