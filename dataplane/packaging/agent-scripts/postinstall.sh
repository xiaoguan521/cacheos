#!/bin/sh
# Install does not start an un-enrolled agent. The enrollment command enables and starts it.
set -eu

install -d -o root -g sycache -m 0750 /opt/sycache
install -d -o sycache-agent -g sycache -m 0700 /var/lib/sycache-agent
install -d -o sycache -g sycache -m 2770 /opt/sycache/instances
install -d -o sycache -g sycache -m 2770 /var/log/sycache
if [ -d /run/systemd/system ] && command -v systemctl >/dev/null 2>&1; then
  systemctl daemon-reload >/dev/null 2>&1 || true
  systemctl enable sycache-agent.service >/dev/null 2>&1 || true
fi
exit 0
