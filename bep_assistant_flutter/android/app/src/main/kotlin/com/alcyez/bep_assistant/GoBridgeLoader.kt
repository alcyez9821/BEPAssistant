// Copyright (c) 2026 alcyez <alcyez9821@gmail.com>
// SPDX-License-Identifier: MIT
package com.alcyez.bep_assistant

import android.content.Context
import android.util.Log
import java.io.File

/**
 * File: GoBridgeLoader.kt
 * Purpose: 共享 Go 库的加载状态和一次性初始化逻辑；CheckService / CheckWorker /
 *          BootReceiver / AlarmReceiver / MainActivity 都通过它调用 Go Init。
 * Author: alcyez (AI Agent 辅助生成注释)
 * Created: 2026-07-06
 *
 * Business Context:
 *   CheckService 和 CheckWorker 之前各自维护 `System.loadLibrary("bridge")` +
 *   `goInitialized` + `initResult = nativeInit(dbPath)` 三段几乎相同的代码，
 *   Go 库路径变更或参数扩展要改两处。抽出此对象消除重复。
 *
 * Notes:
 *   **无法在此类里直接声明 `external fun nativeInit`** —— JNI 符号名由 class 决定，
 *   libbridge.so 里只暴露了 `Java_com_alcyez_bep_1assistant_CheckService_*` 和
 *   `_CheckWorker_*`，把 native 声明迁到这里会导致 UnsatisfiedLinkError（见
 *   2026-07-02 事故复盘）。
 *
 *   因此本类的定位是**加载状态守护 + 初始化路径工具**，实际的 native 声明保留在
 *   CheckService / CheckWorker 的 companion object 里，各自通过 [initIfNeeded] 交出
 *   真正调用 `nativeInit` 的时机。
 *
 * Key Logic:
 *   1. libraryLoaded / initialized 都用 @Volatile + @Synchronized 保护，允许多线程调用
 *   2. loadLibrary 幂等：多次调用不会 crash（System.loadLibrary 本身幂等，但仍加缓存）
 *   3. initIfNeeded 先 extract ONNX 模型 —— 必须在 nativeInit 之前，因为 Go Init
 *      用 sync.Once 一次性完成 solver 初始化；如果那时模型文件还不存在，solver 永远是 nil
 */
object GoBridgeLoader {
    private const val TAG = "GoBridgeLoader"

    @Volatile
    private var libraryLoaded = false

    @Volatile
    private var initialized = false

    /**
     * 幂等加载 libbridge.so。
     *
     * 由 CheckService / CheckWorker 的 companion init 触发；MainActivity / BootReceiver
     * / AlarmReceiver 也会各自调一次（多次调用无副作用）。
     */
    @Synchronized
    fun loadLibrary() {
        if (libraryLoaded) return
        try {
            System.loadLibrary("bridge")
            libraryLoaded = true
            Log.i(TAG, "Go 库加载成功")
        } catch (e: UnsatisfiedLinkError) {
            Log.e(TAG, "Go 库加载失败", e)
        }
    }

    /**
     * 幂等初始化 Go 端 DB / 验证码模型。
     *
     * 因为 native 声明必须留在调用方 class 里（详见类注释），这里通过 lambda 反向调用调用方
     * 的 `nativeInit`；对调用方而言只需要提供"给我 dbPath，调 Go Init 返回 JSON"这一段代码。
     *
     * @param context       Android 上下文
     * @param doNativeInit  调用方 class 的 nativeInit lambda（如 CheckService.nativeInit）
     * @return true 表示 Go 侧可用；false 表示 loadLibrary 失败或 Go Init 返回错误
     */
    @Synchronized
    fun initIfNeeded(context: Context, doNativeInit: (dbPath: String) -> String): Boolean {
        if (initialized) return true
        if (!libraryLoaded) return false

        // Flutter 的 getApplicationDocumentsDirectory() 返回 app_flutter 目录
        val dbDir = File(context.applicationContext.dataDir, "app_flutter")
        if (!dbDir.exists()) dbDir.mkdirs()
        val dbFile = File(dbDir, "app.db")

        // 先把 ONNX 模型 + 字符集从 APK assets 提取到磁盘 —— 必须在 nativeInit 之前，
        // 因为 Go Init 会用 sync.Once 一次性完成 solver 初始化；如果那时模型文件还不存在
        // （Flutter 侧 _extractAssets 比 Kotlin 慢），solver 永远是 nil。
        // 首次装机后无 assets 目录，从 APK 的 flutter_assets/assets/models 里读；后续启动跳过。
        extractModelAssetsIfNeeded(context, dbDir)

        val initResult = try {
            doNativeInit(dbFile.absolutePath)
        } catch (e: Throwable) {
            Log.e(TAG, "Go 初始化异常", e)
            return false
        }
        Log.i(TAG, "Go 初始化: $initResult (db=${dbFile.absolutePath})")
        initialized = initResult.contains("success")
        return initialized
    }

    /**
     * 首次装机时，把 assets/models/{captcha.onnx,charset.json} 从 APK 拷贝到磁盘。
     *
     * ONNX Runtime 需要文件路径而非 stream，因此必须提取到磁盘。已存在则跳过。
     *
     * @param context Android 上下文
     * @param dbDir   目标目录（<dataDir>/app_flutter），会创建 assets/models 子目录
     */
    private fun extractModelAssetsIfNeeded(context: Context, dbDir: File) {
        val modelsDir = File(dbDir, "assets/models")
        val captcha = File(modelsDir, "captcha.onnx")
        val charset = File(modelsDir, "charset.json")
        if (captcha.exists() && charset.exists()) {
            Log.i(TAG, "assets/models 已存在，跳过 extract")
            return
        }
        modelsDir.mkdirs()
        try {
            val am = context.applicationContext.assets
            for (name in listOf("captcha.onnx", "charset.json")) {
                val out = File(modelsDir, name)
                if (out.exists()) continue
                // Flutter 打包时把 pubspec.yaml 的 assets 放到 flutter_assets/ 前缀下
                am.open("flutter_assets/assets/models/$name").use { input ->
                    out.outputStream().use { output -> input.copyTo(output) }
                }
                Log.i(TAG, "extract $name → ${out.absolutePath} size=${out.length()}")
            }
        } catch (e: Exception) {
            Log.e(TAG, "extract assets 失败", e)
        }
    }
}
