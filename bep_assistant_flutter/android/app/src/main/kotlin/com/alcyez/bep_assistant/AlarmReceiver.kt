// Copyright (c) 2026 alcyez <alcyez9821@gmail.com>
// SPDX-License-Identifier: MIT
package com.alcyez.bep_assistant

import android.app.AlarmManager
import android.app.PendingIntent
import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent
import android.os.Build
import android.util.Log
import java.util.Calendar
import java.util.Random

/**
 * File: AlarmReceiver.kt
 * Purpose: 每日两次随机时间的定时检查闹钟 —— 到点广播触发 CheckService。
 * Author: alcyez (AI Agent 辅助生成注释)
 * Created: 2026-07-06
 *
 * Business Context:
 *   北京交警官方 app 会检测短时间内高频调用作为爬虫特征。为分散请求时段，每天在
 *   8:00-12:00 之间随机挑一个时间点触发一次检查，再在 8 小时后（16:00-20:00 之间）
 *   触发第二次。用户对"上午/傍晚各一次"这种密度基本无感，但不容易被服务端识别为固定
 *   刷新频率。
 *
 * Key Logic:
 *   1. scheduleAlarms：随机计算今日两个时间点（已过则推到明天），注册 setAlarmClock
 *      精确闹钟；Android 12+ 未授权精确闹钟时降级到 setWindow ±5 分钟。
 *   2. onReceive：触发前先检查有账户；无账户则不再启动 CheckService、也不排明天的闹钟
 *      —— 删完账号后即使闹钟已排，也能自然退出而不再打扰用户。
 *   3. 每次触发时立即排明天的两个闹钟，保证链不断（不依赖 setRepeating，因为随机时间点）。
 */
class AlarmReceiver : BroadcastReceiver() {

