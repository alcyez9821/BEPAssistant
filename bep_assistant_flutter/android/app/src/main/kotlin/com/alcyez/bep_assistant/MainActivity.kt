// Copyright (c) 2026 alcyez <alcyez9821@gmail.com>
// SPDX-License-Identifier: MIT
package com.alcyez.bep_assistant

import android.app.Notification
import android.app.NotificationManager
import android.content.ComponentName
import android.content.Context
import android.content.Intent
import android.net.Uri
import android.os.Build
import android.os.Bundle
import android.os.PowerManager
import android.provider.Settings
import io.flutter.embedding.android.FlutterActivity
import io.flutter.embedding.engine.FlutterEngine
import io.flutter.plugin.common.MethodChannel

/**
 * File: MainActivity.kt
 * Purpose: Flutter 宿主 Activity —— 承载 Flutter UI + 通过 MethodChannel 处理平台化请求。
 * Author: alcyez (AI Agent 辅助生成注释)
 * Created: 2026-07-06
 *
 * Business Context:
 *   MainActivity 是 Android 端唯一常驻的 Activity。它同时肩负三个职责：
 *   1. 作为 FlutterActivity，让 Flutter UI 有 Android 宿主可以启动
 *   2. 通过 MethodChannel("com.alcyez.bep_assistant/permissions") 处理 Flutter 侧
 *      调 [PermissionBridge] 时的平台请求（权限查询、通知发送、加密、后台检查等）
 *   3. 在 onCreate 里主动加载 Go 库、创建通知渠道、按需注册 AlarmManager 闹钟
 *
 * Key Logic:
 *   1. MethodChannel 的 handler 用 when-case 分派到具体方法，每个方法要么同步返回、
 *      要么 Thread{}.start() 后异步 runOnUiThread 回调
 *   2. runCheckWithCredentials 是主检查入口，走 CredentialsGateway 完成
 *      "读密文 → 解密 → 调 Go → 加密回存"的全流程
 *   3. onCreate 里判 `hasAccounts` 决定排闹钟还是撤销 —— 空账户时不启动后台任务，
 *      避免每日弹出"正在执行定时检查..."的空转通知
 *   4. openAutoStartSettings 对小米 / 华为 / OPPO / vivo / 三星有各自 Intent 尝试列表，
 *      逐个 try/catch 直到成功；全部失败时兜底跳应用详情页
 */
class MainActivity : FlutterActivity() {

    /** MethodChannel 名称，与 [PermissionBridge._channel] 保持一致 */
    private val CHANNEL = "com.alcyez.bep_assistant/permissions"

    /**
     * 配置 Flutter 引擎 —— 注册 MethodChannel handler，分派 Flutter 侧的平台请求。
     *
     * 关键分派：
     *   - 权限查询类同步返回；
     *   - runCheckWithCredentials 走 Thread 异步（涉及 IO / JNI，避免阻塞 UI 线程）；
     *   - encryptSecret 直接调 [CryptoManager.encrypt] 同步返回。
     */
    override fun configureFlutterEngine(flutterEngine: FlutterEngine) {
        super.configureFlutterEngine(flutterEngine)

        MethodChannel(flutterEngine.dartExecutor.binaryMessenger, CHANNEL)
            .setMethodCallHandler { call, result ->
                when (call.method) {
                    "isMiuiDevice" -> result.success(isMiuiDevice())
                    "openAutoStartSettings" -> openAutoStartSettings(result)
                    "openBatteryOptimizationSettings" -> openBatteryOptimizationSettings(result)
                    "isBatteryOptimizationExempt" -> result.success(isBatteryOptimizationExempt())
                    "sendNotification" -> {
                        val title = call.argument<String>("title") ?: ""
                        val body = call.argument<String>("body") ?: ""
                        sendNotification(title, body)
                        result.success(true)
                    }
                    "schedulePolling" -> {
                        CheckWorker.schedulePolling(this)
                        result.success(true)
                    }
                    "cancelAllScheduled" -> {
                        // 删完最后一个账号时调用：撤销所有后台定时任务
                        AlarmReceiver.cancelAlarms(this)
                        CheckWorker.cancelPolling(this)
                        result.success(true)
                    }
                    "getMissedNotifications" -> {
                        result.success(NotificationHelper.getMissedCount(this))
                    }
                    "clearMissedNotifications" -> {
                        NotificationHelper.clearMissedCount(this)
                        result.success(true)
                    }
                    "encryptSecret" -> {
                        val plain = call.argument<String>("value") ?: ""
                        result.success(CryptoManager.encrypt(plain))
                    }
                    "runCheckWithCredentials" -> {
                        // 前台触发一次检查：读密文→解密→调 Go；执行完把新 token 加密回存。
                        // 用 Thread 异步 —— IO + JNI 会阻塞 UI 线程若在此直接调用。
                        Thread {
                            try {
                                val ok = GoBridgeLoader.initIfNeeded(applicationContext) {
                                    CheckService.nativeInit(it)
                                }
                                if (!ok) {
                                    android.util.Log.e("MainActivity", "runCheckWithCredentials: Go init failed")
                                    runOnUiThread { result.error("GO_INIT", "Go 初始化失败", null) }
                                    return@Thread
                                }
                                val (credentialsJson, callbackFile) = CredentialsGateway.prepare(applicationContext)
                                val json = try {
                                    CheckService.nativeRunCheckWithCredentials(credentialsJson)
                                } finally {
                                    // 即使 native 抛异常也必须走 applyRefreshedTokens 消费 JSONL 文件，
                                    // 避免残留明文 token（虽然文件权限 0600，但清理越干净越安全）
                                    CredentialsGateway.applyRefreshedTokens(applicationContext, callbackFile)
                                }
                                android.util.Log.i("MainActivity", "[runCheckWithCredentials] done len=${json.length}")
                                runOnUiThread { result.success(json) }
                            } catch (e: Exception) {
                                android.util.Log.e("MainActivity", "runCheckWithCredentials failed", e)
                                runOnUiThread { result.error("RUN_CHECK", e.message, null) }
                            }
                        }.start()
                    }
                    else -> result.notImplemented()
                }
            }
    }

