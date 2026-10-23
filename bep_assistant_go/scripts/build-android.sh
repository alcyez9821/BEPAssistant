#!/bin/bash
# 交叉编译 Go 核心库为 Android .so
# 需要 Android NDK（Flutter 自带）

set -e

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
GO_PROJECT="$SCRIPT_DIR/.."
FLUTTER_PROJECT="$SCRIPT_DIR/../../bep_assistant_flutter"
OUTPUT_DIR="$FLUTTER_PROJECT/android/app/src/main/jniLibs"

# 查找 Android NDK
NDK_HOME="${ANDROID_NDK_HOME:-}"
if [ -z "$NDK_HOME" ]; then
    # 尝试从 Flutter 的 Android SDK 中查找
    ANDROID_HOME="${ANDROID_HOME:-$HOME/Android/Sdk}"
    NDK_HOME=$(find "$ANDROID_HOME/ndk" -maxdepth 1 -type d 2>/dev/null | sort -V | tail -1)
fi

if [ -z "$NDK_HOME" ] || [ ! -d "$NDK_HOME" ]; then
    echo "错误: 未找到 Android NDK"
    echo "请设置 ANDROID_NDK_HOME 或安装 Android NDK"
    echo "  sdkmanager --install 'ndk;27.0.12077973'"
    exit 1
fi

echo "使用 NDK: $NDK_HOME"
export CGO_ENABLED=1

# 检查 Go 是否安装
export PATH="$HOME/go-install/go/bin:$PATH"
if ! command -v go &>/dev/null; then
    echo "错误: 未找到 Go"
    exit 1
fi

echo "Go 版本: $(go version)"

# 编译函数
build_arch() {
    local GOARCH=$1
    local ANDROID_ARCH=$2
    local CC_PREFIX=$3

    echo "编译 $ANDROID_ARCH ($GOARCH)..."

    export GOOS=android
    export GOARCH=$GOARCH
    export CC="$NDK_HOME/toolchains/llvm/prebuilt/linux-x86_64/bin/${CC_PREFIX}24-clang"

    if [ ! -f "$CC" ]; then
        # 尝试其他 API level
        CC=$(ls "$NDK_HOME/toolchains/llvm/prebuilt/linux-x86_64/bin/${CC_PREFIX}"*-clang 2>/dev/null | head -1)
    fi

    if [ ! -f "$CC" ]; then
        echo "警告: 未找到 $ANDROID_ARCH 的编译器，跳过"
        return
    fi

    mkdir -p "$OUTPUT_DIR/$ANDROID_ARCH"
    cd "$GO_PROJECT"

    go build -buildmode=c-shared \
        -o "$OUTPUT_DIR/$ANDROID_ARCH/libbridge.so" \
        ./mobile/

    echo "$ANDROID_ARCH 编译完成"
}

# 编译 arm64
build_arch "arm64" "arm64-v8a" "aarch64-linux-android"

# 编译 x86_64（模拟器用）
build_arch "amd64" "x86_64" "x86_64-linux-android"

echo ""
echo "编译完成！.so 文件已输出到:"
echo "  $OUTPUT_DIR/"
ls -la "$OUTPUT_DIR"/*/libbridge.so 2>/dev/null
