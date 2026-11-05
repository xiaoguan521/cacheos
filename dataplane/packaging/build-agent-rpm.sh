#!/usr/bin/env bash
# Build the agent and its root-owned wrapper into an RPM.
# Usage: build-agent-rpm.sh <sycache-agent> <sycache-wrapper> <version> <output-dir>
set -euo pipefail
HERE="$(cd "$(dirname "$0")" && pwd)"
AGENT="${1:?usage: build-agent-rpm.sh <agent> <wrapper> <version> <out-dir>}"
WRAPPER="${2:?usage: build-agent-rpm.sh <agent> <wrapper> <version> <out-dir>}"
VERSION="${3:?usage: build-agent-rpm.sh <agent> <wrapper> <version> <out-dir>}"
OUT="${4:?usage: build-agent-rpm.sh <agent> <wrapper> <version> <out-dir>}"
test -x "$AGENT" && test -x "$WRAPPER"
mkdir -p "$OUT"
OUT="$(cd "$OUT" && pwd)"
WORK="$(mktemp -d)"
cleanup() { rm -r -- "$WORK"; }
trap cleanup EXIT
mkdir -p "$WORK/root/usr/bin" "$WORK/root/usr/libexec/sycache" "$WORK/root/etc/systemd/system" "$WORK/root/etc/sudoers.d"
install -m 0755 "$AGENT" "$WORK/root/usr/bin/sycache-agent"
install -o root -g root -m 0755 "$WRAPPER" "$WORK/root/usr/libexec/sycache/sycache-wrapper"
install -m 0644 "$HERE/sycache-agent.service" "$WORK/root/etc/systemd/system/sycache-agent.service"
install -m 0440 "$HERE/sycache-agent.sudoers" "$WORK/root/etc/sudoers.d/sycache-agent"
script() { sed '1d' "$HERE/agent-scripts/$1.sh"; }
RPM_VERSION="${VERSION%%-*}"
if [[ "$VERSION" == *-* ]]; then RPM_RELEASE="0.${VERSION#*-}"; else RPM_RELEASE=1; fi
RPM_RELEASE="${RPM_RELEASE//-/.}"
cat > "$WORK/sycache-agent.spec" <<EOF
Name:           sycache-agent
Version:        $RPM_VERSION
Release:        $RPM_RELEASE
Summary:        CacheOS host management agent
License:        Proprietary
URL:            https://cacheos.invalid
Requires(pre):  /usr/sbin/useradd /usr/sbin/groupadd /usr/bin/getent
Requires:       sudo
AutoReqProv:    yes
%global __os_install_post %{nil}
%global debug_package %{nil}
%global _build_id_links none

%description
Outbound-only host agent for CacheOS. Privileged lifecycle actions are restricted to sycache-wrapper.

%install
mkdir -p %{buildroot}
cp -a $WORK/root/. %{buildroot}/

%pre
$(script preinstall)

%post
$(script postinstall)

%preun
if [ "\$1" -eq 0 ] && command -v systemctl >/dev/null 2>&1; then
  systemctl disable --now sycache-agent.service >/dev/null 2>&1 || true
fi

%files
%attr(0755,root,root) /usr/bin/sycache-agent
%attr(0755,root,root) /usr/libexec/sycache/sycache-wrapper
%config(noreplace) /etc/systemd/system/sycache-agent.service
%config(noreplace) /etc/sudoers.d/sycache-agent
EOF
rpmbuild -bb --quiet --define "_topdir $WORK/rpm" --define "_rpmdir $OUT" \
  --define "_build_name_fmt %{NAME}-%{VERSION}-%{RELEASE}.%{ARCH}.rpm" "$WORK/sycache-agent.spec"
echo "built $(ls "$OUT"/sycache-agent-"$RPM_VERSION"-"$RPM_RELEASE".*.rpm)"
