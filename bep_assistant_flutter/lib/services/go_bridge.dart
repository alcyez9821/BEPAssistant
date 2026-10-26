// Copyright (c) 2026 alcyez <alcyez9821@gmail.com>
// SPDX-License-Identifier: MIT
//
// File: services/go_bridge.dart
// Purpose: 通过 dart:ffi 直接调用 libbridge.so 里 cgo 导出的 C 函数，封装
//          Init / FetchCaptcha / LoginWithCaptcha / AddAccount 等业务符号。
// Author: alcyez (AI Agent 辅助生成注释)
// Created: 2026-07-06
//
// Business Context:
//   Flutter 主进程跟 Go 核心库有两条通信路径：
//   1. dart:ffi 直呼裸符号（本文件）—— 纯业务查询（列车辆、加账号、验证码识别）
//   2. MethodChannel 走 Kotlin 转发（permission_bridge.dart）—— 涉及 Keystore、权限
//      的操作必须走这一条，让 Kotlin 完成加/解密后再传给 Go。
//   本文件不接触敏感字段（密码 / token），凭据流程整体由 Kotlin 主导。
//
// Key Logic:
//   1. lookupFunction 用 typedef 定义好的 C/Dart 双签名映射到具体符号
//   2. 每次调用后必须 FreeStr 释放 Go 返回的 *C.char，否则每次调用泄漏 KB 级堆
//   3. Init 前会 fallback 从 assets 拷贝 ONNX 模型文件到 dbPath 目录（Kotlin
//      GoBridgeLoader 也做同样的事，两侧幂等以避免 solver 保持 nil）
import 'dart:convert';
import 'dart:ffi';
import 'dart:io';
import 'package:ffi/ffi.dart';
import 'package:flutter/foundation.dart';
import 'package:flutter/services.dart';

// === C 函数签名 typedef ===
//
// Dart FFI 要求把 C 侧签名和 Dart 侧签名分开声明：
//   _XxxC:    C 侧 ABI 类型（Pointer<Utf8>, Int64 等）
//   _XxxDart: Dart 端调用时用的类型（Pointer<Utf8>, int）
// lookupFunction 根据两者做类型转换。

/// 无参 → 返回 *char。用于 FetchCaptcha / ListAccounts。
typedef _StrFuncC = Pointer<Utf8> Function();
typedef _StrFuncDart = Pointer<Utf8> Function();

/// 4 个字符串参数 → 返回 *char。用于 LoginWithCaptcha / AddAccount。
typedef _FourStrC = Pointer<Utf8> Function(Pointer<Utf8>, Pointer<Utf8>, Pointer<Utf8>, Pointer<Utf8>);
typedef _FourStrDart = Pointer<Utf8> Function(Pointer<Utf8>, Pointer<Utf8>, Pointer<Utf8>, Pointer<Utf8>);

/// 单个 long 参数 → 返回 *char。用于 DeleteAccount / ListVehicles。
typedef _LongArgC = Pointer<Utf8> Function(Int64);
typedef _LongArgDart = Pointer<Utf8> Function(int);

/// long + 字符串 → 返回 *char。用于 UpdateAccountToken / UpdateAccountNickname /
/// UpdateVehicle / SyncVehicles。
typedef _LongStrC = Pointer<Utf8> Function(Int64, Pointer<Utf8>);
typedef _LongStrDart = Pointer<Utf8> Function(int, Pointer<Utf8>);

/// void FreeStr(*char) —— 释放 Go 返回的 C 字符串。
typedef _FreeStrC = Void Function(Pointer<Utf8>);
typedef _FreeStrDart = void Function(Pointer<Utf8>);

/// 单个字符串参数 → 返回 *char。用于 Init / RecognizeCaptcha。
typedef _StrArgC = Pointer<Utf8> Function(Pointer<Utf8>);
typedef _StrArgDart = Pointer<Utf8> Function(Pointer<Utf8>);

/// dart:ffi 层封装，负责加载 libbridge.so、调用 cgo 导出函数、解析 JSON 返回。
///
/// 使用约定：
/// - 所有方法返回 Future/Map/List —— 内部虽是同步 FFI 调用，用 Future 便于异步 UI。
/// - 传入字符串前用 toNativeUtf8() 分配 C 侧内存；调用完 calloc.free 释放。
/// - Go 返回的 *char 必须调 FreeStr —— 由 _call helper 统一负责。
class GoBridge {
  static DynamicLibrary? _lib;
  static bool _initialized = false;

  /// 加载 libbridge.so；已加载则复用。
  ///
  /// Returns:
  ///   已加载的动态库句柄；未加载失败时抛异常。
  static DynamicLibrary _load() {
    if (_lib != null) return _lib!;
    _lib = DynamicLibrary.open('libbridge.so');
    return _lib!;
  }

