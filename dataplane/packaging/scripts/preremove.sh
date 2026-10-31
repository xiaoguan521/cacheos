#!/bin/sh
# 卸载前（升级时不执行）：仍有实例在运行则拒绝卸载，避免误删正在服务的缓存。RPM %preun 与 DEB prerm 共用。
# 用法：preremove.sh remove|upgrade
set -e
[ "$1" = "remove" ] || exit 0
if [ -d /run/systemd/system ]; then
  running="$(systemctl list-units --state=active --no-legend 'sycache@*.service' 'sycache-sentinel@*.service' 2>/dev/null | awk '{print $1}')"
  if [ -n "$running" ]; then
    echo "sycache: refusing to remove while instances are running:" >&2
    echo "$running" >&2
    echo "stop them from the CacheOS console (or systemctl stop) first." >&2
    exit 1
  fi
  systemctl disable --now sycache-thp.service >/dev/null 2>&1 || true
fi
exit 0
