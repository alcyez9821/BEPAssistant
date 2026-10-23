// Copyright (c) 2026 alcyez <alcyez9821@gmail.com>
// SPDX-License-Identifier: MIT
package com.alcyez.bep_assistant

import android.content.Context
import android.util.Log
import java.io.File
import java.text.SimpleDateFormat
import java.util.Date
import java.util.Locale

/**
 * File: TestMode.kt
 * Purpose: 【临时测试用】固定间隔轮询开关 + 轮询事件日志，用于在云真机上快速验证后台定时任务。
 * Author: alcyez (AI Agent 辅助生成注释)
 * Created: 2026-10-23
 *
 * Business Context:
 *   正式逻辑每天只在上午 / 傍晚随机各触发一次，云真机每天限时 6 小时，无法等到触发。
 *   测试期间把 [INTERVAL_MINUTES] 设为 > 0，AlarmReceiver 改为每 N 分钟触发一次，
 *   并在每个关键节点通过 [PollLog] 输出统一 tag 的日志，便于在云真机"实时日志"中过滤。
 *
 * Notes:
 *   测试结束后将 [INTERVAL_MINUTES] 改回 0（或 git revert 本次测试提交）恢复随机调度。
 *   5 分钟一次的真实请求频率远高于正式逻辑，长时间运行可能被服务端识别为爬虫。
 */
object TestMode {
    /** 固定轮询间隔（分钟）。0 = 关闭测试模式，使用正式的每日两次随机调度。 */
    const val INTERVAL_MINUTES = 5

    /** 测试模式下每次检查结束是否额外弹一条结果通知（屏幕上直接可见）。 */
    const val SHOW_RESULT_NOTIFICATION = true

    /** 是否处于固定间隔测试模式。 */
    val isEnabled: Boolean get() = INTERVAL_MINUTES > 0
}

/**
 * 轮询事件日志：统一 tag 输出到 logcat，同时追加到应用外部私有目录的文本文件。
 *
 * logcat 过滤：tag = `BEP_POLL`
 * 文件位置：/sdcard/Android/data/com.alcyez.bep_assistant/files/poll_log.txt
 * （外部目录不可用时退回内部 files 目录）。文件超过 [MAX_FILE_BYTES] 时清空重写。
 */
object PollLog {
    const val TAG = "BEP_POLL"
    private const val FILE_NAME = "poll_log.txt"
    private const val MAX_FILE_BYTES = 256 * 1024L
    private val timeFormat = SimpleDateFormat("yyyy-MM-dd HH:mm:ss", Locale.CHINA)

    /**
     * 记录一条轮询事件。
     *
     * @param event 事件描述，如 "ALARM_FIRED"、"CHECK_DONE checked=1"
     * 副作用：写 logcat、追加写文件（IO 异常被吞掉，只记 logcat）。
     */
    @Synchronized
    fun log(context: Context, event: String) {
        val now = System.currentTimeMillis()
        val line = "${timeFormat.format(Date(now))} pid=${android.os.Process.myPid()} $event"
        Log.i(TAG, line)
        try {
            val dir = context.getExternalFilesDir(null) ?: context.filesDir
            val file = File(dir, FILE_NAME)
            if (file.exists() && file.length() > MAX_FILE_BYTES) file.writeText("")
            file.appendText(line + "\n")
        } catch (e: Exception) {
            Log.w(TAG, "write poll log failed", e)
        }
    }

    /**
     * 读取轮询日志文件最后 [maxLines] 行（供 App 内诊断页展示，不依赖 logcat）。
     *
     * @return 日志文本；文件不存在时返回提示文字
     */
    fun readTail(context: Context, maxLines: Int = 200): String {
        val dir = context.getExternalFilesDir(null) ?: context.filesDir
        val file = File(dir, FILE_NAME)
        if (!file.exists()) return "(尚无轮询日志：${file.absolutePath})"
        return file.readLines().takeLast(maxLines).joinToString("\n")
    }

    /** 从 Go 返回的 TaskResult JSON 中提取摘要字段，避免把账号 / 车辆细节打到日志里。 */
    fun summarize(resultJson: String): String = try {
        val o = org.json.JSONObject(resultJson)
        "checked=${o.optInt("checked")} processed=${o.optInt("processed")} " +
            "applied=${o.optInt("applied")} notifications=${o.optJSONArray("notifications")?.length() ?: 0}"
    } catch (_: Exception) {
        "len=${resultJson.length}"
    }
}
