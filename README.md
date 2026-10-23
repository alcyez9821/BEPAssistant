# BEP Assistant

进京证助手 — Android 应用。

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
- Android SDK 35（compileSdk 35 / minSdk 24）+ NDK 27.0.12077973

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
  - **后台高耗电** —— 电池 → 后台耗电管理 → 允许后台高耗电

4. 应用每天在上午 8–12 点、傍晚 16–20 点各随机触发一次检查。

### 用户须知

本项目仅用于学习或研究，严禁用于其他任何目的。