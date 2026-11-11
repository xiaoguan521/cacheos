#!/usr/bin/env bash
# 安装冒烟测试：在目标发行版容器中安装 RPM / DEB，运行主从实例，再卸载（规格书 5.3、5.4）。
# 容器内没有 systemd：只验证包依赖、维护脚本、二进制在该 glibc 上能运行，以及基本复制与持久化。
#
# 用法（容器内，安装包目录挂载到 /dist）：test/install/smoke.sh rpm|deb
set -euo pipefail
KIND="${1:?usage: smoke.sh rpm|deb}"
B=/home/atwasoft/sycache/bin
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
deps="$(ldd "$B/sycache-server")"
if [[ "$deps" == *"not found"* ]]; then echo "$deps"; exit 1; fi
ver="$(sed -n 's/^product_version=//p' /home/atwasoft/sycache/share/BUILDINFO)"
v="$("$B/sycache-server" --version)"
[[ "$v" == *"SyCache server v=$ver "* ]] || { echo "unexpected --version: $v"; exit 1; }
test -f /usr/lib/systemd/system/sycache@.service
test -d /home/atwasoft/sycache/instances && test "$(stat -c %U /home/atwasoft/sycache/instances)" = sycache
command -v sycache-cli >/dev/null
sycache-cli --version >/dev/null

step "primary / replica as user sycache"
for p in 7001 7002; do
  install -d -o sycache -g sycache /home/atwasoft/sycache/instances/$p
done
# 最小化镜像（如 openEuler）可能不带 util-linux 的 runuser：按需安装
if ! command -v runuser >/dev/null; then
  if command -v dnf >/dev/null; then dnf install -y -q util-linux >/dev/null; else yum install -y -q util-linux >/dev/null; fi
fi
run() { runuser -u sycache -- "$@"; }
run "$B/sycache-server" --port 7001 --daemonize yes --dir /home/atwasoft/sycache/instances/7001 \
  --logfile /var/log/sycache/7001.log --pidfile /home/atwasoft/sycache/instances/7001/pid
run "$B/sycache-server" --port 7002 --daemonize yes --dir /home/atwasoft/sycache/instances/7002 \
  --logfile /var/log/sycache/7002.log --pidfile /home/atwasoft/sycache/instances/7002/pid --replicaof 127.0.0.1 7001
cli() { "$B/sycache-cli" "$@"; }
# 用变量承接输出再匹配：管道里的 grep -q 提前退出会让上游收到 SIGPIPE，在 pipefail 下被误判为失败
link() { cli -p 7002 info replication 2>/dev/null | tr -d '\r' | sed -n 's/^master_link_status://p'; }
for _ in $(seq 50); do [ "$(link)" = up ] && break; sleep 0.2; done
[ "$(link)" = up ]
cli -p 7001 set smoke:k v >/dev/null
for _ in $(seq 50); do [ "$(cli -p 7002 get smoke:k)" = v ] && break; sleep 0.1; done
[ "$(cli -p 7002 get smoke:k)" = v ]
[ "$(cli -p 7001 eval "return redis.call('GET', KEYS[1])" 1 smoke:k)" = v ]
cli -p 7001 save >/dev/null
chk="$(cd /home/atwasoft/sycache/instances/7001 && "$B/sycache-check-rdb" dump.rdb)"
[[ "$chk" == *"RDB looks OK"* ]] || { echo "$chk"; exit 1; }
cli -p 7002 shutdown nosave >/dev/null 2>&1 || true
cli -p 7001 shutdown nosave >/dev/null 2>&1 || true

step "uninstall"
rm -rf /home/atwasoft/sycache/instances/7001 /home/atwasoft/sycache/instances/7002
case "$KIND" in
  rpm) rpm -e sycache ;;
  deb) apt-get remove -y -qq sycache >/dev/null ;;
esac
test ! -e "$B/sycache-server"
test ! -L /usr/bin/sycache-cli
glibc="$(getconf GNU_LIBC_VERSION | awk '{print $2}')"
echo "NOTICE: $KIND install/run/uninstall OK on $OS ($ARCH, glibc $glibc)"
