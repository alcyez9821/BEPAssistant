// Copyright (c) 2026 alcyez <alcyez9821@gmail.com>
// SPDX-License-Identifier: MIT
//
// File: models/vehicle.dart
// Purpose: 车辆 UI 模型 —— Go SQLite vehicles 表在 Flutter 侧的镜像。
// Author: alcyez (AI Agent 辅助生成注释)
// Created: 2026-07-06
//
// Business Context:
//   一辆车维护三组信息：基础属性 (hphm/hpzl/ybcs 等)、当前进京证 (last_*)、
//   二次办证下一张证 (ec_*)。UI 卡片、模式设置、状态徽章都消费此模型。
//   字段名保留 Go / DB / API 的拼音（yxqz / blzt / jjzzlmc），避免翻译成本和
//   命名漂移。
//
// Key Logic:
//   1. mode: 'reminder'=提醒模式（只发通知）；'agent'=代办模式（自动申请下一张）
//   2. statusColor: 根据 lastYxqz 距今日的天数分绿/红/灰三档，供 StatusBadge
//   3. isExpired: 今日 23:59:59 之后 yxqz 即视为已过期

/// 车辆 UI 模型；字段与 Go 侧 store.Vehicle 一一对应。
class Vehicle {
  /// 自增主键。
  final int id;

  /// 归属账号 ID。
  final int accountId;

  /// 北京交警 API 车辆 ID。
  final String vId;

  /// 号牌号码，例如 "京A12345"。
  final String hphm;

  /// 号牌种类，例如 "02"。
  final String hpzl;

  /// 模式：'reminder' 或 'agent'。
  final String mode;

  /// 代办模式默认目的地所在区（例如"大兴区"）。
  final String defaultArea;

  /// 代办模式默认详细地址。
  final String defaultXxdz;

  /// 代办模式默认进京目的（例如"其它"）。
  final String defaultJjmd;

  /// 进京证种类：01=六环内 / 02=六环外。
  final String jjzzl;

  /// 已办次数。
  final int ybcs;

  /// 剩余次数（API 返回字符串）。
  final String sycs;

  /// 剩余天数。
  final String syts;

  /// 当前进京证状态名称（"生效中"/"审核中"/"待生效"/"不通过"）。
  final String lastStatus;

  /// 当前进京证种类名称。
  final String lastJjzzlmc;

  /// 有效期起 YYYY-MM-DD。
  final String lastYxqs;

  /// 有效期止 YYYY-MM-DD。
  final String lastYxqz;

  /// 驾驶人姓名。
  final String lastJsrxm;

  /// 申请单 ID。
  final String lastApplyId;

  /// 最近一次错误信息；UI 上显示为红色文案。
  final String lastError;

  // === 二次办证字段（ec_* = ecbzxx 前缀） ===

  /// 二次办证办理状态码：0=无，4=不通过，5=审核中，6=待生效。
  final int ecBlzt;

  /// 二次办证有效期起。
  final String ecYxqs;

  /// 二次办证有效期止。
  final String ecYxqz;

  /// 二次办证进京证种类名称。
  final String ecJjzzlmc;

  /// 二次办证审核失败原因描述。
  final String ecShsbyyms;

  Vehicle({
    required this.id,
    required this.accountId,
    required this.vId,
    required this.hphm,
    required this.hpzl,
    this.mode = 'reminder',
    this.defaultArea = '',
    this.defaultXxdz = '',
    this.defaultJjmd = '其它',
    this.jjzzl = '02',
    this.ybcs = 0,
    this.sycs = '',
    this.syts = '',
    this.lastStatus = '',
    this.lastJjzzlmc = '',
    this.lastYxqs = '',
    this.lastYxqz = '',
    this.lastJsrxm = '',
    this.lastApplyId = '',
    this.lastError = '',
    this.ecBlzt = 0,
    this.ecYxqs = '',
    this.ecYxqz = '',
    this.ecJjzzlmc = '',
    this.ecShsbyyms = '',
  });

  /// 从 Go FFI 返回的 JSON Map 构造 Vehicle。
  ///
  /// Args:
  ///   json: Go bridge.go 返回的车辆对象；字段名与 DB 列名一致。
  ///
  /// Returns:
  ///   Vehicle 实例；缺失字段以类型默认值兜底。
  factory Vehicle.fromJson(Map<String, dynamic> json) => Vehicle(
        id: json['id'] ?? 0,
        accountId: json['account_id'] ?? 0,
        vId: json['v_id'] ?? '',
        hphm: json['hphm'] ?? '',
        hpzl: json['hpzl'] ?? '',
        mode: json['mode'] ?? 'reminder',
        defaultArea: json['default_area'] ?? '',
        defaultXxdz: json['default_xxdz'] ?? '',
        defaultJjmd: json['default_jjmd'] ?? '其它',
        jjzzl: json['jjzzl'] ?? '02',
        ybcs: json['ybcs'] ?? 0,
        sycs: json['sycs'] ?? '',
        syts: json['syts'] ?? '',
        lastStatus: json['last_status'] ?? '',
        lastJjzzlmc: json['last_jjzzlmc'] ?? '',
        lastYxqs: json['last_yxqs'] ?? '',
        lastYxqz: json['last_yxqz'] ?? '',
        lastJsrxm: json['last_jsrxm'] ?? '',
        lastApplyId: json['last_apply_id'] ?? '',
        lastError: json['last_error'] ?? '',
        ecBlzt: json['ec_blzt'] ?? 0,
        ecYxqs: json['ec_yxqs'] ?? '',
        ecYxqz: json['ec_yxqz'] ?? '',
        ecJjzzlmc: json['ec_jjzzlmc'] ?? '',
        ecShsbyyms: json['ec_shsbyyms'] ?? '',
      );

  /// 状态颜色：绿(>1天)、红(≤1天)、灰(已过期/未知)。
  ///
  /// UI 上用于 [StatusBadge] 展色，参照进京证到期紧迫度：
  /// - "green"  正常：距离到期还有 >1 天
  /// - "red"    紧迫：今日或明日到期
  /// - "gray"   已过期或未查询到有效期
  String get statusColor {
    if (lastYxqz.isEmpty) return 'gray';
    final expiry = DateTime.tryParse(lastYxqz);
    if (expiry == null) return 'gray';
    // 到期日的 23:59:59 才算真正到期，与 Go 侧 "yxqz>=today" 判断一致
    final expiryEnd = DateTime(expiry.year, expiry.month, expiry.day, 23, 59, 59);
    final diff = expiryEnd.difference(DateTime.now()).inDays;
    if (diff < 0) return 'gray';
    if (diff <= 1) return 'red';
    return 'green';
  }

  /// 是否已过期：yxqz 当天 23:59:59 之后返回 true。
  bool get isExpired {
    if (lastYxqz.isEmpty) return false;
    final expiry = DateTime.tryParse(lastYxqz);
    if (expiry == null) return false;
    final expiryEnd = DateTime(expiry.year, expiry.month, expiry.day, 23, 59, 59);
    return expiryEnd.isBefore(DateTime.now());
  }
}
