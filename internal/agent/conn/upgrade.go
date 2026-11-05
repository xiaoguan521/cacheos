package conn

import (
	"bytes"
	"context"
	"errors"
	"io"
	"regexp"
	"runtime"
	"time"

	agentv1 "github.com/xiaoguan521/cacheos/api/gen/agent/v1"
	"github.com/xiaoguan521/cacheos/internal/agent/selfupdate"
	"github.com/xiaoguan521/cacheos/internal/pkgsig"
)

// agent 自升级（规格书 6.2）：下载升级包 → 校验厂商签名与哈希 → 安装到状态目录 → 退出，由 systemd 重启到新版本。
// 实例进程不受影响。

var upgradeVersionRe = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+(-[a-z0-9.]+)?$`)

const maxPackage = 128 << 20

func fetchPackage(ctx context.Context, client agentv1.AgentGatewayClient, req *agentv1.FetchPackageRequest) ([]byte, error) {
	cctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	s, err := client.FetchPackage(cctx, req)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	for {
		c, err := s.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		if int64(buf.Len()) != c.Offset {
			return nil, errors.New("out-of-order chunk")
		}
		buf.Write(c.Data)
		if buf.Len() > maxPackage {
			return nil, errors.New("package too large")
		}
		if c.Last {
			break
		}
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
