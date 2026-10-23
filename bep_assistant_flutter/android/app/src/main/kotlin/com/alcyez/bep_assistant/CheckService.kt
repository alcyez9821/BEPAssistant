// Copyright (c) 2026 alcyez <alcyez9821@gmail.com>
// SPDX-License-Identifier: MIT
package com.alcyez.bep_assistant

import android.content.Context
import android.content.Intent
import android.os.Build
import android.util.Log
import androidx.lifecycle.LifecycleService
import androidx.lifecycle.lifecycleScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext

/** SharedPreferences 命名空间，与 [NotificationHelper] 共用 */
private const val PREF_NAME = "check_prefs"

/** 首次失败通知标记 —— 避免网络波动导致每次失败都通知 */
private const val KEY_FAILURE_NOTIFIED = "failure_notified"

/**
 * File: CheckService.kt
 * Purpose: 定时/开机触发的进京证检查前台服务 —— 调 Go 完成一轮检查后自动停止。
 * Author: alcyez (AI Agent 辅助生成注释)
 * Created: 2026-07-06
 *
 * Business Context:
 *   Android 8+ 起后台服务受限，需要"前台服务 + 通知"才能保证在 Doze / 内存回收下也不被杀。
 *   本 Service 由 AlarmReceiver / BootReceiver 拉起，展示一条"正在执行定时检查..."的
 *   前台通知，跑完 executeCheck 后调 stopSelf 自动退出。
 *
 * Key Logic:
 *   1. LifecycleService + lifecycleScope：协程随 Service 生命周期自动取消，比裸
 *      Thread{}.start() 更符合现代 Android 后台任务规范（见 code_review_report H-2）
 *   2. native 声明**必须**放在此 companion 里，class 名决定 JNI 符号名 —— 迁到
 *      GoBridgeLoader 会 UnsatisfiedLinkError（见 GoBridgeLoader 注释）
 *   3. 检查失败首次通知，后续静默 —— KEY_FAILURE_NOTIFIED 布尔标记；成功后重置
 *   4. dispatch 后如果有代办申请成功（applied>0）→ 主线程调 CheckWorker.schedulePolling
 *      跟踪审核结果
 */
class CheckService : LifecycleService() {

    companion object {
        private const val TAG = "CheckService"

        // === Go FFI JNI 函数声明 ===
        //
        // native 声明必须留在此 companion 内，class 名决定 JNI 符号：
        // libbridge.so 只导出 Java_com_alcyez_bep_1assistant_CheckService_*，见 jni_bridge.c
        // 迁到其他 class 或 object 会 UnsatisfiedLinkError。

        /**
         * 初始化 Go 核心。首次调用真正干活，后续 sync.Once 保护短路返回。
         * @param dbPath SQLite 文件绝对路径
         * @return {"success":true} JSON 或错误 JSON
         */
        @JvmStatic
        external fun nativeInit(dbPath: String): String

        /**
         * 主检查入口。credentialsJson 由 [CredentialsGateway.prepare] 生成。
         * @param credentialsJson 含 {credentials, refreshedTokenCallbackFile} 的 JSON
         * @return Go TaskResult JSON
         */
        @JvmStatic
        external fun nativeRunCheckWithCredentials(credentialsJson: String): String

        /**
         * 更新车辆字段（列名白名单校验在 Go store 层）。
         * @param id          车辆主键
         * @param updatesJson 列名→新值的 JSON 对象
         */
        @JvmStatic
        external fun nativeUpdateVehicle(id: Long, updatesJson: String): String

        /**
         * 列出所有账号（含密文 password / token 字段）。
         * @return {"success":true,"accounts":[...]} JSON
         */
        @JvmStatic
        external fun nativeListAccounts(): String

        /**
         * 更新账号 accessToken 密文；由 [CredentialsGateway.applyRefreshedTokens] 调用。
         * @param id    账号主键
         * @param token 已加密的 "ENCv1:..." 密文
         */
        @JvmStatic
        external fun nativeUpdateAccountToken(id: Long, token: String): String

        init {
            // Service 类加载时立即加载 libbridge.so，避免第一次 native call 前才加载
            GoBridgeLoader.loadLibrary()
        }
    }

