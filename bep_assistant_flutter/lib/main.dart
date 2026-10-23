// Copyright (c) 2026 alcyez <alcyez9821@gmail.com>
// SPDX-License-Identifier: MIT
//
// File: main.dart
// Purpose: Flutter 应用入口 —— 初始化 Go 核心库、构建 MaterialApp、进入账号列表页。
// Author: alcyez (AI Agent 辅助生成注释)
// Created: 2026-07-06
//
// Business Context:
//   Flutter 侧启动的第一步是通过 dart:ffi 让 Go 库打开 SQLite + 加载 ONNX 模型；
//   失败时应用无法继续任何业务操作（列车辆、加账号、检查都会报错），所以启动阶段
//   直接跳到 _ErrorPage 醒目告知，避免用户在业务页面看到分散错误消息。
//
// Key Logic:
//   1. WidgetsFlutterBinding.ensureInitialized() 让 dart:ffi / path_provider 可用
//   2. getApplicationDocumentsDirectory 拿到应用私有目录，与 Kotlin 的 dataDir/app_flutter 一致
//   3. GoBridge.init 幂等触发 Go 侧 Init（sync.Once 保护，Kotlin 可能已经 nativeInit 过）
//   4. 成功 → AccountsPage；失败 → _ErrorPage 显示错误堆栈
import 'package:flutter/material.dart';
import 'package:path_provider/path_provider.dart';
import 'services/go_bridge.dart';
import 'pages/accounts_page.dart';

/// 应用入口。异步初始化 Go 核心库后 runApp。
///
/// Flutter 侧的 Go init 与 Kotlin GoBridgeLoader.initIfNeeded 是幂等互补关系：
/// - 如果 Kotlin 已经调过 nativeInit（例如闹钟触发过），Go 侧 sync.Once 短路返回 success
/// - 如果 Kotlin 没调过（首次冷启动纯 Flutter 路径），此处的 GoBridge.init 才真正建库
Future<void> main() async {
  WidgetsFlutterBinding.ensureInitialized();

  bool goReady = false;
  String errorMsg = '';
  try {
    final dir = await getApplicationDocumentsDirectory();
    final dbPath = '${dir.path}/app.db';
    debugPrint('[Main] DB path: $dbPath');

    goReady = await GoBridge.init(dbPath);
    debugPrint('[Main] Go init result: $goReady');
  } catch (e, stack) {
    // 捕获所有异常并保留堆栈，展示到 _ErrorPage 便于用户提交反馈
    errorMsg = e.toString();
    debugPrint('[Main] Go init error: $e\n$stack');
  }

  runApp(MyApp(goReady: goReady, errorMsg: errorMsg));
}

/// 应用根 Widget。根据 [goReady] 分派到业务主页或错误页。
class MyApp extends StatelessWidget {
  /// Go 核心库是否就绪。false 时展示 [_ErrorPage] 阻断业务操作。
  final bool goReady;

  /// Go 初始化失败时的原始错误消息（不做脱敏，含堆栈信息便于排查）。
  final String errorMsg;

  const MyApp({super.key, required this.goReady, this.errorMsg = ''});

  @override
  Widget build(BuildContext context) {
    return MaterialApp(
      title: '进京证助手',
      debugShowCheckedModeBanner: false,
      theme: ThemeData(
        colorSchemeSeed: Colors.blue,
        useMaterial3: true,
        brightness: Brightness.light,
      ),
      home: goReady ? const AccountsPage() : _ErrorPage(errorMsg),
    );
  }
}

/// 核心库加载失败时的错误页 —— 完全阻断业务操作，避免用户在业务页看到零散错误。
class _ErrorPage extends StatelessWidget {
  final String message;
  const _ErrorPage(this.message);
  @override
  Widget build(BuildContext context) {
    return Scaffold(
      body: Center(
        child: Padding(
          padding: const EdgeInsets.all(32),
          child: Column(
            mainAxisSize: MainAxisSize.min,
            children: [
              const Icon(Icons.error_outline, size: 48, color: Colors.red),
              const SizedBox(height: 16),
              const Text('核心库加载失败', style: TextStyle(fontSize: 18, fontWeight: FontWeight.bold)),
              const SizedBox(height: 8),
              Text(message, style: const TextStyle(fontSize: 13, color: Colors.grey), textAlign: TextAlign.center),
            ],
          ),
        ),
      ),
    );
  }
}
