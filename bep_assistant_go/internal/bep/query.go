// Copyright (c) 2026 alcyez <alcyez9821@gmail.com>
// SPDX-License-Identifier: MIT

// File: query.go
// Purpose: 北京交警 API 的查询类方法（状态列表 / 证件详情 / 驾驶人 / 可选日期 / 字典）。
// Author: alcyez (AI Agent 辅助生成注释)
// Created: 2026-07-06
//
// Business Context:
//   查询接口全部是 POST，Body 参数少，响应结构不同。为方便上层使用，此文件把响应结构和
//   业务字段拼音字段名一一对应（保留 hphm / yxqz / blzt 等原字段名与 API 契约绑定）。
//
// Key Logic:
//   每个方法一段：构造 body → 调 Client.Post → 反序列化到具体 struct。
package bep

import (
	"encoding/json"
	"fmt"
)

// PermitInfo 单张进京证信息（对应 bzxx / ecbzxx 数组元素）。
//
// blzt 状态码对照表：
//   1 = 生效中（进京证在有效期内）
//   4 = 审核不通过（shsbyyms 会填失败原因）
//   5 = 审核中（提交后待人工审核）
//   6 = 待生效（审核通过但还没到 yxqs 起始日）
type PermitInfo struct {
	ApplyID  string `json:"applyId"`  // 申请单 ID
	Blzt     int    `json:"blzt"`     // 办理状态码
	Blztmc   string `json:"blztmc"`   // 办理状态名称（用于 UI 展示）
	Jjzzlmc  string `json:"jjzzlmc"`  // 进京证种类名称（例如"进京证(六环外)"）
	Yxqs     string `json:"yxqs"`     // 有效期起 YYYY-MM-DD
	Yxqz     string `json:"yxqz"`     // 有效期止 YYYY-MM-DD
	Jsrxm    string `json:"jsrxm"`    // 驾驶人姓名
	Shsbyy   string `json:"shsbyy"`   // 审核失败原因编码
	Shsbyyms string `json:"shsbyyms"` // 审核失败原因描述（文案）
}

// VehicleInfo 单辆车信息（对应 bzclxx 数组元素）。
//
// bzxx / ecbzxx 说明：
//   - bzxx  = 当前进京证（最新一条，可能是生效中 / 审核中 / 待生效 / 不通过）
//   - ecbzxx = 二次办证信息（在生效中的证的基础上又申请了下一张，只在有生效证时出现）
//   两个数组元素通常最多 1 条；空数组表示对应场景不存在。
type VehicleInfo struct {
	VID     string       `json:"vId"`     // 车辆 ID（API 主键）
	Hphm    string       `json:"hphm"`    // 号牌号码
	Hpzl    string       `json:"hpzl"`    // 号牌种类
	Ybcs    int          `json:"ybcs"`    // 已办次数
	Sycs    string       `json:"sycs"`    // 剩余次数（API 返回字符串）
	Syts    string       `json:"syts"`    // 剩余天数
	Ylzsfkb interface{}  `json:"ylzsfkb"` // 六环内是否可办（API 混用 bool 和 string，用 interface{} 兜底）
	Elzsfkb interface{}  `json:"elzsfkb"` // 六环外是否可办
	Cllx    string       `json:"cllx"`    // 车辆类型（默认 "01"）
	Bzxx    []PermitInfo `json:"bzxx"`    // 当前进京证信息列表（一般 0-1 条）
	Ecbzxx  []PermitInfo `json:"ecbzxx"`  // 二次办证信息列表
}

// StateData 车辆状态查询返回数据（stateList 接口的 data 部分）。
//
// elzqyms / ylzqyms 是六环外 / 六环内的可办情况描述（例如"已用 3 次，剩余 X 天"），
// 用于 UI 上显示；elzmc / ylzmc 是进京证种类名称，申请时需要传给服务端。
type StateData struct {
	Bzclxx  []VehicleInfo `json:"bzclxx"`  // 备案车辆信息列表
	Elzqyms string        `json:"elzqyms"` // 六环外剩余描述
	Ylzqyms string        `json:"ylzqyms"` // 六环内剩余描述
	Elzmc   string        `json:"elzmc"`   // 六环外名称
	Ylzmc   string        `json:"ylzmc"`   // 六环内名称
}

// DriverInfo 驾驶人信息（getJsrxx 接口的 data 部分）。
type DriverInfo struct {
	Jsrxm string `json:"jsrxm"` // 驾驶人姓名
	Jszh  string `json:"jszh"`  // 驾驶证号
	Dabh  string `json:"dabh"`  // 档案编号
}

// HandleData 可选进京日期数据（checkHandle 接口的 data 部分）。
type HandleData struct {
	Jjrqs []string `json:"jjrqs"` // 可选进京日期列表 YYYY-MM-DD；默认取第一个
}