    /**
     * Service 启动入口。展示前台通知 → 协程后台跑 executeCheck → stopSelf。
     *
     * 返回 START_NOT_STICKY：Service 被系统杀掉时**不**自动重启，避免陷入"内存紧张
     * → 杀 Service → 重启 → 再被杀"的循环消耗电量。下一次检查由闹钟触发。
     */
    override fun onStartCommand(intent: Intent?, flags: Int, startId: Int): Int {
        super.onStartCommand(intent, flags, startId)
        Log.i(TAG, "[onStartCommand] flags=$flags startId=$startId")

        // Android 8+ 要求 startForegroundService 后 5 秒内调用 startForeground，否则 crash
        NotificationHelper.createChannel(this)
        val fgNotification = NotificationHelper.buildNotification(this, "进京证助手", "正在执行定时检查...")
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.O) {
            startForeground(1003, fgNotification)
            Log.i(TAG, "[onStartCommand] startForeground 1003")
        }

        val prefs = applicationContext.getSharedPreferences(PREF_NAME, Context.MODE_PRIVATE)

        // 后台协程执行检查；lifecycleScope 保证 Service 停止时协程自动取消
        lifecycleScope.launch(Dispatchers.IO) {
            try {
                val result = executeCheck()
                Log.i(TAG, "[onStartCommand] check result: $result")

                // 检查成功，重置失败通知标记 —— 下次失败时能重新通知一次
                prefs.edit().putBoolean(KEY_FAILURE_NOTIFIED, false).apply()

                // 派发通知（含 ec_notified_at 去重标记回写）
                val hasApplied = NotificationDispatcher.dispatch(
                    applicationContext, result,
                    updateVehicle = { id, json -> nativeUpdateVehicle(id, json) },
                )
                Log.i(TAG, "[onStartCommand] dispatch done hasApplied=$hasApplied")

                // 申请提交后启动轮询，跟踪审核结果 —— 每 20 分钟拉一次，最多 1 小时
                if (hasApplied) {
                    withContext(Dispatchers.Main) {
                        CheckWorker.schedulePolling(applicationContext)
                    }
                }
            } catch (e: Exception) {
                Log.e(TAG, "[onStartCommand] check FAILED", e)
                // 首次失败才通知，避免网络波动重复打扰用户；成功后由 prefs 重置
                if (!prefs.getBoolean(KEY_FAILURE_NOTIFIED, false)) {
                    NotificationHelper.notify(applicationContext, "定时检查失败", e.message ?: "未知错误，请打开应用检查")
                    prefs.edit().putBoolean(KEY_FAILURE_NOTIFIED, true).apply()
                }
            } finally {
                // 无论成功失败都要 stopSelf，让 Service 退出以释放前台通知
                stopSelf()
            }
        }

        return START_NOT_STICKY
    }

    /**
     * 实际检查流程：Go init → 从 SQLite 读密文 → 解密 → 调 Go → 加密回存 token。
     *
     * @return Go 返回的 TaskResult JSON 字符串。
     * @throws Exception Go 初始化失败或 native 调用异常。
     */
    private fun executeCheck(): String {
        Log.i(TAG, "[executeCheck] enter")
        val ok = GoBridgeLoader.initIfNeeded(applicationContext) { dbPath -> nativeInit(dbPath) }
        if (!ok) throw Exception("Go 库初始化失败")

        // 从 SQLite 读密文 → Keystore 解密 → 拼成 credentials JSON 传给 Go
        val (credentialsJson, callbackFile) = CredentialsGateway.prepare(applicationContext)
        try {
            val json = nativeRunCheckWithCredentials(credentialsJson)
            Log.i(TAG, "[executeCheck] Go returned len=${json.length}")
            return json
        } finally {
            // Go 若刷新了 token，读取回调文件 → 加密 → 通过 Go FFI 写回 accounts.token
            // finally 保证即使 native 抛异常也消费掉 JSONL 文件（避免残留明文 token）
            CredentialsGateway.applyRefreshedTokens(applicationContext, callbackFile)
        }
    }
}
