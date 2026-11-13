package conn

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	agentv1 "github.com/xiaoguan521/cacheos/api/gen/agent/v1"
	"github.com/xiaoguan521/cacheos/internal/agent/engineupdate"
	"github.com/xiaoguan521/cacheos/internal/agent/instance"
	"github.com/xiaoguan521/cacheos/internal/agent/selfupdate"
	"github.com/xiaoguan521/cacheos/internal/pkgsig"
)

// agent 自升级（规格书 6.2）：下载升级包 → 校验厂商签名与哈希 → 安装到状态目录 → 退出，由 systemd 重启到新版本。
// 实例进程不受影响。

// checkUpgradeRequest 是两条升级路径下载前的统一检查：版本格式，签名文件与命令的名称 / 版本 / 本机架构一致，
// 命令给出的 sha256 与已签名元数据一致。下载后仍由 pkgsig.Check 做完整验签。
func checkUpgradeRequest(name, version, sha256 string, vendorSig []byte) (*pkgsig.Sig, error) {
	if !pkgsig.VersionRe.MatchString(version) {
		return nil, errors.New("invalid version")
	}
	sig, err := pkgsig.Parse(vendorSig)
	if err != nil || sig.Name != name || sig.Version != version || sig.Arch != runtime.GOARCH {
		return nil, errors.New("signature file does not match this upgrade")
	}
	if !sig.DigestMatches(sha256) {
		return nil, errors.New("package sha256 does not match the signed metadata")
	}
	return sig, nil
}

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
	if _, err := fetchPackageTo(ctx, client, req, &buf, pkgsig.MaxPackage); err != nil {
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
	if _, err := checkUpgradeRequest("sycache-agent", u.Version, u.Sha256, u.VendorSignature); err != nil {
		fail(err.Error())
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
	if u.Arch != "" && u.Arch != runtime.GOARCH {
		fail("invalid version or architecture")
		return
	}
	if _, err := checkUpgradeRequest("sycache", u.Version, u.Sha256, u.VendorSignature); err != nil {
		fail(err.Error())
		return
	}
	data, err := fetchPackage(ctx, client, &agentv1.FetchPackageRequest{Kind: "engine", Name: "sycache", Version: u.Version, Arch: runtime.GOARCH})
	if err != nil {
		fail("fetch package: " + err.Error())
		return
	}
	if _, err := engineupdate.Verify(data, u.VendorSignature, runtime.GOARCH, pkgsig.Keys()); err != nil {
		fail("signature check failed: " + err.Error())
		return
	}
	// 本机实例在用扩展模块时，新版本必须自带这些模块，否则实例重启后会因缺少模块文件起不来。
	if required := a.M.RequiredModules(); len(required) > 0 {
		have, err := engineupdate.PackageModules(data)
		if err != nil {
			fail("inspect package modules: " + err.Error())
			return
		}
		if missing := instance.MissingModules(required, func(n string) bool { return have[n] }); len(missing) > 0 {
			fail("this engine package lacks modules in use on this host: " + strings.Join(missing, ", "))
			return
		}
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
	if !pkgsig.VersionRe.MatchString(version) {
		return "", "", errors.New("invalid version")
	}
	pkgPath, sigPath := engineupdate.StagedPaths(a.C.StateDir, version)
	if err := os.MkdirAll(filepath.Dir(pkgPath), 0o700); err != nil {
		return "", "", err
	}
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
