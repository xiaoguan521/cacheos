#!/usr/bin/env bash
# 安装冒烟测试：在目标发行版容器中安装 RPM / DEB，运行主从实例，再卸载（规格书 5.3、5.4）。
# 容器内没有 systemd：只验证包依赖、维护脚本、二进制在该 glibc 上能运行，以及基本复制与持久化。
#
# 用法（容器内，安装包目录挂载到 /dist）：test/install/smoke.sh rpm|deb
set -euo pipefail
KIND="${1:?usage: smoke.sh rpm|deb}"
B=/opt/sycache/bin
. /etc/os-release
OS="$PRETTY_NAME"; ARCH="$(uname -m)"
step() { echo "== $*"; }

step "install ($KIND) on $OS / $ARCH"
case "$KIND" in
  rpm)
    pkg="$(ls /dist/sycache-[0-9]*."$ARCH".rpm)"
    if command -v dnf >/dev/null; then dnf install -y -q "$pkg"
    else
      # CentOS 7 已停止维护，官方镜像源下线：改用 vault 归档源解析依赖
      if ! yum -q makecache >/dev/null 2>&1 && ls /etc/yum.repos.d/CentOS-*.repo >/dev/null 2>&1; then
        sed -i -e 's/^mirrorlist=/#mirrorlist=/' -e 's|^#\?baseurl=http://mirror.centos.org|baseurl=http://vault.centos.org|' \
          /etc/yum.repos.d/CentOS-*.repo
      fi
      yum install -y -q "$pkg"
    fi ;;
  deb)
    pkg="$(ls /dist/sycache_*.deb)"
    export DEBIAN_FRONTEND=noninteractive
    apt-get update -qq && apt-get install -y -qq "$pkg" >/dev/null ;;
  *) exit 2 ;;
esac

step "files and identity"
id sycache
for b in sycache-server sycache-cli sycache-sentinel sycache-benchmark sycache-check-rdb sycache-check-aof; do
  test -x "$B/$b"
done
if ldd "$B/sycache-server" | grep -q 'not found'; then ldd "$B/sycache-server"; exit 1; fi
ver="$(sed -n 's/^product_version=//p' /opt/sycache/share/BUILDINFO)"
"$B/sycache-server" --version | grep -q "SyCache server v=$ver "
test -f /usr/lib/systemd/system/sycache@.service
test -d /opt/sycache/instances && test "$(stat -c %U /opt/sycache/instances)" = sycache

step "primary / replica as user sycache"
for p in 7001 7002; do
  install -d -o sycache -g sycache /opt/sycache/instances/$p
done
run() { runuser -u sycache -- "$@"; }
run "$B/sycache-server" --port 7001 --daemonize yes --dir /opt/sycache/instances/7001 \
  --logfile /var/log/sycache/7001.log --pidfile /opt/sycache/instances/7001/pid
run "$B/sycache-server" --port 7002 --daemonize yes --dir /opt/sycache/instances/7002 \
  --logfile /var/log/sycache/7002.log --pidfile /opt/sycache/instances/7002/pid --replicaof 127.0.0.1 7001
cli() { "$B/sycache-cli" "$@"; }
for _ in $(seq 50); do cli -p 7002 info replication | grep -q 'master_link_status:up' && break; sleep 0.2; done
cli -p 7002 info replication | grep -q 'master_link_status:up'
cli -p 7001 set smoke:k v >/dev/null
for _ in $(seq 50); do [ "$(cli -p 7002 get smoke:k)" = v ] && break; sleep 0.1; done
[ "$(cli -p 7002 get smoke:k)" = v ]
[ "$(cli -p 7001 eval "return redis.call('GET', KEYS[1])" 1 smoke:k)" = v ]
cli -p 7001 save >/dev/null
(cd /opt/sycache/instances/7001 && "$B/sycache-check-rdb" dump.rdb | grep -q 'RDB looks OK')
cli -p 7002 shutdown nosave >/dev/null 2>&1 || true
cli -p 7001 shutdown nosave >/dev/null 2>&1 || true

step "uninstall"
rm -rf /opt/sycache/instances/7001 /opt/sycache/instances/7002
case "$KIND" in
  rpm) rpm -e sycache ;;
  deb) apt-get remove -y -qq sycache >/dev/null ;;
esac
test ! -e "$B/sycache-server"
glibc="$(ldd --version | head -1 | grep -oE '[0-9]+\.[0-9]+$')"
echo "NOTICE: $KIND install/run/uninstall OK on $OS ($ARCH, glibc $glibc)"
