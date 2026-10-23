// Copyright (c) 2026 alcyez <alcyez9821@gmail.com>
// SPDX-License-Identifier: MIT
package com.alcyez.bep_assistant

import android.content.Context
import android.util.Log
import java.io.File
import org.json.JSONArray
import org.json.JSONObject

/**
 * File: CredentialsGateway.kt
 * Purpose: 后台检查（CheckService / CheckWorker）调用 Go 前后的凭据加/解密胶水层。
 * Author: alcyez (AI Agent 辅助生成注释)
 * Created: 2026-07-06
 *
 * Business Context:
 *   Go 侧存储的账号密码 / accessToken 都是 "ENCv1:base64(...)" 密文；scheduler 执行
 *   检查时需要解密后的明文用于登录 API。本文件是 Kotlin 侧的"密文 → 明文 → Go 使用 →
 *   加密回存"胶水层。
 *
 *   关键设计：**Kotlin 侧永远不直接打开 SQLite**，全部通过 Go 的 FFI（CheckService.nativeXxx）
 *   读写数据库。因为 Kotlin 用 android.database.sqlite 打开 DB 会与 Go modernc.org/sqlite 的
 *   WAL / 事务视图打架，导致 Go 的 UPDATE 明明 rowsAffected=1 却读不回来
 *   （2026-07-02 事故根因，见 app/当前进度与问题.md）。
 *
 *   明文只在两个位置存在：
 *     1. Go 内存里的 scheduler.Credentials struct（一次检查的生命周期，几秒）
 *     2. 短命 JSONL 回调文件（token 刷新时才写；执行完立即读 + 删除）
 *   数据库始终是密文，root/backup 拿走 SQLite 也解不了。
 *
 * Key Logic:
 *   1. prepare：读 accounts 表 → 解密 → 拼 credentials JSON → 分配 JSONL 回调文件路径
 *   2. applyRefreshedTokens：读 JSONL → 加密 → 通过 FFI 写回 accounts.token → 删文件
 *   3. hasAccounts：判断是否需要维持后台闹钟；删完账户时用来短路后台任务
 *   4. credentials map key 用 phone 而不是 id：phone 稳定唯一，避免删账号+重加导致
 *      accountID 错位（旧代码用 id 曾误取错凭据 → 5 次错误锁号，2026-07-02 事故）
 */
object CredentialsGateway {
    private const val TAG = "CredentialsGateway"

    /** token 刷新回调 JSONL 文件名 —— 每行一个 {account_id, token} 对象 */
    private const val REFRESH_TOKEN_FILE = "refresh_tokens.jsonl"

    /**
     * 准备一次检查所需的凭据 + 回调文件路径。
     *
     * @param context Android 上下文。
     * @return Pair(credentialsJson, callbackFile)：
     *   - credentialsJson: 交给 Go [scheduler.ExecuteCheck] 的 JSON 字符串，形如
     *     `{"credentials":{"phone":{"password":"...","token":"..."},...},
     *      "refreshedTokenCallbackFile":"/data/.../refresh_tokens.jsonl"}`
     *   - callbackFile: 供 [applyRefreshedTokens] 后续读取的 JSONL 文件对象
     */
    fun prepare(context: Context): Pair<String, File> {
        // 每次检查都用一个全新的空文件：先 delete 保证没有残留
        val callbackFile = File(context.applicationContext.dataDir, "app_flutter/$REFRESH_TOKEN_FILE")
        callbackFile.parentFile?.mkdirs()
        callbackFile.delete()

        val credentialsMap = readAccountsCredentials()

        val json = JSONObject().apply {
            put("credentials", credentialsMap)
            put("refreshedTokenCallbackFile", callbackFile.absolutePath)
        }
        return json.toString() to callbackFile
    }

    /**
     * 检查执行完后：读取 refresh_tokens.jsonl → 用 Keystore 加密 → 通过 Go FFI 写回
     * accounts.token；最后删除 JSONL 文件确保明文不残留。
     *
     * @param context      Android 上下文
     * @param callbackFile 由 [prepare] 返回的 JSONL 文件对象
     */
    fun applyRefreshedTokens(context: Context, callbackFile: File) {
        if (!callbackFile.exists()) return
        try {
            val lines = callbackFile.readLines().filter { it.isNotBlank() }
            if (lines.isEmpty()) return
            for (line in lines) {
                try {
                    val obj = JSONObject(line)
                    val id = obj.optLong("account_id", 0)
                    val plainToken = obj.optString("token", "")
                    if (id > 0 && plainToken.isNotEmpty()) {
                        // 加密后立即通过 FFI 写回 DB —— 明文只在此循环体内驻留几毫秒
                        val ciphertext = CryptoManager.encrypt(plainToken)
                        CheckService.nativeUpdateAccountToken(id, ciphertext)
                        Log.i(TAG, "[applyRefreshedTokens] wrote token for account id=$id")
                    }
                } catch (e: Exception) {
                    Log.w(TAG, "[applyRefreshedTokens] 解析一行失败: $line", e)
                }
            }
        } finally {
            // 无论成功失败都删除文件；避免残留明文 token
            callbackFile.delete()
        }
    }

    /**
     * 是否存在至少一个账户。
     *
     * 用于 AlarmReceiver / BootReceiver / MainActivity 判断是否需要维持后台闹钟。
     * 无账户时撤销闹钟避免每天空转弹通知。
     */
    fun hasAccounts(context: Context): Boolean {
        val accounts = listAccountsViaGo() ?: return false
        return accounts.length() > 0
    }

    /**
     * 通过 Go FFI 拉取账号列表 → 解密 → 组装 {phone: {password, token}} JSON。
     *
     * **key 用 phone 而不是 id**：phone 稳定唯一，避免删账号+重加导致的 accountID 错位。
     * 2026-07-02 事故：旧代码用 id 做 key，用户删账号+立刻重加时新账号拿到新的 id 但 map
     * 里没对应 key，ensureToken hasCred=false → 走 DB 密文兜底 → 把密文当明文送北京通
     * → 连续 5 次错误账号被锁 1 小时。
     */
    private fun readAccountsCredentials(): JSONObject {
        val out = JSONObject()
        val accounts = listAccountsViaGo() ?: return out

        for (i in 0 until accounts.length()) {
            val acc = accounts.optJSONObject(i) ?: continue
            val phone = acc.optString("phone", "")
            if (phone.isEmpty()) continue
            val plainPassword = CryptoManager.decrypt(acc.optString("password", ""))
            val plainToken = CryptoManager.decrypt(acc.optString("token", ""))
            out.put(phone, JSONObject().apply {
                put("password", plainPassword)
                put("token", plainToken)
            })
        }
        return out
    }

    /**
     * 调 Go [CheckService.nativeListAccounts] 拿 accounts 数组。
     *
     * @return accounts JSONArray；出错或字段缺失返回 null（调用方需判空）。
     */
    private fun listAccountsViaGo(): JSONArray? {
        return try {
            val json = CheckService.nativeListAccounts()
            val obj = JSONObject(json)
            obj.optJSONArray("accounts")
        } catch (e: Exception) {
            Log.w(TAG, "[listAccountsViaGo] 失败", e)
            null
        }
    }
}
