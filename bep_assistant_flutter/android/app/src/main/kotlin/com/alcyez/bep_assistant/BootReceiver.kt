// Copyright (c) 2026 alcyez <alcyez9821@gmail.com>
// SPDX-License-Identifier: MIT
package com.alcyez.bep_assistant

import android.app.AlarmManager
import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent
import android.util.Log

/**
 * File: BootReceiver.kt
 * Purpose: 开机 / 应用更新 / 精确闹钟权限变化时恢复定时任务链，并在开机后补做一次检查。
 * Author: alcyez (AI Agent 辅助生成注释)
 * Created: 2026-07-06
 *
 * Business Context:
 *   Android 上闹钟不会跨重启保留；应用被强行停止（Android 15 起）或更新后也会丢失待处理闹钟。
 *   为了让"进京证到期提醒"不受这些情况影响，本 Receiver 监听：
 *   - ACTION_BOOT_COMPLETED：开机（Android 15 起应用从"强行停止"状态恢复时也会收到）
 *   - ACTION_MY_PACKAGE_REPLACED：应用升级 / 覆盖安装后
 *   - ACTION_SCHEDULE_EXACT_ALARM_PERMISSION_STATE_CHANGED：用户授予"闹钟和提醒"权限后，
 *     需要把降级注册的非精确闹钟换成 setAlarmClock
 *   无账户时静默返回，避免刚装 App 就打扰用户。
 *
 * Notes:
 *   - Android 15（targetSdk 35）禁止从 BOOT_COMPLETED 启动 dataSync 类型前台服务，
 *     因此开机检查一律走 WorkManager 一次性任务，而不是 CheckService。
 *   - 国产 ROM（MIUI / OriginOS / ColorOS 等）还要求用户手动开"自启动"，
 *     参见 [PermissionGuideDialog._buildAutoStartItem]。
 */
class BootReceiver : BroadcastReceiver() {

    companion object {
        private const val TAG = "BootReceiver"
    }

    /**
     * 广播回调。重活放在 goAsync + 子线程，避免开机阶段主线程 ANR。
     * 副作用：可能注册闹钟、入队 WorkManager 任务。
     */
    override fun onReceive(context: Context, intent: Intent) {
        val action = intent.action
        Log.i(TAG, "[onReceive] action=$action")
        val isBoot = action == Intent.ACTION_BOOT_COMPLETED
        val handled = isBoot ||
            action == Intent.ACTION_MY_PACKAGE_REPLACED ||
            action == AlarmManager.ACTION_SCHEDULE_EXACT_ALARM_PERMISSION_STATE_CHANGED
        if (!handled) return

        val appContext = context.applicationContext
        PollLog.log(appContext, "BOOT_RECEIVER action=$action")
        val pendingResult = goAsync()
        Thread {
            try {
                GoBridgeLoader.loadLibrary()
                val goReady = GoBridgeLoader.initIfNeeded(appContext) { CheckService.nativeInit(it) }
                if (!goReady) {
                    Log.w(TAG, "[onReceive] Go init failed, skip")
                    return@Thread
                }
                // 无账户时不启动任何后台任务，避免删完账号后重启仍空转
                if (!CredentialsGateway.hasAccounts(appContext)) {
                    Log.i(TAG, "[onReceive] no accounts, skip")
                    return@Thread
                }
                // 先恢复闹钟链，再做检查，避免检查异常影响定时任务
                AlarmReceiver.scheduleAlarms(appContext)
                if (isBoot) {
                    // 开机后补一次检查 —— 重启期间可能错过闹钟
                    AlarmReceiver.launchCheck(appContext, preferWorker = true)
                }
            } catch (e: Throwable) {
                Log.e(TAG, "[onReceive] failed", e)
            } finally {
                pendingResult.finish()
            }
        }.start()
    }
}
