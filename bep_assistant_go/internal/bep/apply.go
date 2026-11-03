// Copyright (c) 2026 alcyez <alcyez9821@gmail.com>
// SPDX-License-Identifier: MIT

// File: apply.go
// Purpose: 申请进京证的完整 8 步流程 —— 从车辆校验到最终提交申请单。
// Author: alcyez (AI Agent 辅助生成注释)
// Created: 2026-07-06
//
// Business Context:
//   北京交警不提供"一键申请"接口，需要客户端串起 8 个前置调用：车辆校验、驾驶人信息、
//   次数校验、路线检查、拿可选日期、拿车辆额度详情、字典编码解析、最终提交。任一步骤
//   失败都要立即中断，避免后续 API 拿到脏状态。
//
// Key Logic:
//   Step 1  applyVehicleCheck    校验此车牌 / 号牌种类是否允许办证
//   Step 2  GetJsrxx              获取驾驶人姓名 / 驾驶证号 / 档案编号
//   Step 3  applyCheckNum         校验驾驶人本人本年度剩余办理次数（六环内 5 次/年）
//   Step 4  checkInputRoadInfo    是否需要填写具体行驶路线（当前一律传空跳过）
//   Step 5  CheckHandle          获取可选进京日期，默认取第一个
//   Step 6  GetStateList         再次拿车辆额度、六环内外描述文本（Step 8 参数需要）
//   Step 7  resolveDicCode       把用户填的"大兴区" / "其它" 翻译成服务端字典编码
//   Step 8  insertApplyRecord    最终提交申请单，服务端进入审核队列
package bep

import (
	"fmt"

	"github.com/alcyez9821/BEPAssistant/bep_assistant_go/internal/util"
)

// ApplyOptions 申请进京证的入参聚合。
//
// 字段说明：
//   VID:   车辆 ID（服务端主键；从 stateList 拿到）
//   Hphm:  号牌号码
//   Hpzl:  号牌种类
//   Jjzzl: 进京证种类，"01"=六环内 "02"=六环外；空串默认 "02"
//   Jjrq:  进京日期 YYYY-MM-DD；空则取 checkHandle 返回的第一个
//   Area:  进京区域中文名（例如"大兴区"）；Step 7 会翻译成字典编码
//   Xxdz:  详细地址（手工输入）
//   Jjmd:  进京目的中文名（例如"其它"）；空串默认"其它"
type ApplyOptions struct {
	VID   string
	Hphm  string
	Hpzl  string
	Jjzzl string
	Jjrq  string
	Area  string
	Xxdz  string
	Jjmd  string
}

