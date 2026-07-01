#!/usr/bin/env bash
# LLM Tester 构建脚本（macOS / Linux）
# 快速构建当前平台可执行文件
# 如需跨平台构建，请使用: make build-all

set -euo pipefail

APP_NAME="llm-tester"
MAIN_PATH="./cmd/llm-tester"
VERSION="$(git describe --tags --always --dirty 2>/dev/null || echo "dev")"
LDFLAGS="-s -w -X main.version=${VERSION}"

echo "🔨 构建 ${APP_NAME} v${VERSION} ($(go env GOOS)/$(go env GOARCH))..."

go build -ldflags="${LDFLAGS}" -o "${APP_NAME}" "${MAIN_PATH}"

echo "✅ 构建完成: ./${APP_NAME}"
echo "   $ ./${APP_NAME}"
echo "   → http://localhost:8912"