    /**
     * Activity 创建时初始化：加载 Go 库 → 建 DB → 建通知渠道 → 按需排闹钟。
     *
     * 排闹钟策略：
     *   - 有账户 → 立即 [AlarmReceiver.scheduleAlarms] 注册今日两个随机闹钟
     *   - 无账户 → [AlarmReceiver.cancelAlarms] 撤销一切残留
     *   注意每次冷启动都会走这里，重复排闹钟会覆盖前一次注册（AlarmManager 用 requestCode
     *   幂等），所以不用担心多次调用。
     */
    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)

        // 主动触发 Go 库加载 + 初始化 DB（保证下面 hasAccounts 走 Go FFI）
        GoBridgeLoader.loadLibrary()
        val goReady = GoBridgeLoader.initIfNeeded(applicationContext) {
            CheckService.nativeInit(it)
        }

        // 创建通知渠道（Android 8+ 才需要，NotificationHelper 内部有版本判断）
        NotificationHelper.createChannel(this)

        // 注册 AlarmManager 定时任务（无账户时跳过，删完账号后打开也不会误排）
        if (goReady && CredentialsGateway.hasAccounts(this)) {
            AlarmReceiver.scheduleAlarms(this)
        } else {
            AlarmReceiver.cancelAlarms(this)
        }
    }

    /**
     * 判断当前设备是否 MIUI / HyperOS。
     *
     * Returns:
     *   true 表示是小米系设备。用 Build 常量判断而非 SystemProperties（Android 9+ 起属
     *   hidden API blocklist），避免运行时报错。
     */
    private fun isMiuiDevice(): Boolean {
        return Build.MANUFACTURER.equals("Xiaomi", ignoreCase = true) ||
            Build.BRAND.equals("Xiaomi", ignoreCase = true) ||
            Build.BRAND.equals("Redmi", ignoreCase = true)
    }

    /**
     * 尝试跳到系统"自启动"设置页 —— 按厂商 Intent 逐个尝试，全部失败时兜底应用详情页。
     *
     * 各厂商自启动页 ComponentName 是通过反编译系统 UI 得到的经验值，可能随系统版本变化；
     * 因此每个 startActivity 都包 try/catch，失败继续下一个尝试。
     */
    private fun openAutoStartSettings(result: MethodChannel.Result) {
        val manufacturer = Build.MANUFACTURER.lowercase()

        // 小米/MIUI
        if (manufacturer.contains("xiaomi") || manufacturer.contains("redmi")) {
            val miuiIntents = listOf(
                ComponentName(
                    "com.miui.securitycenter",
                    "com.miui.permcenter.autostart.AutoStartManagementActivity"
                ),
                ComponentName(
                    "com.miui.securitycenter",
                    "com.miui.permcenter.permissions.PermissionsEditorActivity"
                )
            )
            for (component in miuiIntents) {
                try {
                    startActivity(Intent().apply {
                        this.component = component
                        addFlags(Intent.FLAG_ACTIVITY_NEW_TASK)
                    })
                    result.success(true)
                    return
                } catch (_: Exception) { }
            }
        }

        // 华为
        if (manufacturer.contains("huawei") || manufacturer.contains("honor")) {
            try {
                startActivity(Intent().apply {
                    component = ComponentName(
                        "com.huawei.systemmanager",
                        "com.huawei.systemmanager.startupmgr.ui.StartupNormalAppListActivity"
                    )
                    addFlags(Intent.FLAG_ACTIVITY_NEW_TASK)
                })
                result.success(true)
                return
            } catch (_: Exception) { }
        }

        // OPPO/OnePlus
        if (manufacturer.contains("oppo") || manufacturer.contains("oneplus")) {
            val oppoIntents = listOf(
                ComponentName(
                    "com.coloros.safecenter",
                    "com.coloros.safecenter.startupapp.StartupAppListActivity"
                ),
                ComponentName(
                    "com.oppo.safe",
                    "com.oppo.safe.permission.startup.StartupAppListActivity"
                )
            )
            for (component in oppoIntents) {
                try {
                    startActivity(Intent().apply {
                        this.component = component
                        addFlags(Intent.FLAG_ACTIVITY_NEW_TASK)
                    })
                    result.success(true)
                    return
                } catch (_: Exception) { }
            }
        }

        // vivo
        if (manufacturer.contains("vivo")) {
            try {
                startActivity(Intent().apply {
                    component = ComponentName(
                        "com.vivo.permissionmanager",
                        "com.vivo.permissionmanager.activity.BgStartUpManagerActivity"
                    )
                    addFlags(Intent.FLAG_ACTIVITY_NEW_TASK)
                })
                result.success(true)
                return
            } catch (_: Exception) { }
        }

        // 三星 - 打开后台使用限制页面（AppPowerManagementActivity）
        if (manufacturer.contains("samsung")) {
            try {
                startActivity(Intent(Settings.ACTION_REQUEST_IGNORE_BATTERY_OPTIMIZATIONS).apply {
                    data = Uri.parse("package:$packageName")
                    addFlags(Intent.FLAG_ACTIVITY_NEW_TASK)
                })
                result.success(true)
                return
            } catch (_: Exception) { }
        }

        // Fallback: 应用详情页
        try {
            startActivity(Intent(
                Settings.ACTION_APPLICATION_DETAILS_SETTINGS,
                Uri.parse("package:$packageName")
            ).apply {
                addFlags(Intent.FLAG_ACTIVITY_NEW_TASK)
            })
            result.success(true)
        } catch (_: Exception) {
            result.success(false)
        }
    }

    /**
     * 跳到系统电池优化设置页（一律走应用详情页；用户从那里能进入电池优化子页面）。
     */
    private fun openBatteryOptimizationSettings(result: MethodChannel.Result) {
        try {
            startActivity(Intent(
                Settings.ACTION_APPLICATION_DETAILS_SETTINGS,
                Uri.parse("package:$packageName")
            ).apply {
                addFlags(Intent.FLAG_ACTIVITY_NEW_TASK)
            })
            result.success(true)
        } catch (_: Exception) {
            result.success(false)
        }
    }

    /**
     * 查询当前应用是否已从电池优化列表中免除。
     */
    private fun isBatteryOptimizationExempt(): Boolean {
        val pm = getSystemService(POWER_SERVICE) as PowerManager
        return pm.isIgnoringBatteryOptimizations(packageName)
    }

    /**
     * 发送即时通知 —— 由 Flutter 侧手动刷新流程调用。
     *
     * body 截断到 100 字符，避免过长文案挤破通知栏。ID 用 System.currentTimeMillis 保证唯一。
     */
    private fun sendNotification(title: String, body: String) {
        val notification = Notification.Builder(this, "bep_assistant_channel")
            .setContentTitle(title)
            .setContentText(body.take(100))
            .setSmallIcon(android.R.drawable.ic_menu_info_details)
            .setAutoCancel(true)
            .build()
        val nm = getSystemService(Context.NOTIFICATION_SERVICE) as NotificationManager
        nm.notify(System.currentTimeMillis().toInt(), notification)
    }
}
