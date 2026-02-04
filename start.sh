#!/bin/bash

# Kiro API Server 启动脚本
# 用法: ./start.sh [端口号]
# 示例: ./start.sh 8080

PORT=${1:-${PORT:-8080}}
export PORT

# 检测系统架构
OS=$(uname -s | tr '[:upper:]' '[:lower:]')
ARCH=$(uname -m)
case $ARCH in
    x86_64) ARCH="amd64" ;;
    aarch64|arm64) ARCH="arm64" ;;
esac

BINARY="./dist/kiro-server-${OS}-${ARCH}"

# 如果 dist 目录不存在，尝试本地构建的二进制
if [ ! -f "$BINARY" ]; then
    BINARY="./kiro-server"
fi

if [ ! -f "$BINARY" ]; then
    echo "❌ 找不到可执行文件，请先运行 ./build.sh"
    exit 1
fi

echo "🚀 启动 Kiro API Server"
echo "📡 端口: ${PORT}"
echo "🔗 地址: http://localhost:${PORT}"
echo "================================"

exec $BINARY
