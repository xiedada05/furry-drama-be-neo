#!/bin/sh
# neo-server 启动脚本（nohup 备选，未使用 systemd 时）。
# 用法：sh deploy/run.sh [listen]
# 默认 TCP :5000；可传 unix:/run/furry-drama-be-neo.sock。
set -e

BIN="$(dirname "$0")/../bin/furry-drama-be-neo"
CONFIG="${CONFIG:-/etc/furry-drama-be-neo.ini}"
LISTEN="${1:-tcp:0.0.0.0:5000}"
LOG="/var/log/furry-drama-be-neo.log"

if [ ! -x "$BIN" ]; then
  echo "错误: 二进制不存在 $BIN（先执行 go build -o bin/furry-drama-be-neo ./cmd/server）" >&2
  exit 1
fi

# 前台运行（配合外部日志收集）；需要后台可改为：
#   nohup "$BIN" --config="$CONFIG" --listen="$LISTEN" >>"$LOG" 2>&1 &
exec "$BIN" --config="$CONFIG" --listen="$LISTEN"
