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
 * Purpose: 每日两次随机时间的定时检查闹钟 —— 到点广播触发 CheckService（失败时降级 WorkManager）。
 * Author: alcyez (AI Agent 辅助生成注释)
 * Created: 2026-07-06
 *
 * Business Context:
 *   北京交警官方 app 会检测短时间内高频调用作为爬虫特征。为分散请求时段，每天在
 *   上午窗口 [8:00, 12:00) 随机挑一个时间点触发一次检查，再在傍晚窗口 [16:00, 20:00)
 *   随机触发第二次。用户对"上午/傍晚各一次"这种密度基本无感，但不容易被服务端识别为固定
 *   刷新频率。
 *
 * Key Logic:
 *   1. 两个时段（FIRST / SECOND）独立调度，下一次触发时间持久化在 SharedPreferences：
 *      - scheduleAlarms：若已存的时间仍在未来则原样重注册（App 每次冷启动调用不会把已排好的
 *        闹钟重新随机，也不会导致当天重复/漏触发）；否则按"今天窗口未过取今天，否则明天"计算
 *      - 某时段触发后只把该时段排到"明天"，不影响另一个时段（修复旧实现中第一个闹钟触发时
 *        重排会覆盖掉当天第二个闹钟、或当天再触发一次的问题）
 *   2. 有精确闹钟权限时用 setAlarmClock（Doze 下准时 + 允许后台启动前台服务）；
 *      无权限时降级为 setAndAllowWhileIdle（Doze 下也能投递，但时间不精确）。
 *   3. onReceive 先续排下一次闹钟、再执行检查 —— 即使后续步骤抛异常，闹钟链也不会断。
 *   4. 启动 CheckService 失败（Android 12+ 非精确闹钟触发时不允许后台启动前台服务，
 *      会抛 ForegroundServiceStartNotAllowedException）时降级为 WorkManager 加急任务。
 *   5. 重活（Go 初始化、查 DB）放到 goAsync + 子线程，避免主线程 ANR。
 */
class AlarmReceiver : BroadcastReceiver() {

