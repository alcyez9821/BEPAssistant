// Copyright (c) 2026 alcyez <alcyez9821@gmail.com>
// SPDX-License-Identifier: MIT
package com.alcyez.bep_assistant

import android.security.keystore.KeyGenParameterSpec
import android.security.keystore.KeyProperties
import android.util.Base64
import android.util.Log
import java.security.KeyStore
import javax.crypto.Cipher
import javax.crypto.KeyGenerator
import javax.crypto.SecretKey
import javax.crypto.spec.GCMParameterSpec

/**
 * File: CryptoManager.kt
 * Purpose: Android Keystore + AES-256-GCM 加密工具 —— 敏感字段（密码 / accessToken）
 *          落 SQLite 前的加/解密。
 * Author: alcyez (AI Agent 辅助生成注释)
 * Created: 2026-07-06
 *
 * Business Context:
 *   项目要求账号密码 / token 落 SQLite 前必须加密；密钥必须驻留在硬件安全模块
 *   （TEE / Titan M），应用无法导出，也无法从 SQLite backup 或 root 后的文件系统里
 *   拿到 —— 哪怕拿到密文也解不了。应用被卸载重装后无法解密旧密文（密钥丢失），
 *   属预期行为，用户重新登录即可。
 *
 *   密文格式："ENCv1:base64(IV[12] || CIPHER[N] || TAG[16])"，前缀便于未来升级
 *   密钥版本时兼容识别；缺前缀视为损坏（不会误把明文当密文解密）。
 *
 * Key Logic:
 *   1. getOrCreateKey：首次调用通过 KeyGenParameterSpec 生成 AES-256 密钥；后续复用
 *   2. encrypt：每次随机生成 12 字节 IV，与密文 + 16 字节 GCM tag 拼接后 base64
 *   3. decrypt：严格校验 "ENCv1:" 前缀，防止误解密明文历史数据；异常时返回空串
 *      而非抛异常，避免因单账号损坏拖垮整个后台检查
 *
 * Notes:
 *   - AES-GCM 是 AEAD 密码，一次调用同时提供机密性 + 完整性；tag 篡改会导致解密失败
 *   - IV 由 Cipher 自动生成（KeyProperties.setRandomizedEncryptionRequired(true)），
 *     不能自己指定，避免 IV 重用漏洞
 */
object CryptoManager {
    private const val TAG = "CryptoManager"

    /** Android Keystore Provider 名 */
    private const val KEYSTORE_PROVIDER = "AndroidKeyStore"

    /** 密钥别名 —— v1 表示当前密钥版本；未来密钥轮换时可用 v2 别名并保留 v1 兼容 */
    private const val KEY_ALIAS = "bep_assistant_secret_v1"

    /** AES 变换方案 */
    private const val TRANSFORMATION = "AES/GCM/NoPadding"

    /** GCM IV 长度 12 字节（96 bit，NIST 推荐） */
    private const val IV_LEN = 12

    /** GCM tag 长度 128 bit（最大值，安全性最强） */
    private const val TAG_BITS = 128

    /** 密文信封前缀，用于识别密文版本；未来支持 ENCv2 时便于兼容 */
    private const val ENVELOPE_PREFIX = "ENCv1:"

    /** Keystore 单例，懒加载 */
    private val keyStore: KeyStore by lazy {
        KeyStore.getInstance(KEYSTORE_PROVIDER).apply { load(null) }
    }