  /// 通用调用包装：执行 [fn] → 拿 *char → 转 Dart String → FreeStr → 解 JSON。
  ///
  /// Args:
  ///   fn: 无参 lambda，返回 Go 侧分配的 *char。
  ///
  /// Returns:
  ///   反序列化后的 Map。
  static Map<String, dynamic> _call(Pointer<Utf8> Function() fn) {
    final lib = _load();
    final freeStr = lib.lookupFunction<_FreeStrC, _FreeStrDart>('FreeStr');

    final ptr = fn();
    final jsonStr = ptr.toDartString();
    freeStr(ptr); // 必须释放，否则每次调用泄漏 Go 侧 C 堆

    return jsonDecode(jsonStr) as Map<String, dynamic>;
  }

  /// 初始化 Go 核心库：加载 SQLite、bep 客户端、ONNX 模型。
  ///
  /// 幂等：Kotlin 可能已经调过 nativeInit（sync.Once 保护），此处仍会 lookup 并调用一次
  /// Init 符号，Go 返回 success 即可。首次冷启动纯 Flutter 路径需要此调用真的建库。
  ///
  /// Args:
  ///   dbPath: SQLite 文件绝对路径。
  ///
  /// Returns:
  ///   true 表示 Go 侧就绪；false 表示初始化失败（模型缺失、DB 无法打开等）。
  static Future<bool> init(String dbPath) async {
    if (_initialized) return true;
    final lib = _load();

    // 确保模型文件在文件系统上（Go 需要从文件读取，不支持从 asset bundle 内直接读）
    await _extractAssets(dbPath);

    final initFn = lib.lookupFunction<_StrArgC, _StrArgDart>('Init');
    final dbPathPtr = dbPath.toNativeUtf8();

    final result = _call(() => initFn(dbPathPtr));
    calloc.free(dbPathPtr);

    _initialized = result['success'] == true;
    return _initialized;
  }

  /// 兜底：Kotlin GoBridgeLoader 已在 nativeInit 前 extract 过；这里防它失败时补一次。
  ///
  /// 首次装机后 dbPath 下无 assets/models 目录时把 captcha.onnx / charset.json 从
  /// Flutter rootBundle 拷贝出来。已存在直接返回。
  static Future<void> _extractAssets(String dbPath) async {
    final dir = Directory('${Directory(dbPath).parent.path}/assets/models');
    if (await dir.exists()) return;
    await dir.create(recursive: true);
    for (final name in ['captcha.onnx', 'charset.json']) {
      final data = await rootBundle.load('assets/models/$name');
      final file = File('${dir.path}/$name');
      await file.writeAsBytes(data.buffer.asUint8List());
    }
    debugPrint('[GoBridge._extractAssets] Kotlin fallback: extracted to ${dir.path}');
  }

  /// 获取验证码图片，返回 base64 编码 + sessionID。
  ///
  /// UI 用返回的 sessionID 调用 [loginWithCaptcha] 完成同一会话的登录。
  ///
  /// Returns:
  ///   `Map{"success":bool, "image":"<base64>", "length":N, "session_id":"sN"}`
  ///   或 `{"success":false, "error":"..."}`。
  static Future<Map<String, dynamic>> fetchCaptcha() async {
    final lib = _load();
    final fn = lib.lookupFunction<_StrFuncC, _StrFuncDart>('FetchCaptcha');
    return _call(() => fn());
  }

  /// Go 端 ONNX 识别验证码（base64 图片输入）。
  ///
  /// Args:
  ///   base64Image: 通常来自 [fetchCaptcha] 返回的 image 字段。
  ///
  /// Returns:
  ///   Map{"success":true, "text":"3fk9"} 或 {"success":false, "error":"..."}。
  static Future<Map<String, dynamic>> recognizeCaptcha(String base64Image) async {
    final lib = _load();
    final fn = lib.lookupFunction<_StrArgC, _StrArgDart>('RecognizeCaptcha');
    final p = base64Image.toNativeUtf8();
    final result = _call(() => fn(p));
    calloc.free(p);
    return result;
  }

  /// 使用已识别的验证码登录。
  ///
  /// [sessionId] 来自 [fetchCaptcha] 返回的 `session_id`，标识具体一次登录会话，
  /// 避免并发登录时 cookie 串。
  ///
  /// Args:
  ///   sessionId:   验证码会话 ID。
  ///   phone:       手机号。
  ///   password:    明文密码（登录成功后立即被 Kotlin 加密，不落盘）。
  ///   captchaText: 已识别的验证码文字。
  ///
  /// Returns:
  ///   Map{"success":true, "token":"...", "certName":"...", ...} 或
  ///   {"success":false, "error":"..."}。
  static Future<Map<String, dynamic>> loginWithCaptcha(
      String sessionId, String phone, String password, String captchaText) async {
    final lib = _load();
    final fn = lib.lookupFunction<_FourStrC, _FourStrDart>('LoginWithCaptcha');
    final p1 = sessionId.toNativeUtf8();
    final p2 = phone.toNativeUtf8();
    final p3 = password.toNativeUtf8();
    final p4 = captchaText.toNativeUtf8();
    final result = _call(() => fn(p1, p2, p3, p4));
    calloc.free(p1);
    calloc.free(p2);
    calloc.free(p3);
    calloc.free(p4);
    return result;
  }