// DicItem 字典项（queryDic 接口返回结构）。
//
// 有些字典有二级结构（例如"进京目的"下面有具体分类），子项走 Zdzj 递归。
type DicItem struct {
	Zdbm string    `json:"zdbm"` // 字典编码（提交时用这个）
	Zdz  string    `json:"zdz"`  // 字典值（名称，用户看到的文案）
	Zdzj []DicItem `json:"zdzj"` // 子项
}

// GetStateList 查询用户车辆列表及每辆车的进京证状态。
//
// 这是最常用的接口 —— scheduler 每次检查都调一次。返回全部备案车辆及其
// 当前进京证 / 二次办证信息。
//
// Args:
//   token: JJZ accessToken。
//
// Returns:
//   *StateData: 状态数据。
//   error:      HTTP 或业务错误。
func (c *Client) GetStateList(token string) (*StateData, error) {
	var result StateData
	if err := c.Post("/pro//applyRecordController/stateList", map[string]string{}, token, &result); err != nil {
		return nil, fmt.Errorf("查询状态失败: %w", err)
	}
	return &result, nil
}

// GetCardInfo 查询单张进京证的详细信息（申请时间 / 车辆信息 / 驾驶人 / 目的地等）。
//
// 使用场景：UI 上点击某张进京证查看详情。当前 BEP Assistant 未展示此详细页面，
// 但保留 API 以便后续扩展。
//
// Args:
//   token:   JJZ accessToken。
//   applyID: 申请单 ID（PermitInfo.ApplyID）。
//   sqsj:    申请时间（服务端用于二次校验，UI 上一般从状态里带）。
//
// Returns:
//   json.RawMessage: 原始 JSON（结构未定，透传给调用方）。
//   error:           HTTP 或业务错误。
func (c *Client) GetCardInfo(token, applyID, sqsj string) (json.RawMessage, error) {
	var result json.RawMessage
	if err := c.Post("/pro//applyRecordController/cardInfoById",
		map[string]string{"applyId": applyID, "sqsj": sqsj}, token, &result); err != nil {
		return nil, fmt.Errorf("查询证件失败: %w", err)
	}
	return result, nil
}

// GetJsrxx 获取驾驶人信息（申请进京证的 Step 2）。
//
// 一个北京通账号只关联一个驾驶人（实名认证时确定），因此不需要传车辆 ID。
//
// Args:
//   token: JJZ accessToken。
//
// Returns:
//   *DriverInfo: 驾驶人姓名 + 驾驶证号 + 档案编号。
//   error:       HTTP 或业务错误。
func (c *Client) GetJsrxx(token string) (*DriverInfo, error) {
	var result DriverInfo
	if err := c.Post("/pro//applyRecordController/getJsrxx", map[string]string{}, token, &result); err != nil {
		return nil, fmt.Errorf("获取驾驶人信息失败: %w", err)
	}
	return &result, nil
}

// CheckHandle 获取可选的进京日期列表（申请进京证的 Step 5）。
//
// 服务端根据车牌 + 证件种类返回未来若干天可申请的日期。ApplyForPermit 默认取第一个。
//
// Args:
//   token: JJZ accessToken。
//   vID:   车辆 ID。
//   jjzzl: 进京证种类 "01"=六环内 / "02"=六环外。
//   hphm:  号牌号码。
//
// Returns:
//   *HandleData: 含 Jjrqs 可选日期数组。
//   error:       HTTP 或业务错误。
func (c *Client) CheckHandle(token, vID, jjzzl, hphm string) (*HandleData, error) {
	var result HandleData
	body := map[string]string{"vId": vID, "jjzzl": jjzzl, "hphm": hphm}
	if err := c.Post("/pro//applyRecordController/checkHandle", body, token, &result); err != nil {
		return nil, fmt.Errorf("获取可选日期失败: %w", err)
	}
	return &result, nil
}

// QueryDic 查询字典数据（区域 / 进京目的等编码表）。
//
// 申请进京证时需要把用户填写的中文（例如"大兴区"）转换成服务端字典编码（例如某个数字串），
// 通过此接口拿到映射表后手工查找。
//
// Args:
//   token:   JJZ accessToken。
//   dicType: 字典类型（"qjxz" = 进京区域，"jjmd" = 进京目的）。
//   vID:     车辆 ID（部分字典与车辆相关；不需要时传空串）。
//
// Returns:
//   []DicItem: 字典项数组（可能含 Zdzj 二级子项）。
//   error:     HTTP 或业务错误。
func (c *Client) QueryDic(token, dicType, vID string) ([]DicItem, error) {
	body := map[string]string{"type": dicType}
	if vID != "" {
		body["vId"] = vID
	}
	var result []DicItem
	if err := c.Post("/pro//ucDicController/queryDic", body, token, &result); err != nil {
		return nil, fmt.Errorf("字典查询失败: %w", err)
	}
	return result, nil
}
