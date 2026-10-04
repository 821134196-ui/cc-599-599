#!/usr/bin/env bash
# 一条命令启动报刊亭期刊履约系统：
#   ./run.sh            首次会自动安装前端依赖、构建 Vue、编译并启动 Go 服务
#   ./run.sh --dev      前端开发模式（Vite :5173，热更新，API 代理到 :8080）
set -euo pipefail
cd "$(dirname "$0")"

ADDR="${KIOSK_ADDR:-:8080}"
export KIOSK_ADDR="$ADDR"
export KIOSK_DB="${KIOSK_DB:-kiosk.db}"

# 定位 Go（优先 PATH，其次用户目录安装）
if ! command -v go >/dev/null 2>&1; then
  [ -x "$HOME/sdk/go/bin/go" ] && export PATH="$HOME/sdk/go/bin:$PATH"
fi
command -v go >/dev/null 2>&1 || { echo "未找到 Go，请先安装 Go 1.22+"; exit 1; }

if [ "${1:-}" = "--dev" ]; then
  echo ">> 构建/启动后端 (:8080)"
  go run . & SRV=$!
  echo ">> 启动前端开发服务器 (:5173)"
  ( cd web && [ -d node_modules ] || npm install && npm run dev )
  kill $SRV 2>/dev/null || true
  exit 0
fi

if [ ! -d web/node_modules ] || [ "${FORCE_WEB_BUILD:-0}" = "1" ]; then
  echo ">> 安装前端依赖"
  ( cd web && npm install )
fi
echo ">> 构建 Vue 前端"
( cd web && npm run build )

echo ">> 编译并启动后端 $ADDR（SQLite: $KIOSK_DB）"
go run .
