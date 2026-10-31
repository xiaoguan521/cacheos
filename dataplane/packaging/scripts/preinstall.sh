#!/bin/sh
# 安装前：创建 sycache 系统用户与组（规格书 5.4）。RPM %pre 与 DEB preinst 共用。
set -e
getent group sycache >/dev/null 2>&1 || groupadd -r sycache
getent passwd sycache >/dev/null 2>&1 || \
  useradd -r -g sycache -d /opt/sycache -s /sbin/nologin -c "CacheOS sycache" sycache
exit 0