    companion object {
        private const val TAG = "AlarmReceiver"
        private const val REQUEST_CODE_FIRST = 1001
        private const val REQUEST_CODE_SECOND = 1002
        private const val ACTION_FIRST = "com.alcyez.bep_assistant.ALARM_FIRST"
        private const val ACTION_SECOND = "com.alcyez.bep_assistant.ALARM_SECOND"

        /** SharedPreferences 命名空间：保存每个时段的下一次触发时间（epoch millis） */
        private const val PREF_NAME = "alarm_prefs"
        private const val KEY_NEXT_FIRST = "next_first_millis"
        private const val KEY_NEXT_SECOND = "next_second_millis"

        /**
         * 一个时段的静态描述。
         *
         * @property startHour 窗口起始小时（含）
         * @property spanHours 窗口长度（小时），实际触发时间在 [startHour, startHour+spanHours) 内随机
         */
        private enum class Slot(
            val requestCode: Int,
            val action: String,
            val prefKey: String,
            val startHour: Int,
            val spanHours: Int,
        ) {
            FIRST(REQUEST_CODE_FIRST, ACTION_FIRST, KEY_NEXT_FIRST, 8, 4),
            SECOND(REQUEST_CODE_SECOND, ACTION_SECOND, KEY_NEXT_SECOND, 16, 4),
        }

        /**
         * 注册（或恢复）两个时段的闹钟。App 冷启动、开机、精确闹钟权限变化时调用。
         *
         * 已持久化的触发时间仍在未来时原样重注册（幂等）；已过期或不存在时重新随机计算。
         * 副作用：写 SharedPreferences、调用 AlarmManager。
         */
        fun scheduleAlarms(context: Context) {
            Log.i(TAG, "[scheduleAlarms] called")
            if (TestMode.isEnabled) {
                // 测试模式：只用 FIRST 一个闹钟，每 N 分钟一次；撤掉 SECOND 避免干扰
                cancelSlot(context, Slot.SECOND)
                val stored = context.getSharedPreferences(PREF_NAME, Context.MODE_PRIVATE)
                    .getLong(Slot.FIRST.prefKey, 0L)
                val now = System.currentTimeMillis()
                val maxAhead = TestMode.INTERVAL_MINUTES * 60_000L
                // 已存时间仍在未来且不超过一个周期（防止残留正式模式的"明天"时间）则沿用
                val target = if (stored > now && stored - now <= maxAhead) stored else nextTriggerMillis(Slot.FIRST, now, allowToday = true)
                registerSlot(context, Slot.FIRST, target)
                PollLog.log(context, "SCHEDULED test-mode next=${java.util.Date(target)} exact=${canScheduleExact(context)}")
                return
            }
            val prefs = context.getSharedPreferences(PREF_NAME, Context.MODE_PRIVATE)
            val now = System.currentTimeMillis()
            for (slot in Slot.values()) {
                val stored = prefs.getLong(slot.prefKey, 0L)
                val target = if (stored > now) stored else nextTriggerMillis(slot, now, allowToday = true)
                registerSlot(context, slot, target)
            }
        }

        /**
         * 取消所有已注册的闹钟并清除持久化的触发时间。
         *
         * 删完最后一个账号时调用；避免"空账户但闹钟还在响"导致 CheckService 空转，
         * 每天弹出"正在执行定时检查..."通知打扰用户。
         */
        fun cancelAlarms(context: Context) {
            for (slot in Slot.values()) cancelSlot(context, slot)
            context.getSharedPreferences(PREF_NAME, Context.MODE_PRIVATE).edit().clear().apply()
            Log.i(TAG, "[cancelAlarms] cancelled all")
        }

        /**
         * 计算某时段的下一次随机触发时间。
         *
         * @param now        当前时间（epoch millis）
         * @param allowToday true 时若今天窗口内随机到的时间点尚未过去则取今天；
         *                   false 时强制从明天开始（用于该时段刚触发完后的续排）
         * @return 下一次触发时间（epoch millis）
         */
        private fun nextTriggerMillis(slot: Slot, now: Long, allowToday: Boolean): Long {
            if (TestMode.isEnabled) return now + TestMode.INTERVAL_MINUTES * 60_000L
            val random = Random()
            val cal = Calendar.getInstance().apply {
                timeInMillis = now
                set(Calendar.HOUR_OF_DAY, slot.startHour + random.nextInt(slot.spanHours))
                set(Calendar.MINUTE, random.nextInt(60))
                set(Calendar.SECOND, 0)
                set(Calendar.MILLISECOND, 0)
            }
            if (!allowToday || cal.timeInMillis <= now) cal.add(Calendar.DAY_OF_MONTH, 1)
            return cal.timeInMillis
        }

        /**
         * 【测试】[delayMs] 毫秒后触发一次 FIRST 闹钟（覆盖当前已排时间），用于在云真机上快速验证后台链路。
         * 副作用：AlarmManager 注册、写 SharedPreferences、写轮询日志。
         */
        fun scheduleTestAlarmIn(context: Context, delayMs: Long) {
            val target = System.currentTimeMillis() + delayMs
            registerSlot(context, Slot.FIRST, target)
            PollLog.log(context, "SCHEDULED manual-test next=${java.util.Date(target)} exact=${canScheduleExact(context)}")
        }

        /**
         * 【测试】导出闹钟调度状态，供诊断页展示。
         *
         * pendingIntentAlive=false 而 next 有值，说明闹钟已被系统清除（如应用被强行停止 ——
         * Android 15+ 强行停止会取消该应用全部 PendingIntent）。
         *
         * @return 多行文本
         */
        fun debugState(context: Context): String {
            val prefs = context.getSharedPreferences(PREF_NAME, Context.MODE_PRIVATE)
            val sb = StringBuilder()
            for (slot in Slot.values()) {
                val next = prefs.getLong(slot.prefKey, 0L)
                val intent = Intent(context, AlarmReceiver::class.java).apply { action = slot.action }
                val alive = PendingIntent.getBroadcast(
                    context, slot.requestCode, intent,
                    PendingIntent.FLAG_NO_CREATE or PendingIntent.FLAG_IMMUTABLE
                ) != null
                sb.append("$slot next=${if (next > 0) java.util.Date(next) else "-"} pendingIntentAlive=$alive\n")
            }
            val alarmManager = context.getSystemService(Context.ALARM_SERVICE) as AlarmManager
            val clock = alarmManager.nextAlarmClock
            val clockOwner = clock?.showIntent?.creatorPackage
            sb.append("系统下一个闹钟(AlarmClock)=${clock?.let { java.util.Date(it.triggerTime) } ?: "-"} owner=$clockOwner\n")
            sb.append("canScheduleExact=${canScheduleExact(context)}\n")
            return sb.toString()
        }

        /** 取消单个时段的闹钟（不清除持久化时间）。 */
        private fun cancelSlot(context: Context, slot: Slot) {
            val alarmManager = context.getSystemService(Context.ALARM_SERVICE) as AlarmManager
            val pendingIntent = buildPendingIntent(context, slot)
            alarmManager.cancel(pendingIntent)
            pendingIntent.cancel()
        }

        /** 构造某时段对应的广播 PendingIntent（requestCode + action 区分时段，FLAG_UPDATE_CURRENT 幂等覆盖）。 */
        private fun buildPendingIntent(context: Context, slot: Slot): PendingIntent {
            val intent = Intent(context, AlarmReceiver::class.java).apply { action = slot.action }
            return PendingIntent.getBroadcast(
                context, slot.requestCode, intent,
                PendingIntent.FLAG_UPDATE_CURRENT or PendingIntent.FLAG_IMMUTABLE
            )
        }

        /**
         * 注册单个时段的闹钟并持久化触发时间。
         *
         * 有精确闹钟权限 → setAlarmClock（最高优先级，Doze 下准时，且系统允许其广播从后台启动前台服务）；
         * 无权限 → setAndAllowWhileIdle（Doze 下仍可投递，时间由系统批量调整，可能延后）。
         * 副作用：AlarmManager 注册、写 SharedPreferences。所有异常被吞掉并记日志，避免影响调用方。
         *
         * @param targetMillis 目标触发时间（epoch millis）
         */
        private fun registerSlot(context: Context, slot: Slot, targetMillis: Long) {
            val alarmManager = context.getSystemService(Context.ALARM_SERVICE) as AlarmManager
            val pendingIntent = buildPendingIntent(context, slot)
            try {
                if (canScheduleExact(context)) {
                    val launchIntent = context.packageManager.getLaunchIntentForPackage(context.packageName)
                        ?: Intent(context, MainActivity::class.java)
                    val showIntent = PendingIntent.getActivity(
                        context, 0, launchIntent, PendingIntent.FLAG_IMMUTABLE
                    )
                    alarmManager.setAlarmClock(
                        AlarmManager.AlarmClockInfo(targetMillis, showIntent), pendingIntent
                    )
                    Log.i(TAG, "[registerSlot] $slot setAlarmClock at ${java.util.Date(targetMillis)}")
                } else {
                    Log.w(TAG, "[registerSlot] $slot 无精确闹钟权限，降级为 setAndAllowWhileIdle at ${java.util.Date(targetMillis)}")
                    alarmManager.setAndAllowWhileIdle(AlarmManager.RTC_WAKEUP, targetMillis, pendingIntent)
                }
                context.getSharedPreferences(PREF_NAME, Context.MODE_PRIVATE)
                    .edit().putLong(slot.prefKey, targetMillis).apply()
            } catch (e: SecurityException) {
                Log.e(TAG, "注册闹钟被拒（精确闹钟权限未授予）", e)
            } catch (e: Exception) {
                Log.e(TAG, "注册闹钟失败", e)
            }
        }

        /**
         * 当前是否可注册精确闹钟。Android 12 以下恒为 true；12+ 取决于用户是否授予
         * "闹钟和提醒"（SCHEDULE_EXACT_ALARM）权限 —— Android 14+ 新安装默认拒绝。
         */
        fun canScheduleExact(context: Context): Boolean {
            if (Build.VERSION.SDK_INT < Build.VERSION_CODES.S) return true
            val alarmManager = context.getSystemService(Context.ALARM_SERVICE) as AlarmManager
            return alarmManager.canScheduleExactAlarms()
        }

        /**
         * 拉起一次后台检查：优先前台服务 CheckService，被系统拒绝时降级为 WorkManager 加急任务。
         *
         * Android 12+ 仅在少数豁免场景（精确闹钟触发、用户可见等）允许后台启动前台服务；
         * 非精确闹钟、开机广播（Android 15 禁止 dataSync 类型）等场景会抛
         * ForegroundServiceStartNotAllowedException（IllegalStateException 子类）。
         *
         * @param preferWorker true 时跳过前台服务直接走 WorkManager（已知不允许启动前台服务的场景）
         */
        fun launchCheck(context: Context, preferWorker: Boolean = false) {
            if (!preferWorker) {
                try {
                    val serviceIntent = Intent(context, CheckService::class.java)
                    if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.O) {
                        context.startForegroundService(serviceIntent)
                    } else {
                        context.startService(serviceIntent)
                    }
                    Log.i(TAG, "[launchCheck] CheckService started")
                    PollLog.log(context, "LAUNCH via=CheckService")
                    return
                } catch (e: IllegalStateException) {
                    Log.w(TAG, "[launchCheck] 前台服务启动被拒，降级 WorkManager", e)
                } catch (e: SecurityException) {
                    Log.w(TAG, "[launchCheck] 前台服务启动被拒（权限），降级 WorkManager", e)
                }
            }
            PollLog.log(context, "LAUNCH via=WorkManager preferWorker=$preferWorker")
            CheckWorker.enqueueOneTimeCheck(context)
        }
    }

    /**
     * 闹钟到点广播回调。
     *
     * 处理顺序：
     *   1. 立即把触发的这个时段续排到明天（不依赖 Go，保证链不断）
     *   2. goAsync + 子线程：加载 Go 库 + init → 无账户则 cancelAlarms 并退出
     *   3. 有账户 → launchCheck（前台服务，失败降级 WorkManager）
     */
    override fun onReceive(context: Context, intent: Intent) {
        Log.i(TAG, "[onReceive] action=${intent.action}")
        val slot = Slot.values().firstOrNull { it.action == intent.action }
        if (slot == null) {
            Log.w(TAG, "[onReceive] unknown action, ignore")
            return
        }

        PollLog.log(context, "ALARM_FIRED slot=$slot exact=${canScheduleExact(context)}")

        // 第一步就续排，确保后续任何异常都不会让闹钟链断掉
        val next = nextTriggerMillis(slot, System.currentTimeMillis(), allowToday = false)
        registerSlot(context, slot, next)
        PollLog.log(context, "SCHEDULED slot=$slot next=${java.util.Date(next)}")

        val appContext = context.applicationContext
        val pendingResult = goAsync()
        Thread {
            try {
                GoBridgeLoader.loadLibrary()
                val goReady = GoBridgeLoader.initIfNeeded(appContext) { CheckService.nativeInit(it) }
                if (!goReady) {
                    // Go 初始化失败也交给 Worker：Worker 有重试，且不受广播时限约束
                    Log.w(TAG, "[onReceive] Go init failed, fallback to worker")
                    CheckWorker.enqueueOneTimeCheck(appContext)
                    return@Thread
                }
                // 无账户则不启动检查、并撤销闹钟，避免删完账号后仍每日弹出"正在执行定时检查..."
                if (!CredentialsGateway.hasAccounts(appContext)) {
                    Log.i(TAG, "[onReceive] no accounts, cancelling future alarms")
                    cancelAlarms(appContext)
                    return@Thread
                }
                launchCheck(appContext)
            } catch (e: Throwable) {
                Log.e(TAG, "[onReceive] failed", e)
                PollLog.log(appContext, "ALARM_ERROR ${e.javaClass.simpleName}: ${e.message}")
            } finally {
                pendingResult.finish()
            }
        }.start()
    }
}
