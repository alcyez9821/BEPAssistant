#!/bin/bash
# 编译 Go .so + Flutter APK + adb 安装
# 首次运行时会自动补齐两个大文件依赖：
#   - libonnxruntime.so（从 Maven onnxruntime-android-1.25.0.aar 提取）
#   - captcha.onnx + charset.json（从 Python ddddocr 生成）
set -e

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
FLUTTER_ROOT="$SCRIPT_DIR/bep_assistant_flutter"
JNILIBS="$FLUTTER_ROOT/android/app/src/main/jniLibs"
MODELS="$FLUTTER_ROOT/assets/models"

# ---- 依赖 1：libonnxruntime.so（arm64 + x86_64） ------------------
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

# ---- 依赖 2：captcha.onnx + charset.json（从 ddddocr 生成） -------
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

echo "=== 编译 Go .so ==="
cd "$SCRIPT_DIR/bep_assistant_go"
export PATH="$HOME/go-install/go/bin:$PATH"
export ANDROID_HOME="${ANDROID_HOME:-$HOME/android-sdk}"
bash scripts/build-android.sh

echo ""
echo "=== 编译 Flutter APK ==="
cd "$FLUTTER_ROOT"
flutter build apk --debug

echo ""
echo "=== adb 安装 ==="
adb install -r build/app/outputs/flutter-apk/app-debug.apk

echo ""
echo "=== 启动应用 ==="
adb shell am start -n com.alcyez.bep_assistant/com.alcyez.bep_assistant.MainActivity

echo ""
echo "完成！"
