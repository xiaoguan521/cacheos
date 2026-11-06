#!/usr/bin/env bash
# Install an agent binary and its root-only wrapper on a Linux host.
# Usage: install-agent.sh <sycache-agent> <sycache-wrapper>
set -euo pipefail
HERE="$(cd "$(dirname "$0")" && pwd)"
AGENT="${1:?usage: install-agent.sh <sycache-agent> <sycache-wrapper>}"
WRAPPER="${2:?usage: install-agent.sh <sycache-agent> <sycache-wrapper>}"
test -x "$AGENT" && test -x "$WRAPPER"
if [ "$(id -u)" -ne 0 ]; then
  echo "run this installer as root" >&2
  exit 1
fi

# Reuse the same user/group/directory creation logic as the DEB/RPM packages.
PREINSTALL="$(cd "$(dirname "$0")/../dataplane/packaging/agent-scripts" && pwd)/preinstall.sh"
if [ ! -f "$PREINSTALL" ]; then
  echo "preinstall.sh not found at $PREINSTALL" >&2
  exit 1
fi
bash "$PREINSTALL"

install -D -o root -g root -m 0755 "$AGENT" /usr/bin/sycache-agent
install -D -o root -g root -m 0755 "$WRAPPER" /usr/libexec/sycache/sycache-wrapper
install -D -o root -g root -m 0644 "$HERE/sycache-agent.service" /etc/systemd/system/sycache-agent.service
install -D -o root -g root -m 0440 "$HERE/sycache-agent.sudoers" /etc/sudoers.d/sycache-agent

systemctl daemon-reload
systemctl enable sycache-agent.service >/dev/null
echo "agent installed; enroll this host, then run: systemctl enable --now sycache-agent"
