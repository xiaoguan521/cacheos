#!/bin/sh
# 安装 / 升级后：目录权限、内核参数、systemd 重新加载。RPM %post 与 DEB postinst 共用。
# 不启动、不重启任何实例：实例生命周期与滚动升级由控制台编排（规格书 6.4）。
set -e
install -d -o sycache -g sycache -m 0750 /opt/sycache/instances
install -d -o sycache -g sycache -m 0750 /var/log/sycache
if command -v sysctl >/dev/null 2>&1; then
  sysctl -q -p /usr/lib/sysctl.d/90-sycache.conf >/dev/null 2>&1 || true
fi
if [ -d /run/systemd/system ]; then
  systemctl daemon-reload >/dev/null 2>&1 || true
  systemctl enable --now sycache-thp.service >/dev/null 2>&1 || true
fi
exit 0
