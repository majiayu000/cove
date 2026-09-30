#!/bin/sh
# Finder entry point for the unpacked macOS build.
cd "$(dirname "$0")" || exit 1
if [ ! -x ./bin/gatt ]; then
  printf '未找到 Cove 程序，请使用完整的发行压缩包。\n'
  exit 1
fi
if ./bin/gatt -config config.example.json open; then
  exit 0
fi
printf '正在启动 Cove。请保留此终端窗口；按 Control-C 可停止服务。\n'
./bin/gatt -config config.example.json serve
result=$?
if [ "$result" -ne 0 ]; then
  printf 'Cove 未能启动，请检查上方错误。按回车关闭。\n'
  read -r answer
fi
exit "$result"
