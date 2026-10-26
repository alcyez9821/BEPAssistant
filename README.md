# BEP Assistant

进京证助手 — Android 应用。

## 这个项目做什么

北京的外地车牌进入北京需要办理进京证，有效期满后要重新申请。手动维护这件事的麻烦在于：
要记住每辆车的到期日、在到期当天打开官方 APP、过一次图形验证码登录、走完一套多步表单，
提交后还得隔一段时间回来看审核结果。车或账号多了就更容易漏。

本应用把这套流程放到后台自动完成：每天在上午和傍晚各随机一次检查所有账号下的车辆，
到期前发通知提醒，或者直接自动提交下一张进京证的申请并跟踪审核结果。

## 特点

**后台自动运行，不需要每天打开**
每天上午 8–12 点、傍晚 16–20 点各随机触发一次检查
（`AlarmReceiver.kt:67`）。随机化而非固定整点，是为了避开规律性的请求特征。

**没有自建服务器，账号只发往官方接口**
运行时的网络请求只到北京交警与北京通的官方域名（`client.go:33`、`login.go:265-427`）。
没有项目自己的后端，也没有统计、埋点、崩溃上报或广告 SDK —— 应用层依赖只有
`ffi`、`path_provider`、`permission_handler`、`shared_preferences`
（`pubspec.yaml:36-40`）与 WorkManager、Lifecycle、协程三个 Android 库
（`build.gradle.kts:84-86`）。

**凭据加密后存本机**
账号密码与 token 经 Android Keystore 的 AES-256-GCM 加密（12 字节随机 IV、128 位 tag）
后以 `ENCv1:` 前缀存入应用私有目录的 SQLite（`CryptoManager.kt:42-114`）。
密钥由 Keystore 托管，应用代码没有导出路径；卸载重装后旧密文无法解密。
注意加密不可用时会退化为明文落盘（`CryptoManager.kt:117-120`），这是可用性兜底而非安全保证。

**验证码本地识别，不依赖打码平台**
登录所需的图形验证码由内置 ONNX 模型在设备上识别（CRNN + CTC 贪心解码，
`solver.go:78-261`），不上传验证码图片，也不调用任何云端打码服务。
代价是安装包较大：模型 13MB，加上 ONNX Runtime 与 Go 核心库，APK 约 106MB。

**自动办理有多层重复提交防护**
代办模式走完服务端 8 步申请流程（`apply.go:70-187`）。提交前检查当前证是否仍有效、
是否已有办理中的申请（`task.go:331-348`）；提交成功后置 `ec_auto_applied` 标志阻止重复
（`task.go:365-396`）；前后台并发检查由全局锁串行化（`bridge.go:88`）。
年度办理次数由服务端校验（`apply.go:201-211`），本地不做判断。
固定申请六环外（种类 `02`），不支持六环内。

**通知去重，不重复打扰**
到期、过期、审核通过、审核不通过四类状态通知以 `ec_notified_at` 单字段去重
（`task.go:318-444`、`NotificationDispatcher.kt:72-83`），同一状态只通知一次，
证重新有效后才重置。注意通知权限被关闭时该字段仍会写入，这条通知不会补发，
用户回到应用只会看到"错过 N 条"横幅。

**针对国产 ROM 后台管控做了降级**
没有精确闹钟权限时降级为 `setAndAllowWhileIdle`（`AlarmReceiver.kt:148-161`）；
前台服务被系统拒绝时降级为 WorkManager 加急任务（`:190-208`、`CheckService.kt:111-120`）；
闹钟触发后先续排下一次再执行检查，避免异常中断调度链（`:227-228`）；
开机、覆盖安装、用户补授精确闹钟权限都会重新注册闹钟（`BootReceiver.kt:45-71`）。
应用被"强行停止"后系统会清除其待触发闹钟，这种情况需要用户再打开一次应用，代码无法绕过。

**多账号、多车辆**
账号按手机号查重后分组串行检查，单个账号失败不影响其他账号（`task.go:223-270`）。
车辆从服务端同步，提醒模式与 Auto 模式按车辆单独设置。

## 不支持 / 限制

