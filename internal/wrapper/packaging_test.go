package wrapper

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func repoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "../.."))
}

func readPackaging(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(repoRoot(t), path))
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if len(b) == 0 {
		t.Fatalf("packaging file %s is empty", path)
	}
	return string(b)
}

func TestAgentPackagingIsPresentAndHardened(t *testing.T) {
	files := []string{
		"dataplane/packaging/agent-scripts/preinstall.sh",
		"dataplane/packaging/agent-scripts/postinstall.sh",
		"dataplane/packaging/build-agent-deb.sh",
		"dataplane/packaging/build-agent-rpm.sh",
		"dataplane/packaging/sycache-agent.service",
		"dataplane/packaging/sycache-agent.sudoers",
		"deploy/install-agent.sh",
		"deploy/install-console.sh",
		"deploy/sycache-agent.service",
		"deploy/sycache-agent.sudoers",
		"deploy/sycache-console.service",
	}
	for _, path := range files {
		body := readPackaging(t, path)
		upstreamName := "val" + "key"
		if strings.Contains(strings.ToLower(body), upstreamName) {
			t.Errorf("%s contains forbidden upstream product name", path)
		}
		if strings.Contains(body, "rm -rf") {
			t.Errorf("%s contains destructive recursive removal", path)
		}
	}

	service := readPackaging(t, "dataplane/packaging/sycache-agent.service")
	for _, want := range []string{"User=sycache-agent", "Group=sycache", "ExecStart=/usr/bin/sycache-agent run", "Restart=on-failure", "ProtectSystem=strict", "UMask=0007", "RuntimeDirectory=sycache", "ReadWritePaths=/var/lib/sycache-agent /opt/sycache /run/sycache"} {
		if !strings.Contains(service, want) {
			t.Errorf("agent service missing %q", want)
		}
	}

	// 以非 root 用户运行时，这些选项会让 systemd 强制启用 NoNewPrivileges（写 NoNewPrivileges=no 也关不掉），
	// sudo 随之无法提权，wrapper 的启停、重启、切换版本全部失败。
	impliesNoNewPrivs := []string{"NoNewPrivileges=yes", "RestrictAddressFamilies=", "ProtectKernelTunables=", "ProtectKernelModules=",
		"RestrictSUIDSGID=", "SystemCallFilter=", "SystemCallArchitectures=", "RestrictNamespaces=", "PrivateDevices=",
		"ProtectKernelLogs=", "ProtectClock=", "MemoryDenyWriteExecute=", "RestrictRealtime=", "LockPersonality=", "DynamicUser="}
	for _, path := range []string{"dataplane/packaging/sycache-agent.service", "deploy/sycache-agent.service"} {
		for _, line := range strings.Split(readPackaging(t, path), "\n") {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "#") {
				continue
			}
			for _, opt := range impliesNoNewPrivs {
				if strings.HasPrefix(line, opt) {
					t.Errorf("%s sets %q, which makes systemd force NoNewPrivileges and breaks sudo to the wrapper", path, line)
				}
			}
		}
	}

	sudoers := readPackaging(t, "dataplane/packaging/sycache-agent.sudoers")
	for _, want := range []string{"sycache-agent", "NOPASSWD", "/usr/libexec/sycache/sycache-wrapper"} {
		if !strings.Contains(sudoers, want) {
			t.Errorf("sudoers missing %q", want)
		}
	}

	pre := readPackaging(t, "dataplane/packaging/agent-scripts/preinstall.sh")
	for _, want := range []string{"groupadd", "useradd", "sycache-agent", "/var/lib/sycache-agent", "/opt/sycache"} {
		if !strings.Contains(pre, want) {
			t.Errorf("agent preinstall missing %q", want)
		}
	}

	consoleInstaller := readPackaging(t, "deploy/install-console.sh")
	for _, want := range []string{"sycache-console", "/var/lib/sycache-console", "/etc/cacheos", "useradd", "systemctl enable sycache-console.service"} {
		if !strings.Contains(consoleInstaller, want) {
			t.Errorf("console installer missing %q", want)
		}
	}
	consoleService := readPackaging(t, "deploy/sycache-console.service")
	for _, want := range []string{"User=sycache-console", "Group=sycache-console", "ReadWritePaths=/var/lib/sycache-console /etc/cacheos"} {
		if !strings.Contains(consoleService, want) {
			t.Errorf("console service missing %q", want)
		}
	}
}