    companion object {
        private const val TAG = "AlarmReceiver"
        private const val REQUEST_CODE_FIRST = 1001
        private const val REQUEST_CODE_SECOND = 1002
        private const val ACTION_FIRST = "com.alcyez.bep_assistant.ALARM_FIRST"
        private const val ACTION_SECOND = "com.alcyez.bep_assistant.ALARM_SECOND"

        /**
         * 注册今日两个闹钟（随机 8-12 点 + 8h 后），已过则推到明天。
         *
         * 随机时间生成规则：
         *   firstTime  = 今天 [8-11] 点 [0-59] 分（若已过，滚到明天）
         *   secondTime = firstTime + 8 小时
         *
         * 两次触发间隔 8 小时能让上午 / 傍晚各刷新一次，覆盖大多数用户想看到到期提醒的时间段。
         */
        fun scheduleAlarms(context: Context) {
            Log.i(TAG, "[scheduleAlarms] called")
            val alarmManager = context.getSystemService(Context.ALARM_SERVICE) as AlarmManager
            val random = Random()

            val firstTime = Calendar.getInstance().apply {
                set(Calendar.HOUR_OF_DAY, 8 + random.nextInt(4))
                set(Calendar.MINUTE, random.nextInt(60))
                set(Calendar.SECOND, 0)
                set(Calendar.MILLISECOND, 0)
                if (before(Calendar.getInstance())) add(Calendar.DAY_OF_MONTH, 1)
            }

            val secondTime = (firstTime.clone() as Calendar).apply {
                add(Calendar.HOUR_OF_DAY, 8)
            }

            registerAlarm(context, alarmManager, REQUEST_CODE_FIRST, ACTION_FIRST, firstTime)
            registerAlarm(context, alarmManager, REQUEST_CODE_SECOND, ACTION_SECOND, secondTime)
            Log.i(TAG, "[scheduleAlarms] registered first=${firstTime.time} second=${secondTime.time}")
        }

        /**
         * 取消所有已注册的闹钟。
         *
         * 删完最后一个账号时调用；避免"空账户但闹钟还在响"导致 CheckService 空转，
         * 每天弹出"正在执行定时检查..."通知打扰用户。
         */
        fun cancelAlarms(context: Context) {
            val alarmManager = context.getSystemService(Context.ALARM_SERVICE) as AlarmManager
            for ((requestCode, action) in listOf(
                REQUEST_CODE_FIRST to ACTION_FIRST,
                REQUEST_CODE_SECOND to ACTION_SECOND,
            )) {
                val intent = Intent(context, AlarmReceiver::class.java).apply { this.action = action }
                val pendingIntent = PendingIntent.getBroadcast(
                    context, requestCode, intent,
                    PendingIntent.FLAG_UPDATE_CURRENT or PendingIntent.FLAG_IMMUTABLE
                )
                alarmManager.cancel(pendingIntent)
                pendingIntent.cancel()
            }
            Log.i(TAG, "[cancelAlarms] cancelled all")
        }

        /**
         * 注册单个闹钟，Android 12+ 精确闹钟权限缺失时自动降级到 setWindow。
         *
         * @param context      Android 上下文
         * @param alarmManager AlarmManager 实例（外部拿一次复用）
         * @param requestCode  PendingIntent request code（1001 / 1002 分别是两次闹钟）
         * @param action       BroadcastReceiver 里区分两次闹钟的 action string
         * @param target       目标时间点（Calendar）
         */
        private fun registerAlarm(
            context: Context, alarmManager: AlarmManager,
            requestCode: Int, action: String, target: Calendar
        ) {
            val intent = Intent(context, AlarmReceiver::class.java).apply { this.action = action }
            val pendingIntent = PendingIntent.getBroadcast(
                context, requestCode, intent,
                PendingIntent.FLAG_UPDATE_CURRENT or PendingIntent.FLAG_IMMUTABLE
            )
            try {
                // Android 12+ 需运行时权限：未授予时降级到不精确闹钟，避免 SecurityException 静默丢失
                val canExact = Build.VERSION.SDK_INT < Build.VERSION_CODES.S ||
                    alarmManager.canScheduleExactAlarms()
                if (canExact) {
                    // setAlarmClock 是最高优先级：即使 Doze 也会准时唤醒；点击闹钟图标能拉起 App
                    val showIntent = PendingIntent.getActivity(
                        context, 0,
                        context.packageManager.getLaunchIntentForPackage(context.packageName),
                        PendingIntent.FLAG_IMMUTABLE
                    )
                    alarmManager.setAlarmClock(
                        AlarmManager.AlarmClockInfo(target.timeInMillis, showIntent), pendingIntent
                    )
                } else {
                    Log.w(TAG, "精确闹钟权限未授予，降级为 setWindow（±5 分钟）")
                    alarmManager.setWindow(
                        AlarmManager.RTC_WAKEUP,
                        target.timeInMillis,
                        5 * 60 * 1000L,
                        pendingIntent
                    )
                }
            } catch (e: SecurityException) {
                Log.e(TAG, "注册闹钟被拒（精确闹钟权限未授予）", e)
            } catch (e: Exception) {
                Log.e(TAG, "注册闹钟失败", e)
            }
        }
    }

    /**
     * 闹钟到点广播回调。
     *
     * 处理顺序：
     *   1. 加载 Go 库 + init（保证下面 hasAccounts 走 Go FFI 查 DB）
     *   2. 无账户则 cancelAlarms 阻止后续触发 —— 用户删完账号后立即生效
     *   3. 启动 CheckService（Android 8+ 用 startForegroundService）
     *   4. 排明天的两个闹钟，保证链不断
     */
    override fun onReceive(context: Context, intent: Intent) {
        Log.i(TAG, "[onReceive] action=${intent.action}")

        // 先确保 Go 初始化，才能通过 FFI 查 accounts
        GoBridgeLoader.loadLibrary()
        val goReady = GoBridgeLoader.initIfNeeded(context) { CheckService.nativeInit(it) }
        if (!goReady) {
            Log.w(TAG, "[onReceive] Go init failed, skip")
            return
        }

        // 无账户则不启动前台服务、也不再排明天的闹钟，避免删完账号后仍每日弹出"正在执行定时检查..."
        if (!CredentialsGateway.hasAccounts(context)) {
            Log.i(TAG, "[onReceive] no accounts, cancelling future alarms")
            cancelAlarms(context)
            return
        }

        val serviceIntent = Intent(context, CheckService::class.java)
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.O) {
            Log.i(TAG, "[onReceive] startForegroundService")
            context.startForegroundService(serviceIntent)
        } else {
            context.startService(serviceIntent)
        }

        // 注册明天的两个闹钟，保证链不断（AlarmManager 用 requestCode 幂等，会覆盖）
        scheduleAlarms(context)
    }
}
