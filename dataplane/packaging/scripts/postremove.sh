#!/bin/sh
# 卸载后（升级时不执行）：删除 sycache-cli 入口软链。只删除指向本产品当前版本路径的软链，不删真实文件。
# RPM %postun 与 DEB postrm 共用。
# 用法：postremove.sh remove|upgrade
set -e
[ "$1" = "remove" ] || exit 0
if [ -L /usr/bin/sycache-cli ] && [ "$(readlink /usr/bin/sycache-cli)" = /home/atwasoft/sycache/current/bin/sycache-cli ]; then
  rm -f /usr/bin/sycache-cli
fi
exit 0