    /**
     * 幂等获取 AES-256 密钥；不存在时通过 KeyGenParameterSpec 生成。
     *
     * 生成参数关键点：
     *   - PURPOSE_ENCRYPT | PURPOSE_DECRYPT：明确用途，Keystore 会拒绝签名类操作
     *   - BLOCK_MODE_GCM + PADDING_NONE：AEAD 密码不需要 padding
     *   - keySize=256：AES-256，比 128 更保险
     *   - setRandomizedEncryptionRequired(true)：强制每次生成随机 IV，防 IV 重用
     */
    private fun getOrCreateKey(): SecretKey {
        keyStore.getEntry(KEY_ALIAS, null)?.let {
            return (it as KeyStore.SecretKeyEntry).secretKey
        }
        val generator = KeyGenerator.getInstance(KeyProperties.KEY_ALGORITHM_AES, KEYSTORE_PROVIDER)
        val spec = KeyGenParameterSpec.Builder(
            KEY_ALIAS,
            KeyProperties.PURPOSE_ENCRYPT or KeyProperties.PURPOSE_DECRYPT
        )
            .setBlockModes(KeyProperties.BLOCK_MODE_GCM)
            .setEncryptionPaddings(KeyProperties.ENCRYPTION_PADDING_NONE)
            .setKeySize(256)
            .setRandomizedEncryptionRequired(true)
            .build()
        generator.init(spec)
        return generator.generateKey()
    }

    /**
     * 使用 Keystore AES-GCM 加密明文。
     *
     * @param plaintext 待加密明文（密码 / token）；空串直接返回空串（避免调用方判空）。
     * @return "ENCv1:base64(IV||CIPHER||TAG)" 密文；Keystore 不可用等异常情况下**退化为
     *         返回原文**，让调用方仍能保存。此兜底策略由 UI 层 [PermissionBridge.encryptSecret]
     *         配合，属于极端异常场景的最后手段。
     */
    fun encrypt(plaintext: String): String {
        if (plaintext.isEmpty()) return ""
        try {
            val cipher = Cipher.getInstance(TRANSFORMATION)
            cipher.init(Cipher.ENCRYPT_MODE, getOrCreateKey())
            val iv = cipher.iv
            val cipherBytes = cipher.doFinal(plaintext.toByteArray(Charsets.UTF_8))
            // 拼接 IV || CIPHER (含 GCM tag)：GCM 自带 tag 追加在 cipherBytes 末尾
            val out = ByteArray(iv.size + cipherBytes.size)
            System.arraycopy(iv, 0, out, 0, iv.size)
            System.arraycopy(cipherBytes, 0, out, iv.size, cipherBytes.size)
            val enc = ENVELOPE_PREFIX + Base64.encodeToString(out, Base64.NO_WRAP)
            Log.i(TAG, "[encrypt] plain.len=${plaintext.length} → cipher.len=${enc.length}")
            return enc
        } catch (e: Exception) {
            Log.e(TAG, "[encrypt] ERROR plain.len=${plaintext.length}", e)
            return plaintext
        }
    }

    /**
     * 解密带 [ENVELOPE_PREFIX] 前缀的密文。
     *
     * @param envelope "ENCv1:base64(...)" 密文；空串或前缀缺失时返回空串。
     * @return 明文；密钥丢失（应用重装）或密文损坏时返回空串并告警，**不抛异常**。
     *         避免因单个账号损坏拖垮整个后台检查（scheduler 会把空 token 当过期处理，
     *         走重登流程）。
     */
    fun decrypt(envelope: String): String {
        if (envelope.isEmpty()) return ""
        if (!envelope.startsWith(ENVELOPE_PREFIX)) {
            Log.w(TAG, "[decrypt] cipher.len=${envelope.length} 缺少 ENCv1 前缀，视为损坏返回空")
            return ""
        }
        return try {
            val raw = Base64.decode(envelope.removePrefix(ENVELOPE_PREFIX), Base64.NO_WRAP)
            if (raw.size <= IV_LEN) {
                Log.w(TAG, "[decrypt] cipher.len=${envelope.length} 密文长度异常 rawSize=${raw.size}")
                return ""
            }
            val iv = raw.copyOfRange(0, IV_LEN)
            val cipherBytes = raw.copyOfRange(IV_LEN, raw.size)
            val cipher = Cipher.getInstance(TRANSFORMATION)
            cipher.init(Cipher.DECRYPT_MODE, getOrCreateKey(), GCMParameterSpec(TAG_BITS, iv))
            String(cipher.doFinal(cipherBytes), Charsets.UTF_8)
        } catch (e: Exception) {
            Log.w(TAG, "[decrypt] 解密失败：密钥换过或密文损坏 cipher.len=${envelope.length}", e)
            ""
        }
    }
}
