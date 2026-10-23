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
  - Kotlin native 声明：`bep_assistant_flutter/android/app/src/main/kotlin/com/alcyez/bep_assistant/CheckService.kt`（所有 `nativeXxx` 声明挂这里，JNI 符号名由 class 决定不能移）
- **改 Go 后**：`cd bep_assistant_go && ./scripts/build-android.sh` 重新交叉编译 `.so`
- **改 Dart / Kotlin 后**：`cd bep_assistant_flutter && flutter build apk --debug`
- **一键**：项目根 `./build-and-install.sh`

---

## 安装

### 1. 环境要求

- Ubuntu 20.04+ / macOS
- Go 1.25.x
- JDK 17
- Flutter 3.29.x
- Android SDK 35 + NDK 27.0.12077973

### 2. 编译并安装到手机

连上手机（开启 USB 调试），项目根一键：

```bash
./build-and-install.sh
```

**首次运行**会自动下载 `libonnxruntime.so`（从 Maven onnxruntime-android 1.25.0）+ 生成 `captcha.onnx`（需要 Python + 自动 `pip install ddddocr`），之后再跑就直接构建。

## 使用

1. 打开 app → 右下角 **+** → 输入手机号 + 北京通密码 → 登录并添加账号
2. 进入账号详情 → 车辆卡片 → 选择 **提醒模式** 或 **Auto模式**
   - 提醒模式：进京证到期发通知
   - Auto模式：到期自动办理，需先填目的地所在区 + 详细地址
   - 只办理六环外，不办理六环内
3. 系统会拦截后台闹钟，需手动开启两个权限：

  - **设置 → 应用 → 进京证助手 → 自启动 → 开启**
  - **设置 → 应用 → 进京证助手 → 电池 → 无限制**

4. 首次启动 app 会弹权限引导对话框。

### 用户须知

本项目仅用于学习或研究，严禁用于其他任何目的。