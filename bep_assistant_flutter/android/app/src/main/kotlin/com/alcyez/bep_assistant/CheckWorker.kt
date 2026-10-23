// Copyright (c) 2026 alcyez <alcyez9821@gmail.com>
// SPDX-License-Identifier: MIT
package com.alcyez.bep_assistant

import android.content.Context
import android.content.pm.ServiceInfo
import android.os.Build
import android.util.Log
import androidx.work.*
import java.util.concurrent.TimeUnit

/**
 * File: CheckWorker.kt
 * Purpose: 代办申请提交后每 20 分钟轮询审核结果（WorkManager PeriodicWork）。
 * Author: alcyez (AI Agent 辅助生成注释)
 * Created: 2026-07-06
 *
 * Business Context:
 *   代办模式提交进京证申请后，服务端一般 10-30 分钟内出审核结果（blzt=6 通过 / blzt=4 不通过）。
 *   为了让用户第一时间收到通知，CheckService 在检测到 [applied>0] 时会调用
 *   [schedulePolling] 注册一个 20 分钟周期的 Worker；1 小时后由 CancelPollingWorker
 *   自动取消，避免长期空转浪费电量。
 *
 * Key Logic:
 *   1. 与 CheckService 一样有 native 函数声明 —— 两个 class 必须各自持有一份，符号名不同
 *   2. schedulePolling：ExistingPeriodicWorkPolicy.REPLACE 让每次调用都用最新的初始延迟
 *   3. 1 小时兜底：额外 enqueue 一个 OneTimeWorkRequest 到达后调 [cancelPolling]
 *   4. doWork 失败重试 3 次；仍失败时才发通知，避免网络波动导致每次都通知
 */
