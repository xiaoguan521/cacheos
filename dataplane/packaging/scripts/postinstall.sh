#!/bin/sh
# 安装 / 升级后：目录权限、内核参数、systemd 重新加载。RPM %post 与 DEB postinst 共用。
# 不启动、不重启任何实例：实例生命周期与滚动升级由控制台编排（规格书 6.4）。
set -e
# 实例目录：setgid + 组可写，sycache-agent（属 sycache 组）在此创建实例目录与配置
install -d -o sycache -g sycache -m 2770 /home/atwasoft/sycache/instances
# 版本目录（规格书 6.4）：本包作为「基础版本」登记为 versions/<产品版本> -> ..；
# current 不存在时指向它。之后由 sycache-wrapper switch-version 原子切换，安装 / 升级本包不改动 current。
ver="$(sed -n 's/^product_version=//p' /home/atwasoft/sycache/share/BUILDINFO 2>/dev/null || true)"
if [ -n "$ver" ]; then
  install -d -m 0755 /home/atwasoft/sycache/versions
  # 包升级后旧的基础版本登记已不对应真实文件：删除，current 若指向它则改指新版本
  for l in /home/atwasoft/sycache/versions/*; do
    [ -L "$l" ] && [ "$(readlink "$l")" = ".." ] && [ "${l##*/}" != "$ver" ] || continue
    [ "$(readlink /home/atwasoft/sycache/current 2>/dev/null)" = "versions/${l##*/}" ] && ln -sfn "versions/$ver" /home/atwasoft/sycache/current
    rm -f "$l"
  done
  ln -sfn .. "/home/atwasoft/sycache/versions/$ver"
  [ -e /home/atwasoft/sycache/current ] || ln -s "versions/$ver" /home/atwasoft/sycache/current
fi
install -d -o sycache -g sycache -m 0750 /var/log/sycache
if command -v sysctl >/dev/null 2>&1; then
  sysctl -q -p /usr/lib/sysctl.d/90-sycache.conf >/dev/null 2>&1 || true
fi
if [ -d /run/systemd/system ]; then
  systemctl daemon-reload >/dev/null 2>&1 || true
  systemctl enable --now sycache-thp.service >/dev/null 2>&1 || true
fi
exit 0