- 只支持 arm64-v8a（`x86_64` 仅供模拟器），不支持 32 位设备（`build.gradle.kts:48`）
- Android 7.0 及以上（minSdk 24）
- 只办理六环外进京证
- 申请后审核结果轮询（每 20 分钟、最长 1 小时）仅在前台服务路径生效，
  经 WorkManager 降级完成的申请不会自动跟踪审核结果（`CheckWorker.kt:194`）

---

**给 AI Coding Agent 读**：项目定位与关键文件索引，帮助快速找到该改的位置。人类读者跳到下面。

- **架构**：Flutter (Dart) UI ──dart:ffi──▶ Go 核心库 `.so` ──HTTPS──▶ 北京交警 API；Kotlin 负责定时/前台服务/Keystore 加密/通知
- **Application ID**：`com.alcyez.bep_assistant`
- **入口文件**：
  - Go FFI 导出：`bep_assistant_go/mobile/bridge.go`
  - Go 状态机：`bep_assistant_go/internal/scheduler/task.go`
  - Dart FFI 封装：`bep_assistant_flutter/lib/services/go_bridge.dart`
  - MethodChannel：`bep_assistant_flutter/lib/services/permission_bridge.dart`
  - Kotlin native 声明：`CheckService.kt` 与 `CheckWorker.kt` 各自的 `companion object`（JNI 符号名由 class 决定，声明不能移到别处，两者各自声明自己用到的 `nativeXxx`）
- **改 Go 后**：`cd bep_assistant_go && ./scripts/build-android.sh` 重新交叉编译 `.so`
- **改 Dart / Kotlin 后**：`cd bep_assistant_flutter && flutter build apk --debug`
- **一键 debug 装机**：项目根 `./build-and-install.sh`
- **release 打包**：项目根 `./build-release.sh`（需先配置 `android/key.properties`，否则 release 降级为 debug 签名）

---

## 安装

### 1. 环境要求

- Linux（构建脚本的 NDK 工具链路径写死为 `linux-x86_64`，macOS 需自行调整 `bep_assistant_go/scripts/build-android.sh`）
- Go 1.25+（`go.mod` 声明 `go 1.25.0`；低于此版本需要 `GOTOOLCHAIN=auto` 自动下载）
- JDK 17
- Flutter 3.29.x（Dart SDK ^3.7.2）
- Android SDK 35（compileSdk 35 / targetSdk 35 / minSdk 24）+ NDK 27.0.12077973

构建脚本默认从 `$HOME/go-install/go/bin` 找 Go、从 `$ANDROID_HOME`（默认 `$HOME/android-sdk`）找 SDK 与 NDK。
路径不同时先 `export PATH` 和 `export ANDROID_HOME` 再执行脚本。

### 2. 编译并安装到手机

连上手机（开启 USB 调试），项目根一键：

```bash
./build-and-install.sh
```

**首次运行**会自动下载 `libonnxruntime.so`（从 Maven onnxruntime-android 1.25.0）+ 生成 `captcha.onnx`（需要 Python + 自动 `pip install ddddocr`），之后再跑就直接构建。

## 使用

1. 打开 app → 右下角 **+** → 输入手机号 + 北京通密码 → 登录并添加账号（验证码由 Go 端 ONNX 模型自动识别，无需手输）
2. 进入账号详情 → 车辆卡片 → 选择 **提醒模式** 或 **Auto 模式**
   - 提醒模式（`reminder`）：进京证到期发通知
   - Auto 模式（`agent`）：到期自动办理，需先填目的地所在区 + 详细地址
   - 固定办理六环外（进京证种类 `02`），界面不提供六环内选项
3. 国产 ROM 会拦截后台闹钟，需手动开启系统权限。首次启动会弹权限引导对话框，也可从 **设置 → 检查应用权限** 再次进入：

  - **通知权限** —— 到期提醒和检查结果通知
  - **闹钟和提醒** —— 准时触发每日定时检查
  - **电池优化** —— 将应用移出系统电池优化列表
  - **自启动** —— 小米、vivo、OPPO、华为等机型需手动开启
  - **后台高耗电** —— 仅 vivo / iQOO：电池 → 后台耗电管理 → 允许后台高耗电

检查在上午 8–12 点、傍晚 16–20 点两个时段内各随机一次，不是固定时刻。

### 用户须知

本项目仅用于学习或研究，严禁用于其他任何目的。

## 许可

MIT License，详见 [LICENSE](LICENSE)。
