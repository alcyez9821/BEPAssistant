/**
 * File: jni_bridge.c
 * Purpose: JNI 包装层，让 Kotlin 侧 external fun nativeXxx 能找到
 *          Java_com_alcyez_bep_1assistant_* 符号。
 * Author:  alcyez (AI Agent 辅助生成注释)
 * Created: 2026-07-06
 *
 * Business Context:
 *   cgo 的 //export 生成的是纯 C 符号（Init / RunCheckWithCredentials / UpdateVehicle 等），
 *   Kotlin 通过 external fun 声明 native 方法时，JVM 遵循 JNI 规范查找
 *   `Java_<package>_<Class>_<method>` 命名的符号 —— 直接暴露 cgo 符号会导致
 *   `UnsatisfiedLinkError`。此文件为每个 Kotlin 侧 native 方法写一层 JNI 转发。
 *
 * Key Logic:
 *   1. cgo 编译 mobile/ 包时会自动把同目录下的 .c 文件一并编进 libbridge.so，
 *      不要单独维护 CMake 或删除本文件（历史上误删过一次导致 CheckService crash）
 *   2. JNI 方法名必须与 Kotlin class 名严格对应：类名下划线转 "_1"、包名 "." 转 "_"，
 *      所以本文件里的符号形如 Java_com_alcyez_bep_1assistant_CheckService_nativeInit
 *   3. 相同的 Go 函数（例如 RunCheckWithCredentials）在两个 Kotlin class 里都要 external
 *      声明 → 需要两个 JNI wrapper 分别指向同一个 Go 符号（CheckService_*, CheckWorker_*）
 *   4. FreeStr 释放的是 Go 侧 C.CString 分配的 C 堆内存；NewStringUTF 拷贝到 Java 堆后
 *      C 侧指针必须显式释放，否则每次调用泄漏 KB 级堆
 *
 * Notes:
 *   不要迁到 Kotlin GoBridgeLoader 或抽出成"通用 native"class —— JNI 符号名由 class 决定，
 *   迁移后符号就变成 `Java_..._GoBridgeLoader_nativeXxx`，运行时找不到会 crash。
 *   验证符号存在：`nm -D libbridge.so | grep Java_com_alcyez`
 */

#include <jni.h>
#include <stdlib.h>

// Go 导出的 C 函数声明（由 cgo 生成的 _cgo_export.h 提供签名，此处仅需符号）
extern char* Init(char* dbPath);
extern char* RunCheckWithCredentials(char* credentialsJSON);
extern char* UpdateVehicle(long id, char* updatesJSON);
extern char* ListAccounts(void);
extern char* UpdateAccountToken(long id, char* token);
extern void  FreeStr(char* s);

// ------------------------------------------------------------
// CheckService  —  前台定时/开机触发的检查（LifecycleService + lifecycleScope）
// ------------------------------------------------------------

/**
 * CheckService.nativeInit(dbPath: String): String
 *
 * 初始化 Go 核心：打开 SQLite、创建 bep 客户端、加载验证码模型。转发到 Go 侧的 Init。
 * 幂等 —— Kotlin 每次拉起 CheckService 都会 nativeInit 一次，Go 侧 sync.Once 保证只干一次活。
 *
 * @param dbPath  SQLite 文件绝对路径（例如 /data/user/0/.../app_flutter/app.db）
 * @return Go 返回的 JSON 字符串：{"success":true} 或 {"success":false,"error":"..."}
 */
JNIEXPORT jstring JNICALL
Java_com_alcyez_bep_1assistant_CheckService_nativeInit(
    JNIEnv *env, jclass clazz, jstring dbPath) {
    const char *cDbPath = (*env)->GetStringUTFChars(env, dbPath, NULL);
    char *result = Init((char*)cDbPath);
    (*env)->ReleaseStringUTFChars(env, dbPath, cDbPath);

    jstring jResult = (*env)->NewStringUTF(env, result);
    FreeStr(result);
    return jResult;
}

/**
 * CheckService.nativeRunCheckWithCredentials(credentialsJson: String): String
 *
 * 主检查入口。Kotlin CredentialsGateway 组装的 {credentials, refreshedTokenCallbackFile}
 * JSON 交给 Go scheduler.ExecuteCheck。Go 内部串行化（runCheckMu），并发调用会排队。
 *
 * @param credentialsJson 由 CredentialsGateway.prepare 生成的凭据 JSON
 * @return TaskResult 或错误 JSON
 */
JNIEXPORT jstring JNICALL
Java_com_alcyez_bep_1assistant_CheckService_nativeRunCheckWithCredentials(
    JNIEnv *env, jclass clazz, jstring credentialsJson) {
    const char *cJson = (*env)->GetStringUTFChars(env, credentialsJson, NULL);
    char *result = RunCheckWithCredentials((char*)cJson);
    (*env)->ReleaseStringUTFChars(env, credentialsJson, cJson);
    jstring jResult = (*env)->NewStringUTF(env, result);
    FreeStr(result);
    return jResult;
}

