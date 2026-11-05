#!/usr/bin/env bash
# Build the agent and its root-owned wrapper into a standalone DEB.
# Usage: build-agent-deb.sh <sycache-agent> <sycache-wrapper> <version> <output-dir>
set -euo pipefail
HERE="$(cd "$(dirname "$0")" && pwd)"
AGENT="${1:?usage: build-agent-deb.sh <agent> <wrapper> <version> <out-dir>}"
WRAPPER="${2:?usage: build-agent-deb.sh <agent> <wrapper> <version> <out-dir>}"
VERSION="${3:?usage: build-agent-deb.sh <agent> <wrapper> <version> <out-dir>}"
OUT="${4:?usage: build-agent-deb.sh <agent> <wrapper> <version> <out-dir>}"
test -x "$AGENT" && test -x "$WRAPPER"
mkdir -p "$OUT"
OUT="$(cd "$OUT" && pwd)"
WORK="$(mktemp -d)"
cleanup() { rm -r -- "$WORK"; }
trap cleanup EXIT
ROOT="$WORK/root"
mkdir -p "$ROOT/DEBIAN" "$ROOT/etc/systemd/system" "$ROOT/etc/sudoers.d" "$ROOT/usr/libexec/sycache"
install -D -m 0755 "$AGENT" "$ROOT/usr/bin/sycache-agent"
install -D -o root -g root -m 0755 "$WRAPPER" "$ROOT/usr/libexec/sycache/sycache-wrapper"
install -D -m 0644 "$HERE/sycache-agent.service" "$ROOT/etc/systemd/system/sycache-agent.service"
install -D -m 0440 "$HERE/sycache-agent.sudoers" "$ROOT/etc/sudoers.d/sycache-agent"
install -D -m 0755 "$HERE/agent-scripts/preinstall.sh" "$ROOT/DEBIAN/preinst"
install -D -m 0755 "$HERE/agent-scripts/postinstall.sh" "$ROOT/DEBIAN/postinst"
cat > "$ROOT/DEBIAN/prerm" <<'EOF'
#!/bin/sh
set -eu
if [ "${1:-}" = remove ] && command -v systemctl >/dev/null 2>&1; then
  systemctl disable --now sycache-agent.service >/dev/null 2>&1 || true
fi
exit 0
EOF
chmod 0755 "$ROOT/DEBIAN/prerm"
cat > "$ROOT/DEBIAN/control" <<EOF
Package: sycache-agent
Version: $VERSION
Architecture: $(dpkg --print-architecture)
Maintainer: CacheOS <support@cacheos.invalid>
Section: admin
Priority: optional
Pre-Depends: passwd
Depends: sudo
Installed-Size: $(du -sk --exclude=DEBIAN "$ROOT" | cut -f1)
Description: CacheOS host management agent
 Outbound-only host agent for CacheOS. Privileged lifecycle actions are restricted to sycache-wrapper.
EOF
printf '%s\n' /etc/systemd/system/sycache-agent.service /etc/sudoers.d/sycache-agent > "$ROOT/DEBIAN/conffiles"
DEB_VERSION="${VERSION//-/\~}-1"
DEB="$OUT/sycache-agent_${DEB_VERSION}_$(dpkg --print-architecture).deb"
dpkg-deb --root-owner-group -Zxz --build "$ROOT" "$DEB" >/dev/null
echo "built $DEB"
