package conn

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	agentv1 "github.com/xiaoguan521/cacheos/api/gen/agent/v1"
	"github.com/xiaoguan521/cacheos/internal/agent/selfupdate"
	"github.com/xiaoguan521/cacheos/internal/pkgsig"
)

// agent 自升级（规格书 6.2）：下载升级包 → 校验厂商签名与哈希 → 安装到状态目录 → 退出，由 systemd 重启到新版本。
// 实例进程不受影响。

var upgradeVersionRe = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+(-[a-z0-9.]+)?$`)

const maxPackage = 128 << 20

func fetchPackageTo(ctx context.Context, client agentv1.AgentGatewayClient, req *agentv1.FetchPackageRequest, dst io.Writer, maxBytes int64) (int64, error) {
	timeout := 2 * time.Minute
	if req.GetKind() == "backup" {
		// Backups can be much larger than an agent package; keep the stream
		// bounded by disk space and the caller's context rather than RAM.
		timeout = 15 * time.Minute
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	s, err := client.FetchPackage(cctx, req)
	if err != nil {
		return 0, err
	}
	var written int64
	for {
		c, err := s.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			return 0, err
		}
		if written != c.Offset {
			return 0, errors.New("out-of-order chunk")
		}
		if maxBytes > 0 && written+int64(len(c.Data)) > maxBytes {
			return 0, errors.New("package too large")
		}
		n, err := dst.Write(c.Data)
		if err != nil {
			return 0, err
		}
		if n != len(c.Data) {
			return 0, io.ErrShortWrite
		}
		written += int64(n)
		if c.Last {
			break
		}
	}
	return written, nil
}

func fetchPackage(ctx context.Context, client agentv1.AgentGatewayClient, req *agentv1.FetchPackageRequest) ([]byte, error) {
	var buf bytes.Buffer
	if _, err := fetchPackageTo(ctx, client, req, &buf, maxPackage); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// handleAgentUpgrade 下载并校验 agent 升级包，安装后退出，由 systemd 重启到新版本。
// vendor_signature 是 pkgsig 签名文件（JSON，含 Ed25519 签名与 sha256）。
func (a *Agent) handleAgentUpgrade(ctx context.Context, client agentv1.AgentGatewayClient, u *agentv1.UpgradeAgent, reply func(*agentv1.CommandResult)) {
	fail := func(msg string) {
		reply(&agentv1.CommandResult{Status: agentv1.CommandResult_FAILED, ErrorCode: "INVALID", Message: msg})
	}
	if !upgradeVersionRe.MatchString(u.Version) {
		fail("invalid version")
		return
	}
	sig, err := pkgsig.Parse(u.VendorSignature)
	if err != nil || sig.Name != "sycache-agent" || sig.Version != u.Version || sig.Arch != runtime.GOARCH {
		fail("signature file does not match this upgrade")
		return
	}
	data, err := fetchPackage(ctx, client, &agentv1.FetchPackageRequest{Kind: "agent", Name: "sycache-agent", Version: u.Version, Arch: runtime.GOARCH})
	if err != nil {
		fail("fetch package: " + err.Error())
		return
	}
	if _, err := selfupdate.Install(a.C.StateDir, data, u.VendorSignature, runtime.GOARCH, pkgsig.Keys()); err != nil {
		fail(err.Error())
		return
	}
	reply(&agentv1.CommandResult{Status: agentv1.CommandResult_OK, Message: "agent " + u.Version + " installed; restarting the agent process"})
	a.log.Info("agent self-upgrade installed, exiting for restart", "version", u.Version)
	go func() {
		time.Sleep(2 * time.Second)
		a.exit(0)
	}()
}

// handleEngineUpgrade 下载、验签并把数据面包暂存给 root wrapper；wrapper 会再次验签，
// 只把 home/atwasoft/sycache 内容安装到 versions/<version>，然后原子切换 current。
// 复用 SwitchVersion 消息保持既有四条 sudoers 规则不变。
func (a *Agent) handleEngineUpgrade(ctx context.Context, client agentv1.AgentGatewayClient, u *agentv1.SwitchVersion, reply func(*agentv1.CommandResult)) {
	fail := func(msg string) {
		reply(&agentv1.CommandResult{Status: agentv1.CommandResult_FAILED, ErrorCode: "INVALID", Message: msg})
	}
	if !upgradeVersionRe.MatchString(u.Version) || (u.Arch != "" && u.Arch != runtime.GOARCH) {
		fail("invalid version or architecture")
		return
	}
	sig, err := pkgsig.Parse(u.VendorSignature)
	if err != nil || sig.Name != "sycache" || sig.Version != u.Version || sig.Arch != runtime.GOARCH {
		fail("signature file does not match this engine upgrade")
		return
	}
	data, err := fetchPackage(ctx, client, &agentv1.FetchPackageRequest{Kind: "engine", Name: "sycache", Version: u.Version, Arch: runtime.GOARCH})
	if err != nil {
		fail("fetch package: " + err.Error())
		return
	}
	if err := pkgsig.Verify(sig, data, pkgsig.Keys()); err != nil {
		fail("signature check failed: " + err.Error())
		return
	}
	if u.Sha256 != "" && !strings.EqualFold(u.Sha256, sig.SHA256) {
		fail("package sha256 does not match the signed metadata")
		return
	}
	pkgPath, sigPath, err := a.stageEnginePackage(u.Version, data, u.VendorSignature)
	if err != nil {
		fail("stage package: " + err.Error())
		return
	}
	if out, err := a.M.Run(ctx, "switch-version", u.Version); err != nil {
		fail("install or switch engine version: " + strings.TrimSpace(out))
		return
	}
	_ = os.Remove(pkgPath)
	_ = os.Remove(sigPath)
	a.log.Info("engine version installed and switched; instances pick it up on their next restart", "version", u.Version)
	reply(&agentv1.CommandResult{Status: agentv1.CommandResult_OK, Message: "engine " + u.Version + " installed; current switched"})
}

func (a *Agent) stageEnginePackage(version string, data, sig []byte) (string, string, error) {
	if !upgradeVersionRe.MatchString(version) {
		return "", "", errors.New("invalid version")
	}
	dir := filepath.Join(a.C.StateDir, "upgrade")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", "", err
	}
	pkgPath := filepath.Join(dir, version+".tar.gz")
	sigPath := filepath.Join(dir, version+".sig")
	if err := writeUpgradeFile(pkgPath, data); err != nil {
		return "", "", err
	}
	if err := writeUpgradeFile(sigPath, sig); err != nil {
		_ = os.Remove(pkgPath)
		return "", "", err
	}
	return pkgPath, sigPath, nil
}

func writeUpgradeFile(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".upgrade-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer func() {
		_ = os.Remove(tmpPath)
	}()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}
