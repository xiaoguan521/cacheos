#!/usr/bin/env bash
# Install the CacheOS console binary and its dedicated system account.
# Usage: install-console.sh <sycache-console> [console.env]
set -euo pipefail
HERE="$(cd "$(dirname "$0")" && pwd)"
CONSOLE="${1:?usage: install-console.sh <sycache-console> [console.env]}"
ENV_FILE="${2:-}"

test -x "$CONSOLE"
if [ "$(id -u)" -ne 0 ]; then
  echo "run this installer as root" >&2
  exit 1
fi

if ! getent group sycache-console >/dev/null 2>&1; then
  groupadd -r sycache-console
fi
if ! getent passwd sycache-console >/dev/null 2>&1; then
  useradd -r -g sycache-console -d /var/lib/sycache-console -s /usr/sbin/nologin -c "CacheOS console" sycache-console
fi

install -D -o root -g root -m 0755 "$CONSOLE" /usr/bin/sycache-console
install -D -o root -g root -m 0644 "$HERE/sycache-console.service" /etc/systemd/system/sycache-console.service
install -d -o sycache-console -g sycache-console -m 0700 /var/lib/sycache-console
install -d -o root -g sycache-console -m 0750 /etc/cacheos
if [ -n "$ENV_FILE" ]; then
  test -f "$ENV_FILE"
  install -D -o root -g sycache-console -m 0640 "$ENV_FILE" /etc/cacheos/console.env
fi

systemctl daemon-reload
systemctl enable sycache-console.service >/dev/null
echo "console installed; configure /etc/cacheos/console.env, then run: systemctl enable --now sycache-console"