class CheckWorker(
    context: Context,
    params: WorkerParameters
) : CoroutineWorker(context, params) {

    companion object {
        private const val TAG = "CheckWorker"

        /** WorkManager 唯一任务名，用于 enqueueUniquePeriodicWork 幂等替换 */
        private const val WORK_NAME_POLLING = "polling_check"

        /** 轮询 1 小时后自动取消的唯一任务名 */
        private const val WORK_NAME_CANCEL_POLLING = "polling_check_cancel"

        /** 前台服务降级用的一次性检查唯一任务名 */
        private const val WORK_NAME_ONE_TIME = "one_time_check"

        /** 加急任务在 Android 12 以下以前台服务形式运行时使用的通知 ID */
        private const val FOREGROUND_NOTIFICATION_ID = 1004

        // === Go FFI JNI 函数声明 ===
        //
        // native 声明必须留在此 companion 内，class 名决定 JNI 符号：
        // libbridge.so 只导出 Java_com_alcyez_bep_1assistant_CheckWorker_*，见 jni_bridge.c

        @JvmStatic
        external fun nativeInit(dbPath: String): String

        @JvmStatic
        external fun nativeRunCheckWithCredentials(credentialsJson: String): String

        @JvmStatic
        external fun nativeUpdateVehicle(id: Long, updatesJson: String): String

        init {
            GoBridgeLoader.loadLibrary()
        }

        /**
         * 注册申请后轮询任务（每 20 分钟，最长 1 小时）。
         *
         * 参数选择理由：
         *   - PeriodicWorkRequestBuilder 最小周期 15 分钟；用 20 分钟给系统一些调度余地
         *   - REQUIRE NetworkType.CONNECTED：无网络时跳过（避免烧电池却拿不到审核结果）
         *   - setInitialDelay 20 分钟：首次触发前给服务端处理时间（早于 20 分钟一般还没审核完）
         *   - 1 小时后 CancelPollingWorker 自动取消：避免用户长期忘记后仍轮询
         */
        fun schedulePolling(context: Context) {
            val request = PeriodicWorkRequestBuilder<CheckWorker>(
                20, TimeUnit.MINUTES
            )
                .addTag(WORK_NAME_POLLING)
                .setConstraints(
                    Constraints.Builder()
                        .setRequiredNetworkType(NetworkType.CONNECTED)
                        .build()
                )
                .setInitialDelay(20, TimeUnit.MINUTES)
                .build()

            WorkManager.getInstance(context).enqueueUniquePeriodicWork(
                WORK_NAME_POLLING,
                ExistingPeriodicWorkPolicy.REPLACE,
                request
            )
            Log.i(TAG, "轮询任务已注册（每 20 分钟，最长 1 小时）")

            // 1 小时后自动取消轮询 —— 兜底方案，避免代办审核卡死时用户手机长期轮询。
            // 用唯一任务 + REPLACE：重复申请时取消任务的计时随之重置，旧的取消任务不会提前掐掉新轮询。
            val cancelRequest = OneTimeWorkRequestBuilder<CancelPollingWorker>()
                .setInitialDelay(1, TimeUnit.HOURS)
                .build()
            WorkManager.getInstance(context).enqueueUniqueWork(
                WORK_NAME_CANCEL_POLLING,
                ExistingWorkPolicy.REPLACE,
                cancelRequest
            )
        }

        /**
         * 立即执行一次检查（WorkManager 加急任务），作为前台服务无法启动时的降级通道。
         *
         * 使用场景：非精确闹钟触发 / 开机广播等不允许后台启动前台服务的情况。
         * 加急配额耗尽时自动退化为普通任务（RUN_AS_NON_EXPEDITED_WORK_REQUEST）。
         * 唯一任务 + KEEP：两次触发撞在一起时只跑一次。
         * 副作用：向 WorkManager 入队任务（持久化到 WorkManager 自身数据库）。
         */
        fun enqueueOneTimeCheck(context: Context) {
            val request = OneTimeWorkRequestBuilder<CheckWorker>()
                .addTag(WORK_NAME_ONE_TIME)
                .setConstraints(
                    Constraints.Builder()
                        .setRequiredNetworkType(NetworkType.CONNECTED)
                        .build()
                )
                .setExpedited(OutOfQuotaPolicy.RUN_AS_NON_EXPEDITED_WORK_REQUEST)
                .build()
            WorkManager.getInstance(context).enqueueUniqueWork(
                WORK_NAME_ONE_TIME,
                ExistingWorkPolicy.KEEP,
                request
            )
            Log.i(TAG, "一次性检查任务已入队（expedited）")
        }

        /** 取消轮询任务；由 CancelPollingWorker 或 [MainActivity.cancelAllScheduled] 调用 */
        fun cancelPolling(context: Context) {
            WorkManager.getInstance(context).cancelUniqueWork(WORK_NAME_POLLING)
            Log.i(TAG, "轮询任务已取消")
        }

        /** 取消尚未执行的一次性降级检查任务；删完最后一个账号时调用。 */
        fun cancelOneTimeCheck(context: Context) {
            WorkManager.getInstance(context).cancelUniqueWork(WORK_NAME_ONE_TIME)
        }
    }

    /**
     * 加急任务在 Android 12 以下会以前台服务方式运行，WorkManager 需要此通知；
     * 未实现时 setExpedited 任务在旧系统上会抛 IllegalStateException。
     *
     * @return 前台通知信息（Android 10+ 带 dataSync 类型，与 Manifest 中 SystemForegroundService 声明一致）
     */
    override suspend fun getForegroundInfo(): ForegroundInfo {
        NotificationHelper.createChannel(applicationContext)
        val notification = NotificationHelper.buildNotification(
            applicationContext, "进京证助手", "正在执行定时检查..."
        )
        return if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.Q) {
            ForegroundInfo(
                FOREGROUND_NOTIFICATION_ID, notification,
                ServiceInfo.FOREGROUND_SERVICE_TYPE_DATA_SYNC
            )
        } else {
            ForegroundInfo(FOREGROUND_NOTIFICATION_ID, notification)
        }
    }

    /**
     * WorkManager 单次执行入口。
     *
     * 流程与 CheckService.executeCheck 相同 —— Go init → 读密文 → 调 Go → 派通知 → 加密回存。
     * 差别：这里返回 [Result.retry]（最多 3 次）或 [Result.failure]（多次失败通知一次）。
     */
    override suspend fun doWork(): Result {
        Log.i(TAG, "[doWork] start attempt=$runAttemptCount")

        return try {
            val ok = GoBridgeLoader.initIfNeeded(applicationContext) { dbPath -> nativeInit(dbPath) }
            if (!ok) {
                Log.e(TAG, "[doWork] Go 初始化失败，跳过检查")
                return Result.failure()
            }

            // 直接调用 Go 执行检查（带凭据），走完整的加密流程
            val (credentialsJson, callbackFile) = CredentialsGateway.prepare(applicationContext)
            val result = try {
                nativeRunCheckWithCredentials(credentialsJson)
            } finally {
                CredentialsGateway.applyRefreshedTokens(applicationContext, callbackFile)
            }
            Log.i(TAG, "[doWork] result: $result")

            // 派发通知（含 ec_notified_at 去重标记回写）
            NotificationDispatcher.dispatch(
                applicationContext, result,
                updateVehicle = { id, json -> nativeUpdateVehicle(id, json) },
            )

            Log.i(TAG, "[doWork] SUCCESS")
            Result.success()
        } catch (e: Exception) {
            Log.e(TAG, "[doWork] FAILED", e)
            // 最多重试 3 次；仍失败时才通知避免网络抖动打扰用户
            if (runAttemptCount < 3) {
                Result.retry()
            } else {
                NotificationHelper.notify(
                    applicationContext,
                    "检查失败",
                    "多次检查失败，请检查网络连接"
                )
                Result.failure()
            }
        }
    }
}

/**
 * 一次性辅助 Worker：1 小时后由 WorkManager 自动执行，调用 [CheckWorker.cancelPolling]
 * 停掉轮询任务。放在独立 Worker 里是为了利用 WorkManager 的持久调度，
 * 避免 CheckWorker 自己起线程 sleep 1 小时。
 */
class CancelPollingWorker(
    context: Context,
    params: WorkerParameters
) : CoroutineWorker(context, params) {
    override suspend fun doWork(): Result {
        CheckWorker.cancelPolling(applicationContext)
        return Result.success()
    }
}
