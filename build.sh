#!/bin/bash
set -e

VERSION=${VERSION:-"1.0.0"}
BUILD_TIME=$(date +%Y%m%d%H%M%S)
OUTPUT_DIR="dist"

mkdir -p $OUTPUT_DIR

echo "🔨 构建 Kiro API Server v${VERSION}"
echo "================================"

# 构建目标平台
PLATFORMS=(
    "darwin/amd64"
    "darwin/arm64"
    "linux/amd64"
    "linux/arm64"
    "windows/amd64"
)

for PLATFORM in "${PLATFORMS[@]}"; do
    GOOS=${PLATFORM%/*}
    GOARCH=${PLATFORM#*/}
    OUTPUT="${OUTPUT_DIR}/kiro-server-${GOOS}-${GOARCH}"
    
    if [ "$GOOS" = "windows" ]; then
        OUTPUT="${OUTPUT}.exe"
    fi
    
    echo "📦 构建 ${GOOS}/${GOARCH}..."
    GOOS=$GOOS GOARCH=$GOARCH go build -ldflags="-s -w" -o $OUTPUT ./server
done

echo ""
echo "✅ 构建完成！输出目录: ${OUTPUT_DIR}/"
ls -lh $OUTPUT_DIR/
