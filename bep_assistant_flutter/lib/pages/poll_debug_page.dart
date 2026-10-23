// Copyright (c) 2026 alcyez <alcyez9821@gmail.com>
// SPDX-License-Identifier: MIT
//
// File: pages/poll_debug_page.dart
// Purpose: 【临时测试用】后台轮询诊断页 —— 展示闹钟调度状态与 BEP_POLL 轮询日志，
//          并提供"1 分钟后触发测试闹钟"按钮。
// Author: alcyez (AI Agent 辅助生成注释)
// Created: 2026-10-23
//
// Business Context:
//   云真机的 logcat 导出受系统限流（每进程每秒有限行数）且需要在正确时间窗口导出，
//   本页直接读取 App 自己落盘的轮询日志文件，重新打开 App 即可回看后台期间发生了什么。
import 'package:flutter/material.dart';
import 'package:flutter/services.dart';

/// 后台轮询诊断页。
class PollDebugPage extends StatefulWidget {
  const PollDebugPage({super.key});

  @override
  State<PollDebugPage> createState() => _PollDebugPageState();
}

class _PollDebugPageState extends State<PollDebugPage> {
  static const _channel = MethodChannel('com.alcyez.bep_assistant/permissions');
  String _text = '加载中...';

  @override
  void initState() {
    super.initState();
    _refresh();
  }

  /// 从 Kotlin 侧拉取调度状态与日志文本并刷新显示。
  Future<void> _refresh() async {
    try {
      final s = await _channel.invokeMethod<String>('debugPollState');
      if (mounted) setState(() => _text = s ?? '');
    } catch (e) {
      if (mounted) setState(() => _text = '读取失败: $e');
    }
  }

  /// 注册一个 [seconds] 秒后触发的测试闹钟，随后提示用户划掉 App 等待。
  Future<void> _scheduleTest(int seconds) async {
    await _channel.invokeMethod('scheduleTestAlarm', {'seconds': seconds});
    await _refresh();
    if (!mounted) return;
    ScaffoldMessenger.of(context).showSnackBar(SnackBar(
      content: Text('已排 $seconds 秒后的测试闹钟，现在可以划掉 App 并等待通知'),
      duration: const Duration(seconds: 4),
    ));
  }

  @override
  Widget build(BuildContext context) {
    return Scaffold(
      appBar: AppBar(
        title: const Text('后台轮询诊断（测试）'),
        actions: [
          IconButton(icon: const Icon(Icons.refresh), onPressed: _refresh, tooltip: '刷新'),
          IconButton(
            icon: const Icon(Icons.copy),
            tooltip: '复制',
            onPressed: () => Clipboard.setData(ClipboardData(text: _text)),
          ),
        ],
      ),
      body: Column(
        children: [
          Padding(
            padding: const EdgeInsets.all(8),
            child: Wrap(spacing: 8, children: [
              FilledButton(onPressed: () => _scheduleTest(60), child: const Text('1 分钟后触发')),
              OutlinedButton(onPressed: () => _scheduleTest(180), child: const Text('3 分钟后触发')),
            ]),
          ),
          Expanded(
            child: SingleChildScrollView(
              padding: const EdgeInsets.all(8),
              child: SelectableText(
                _text,
                style: const TextStyle(fontFamily: 'monospace', fontSize: 11),
              ),
            ),
          ),
        ],
      ),
    );
  }
}
