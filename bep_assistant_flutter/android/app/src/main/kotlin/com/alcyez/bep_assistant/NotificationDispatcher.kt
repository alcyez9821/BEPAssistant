// Copyright (c) 2026 alcyez <alcyez9821@gmail.com>
// SPDX-License-Identifier: MIT
package com.alcyez.bep_assistant

import android.content.Context
import android.util.Log
import org.json.JSONObject

/**
 * File: NotificationDispatcher.kt
 * Purpose: 统一解析 Go 端 RunCheck 返回的 JSON 并派发通知；回写 ec_notified_at 去重标记。
 * Author: alcyez (AI Agent 辅助生成注释)
 * Created: 2026-07-06
 *
 * Business Context:
 *   之前 CheckService / CheckWorker 各自维护一份约 40 行的解析代码，Go 返回结构变更时
 *   要改两处。抽到这里后两方共用。
 *
 *   Go 返回的 TaskResult JSON 里有两组通知：
 *   1. notifications: 结构化通知，含 vehicle_id + notified_at 去重标记（到期/过期/审核通过/不通过）
 *      → 发通知 + 回写 ec_notified_at
 *   2. messages: 一次性通知（申请已提交、申请失败、缺默认值等）
 *      → 只发通知，不回写；且需要去重掉已在 notifications 里发过的
 *
 * Key Logic:
 *   1. 解析 notifications 数组 → 通知 → 通过 [updateVehicle] 回调回写去重标记
 *   2. 记录已发的 notifications 消息到 notifMessages set
 *   3. 遍历 messages 数组，跳过 notifMessages 里的重复消息
 *   4. 通知标题按 applied 分派："办理成功"（有申请提交）或"进京证提醒"（普通消息）
 *
 * Notes:
 *   [updateVehicle] 由调用方注入 —— CheckService.nativeUpdateVehicle vs
 *   CheckWorker.nativeUpdateVehicle 两处 JNI 符号名不同，不能在此单例内直接调用。
 */
object NotificationDispatcher {
    private const val TAG = "NotificationDispatcher"

    /**
     * 解析 Go 返回的 JSON 并派发通知。
     *
     * @param context       Android 上下文
     * @param resultJson    Go RunCheckWithCredentials 返回的 TaskResult JSON
     * @param updateVehicle 更新车辆字段的回调（外部注入以规避 JNI 符号绑定问题）；
     *                      发通知成功后会用此回调写 ec_notified_at 去重标记
     * @return `applied > 0` 时返回 true，供调用方启动申请后轮询（CheckWorker.schedulePolling）
     */
    fun dispatch(
        context: Context,
        resultJson: String,
        updateVehicle: (id: Long, updatesJson: String) -> Unit
    ): Boolean {
        val json = try {
            JSONObject(resultJson)
        } catch (e: Exception) {
            Log.e(TAG, "[dispatch] parse ERROR", e)
            return false
        }

        val applied = json.optInt("applied", 0)
        val notifications = json.optJSONArray("notifications")

        // 收集已通过 notifications 数组发过的消息，避免下面 messages 再重复发送。
        val notifMessages = mutableSetOf<String>()

        // notifications 数组：含 vehicle_id + notified_at 去重标记的通知（到期/过期/审核通过/不通过）
        if (notifications != null && notifications.length() > 0) {
            for (i in 0 until notifications.length()) {
                val entry = notifications.optJSONObject(i) ?: continue
                val msg = entry.optString("message", "")
                if (msg.isEmpty()) continue

                NotificationHelper.notify(context, "进京证提醒", msg)
                notifMessages.add(msg)

                val vehicleId = entry.optLong("vehicle_id", 0)
                val notifiedAt = entry.optString("notified_at", "")
                if (vehicleId > 0 && notifiedAt.isNotEmpty()) {
                    try {
                        val payload = JSONObject().put("ec_notified_at", notifiedAt).toString()
                        updateVehicle(vehicleId, payload)
                    } catch (e: Exception) {
                        Log.e(TAG, "[dispatch] 更新 ec_notified_at 失败", e)
                    }
                }
            }
        }

        // messages 数组：一次性通知（申请已提交、申请失败等），与 notifications 独立。
        // Go 端可能同时返回两组（例如"过期"通知 + "申请已提交"消息），必须都处理。
        val messages = json.optJSONArray("messages")
        if (messages != null && messages.length() > 0) {
            for (i in 0 until messages.length()) {
                val msg = messages.optString(i, "")
                if (msg.isNotEmpty() && msg !in notifMessages) {
                    // applied > 0 时用"办理成功"更醒目地告知用户
                    val title = if (applied > 0) "办理成功" else "进京证提醒"
                    NotificationHelper.notify(context, title, msg)
                }
            }
        }

        return applied > 0
    }
}
