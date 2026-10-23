// Copyright (c) 2026 alcyez <alcyez9821@gmail.com>
// SPDX-License-Identifier: MIT
//
// File: models/account.dart
// Purpose: 账号 UI 模型 —— Go SQLite accounts 表在 Flutter 侧的镜像。
// Author: alcyez (AI Agent 辅助生成注释)
// Created: 2026-07-06
//
// Business Context:
//   Go 返回的账号 JSON 中 password / token 已经是 Kotlin Keystore AES-GCM 加密后的
//   "ENCv1:base64(...)" 密文；Flutter 只做展示层，不解密不使用（涉及登录 / 检查的
//   凭据流程全部由 Kotlin CredentialsGateway 负责）。
//
// Key Logic:
//   fromJson 用 `json['xxx'] ?? default` 兜底，兼容后端老版本可能缺字段的情况。

/// 账号 UI 模型。字段与 Go 侧 store.Account 一一对应。
class Account {
  /// 自增主键（Go 侧 accounts.id）。
  final int id;

  /// 手机号（11 位，用作凭据 map key 和 UI 兜底显示）。
  final String phone;

  /// 用户备注名；空串时 UI 上显示 [phone]。
  final String nickname;

  /// 密文密码（Kotlin Keystore AES-GCM 加密后 base64）；空串代表尚未持久化。
  final String password;

  /// 密文 token（同上）。
  final String token;

  Account({
    required this.id,
    required this.phone,
    required this.nickname,
    this.password = '',
    this.token = '',
  });

  /// 从 Go FFI 返回的 JSON Map 构造 Account。
  ///
  /// Args:
  ///   json: Go bridge.go 返回的账号对象；期望字段 id/phone/nickname/password/token。
  ///
  /// Returns:
  ///   Account 实例；缺失字段以类型默认值兜底。
  factory Account.fromJson(Map<String, dynamic> json) => Account(
        id: json['id'] ?? 0,
        phone: json['phone'] ?? '',
        nickname: json['nickname'] ?? '',
        password: json['password'] ?? '',
        token: json['token'] ?? '',
      );
}
