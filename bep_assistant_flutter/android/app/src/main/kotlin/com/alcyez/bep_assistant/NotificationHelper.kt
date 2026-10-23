// Copyright (c) 2026 alcyez <alcyez9821@gmail.com>
// SPDX-License-Identifier: MIT
package com.alcyez.bep_assistant

import android.app.Notification
import android.app.NotificationChannel
import android.app.NotificationManager
import android.content.Context
import android.os.Build
import android.util.Log
import androidx.core.app.NotificationCompat
import androidx.core.app.NotificationManagerCompat

/**
 * File: NotificationHelper.kt
 * Purpose: 通知发送 + 通知渠道管理 + "错过通知计数"（用于通知权限未授予时的兜底提示）。
 * Author: alcyez (AI Agent 辅助生成注释)
 * Created: 2026-07-06
 *
 * Business Context:
 *   Android 13+ 起需要 POST_NOTIFICATIONS 运行时权限；任何版本用户都可能手动关闭。
 *   通知被系统拦截时我们无法直接送达，但需要让用户知道"你错过了 N 条到期提醒"—— 于是
 *   在 SharedPreferences 里维护错过条数，AccountsPage 启动时读取显示为 MaterialBanner。
 *
 * Key Logic:
 *   1. createChannel：Android 8+ 才需要通知渠道；importance=DEFAULT 表示有声不弹通知栏
 *   2. AtomicInteger 递增 ID：避免使用 timestamp 时同一毫秒内两条通知覆盖
 *   3. notify 前用 NotificationManagerCompat.areNotificationsEnabled 主动查权限，
 *      未授权直接 recordMissed 累计并返回，不尝试发送
 *   4. SecurityException 兜底：极小概率 areNotificationsEnabled=true 但实际权限未授予
 *      （OEM 定制系统），此时同样累计 miss
 */
object NotificationHelper {

    private const val TAG = "NotificationHelper"

    /** 通知渠道 ID —— 与 [MainActivity.sendNotification] 保持一致 */
    private const val CHANNEL_ID = "bep_assistant_channel"

    /** 用户在系统通知设置里看到的渠道名 */
    private const val CHANNEL_NAME = "进京证提醒"

    /** SharedPreferences 命名空间，与 [CheckService.PREF_NAME] 共用 */
    private const val PREF_NAME = "check_prefs"

    /** SharedPreferences key：累计错过的通知数 */
    private const val KEY_MISSED_COUNT = "missed_notifications"

    /** 通知 ID 计数器，起始 1000 避免与 CheckService 的 1003 前台通知冲突 */
    private val idCounter = java.util.concurrent.atomic.AtomicInteger(1000)

    /**
     * 创建通知渠道 —— Android 8+ 才需要，之前版本静默跳过。幂等，多次调用不会重复创建。
     */
    fun createChannel(context: Context) {
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.O) {
            val channel = NotificationChannel(
                CHANNEL_ID,
                CHANNEL_NAME,
                NotificationManager.IMPORTANCE_DEFAULT
            ).apply {
                description = "进京证到期提醒和申请结果通知"
            }
            val manager = context.getSystemService(NotificationManager::class.java)
            manager.createNotificationChannel(channel)
        }
    }

    /**
     * 构造 Notification 对象；用于 [CheckService.onStartCommand] 的前台通知 + 本文件 notify。
     *
     * @param context Android 上下文
     * @param title   通知标题
     * @param body    通知正文（不做截断，Android 会自动折叠展示）
     * @return 已配置好的 Notification 对象
     */
    fun buildNotification(context: Context, title: String, body: String): Notification {
        createChannel(context)
        return NotificationCompat.Builder(context, CHANNEL_ID)
            .setSmallIcon(android.R.drawable.ic_dialog_info)
            .setContentTitle(title)
            .setContentText(body)
            .setPriority(NotificationCompat.PRIORITY_DEFAULT)
            .setAutoCancel(true)
            .build()
    }

    /**
     * 发送即时通知；未授权或异常时累计 miss 计数。
     *
     * 处理顺序：
     *   1. areNotificationsEnabled 查权限 → 未授权 → recordMissed → 返回
     *   2. 递增 ID，构造 Notification → NotificationManagerCompat.notify
     *   3. 极小概率 SecurityException（OEM 定制系统权限判断不同步）→ recordMissed
     */
    fun notify(context: Context, title: String, body: String) {
        val compat = NotificationManagerCompat.from(context)
        if (!compat.areNotificationsEnabled()) {
            // 权限未授予（Android 13+ 需要 POST_NOTIFICATIONS，任何版本用户都可手动关）
            // 通知无法送达，用户回到 app 时通过启动横幅提示他们错过了几条
            recordMissed(context)
            Log.w(TAG, "[notify] BLOCKED (permission off) title=$title miss.total=${getMissedCount(context)}")
            return
        }
        try {
            val id = idCounter.incrementAndGet()
            compat.notify(id, buildNotification(context, title, body))
            Log.i(TAG, "[notify] SENT id=$id title=$title body.len=${body.length}")
        } catch (e: SecurityException) {
            // areNotificationsEnabled 与实际权限有极小概率不同步（例如 OEM 定制系统），兜底
            recordMissed(context)
            Log.w(TAG, "[notify] SECURITY ERROR title=$title miss.total=${getMissedCount(context)}", e)
        }
    }

    /**
     * 累加错过的通知数。SharedPreferences 落盘，App 重启后仍能读到。
     */
    private fun recordMissed(context: Context) {
        val prefs = context.applicationContext.getSharedPreferences(PREF_NAME, Context.MODE_PRIVATE)
        val current = prefs.getInt(KEY_MISSED_COUNT, 0)
        prefs.edit().putInt(KEY_MISSED_COUNT, current + 1).apply()
    }

    /** 读取错过的通知数，供 AccountsPage 展示 MaterialBanner。 */
    fun getMissedCount(context: Context): Int {
        return context.applicationContext
            .getSharedPreferences(PREF_NAME, Context.MODE_PRIVATE)
            .getInt(KEY_MISSED_COUNT, 0)
    }

    /** 清零错过的通知数（用户授权成功或忽略提示后调用）。 */
    fun clearMissedCount(context: Context) {
        context.applicationContext
            .getSharedPreferences(PREF_NAME, Context.MODE_PRIVATE)
            .edit()
            .remove(KEY_MISSED_COUNT)
            .apply()
    }
}
