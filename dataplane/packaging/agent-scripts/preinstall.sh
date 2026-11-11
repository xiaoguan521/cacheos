#!/bin/sh
# Create the least-privilege account used by the outbound-only agent.
set -eu

getent group sycache >/dev/null 2>&1 || groupadd -r sycache
if ! getent passwd sycache-agent >/dev/null 2>&1; then
  useradd -r -g sycache -d /var/lib/sycache-agent -s /usr/sbin/nologin -c "CacheOS agent" sycache-agent
fi

install -d -o root -g sycache -m 0750 /home/atwasoft/sycache
install -d -o sycache-agent -g sycache -m 0700 /var/lib/sycache-agent
install -d -o sycache -g sycache -m 2770 /home/atwasoft/sycache/instances
install -d -o sycache -g sycache -m 2770 /var/log/sycache
exit 0
