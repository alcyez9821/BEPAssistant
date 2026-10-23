// Copyright (c) 2026 alcyez <alcyez9821@gmail.com>
// SPDX-License-Identifier: MIT
package com.alcyez.bep_assistant

import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent
import android.util.Log

/**
 * File: BootReceiver.kt
 * Purpose: 开机自启 —— 系统开机完成后立即执行一次检查并注册闹钟，恢复定时任务链。
 * Author: alcyez (AI Agent 辅助生成注释)
 * Created: 2026-07-06
 *
 * Business Context:
 *   Android 上闹钟不会跨重启保留；每次系统重启后 AlarmManager 的注册全部清空。
 *   为了让"进京证到期提醒"不受用户重启手机影响，BootReceiver 监听
 *   ACTION_BOOT_COMPLETED，开机后立即：
 *   1. 检查有账户 → 拉起 CheckService 做一次即时检查
 *   2. 注册后续闹钟（AlarmReceiver.scheduleAlarms）
 *   无账户时静默返回，避免刚装 App 就打扰用户。
 *
 * Notes:
 *   AndroidManifest 需声明 android.permission.RECEIVE_BOOT_COMPLETED 并注册 receiver；
 *   MIUI 还额外要求用户手动开"自启动"，参见 [PermissionGuideDialog._buildAutoStartItem]。
 */
class BootReceiver : BroadcastReceiver() {

    companion object {
        private const val TAG = "BootReceiver"
    }

    /**
     * 开机完成广播回调。仅处理 ACTION_BOOT_COMPLETED，其他 action 静默忽略。
     */
    override fun onReceive(context: Context, intent: Intent) {
        Log.i(TAG, "[onReceive] action=${intent.action}")
        if (intent.action == Intent.ACTION_BOOT_COMPLETED) {
            GoBridgeLoader.loadLibrary()
            val goReady = GoBridgeLoader.initIfNeeded(context) { CheckService.nativeInit(it) }
            if (!goReady) {
                Log.w(TAG, "[onReceive] Go init failed, skip")
                return
            }
            // 无账户时开机不启动任何后台任务，避免删完账号后重启仍空转
            if (!CredentialsGateway.hasAccounts(context)) {
                Log.i(TAG, "[onReceive] boot completed but no accounts, skip")
                return
            }
            Log.i(TAG, "[onReceive] boot completed, launching CheckService + scheduleAlarms")
            // 立即执行一次检查 —— 让重启后的到期提醒不用等到下次闹钟
            val serviceIntent = Intent(context, CheckService::class.java)
            if (android.os.Build.VERSION.SDK_INT >= android.os.Build.VERSION_CODES.O) {
                context.startForegroundService(serviceIntent)
            } else {
                context.startService(serviceIntent)
            }
            // 注册后续定时任务：恢复每日两次随机闹钟
            AlarmReceiver.scheduleAlarms(context)
        }
    }
}