// ApplyForPermit 申请进京证 —— 8 步流程的完整实现。
//
// Args:
//   token: JJZ accessToken。
//   opts:  申请参数；Jjzzl/Jjmd 空串走默认。
//
// Returns:
//   *APIResponse: 服务端 Step 8 的完整响应（含 msg，用于给用户提示"申请已受理"等文案）。
//   error:        任一步骤失败；error 消息里包含 "stepN xxx" 前缀便于定位是哪步挂了。
//
// Notes:
//   - 8 步之间强顺序依赖：Step 2 拿到 jsrxx 才能做 Step 3 校验；Step 5 拿到 jjrq 才能
//     做 Step 8 提交。中间任一失败立即返回。
//   - Step 6 又调了一次 stateList，看似冗余但目的是拿最新的 ylzsfkb/elzsfkb/elzqyms/
//     ylzqyms 字段，这些是 Step 8 必填参数且外部调用方不方便自己传。
//   - 若 opts.Area / opts.Jjmd 是字典里没有的名称（例如笔误"其他"），resolveDicCode
//     返回空串会立即中断，避免用空编码提交导致服务端返回令人困惑的错误。
func (c *Client) ApplyForPermit(token string, opts ApplyOptions) (*APIResponse, error) {
	// 默认值兜底：Jjzzl / Jjmd 为空时用最常用值
	if opts.Jjzzl == "" {
		opts.Jjzzl = "02"
	}
	if opts.Jjmd == "" {
		opts.Jjmd = "其它"
	}

	// Step 1: 校验车辆
	if err := c.applyVehicleCheck(token, opts.Hphm, opts.Hpzl); err != nil {
		return nil, fmt.Errorf("step1 车辆校验: %w", err)
	}

	// Step 2: 获取驾驶人信息
	jsrxx, err := c.GetJsrxx(token)
	if err != nil {
		return nil, fmt.Errorf("step2 驾驶人信息: %w", err)
	}

	// Step 3: 校验办理次数
	if err := c.applyCheckNum(token, jsrxx); err != nil {
		return nil, fmt.Errorf("step3 次数校验: %w", err)
	}

	// Step 4: 检查行驶路线
	if err := c.checkInputRoadInfo(token, opts.VID); err != nil {
		return nil, fmt.Errorf("step4 路线检查: %w", err)
	}

	// Step 5: 获取可选日期
	handleData, err := c.CheckHandle(token, opts.VID, opts.Jjzzl, opts.Hphm)
	if err != nil {
		return nil, fmt.Errorf("step5 可选日期: %w", err)
	}
	jjrq := opts.Jjrq
	if jjrq == "" {
		if len(handleData.Jjrqs) == 0 {
			return nil, fmt.Errorf("step5 没有可选的进京日期")
		}
		jjrq = handleData.Jjrqs[0]
	}

	// Step 6: 获取车辆详情和描述信息（Step 8 提交时要透传六环内外描述文案）
	stateData, err := c.GetStateList(token)
	if err != nil {
		return nil, fmt.Errorf("step6 查询状态: %w", err)
	}
	var vehicle *VehicleInfo
	for i := range stateData.Bzclxx {
		if stateData.Bzclxx[i].VID == opts.VID {
			vehicle = &stateData.Bzclxx[i]
			break
		}
	}
	if vehicle == nil {
		return nil, fmt.Errorf("step6 未找到车辆 %s", opts.VID)
	}

	// Step 7: 查字典解析编码 —— 用户填的中文名 → 服务端要求的编码
	jjdq, err := c.resolveDicCode(token, "qjxz", opts.Area, "")
	if err != nil {
		return nil, fmt.Errorf("step7 区域编码: %w", err)
	}
	if jjdq == "" {
		return nil, fmt.Errorf("step7 未找到区域\"%s\"对应的编码", opts.Area)
	}

	jjmdCode, err := c.resolveDicCode(token, "jjmd", opts.Jjmd, "")
	if err != nil {
		return nil, fmt.Errorf("step7 目的编码: %w", err)
	}
	if jjmdCode == "" {
		return nil, fmt.Errorf("step7 未找到目的\"%s\"对应的编码", opts.Jjmd)
	}

	// Step 8: 提交申请
	//
	// 参数说明：
	//   sqdzgdjd/sqdzgdwd: 申请地址高德经纬度，硬编码天安门坐标（服务端未做严格校验）
	//   jjdzgdjd/jjdzgdwd: 进京地址高德经纬度，传空由服务端根据 area+xxdz 反查
	//   zjxxdz / zjxxdzgd*: 中间详细地址，与主地址一致时直接复用（未来支持多段路线时会分开填）
	//   sfzj / txrxx / jjlk*: 是否携带 / 同行人 / 进京路口相关字段，当前不涉及，填默认值
	params := map[string]interface{}{
		"vId":      opts.VID,
		"hphm":     opts.Hphm,
		"hpzl":     opts.Hpzl,
		"ylzsfkb":  vehicle.Ylzsfkb,
		"elzsfkb":  vehicle.Elzsfkb,
		"elzqyms":  stateData.Elzqyms,
		"ylzqyms":  stateData.Ylzqyms,
		"elzmc":    util.OrDefault(stateData.Elzmc, "进京证(六环外)"),
		"ylzmc":    util.OrDefault(stateData.Ylzmc, "进京证(六环内)"),
		"cllx":     util.OrDefault(vehicle.Cllx, "01"),
		"jjzzl":    opts.Jjzzl,
		"jsrxm":    jsrxx.Jsrxm,
		"jszh":     jsrxx.Jszh,
		"dabh":     jsrxx.Dabh,
		"rylx":     rylxOf(jsrxx),
		"txrxx":    []string{},
		"jjrq":     jjrq,
		"area":     opts.Area,
		"jjdq":     jjdq,
		"xxdz":     opts.Xxdz,
		"jjdzgdwd": "", "jjdzgdjd": "", "jingState": "",
		"jjmd": jjmdCode, "jjmdmc": opts.Jjmd,
		"sqdzgdjd": "116.4", "sqdzgdwd": "39.9", // 硬编码天安门坐标；服务端未严格校验
		"sfzj":       "1",
		"zjxxdz":     opts.Xxdz,
		"zjxxdzgdjd": "", "zjxxdzgdwd": "",
		"jjlk": "", "jjlkmc": "", "jjlkgdjd": "", "jjlkgdwd": "",
	}

	result, err := c.PostRaw("/pro//applyRecordController/insertApplyRecord", params, token)
	if err != nil {
		return nil, fmt.Errorf("step8 提交申请: %w", err)
	}
	return result, nil
}

