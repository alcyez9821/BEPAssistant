#!/bin/bash
# 正式发布打包：编译 Go .so + Flutter release APK + 校验签名 + 输出 BEPAssistant.apk
#
# 用法：
#   ./build-release.sh              # 只打包
#   ./build-release.sh --install    # 打包并 adb 安装（用于本机验证）
#
# 前置条件（首次发布前必做）：
#   1. cd bep_assistant_flutter/android
#      keytool -genkey -v -keystore BEPAssistant-release.keystore \
#        -keyalg RSA -keysize 2048 -validity 1000 -alias BEPAssistant
#   2. 建 bep_assistant_flutter/android/key.properties（不进版本控制）：
#        storeFile=<绝对路径>/BEPAssistant-release.keystore
#        storePassword=<口令>
#        keyAlias=BEPAssistant
#        keyPassword=<口令>
#   3. **务必备份 keystore + 口令**，丢失后 app 无法继续上架/升级。
set -e

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
FLUTTER_ROOT="$SCRIPT_DIR/bep_assistant_flutter"
JNILIBS="$FLUTTER_ROOT/android/app/src/main/jniLibs"
MODELS="$FLUTTER_ROOT/assets/models"
KEYPROPS="$FLUTTER_ROOT/android/key.properties"
OUT_APK="$SCRIPT_DIR/BEPAssistant.apk"

INSTALL=0
if [ "${1:-}" = "--install" ]; then
    INSTALL=1
fi

# ---- 前置：签名配置检查 ---------------------------------------------
if [ ! -f "$KEYPROPS" ]; then
    echo "✗ 找不到 $KEYPROPS"
    echo ""
    echo "首次发布前必做（详见脚本顶部注释）："
    echo "  1. cd bep_assistant_flutter/android"
    echo "  2. keytool -genkey -v -keystore BEPAssistant-release.keystore \\"
    echo "       -keyalg RSA -keysize 2048 -validity 1000 -alias BEPAssistant"
    echo "  3. 创建 android/key.properties（4 行：storeFile/storePassword/keyAlias/keyPassword）"
    echo ""
    echo "如只想本地测试，可以先建一个 key.properties 指向 keystore 本地路径。"
    echo "如果你只是想 debug 装机，请用 ./build-and-install.sh"
    exit 1
fi

# 解析 key.properties 里的 storeFile 并检查文件存在
STORE_FILE=$(grep -E "^storeFile=" "$KEYPROPS" | cut -d= -f2-)
if [ -z "$STORE_FILE" ] || [ ! -f "$STORE_FILE" ]; then
    echo "✗ key.properties 中的 storeFile 路径无效或文件不存在: $STORE_FILE"
    exit 1
fi
echo "✓ 签名配置有效: $STORE_FILE"

# ---- 依赖 1：libonnxruntime.so ---------------------------------------
if [ ! -f "$JNILIBS/arm64-v8a/libonnxruntime.so" ] || [ ! -f "$JNILIBS/x86_64/libonnxruntime.so" ]; then
    echo "=== 首次运行：下载 libonnxruntime.so ==="
    TMP=$(mktemp -d)
    trap "rm -rf $TMP" EXIT
    curl -fsSL "https://repo1.maven.org/maven2/com/microsoft/onnxruntime/onnxruntime-android/1.25.0/onnxruntime-android-1.25.0.aar" \
        -o "$TMP/onnxruntime.aar"
    unzip -oq "$TMP/onnxruntime.aar" -d "$TMP/onnxruntime_aar"
    mkdir -p "$JNILIBS/arm64-v8a" "$JNILIBS/x86_64"
    cp "$TMP/onnxruntime_aar/jni/arm64-v8a/libonnxruntime.so" "$JNILIBS/arm64-v8a/"
    cp "$TMP/onnxruntime_aar/jni/x86_64/libonnxruntime.so" "$JNILIBS/x86_64/"
    echo "libonnxruntime.so 已就位"
fi

# ---- 依赖 2：captcha.onnx + charset.json -----------------------------
if [ ! -f "$MODELS/captcha.onnx" ] || [ ! -f "$MODELS/charset.json" ]; then
    echo "=== 首次运行：生成 captcha.onnx ==="
    mkdir -p "$MODELS"
    if ! python3 -c "import ddddocr" 2>/dev/null; then
        echo "安装 ddddocr（首次运行需几分钟）..."
        pip install ddddocr --quiet
    fi
    python3 - <<PYEOF
import ddddocr, json, shutil
ocr = ddddocr.DdddOcr(show_ad=False, beta=False)
shutil.copy(ocr.ocr_engine.session._model_path, "$MODELS/captcha.onnx")
with open("$MODELS/charset.json", "w") as f:
    json.dump(ocr.ocr_engine.charset_manager.charset, f, ensure_ascii=False)
print("captcha.onnx + charset.json 已生成")
PYEOF
fi

echo ""
echo "=== 编译 Go .so ==="
cd "$SCRIPT_DIR/bep_assistant_go"
export PATH="$HOME/go-install/go/bin:$PATH"
export ANDROID_HOME="${ANDROID_HOME:-$HOME/android-sdk}"
bash scripts/build-android.sh

echo ""
echo "=== 编译 Flutter release APK ==="
cd "$FLUTTER_ROOT"
flutter build apk --release

BUILD_APK="$FLUTTER_ROOT/build/app/outputs/flutter-apk/app-release.apk"
if [ ! -f "$BUILD_APK" ]; then
    echo "✗ 构建失败，找不到 $BUILD_APK"
    exit 1
fi

# ---- 拷贝并重命名 ---------------------------------------------------
cp -f "$BUILD_APK" "$OUT_APK"
SIZE=$(du -h "$OUT_APK" | cut -f1)
echo ""
echo "✓ 输出: $OUT_APK ($SIZE)"

# ---- 校验签名 -------------------------------------------------------
APKSIGNER=$(ls "$ANDROID_HOME/build-tools/"*/apksigner 2>/dev/null | tail -1)
if [ -n "$APKSIGNER" ] && [ -x "$APKSIGNER" ]; then
    echo ""
    echo "=== 签名校验 ==="
    "$APKSIGNER" verify --print-certs "$OUT_APK" | grep -E "^Signer|DN:" | head
    # 警告：如果签名者 DN 里出现 "AndroidDebug"，说明用的是 debug key，不能上架
    if "$APKSIGNER" verify --print-certs "$OUT_APK" 2>/dev/null | grep -q "AndroidDebug"; then
        echo ""
        echo "⚠ 警告：APK 使用了 debug 签名，不能上架应用商店！"
        echo "  请检查 key.properties 配置是否正确。"
    else
        echo ""
        echo "✓ 使用正式签名，可上架应用商店"
    fi
else
    echo "⚠ 未找到 apksigner（$ANDROID_HOME/build-tools/*/apksigner），跳过签名校验"
fi

# ---- 可选安装 -------------------------------------------------------
if [ "$INSTALL" = "1" ]; then
    echo ""
    echo "=== adb 安装 ==="
    if ! adb devices | grep -qE "\sdevice$"; then
        echo "✗ 未检测到连接的设备，跳过安装"
        exit 0
    fi
    adb install -r "$OUT_APK"
    echo ""
    echo "=== 启动应用 ==="
    adb shell am start -n com.alcyez.bep_assistant/com.alcyez.bep_assistant.MainActivity
fi

echo ""
echo "完成！"