/**
 * CheckService.nativeUpdateVehicle(id: Long, updatesJson: String): String
 *
 * 更新车辆字段（列名白名单校验在 Go store 层）。用于 NotificationDispatcher 通知成功后
 * 回写 ec_notified_at 去重标记。
 *
 * @param id           车辆主键
 * @param updatesJson  列名→新值的 JSON 对象
 * @return {"success":true} 或错误 JSON
 */
JNIEXPORT jstring JNICALL
Java_com_alcyez_bep_1assistant_CheckService_nativeUpdateVehicle(
    JNIEnv *env, jclass clazz, jlong id, jstring updatesJson) {
    const char *cJson = (*env)->GetStringUTFChars(env, updatesJson, NULL);
    char *result = UpdateVehicle((long)id, (char*)cJson);
    (*env)->ReleaseStringUTFChars(env, updatesJson, cJson);

    jstring jResult = (*env)->NewStringUTF(env, result);
    FreeStr(result);
    return jResult;
}

/**
 * CheckService.nativeListAccounts(): String
 *
 * 列出所有账号（含密文 password / token 字段）。CredentialsGateway.readAccountsCredentials
 * 拿到后用 Keystore 解密再拼 credentials JSON。
 *
 * @return {"success":true,"accounts":[...]} 或错误 JSON
 */
JNIEXPORT jstring JNICALL
Java_com_alcyez_bep_1assistant_CheckService_nativeListAccounts(
    JNIEnv *env, jclass clazz) {
    char *result = ListAccounts();
    jstring jResult = (*env)->NewStringUTF(env, result);
    FreeStr(result);
    return jResult;
}

/**
 * CheckService.nativeUpdateAccountToken(id: Long, token: String): String
 *
 * 更新账号的 accessToken 密文。CredentialsGateway.applyRefreshedTokens 读 JSONL 回调文件、
 * 加密后调本函数写回。
 *
 * @param id     账号主键
 * @param token  "ENCv1:..." 密文
 * @return {"success":true} 或错误 JSON
 */
JNIEXPORT jstring JNICALL
Java_com_alcyez_bep_1assistant_CheckService_nativeUpdateAccountToken(
    JNIEnv *env, jclass clazz, jlong id, jstring token) {
    const char *cTok = (*env)->GetStringUTFChars(env, token, NULL);
    char *result = UpdateAccountToken((long)id, (char*)cTok);
    (*env)->ReleaseStringUTFChars(env, token, cTok);
    jstring jResult = (*env)->NewStringUTF(env, result);
    FreeStr(result);
    return jResult;
}

// ------------------------------------------------------------
// CheckWorker  —  申请提交后 20 分钟轮询审核结果（WorkManager）
// ------------------------------------------------------------

/**
 * CheckWorker.nativeInit(dbPath: String): String
 *
 * 与 CheckService_nativeInit 逻辑相同 —— 两个 class 各需一个符号，Go 侧共享 sync.Once
 * 保证 DB / solver 只真的初始化一次。
 */
JNIEXPORT jstring JNICALL
Java_com_alcyez_bep_1assistant_CheckWorker_nativeInit(
    JNIEnv *env, jclass clazz, jstring dbPath) {
    const char *cDbPath = (*env)->GetStringUTFChars(env, dbPath, NULL);
    char *result = Init((char*)cDbPath);
    (*env)->ReleaseStringUTFChars(env, dbPath, cDbPath);

    jstring jResult = (*env)->NewStringUTF(env, result);
    FreeStr(result);
    return jResult;
}

/**
 * CheckWorker.nativeRunCheckWithCredentials(credentialsJson: String): String
 *
 * 与 CheckService_nativeRunCheckWithCredentials 逻辑相同。WorkManager 的 CoroutineWorker
 * 在申请提交后每 20 分钟触发一次；Go 内部 runCheckMu 与 CheckService 共享，避免并发。
 */
JNIEXPORT jstring JNICALL
Java_com_alcyez_bep_1assistant_CheckWorker_nativeRunCheckWithCredentials(
    JNIEnv *env, jclass clazz, jstring credentialsJson) {
    const char *cJson = (*env)->GetStringUTFChars(env, credentialsJson, NULL);
    char *result = RunCheckWithCredentials((char*)cJson);
    (*env)->ReleaseStringUTFChars(env, credentialsJson, cJson);
    jstring jResult = (*env)->NewStringUTF(env, result);
    FreeStr(result);
    return jResult;
}

/**
 * CheckWorker.nativeUpdateVehicle(id: Long, updatesJson: String): String
 *
 * 与 CheckService_nativeUpdateVehicle 逻辑相同 —— 两个 class 都需要 wrapper。
 */
JNIEXPORT jstring JNICALL
Java_com_alcyez_bep_1assistant_CheckWorker_nativeUpdateVehicle(
    JNIEnv *env, jclass clazz, jlong id, jstring updatesJson) {
    const char *cJson = (*env)->GetStringUTFChars(env, updatesJson, NULL);
    char *result = UpdateVehicle((long)id, (char*)cJson);
    (*env)->ReleaseStringUTFChars(env, updatesJson, cJson);

    jstring jResult = (*env)->NewStringUTF(env, result);
    FreeStr(result);
    return jResult;
}