// applyVehicleCheck Step 1：校验车辆是否可办进京证。
//
// 常见拒绝原因：非本地车牌、被扣分、被冻结办证权限等。返回 nil 表示可办。
func (c *Client) applyVehicleCheck(token, hphm, hpzl string) error {
	return c.Post("/pro//applyRecordController/applyVehicleCheck",
		map[string]string{"hphm": hphm, "hpzl": hpzl}, token, nil)
}

// applyCheckNum Step 3：校验驾驶人剩余办理次数。
//
// 北京交警对同一驾驶人有全年办理次数限制（六环内 5 次/年），超过会被拒绝。
// txrxx / txrkg / wtxr 是同行人相关字段，我们不涉及，传空/默认值即可。
func (c *Client) applyCheckNum(token string, jsrxx *DriverInfo) error {
	body := map[string]interface{}{
		"jsrxm": jsrxx.Jsrxm,
		"jszh":  jsrxx.Jszh,
		"dabh":  jsrxx.Dabh,
		"rylx":  rylxOf(jsrxx),
		"txrxx": []string{},
		"txrkg": "0",
		"wtxr":  "",
	}
	return c.Post("/pro//applyRecordController/applyCheckNum", body, token, nil)
}

// rylxOf 返回驾驶人员类型，供 applyCheckNum / insertApplyRecord 请求体使用。
//
// getJsrxx 返回 rylx 为 "1"（字符串或数字）时视为港澳台及外籍人士返回 "1"，
// 其余情况（含缺失、nil）一律按普通人员返回 "0"，与 H5 前端行为一致。
func rylxOf(d *DriverInfo) string {
	if d != nil && d.Rylx != nil && fmt.Sprint(d.Rylx) == "1" {
		return "1"
	}
	return "0"
}

// checkInputRoadInfo Step 4：是否需要填写行驶路线。
//
// 目前所有车辆都不需要路线信息，服务端返回 nil 视为通过。保留这一步以便未来遇到
// 特殊车辆要求填路线时便于扩展。
func (c *Client) checkInputRoadInfo(token, vID string) error {
	return c.Post("/pro//applyRecordController/checkInputRoadInfo",
		map[string]string{"vId": vID}, token, nil)
}

// resolveDicCode 从字典里查找中文名对应的服务端编码。
//
// 遍历一级和二级子项（Zdzj），完全匹配 Zdz（名称）时返回 Zdbm（编码）；查不到返回空串。
// 上层收到空串会返回明确错误"未找到 xxx 对应的编码"，避免用空编码提交。
//
// Args:
//   token:   accessToken。
//   dicType: 字典类型（qjxz / jjmd 等）。
//   name:    要查的中文名（用户在 UI 上选择的值）。
//   vID:     部分字典依赖车辆；无关时传空串。
//
// Returns:
//   string: 找到的编码，或空串。
//   error:  QueryDic 底层错误。
func (c *Client) resolveDicCode(token, dicType, name, vID string) (string, error) {
	list, err := c.QueryDic(token, dicType, vID)
	if err != nil {
		return "", err
	}
	for _, item := range list {
		if item.Zdz == name {
			return item.Zdbm, nil
		}
		for _, sub := range item.Zdzj {
			if sub.Zdz == name {
				return sub.Zdbm, nil
			}
		}
	}
	return "", nil
}
