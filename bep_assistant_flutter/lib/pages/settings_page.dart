// Copyright (c) 2026 alcyez <alcyez9821@gmail.com>
// SPDX-License-Identifier: MIT
//
// File: pages/settings_page.dart
// Purpose: 设置页 —— 权限自检入口 + 反馈邮箱 / 项目地址复制 + 版本信息。
// Author: alcyez (AI Agent 辅助生成注释)
// Created: 2026-07-06
//
// Business Context:
//   除了权限引导对话框在启动时自动弹出外，用户也可以随时进入设置页手动打开引导；
//   反馈邮箱和项目地址提供复制到剪贴板方便用户发邮件或访问 GitHub。
import 'package:flutter/material.dart';
import 'package:flutter/services.dart';
import '../widgets/permission_guide_dialog.dart';
import 'poll_debug_page.dart';

/// 应用设置页。三个分区：权限设置、问题反馈、关于。
class SettingsPage extends StatelessWidget {
  const SettingsPage({super.key});

  /// 反馈邮箱地址（也作为版权字段里的作者邮箱）。
  static const _authorEmail = 'alcyez9821@gmail.com';

  /// 项目 GitHub 地址。
  static const _repoUrl = 'https://github.com/alcyez9821/BEPAssistant';

  @override
  Widget build(BuildContext context) {
    return Scaffold(
      appBar: AppBar(title: const Text('设置')),
      body: ListView(
        children: [
          const _SectionHeader(title: '权限设置'),
          ListTile(
            leading: const Icon(Icons.security),
            title: const Text('检查应用权限'),
            subtitle: const Text('通知、闹钟和提醒、电池优化、自启动、后台高耗电'),
            trailing: const Icon(Icons.chevron_right),
            onTap: () => showDialog(
              context: context,
              builder: (_) => const PermissionGuideDialog(),
            ),
          ),
          ListTile(
            leading: const Icon(Icons.bug_report_outlined),
            title: const Text('后台轮询诊断（测试）'),
            subtitle: const Text('查看闹钟状态与 BEP_POLL 轮询日志'),
            trailing: const Icon(Icons.chevron_right),
            onTap: () => Navigator.push(
              context,
              MaterialPageRoute(builder: (_) => const PollDebugPage()),
            ),
          ),
          const Divider(),
          const _SectionHeader(title: '问题反馈'),
          ListTile(
            leading: const Icon(Icons.email_outlined),
            title: const Text('反馈邮箱'),
            subtitle: const Text(_authorEmail),
            trailing: const Icon(Icons.copy, size: 18),
            onTap: () => _copyToClipboard(context, _authorEmail, '邮箱已复制到剪贴板'),
          ),
          ListTile(
            leading: const Icon(Icons.code),
            title: const Text('项目地址'),
            subtitle: const Text(_repoUrl),
            trailing: const Icon(Icons.copy, size: 18),
            onTap: () => _copyToClipboard(context, _repoUrl, '链接已复制到剪贴板'),
          ),
          const Divider(),
          const _SectionHeader(title: '关于'),
          const ListTile(
            title: Text('作者'),
            subtitle: Text('alcyez'),
          ),
          const ListTile(
            title: Text('版本'),
            subtitle: Text('1.0.0'),
          ),
        ],
      ),
    );
  }

  /// 复制文本到剪贴板并弹 SnackBar 提示。
  ///
  /// Args:
  ///   context: 上下文。
  ///   text:    要复制的文本。
  ///   msg:     SnackBar 展示的提示文案。
  static void _copyToClipboard(BuildContext context, String text, String msg) {
    Clipboard.setData(ClipboardData(text: text));
    ScaffoldMessenger.of(context).showSnackBar(
      SnackBar(content: Text(msg), duration: const Duration(seconds: 2)),
    );
  }
}

/// 分区标题，用于列表分组的次级标题（灰色小字）。
class _SectionHeader extends StatelessWidget {
  final String title;
  const _SectionHeader({required this.title});

  @override
  Widget build(BuildContext context) {
    return Padding(
      padding: const EdgeInsets.fromLTRB(16, 16, 16, 4),
      child: Text(title,
          style: TextStyle(
              fontSize: 13,
              fontWeight: FontWeight.w600,
              color: Colors.grey.shade600)),
    );
  }
}