  /// 列出所有账号（含密文 password/token 字段）。
  ///
  /// Returns:
  ///   账号 JSON 列表；空数组表示无账户。
  static Future<List<dynamic>> listAccounts() async {
    final lib = _load();
    final fn = lib.lookupFunction<_StrFuncC, _StrFuncDart>('ListAccounts');
    final result = _call(() => fn());
    return result['accounts'] ?? [];
  }

  /// 添加账号（登录成功后立即调用）。
  ///
  /// Args:
  ///   phone:    手机号。
  ///   nickname: 备注名（可空）。
  ///   password: **已加密的密文密码**（"ENCv1:..."；调用前必须 encryptSecret）。
  ///   token:    **已加密的密文 token**（同上）。
  ///
  /// Returns:
  ///   成功时返回完整 Account 对象；失败时含 error 字段。
  ///
  /// Notes:
  ///   不要传明文；本函数不做二次加密。UI 层 [AddAccountPage._submit] 已在此前调用
  ///   [PermissionBridge.encryptSecret] 加密。
  static Future<Map<String, dynamic>> addAccount(
      String phone, String nickname, String password, String token) async {
    final lib = _load();
    final fn = lib.lookupFunction<_FourStrC, _FourStrDart>('AddAccount');
    final p1 = phone.toNativeUtf8();
    final p2 = nickname.toNativeUtf8();
    final p3 = password.toNativeUtf8();
    final p4 = token.toNativeUtf8();
    final result = _call(() => fn(p1, p2, p3, p4));
    calloc.free(p1);
    calloc.free(p2);
    calloc.free(p3);
    calloc.free(p4);
    return result;
  }

  /// 修改账号备注名。
  ///
  /// Args:
  ///   id:       账号主键。
  ///   nickname: 新备注名（空串则 UI 显示回退到手机号）。
  static Future<Map<String, dynamic>> updateAccountNickname(int id, String nickname) async {
    final lib = _load();
    final fn = lib.lookupFunction<_LongStrC, _LongStrDart>('UpdateAccountNickname');
    final n = nickname.toNativeUtf8();
    final result = _call(() => fn(id, n));
    calloc.free(n);
    return result;
  }

  /// 删除账号及其关联车辆（Go 端事务保证原子性）。
  static Future<Map<String, dynamic>> deleteAccount(int id) async {
    final lib = _load();
    final fn = lib.lookupFunction<_LongArgC, _LongArgDart>('DeleteAccount');
    return _call(() => fn(id));
  }

  /// 列出指定账号的车辆。
  ///
  /// Args:
  ///   accountId: 账号主键。
  ///
  /// Returns:
  ///   车辆 JSON 列表；空数组表示该账号未同步或无车辆。
  static Future<List<dynamic>> listVehicles(int accountId) async {
    final lib = _load();
    final fn = lib.lookupFunction<_LongArgC, _LongArgDart>('ListVehicles');
    final result = _call(() => fn(accountId));
    return result['vehicles'] ?? [];
  }

  /// 更新车辆字段（列名白名单校验在 Go store 层）。
  ///
  /// Args:
  ///   id:      车辆主键。
  ///   updates: 列名 → 新值 map；例如 {'mode': 'agent', 'default_area': '大兴区'}。
  static Future<Map<String, dynamic>> updateVehicle(int id, Map<String, dynamic> updates) async {
    final lib = _load();
    final fn = lib.lookupFunction<_LongStrC, _LongStrDart>('UpdateVehicle');
    final updatesJson = jsonEncode(updates).toNativeUtf8();
    final result = _call(() => fn(id, updatesJson));
    calloc.free(updatesJson);
    return result;
  }

  /// 同步车辆状态（调 API + 落 SQLite），仅在添加账号后立即刷新时使用。
  ///
  /// 与 [PermissionBridge.runCheckWithCredentials] 的区别：本函数只同步不做通知
  /// / 自动申请，用于新增账号后快速展示车辆列表。
  ///
  /// Args:
  ///   token:     **明文 accessToken**（添加账号流程刚拿到，未加密）。
  ///   accountId: 账号主键。
  static Future<List<dynamic>> syncVehicles(String token, int accountId) async {
    final lib = _load();
    final fn = lib.lookupFunction<_LongStrC, _LongStrDart>('SyncVehicles');
    final t = token.toNativeUtf8();
    final result = _call(() => fn(accountId, t));
    calloc.free(t);
    return result['vehicles'] ?? [];
  }
}
