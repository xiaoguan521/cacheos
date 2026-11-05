// Package conn 实现 sycache-agent 与控制台之间的连接：首次注册、mTLS 长连接、心跳与指令执行（CacheOS 规格书 6.2）。
//
// agent 只主动连接控制台，不监听任何端口；每条指令先验应用层签名再执行。
// 实例操作全部委托给 instance.Manager（Unix Socket + 管理账号；需要 root 的动作只经 sycache-wrapper）。
package conn

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	grpcstatus "google.golang.org/grpc/status"

	agentv1 "github.com/xiaoguan521/cacheos/api/gen/agent/v1"
	"github.com/xiaoguan521/cacheos/internal/agent/collect"
	"github.com/xiaoguan521/cacheos/internal/agent/instance"
	"github.com/xiaoguan521/cacheos/internal/cmdsig"
	"github.com/xiaoguan521/cacheos/internal/license"
	"github.com/xiaoguan521/cacheos/internal/version"
)

// Config 是 agent 的运行参数。
type Config struct {
	Server    string // 控制台 gRPC 地址 host:port
	StateDir  string // /var/lib/sycache-agent
	Heartbeat time.Duration
	Layout    instance.Layout
	Wrapper   []string // sudo -n /usr/libexec/sycache/sycache-wrapper（按空格切分，不经过 shell）
}

// state 是注册后持久化的身份信息。
type state struct {
	HostID     string `json:"host_id"`
	Server     string `json:"server"`
	CommandPub []byte `json:"command_pub"`
}

// Agent 是一台主机上的探针。
type Agent struct {
	C   Config
	M   *instance.Manager
	log *slog.Logger

	mu       sync.Mutex
	st       state
	verifier *cmdsig.Verifier
	licRaw   string

	exit func(code int) // 自升级后退出（测试可替换），默认 os.Exit
}

// New 创建 agent。
func New(c Config, log *slog.Logger) *Agent {
	if c.Heartbeat == 0 {
		c.Heartbeat = 15 * time.Second
	}
	if c.Layout.Root == "" {
		c.Layout = instance.DefaultLayout(c.StateDir)
	}
	m := &instance.Manager{L: c.Layout, Run: instance.WrapperRunner(c.Wrapper), Active: instance.SystemdActive}
	return &Agent{C: c, M: m, log: log, exit: os.Exit}
}

func (a *Agent) path(n string) string { return filepath.Join(a.C.StateDir, n) }

// Enrolled 报告本机是否已完成注册。
func (a *Agent) Enrolled() bool {
	_, err := os.Stat(a.path("node.json"))
	return err == nil
}

// Enroll 用一次性令牌完成注册。caFingerprint 是控制台 CA 证书 DER 的 sha256，
// 由“添加节点”页面生成的安装命令提供，用于首次连接时固定控制台身份。
func (a *Agent) Enroll(ctx context.Context, token, caFingerprint string) error {
	if err := os.MkdirAll(a.C.StateDir, 0o700); err != nil {
		return err
	}
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	_ = pub
	host, _ := os.Hostname()
	csr, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{Subject: pkix.Name{CommonName: host}}, priv)
	if err != nil {
		return err
	}
	var seenCA []byte
	tlsCfg := &tls.Config{
		MinVersion:         tls.VersionTLS13,
		InsecureSkipVerify: true, //nolint:gosec // 由 VerifyPeerCertificate 固定 CA 指纹并校验证书链
		VerifyPeerCertificate: func(raw [][]byte, _ [][]*x509.Certificate) error {
			for _, der := range raw {
				if fp(der) == strings.ToLower(caFingerprint) {
					ca, err := x509.ParseCertificate(der)
					if err != nil {
						return err
					}
					leaf, err := x509.ParseCertificate(raw[0])
					if err != nil {
						return err
					}
					pool := x509.NewCertPool()
					pool.AddCert(ca)
					if _, err := leaf.Verify(x509.VerifyOptions{Roots: pool, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}); err != nil {
						return err
					}
					seenCA = der
					return nil
				}
			}
			return errors.New("console CA fingerprint mismatch")
		},
	}
	cc, err := grpc.NewClient(a.C.Server, grpc.WithNoProxy(), grpc.WithTransportCredentials(credentials.NewTLS(tlsCfg)))
	if err != nil {
		return err
	}
	defer cc.Close()
	cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	resp, err := agentv1.NewAgentGatewayClient(cc).Enroll(cctx, &agentv1.EnrollRequest{
		EnrollToken: token, CsrDer: csr, Hostname: host,
		MachineFingerprint: machineFingerprint(), AgentVersion: version.Version,
	})
	if err != nil {
		return fmt.Errorf("enroll: %w", err)
	}
	if !bytes.Equal(resp.CaCertDer, seenCA) {
		return errors.New("enroll: CA in response differs from pinned CA")
	}
	kder, _ := x509.MarshalPKCS8PrivateKey(priv)
	files := map[string][]byte{
		"node.key": pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: kder}),
		"node.crt": pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: resp.ClientCertDer}),
		"ca.crt":   pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: resp.CaCertDer}),
	}
	// 每个文件先写临时文件再 rename；node.json 最后写，它的出现代表注册完成
	for n, b := range files {
		if err := a.writeAtomic(n, b); err != nil {
			return err
		}
	}
	st, _ := json.MarshalIndent(state{HostID: resp.HostId, Server: a.C.Server, CommandPub: resp.CommandPubkey}, "", "  ")
	if err := a.writeAtomic("node.json", st); err != nil {
		return err
	}
	a.log.Info("enrolled", "host_id", resp.HostId)
	return nil
}

// writeAtomic 写入身份文件。enroll 通常以 root 执行，而 agent 以 sycache-agent 运行：
// 文件属主跟随状态目录，运行中的 agent 才能读取新身份。
func (a *Agent) writeAtomic(name string, b []byte) error {
	tmp := a.path(name + ".tmp")
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	if fi, err := os.Stat(a.C.StateDir); err == nil && os.Geteuid() == 0 {
		if st, ok := fi.Sys().(*syscall.Stat_t); ok {
			if err := os.Chown(tmp, int(st.Uid), int(st.Gid)); err != nil {
				return err
			}
		}
	}
	return os.Rename(tmp, a.path(name))
}

func fp(der []byte) string { s := sha256.Sum256(der); return hex.EncodeToString(s[:]) }

func machineFingerprint() string {
	b, _ := os.ReadFile("/etc/machine-id")
	return fp(bytes.TrimSpace(b))
}

func (a *Agent) load() (*tls.Config, error) {
	b, err := os.ReadFile(a.path("node.json"))
	if err != nil {
		return nil, err
	}
	var st state
	if err := json.Unmarshal(b, &st); err != nil {
		return nil, err
	}
	cert, err := tls.LoadX509KeyPair(a.path("node.crt"), a.path("node.key"))
	if err != nil {
		return nil, err
	}
	caPEM, err := os.ReadFile(a.path("ca.crt"))
	if err != nil {
		return nil, err
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPEM) {
		return nil, errors.New("bad ca.crt")
	}
	a.mu.Lock()
	a.st = st
	a.verifier = &cmdsig.Verifier{Pub: ed25519.PublicKey(st.CommandPub)}
	a.mu.Unlock()
	return &tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{cert}, RootCAs: pool}, nil
}

// Run 保持与控制台的连接，断线后指数退避重连（1s → 60s），直到 ctx 结束。
// 每次重连前重新读取注册身份：节点被吊销后在本机重新注册，运行中的 agent 无需重启即可换用新身份。
func (a *Agent) Run(ctx context.Context) error {
	tlsCfg, err := a.load()
	if err != nil {
		return fmt.Errorf("not enrolled: %w", err)
	}
	backoff := time.Second
	for ctx.Err() == nil {
		start := time.Now()
		ident := a.identityStamp()
		err := a.session(ctx, tlsCfg)
		if ctx.Err() != nil {
			break
		}
		if time.Since(start) > time.Minute {
			backoff = time.Second
		}
		if grpcstatus.Code(err) == codes.PermissionDenied {
			a.log.Error("console rejected this host (revoked?); re-add it in the console and run the enroll command on this host; instances keep serving", "err", err)
		} else {
			a.log.Warn("disconnected from console; instances keep serving", "err", err, "retry_in", backoff)
		}
		// 等待退避时间，期间若本机重新注册则立即重连
		deadline := time.After(backoff)
	wait:
		for {
			select {
			case <-ctx.Done():
				break wait
			case <-deadline:
				break wait
			case <-time.After(2 * time.Second):
				if a.identityStamp() != ident {
					backoff = time.Second / 2
					break wait
				}
			}
		}
		if cfg, err := a.load(); err == nil {
			tlsCfg = cfg
		} else {
			a.log.Warn("reload identity failed; keeping current one", "err", err)
		}
		if backoff *= 2; backoff > time.Minute {
			backoff = time.Minute
		}
	}
	return ctx.Err()
}

// identityStamp 标识当前注册身份（node.json 的修改时间与大小），用于发现本机重新注册。
func (a *Agent) identityStamp() string {
	fi, err := os.Stat(a.path("node.json"))
	if err != nil {
		return ""
	}
	return fmt.Sprintf("%d/%d", fi.ModTime().UnixNano(), fi.Size())
}

func (a *Agent) session(ctx context.Context, tlsCfg *tls.Config) error {
	cc, err := grpc.NewClient(a.C.Server, grpc.WithNoProxy(), grpc.WithTransportCredentials(credentials.NewTLS(tlsCfg)))
	if err != nil {
		return err
	}
	defer cc.Close()
	client := agentv1.NewAgentGatewayClient(cc)
	sctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stream, err := client.Connect(sctx)
	if err != nil {
		return err
	}
	out := make(chan *agentv1.AgentMessage, 16)
	errc := make(chan error, 2)
	go func() { // 唯一的发送者
		for {
			select {
			case <-sctx.Done():
				return
			case m := <-out:
				if err := stream.Send(m); err != nil {
					errc <- err
					return
				}
			}
		}
	}()
	send := func(m *agentv1.AgentMessage) {
		m.MsgId = newID()
		select {
		case out <- m:
		case <-sctx.Done():
		}
	}
	send(&agentv1.AgentMessage{Body: &agentv1.AgentMessage_Hello{Hello: a.hello(sctx)}})
	a.log.Info("connected to console", "server", a.C.Server)

	go a.heartbeat(sctx, send)
	go func() {
		for {
			m, err := stream.Recv()
			if err != nil {
				errc <- err
				return
			}
			go a.handle(sctx, client, m, send)
		}
	}()
	return <-errc
}

func (a *Agent) hello(ctx context.Context) *agentv1.Hello {
	host, _ := os.Hostname()
	h := &agentv1.Hello{AgentVersion: version.Version + " linux/" + runtime.GOARCH, Hostname: host, MachineFingerprint: machineFingerprint(),
		CpuCores: uint32(runtime.NumCPU()), MemTotalBytes: collect.MemTotal(), OsRelease: collect.OSRelease(), Kernel: collect.Kernel(),
		Tuning: collect.Tuning()}
	h.EngineVersions, h.EngineCurrent = a.C.Layout.InstalledVersions()
	if b, err := os.ReadFile(filepath.Join(a.C.Layout.Root, "current", "share", "BUILDINFO")); err == nil && len(b) < 16<<10 {
		h.EngineBuildinfo = string(b)
	}
	for _, p := range a.M.Ports() {
		h.Instances = append(h.Instances, a.M.Status(ctx, p))
	}
	return h
}

func (a *Agent) heartbeat(ctx context.Context, send func(*agentv1.AgentMessage)) {
	host := &collect.Host{}
	host.Sample()
	t := time.NewTicker(a.C.Heartbeat)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			cpu, mem := host.Sample()
			hb := &agentv1.Heartbeat{CpuPercent: cpu, MemUsedBytes: mem, MemTotalBytes: collect.MemTotal(),
				DataDiskFreeBytes: collect.DiskFree(filepath.Join(a.C.Layout.Root, "instances"))}
			for _, p := range a.M.Ports() {
				hb.Instances = append(hb.Instances, a.M.Status(ctx, p))
			}
			send(&agentv1.AgentMessage{Body: &agentv1.AgentMessage_Heartbeat{Heartbeat: hb}})
		}
	}
}

// result 把实例操作的返回值转成 CommandResult。
func result(cmdID string, s *agentv1.InstanceStatus, err error) *agentv1.CommandResult {
	r := &agentv1.CommandResult{CmdId: cmdID, Status: agentv1.CommandResult_OK, Instance: s}
	if err != nil {
		r.Status = agentv1.CommandResult_FAILED
		r.Message = err.Error()
		var ie *instance.Error
		if errors.As(err, &ie) {
			r.ErrorCode = ie.Code
			r.Message = ie.Msg
		} else {
			r.ErrorCode = "INTERNAL"
		}
	}
	return r
}

func (a *Agent) handle(ctx context.Context, client agentv1.AgentGatewayClient, m *agentv1.ServerMessage, send func(*agentv1.AgentMessage)) {
	sendResult := func(r *agentv1.CommandResult) {
		r.CmdId = m.CmdId
		send(&agentv1.AgentMessage{Body: &agentv1.AgentMessage_Result{Result: r}})
	}
	fail := func(code, msg string) {
		sendResult(&agentv1.CommandResult{Status: agentv1.CommandResult_FAILED, ErrorCode: code, Message: msg})
	}
	a.mu.Lock()
	v := a.verifier
	a.mu.Unlock()
	if err := v.Verify(m, time.Now()); err != nil {
		a.log.Warn("rejected unsigned or replayed command", "cmd_id", m.CmdId, "err", err)
		fail("INVALID", "signature check failed: "+err.Error())
		return
	}
	// 授权只读（过期 / 超限）时只拦截新建实例、参数变更与版本切换；启停、切换、账号同步、诊断照常（规格书 10 节：授权失效不停实例）
	switch m.Body.(type) {
	case *agentv1.ServerMessage_Create, *agentv1.ServerMessage_Params, *agentv1.ServerMessage_SwitchVersion:
		if ok, why := a.licenseWritable(); !ok {
			fail("LICENSE", "license does not allow this change: "+why)
			return
		}
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	switch b := m.Body.(type) {
	case *agentv1.ServerMessage_License:
		a.mu.Lock()
		a.licRaw = b.License.Raw
		a.mu.Unlock()
		ok, why := license.AgentCheck(b.License.Raw, license.BuiltinKeyRing(), time.Now())
		a.log.Info("license updated", "writable", ok, "reason", why)
		sendResult(&agentv1.CommandResult{Status: agentv1.CommandResult_OK, Message: "license received"})
	case *agentv1.ServerMessage_Create:
		a.log.Info("creating instance", "port", b.Create.Port, "kind", b.Create.Kind, "revision", b.Create.RevisionId)
		s, err := a.M.Create(ctx, b.Create)
		a.logResult("create", b.Create.Port, err)
		sendResult(result(m.CmdId, s, err))
	case *agentv1.ServerMessage_Params:
		a.log.Info("applying parameters", "port", b.Params.Port, "revision", b.Params.RevisionId, "parent", b.Params.ParentRevisionId)
		r, err := a.M.Apply(ctx, b.Params)
		if err != nil {
			a.logResult("params", b.Params.Port, err)
			sendResult(result(m.CmdId, nil, err))
			return
		}
		a.log.Info("parameters result", "port", b.Params.Port, "status", r.Status, "message", r.Message)
		sendResult(&agentv1.CommandResult{Status: r.Status, Message: r.Message, Instance: a.M.Status(ctx, b.Params.Port),
			Details: map[string]string{"applied": strings.Join(r.Applied, ","), "pending": strings.Join(r.Pending, ",")}})
	case *agentv1.ServerMessage_Action:
		a.log.Info("instance action", "port", b.Action.Port, "action", b.Action.Action)
		s, err := a.M.Action(ctx, b.Action.Port, b.Action.Action)
		a.logResult("action", b.Action.Port, err)
		sendResult(result(m.CmdId, s, err))
	case *agentv1.ServerMessage_Replication:
		s, err := a.M.SetReplication(ctx, b.Replication)
		a.logResult("replication", b.Replication.Port, err)
		sendResult(result(m.CmdId, s, err))
	case *agentv1.ServerMessage_Failover:
		a.log.Info("failover", "port", b.Failover.Port, "mode", b.Failover.Mode)
		s, err := a.M.Failover(ctx, b.Failover)
		a.logResult("failover", b.Failover.Port, err)
		sendResult(result(m.CmdId, s, err))
	case *agentv1.ServerMessage_Acl:
		d, err := a.M.SyncACL(ctx, b.Acl)
		a.logResult("acl", b.Acl.Port, err)
		r := result(m.CmdId, nil, err)
		r.Details = d
		sendResult(r)
	case *agentv1.ServerMessage_Probe:
		sendResult(result(m.CmdId, a.M.Status(ctx, b.Probe.Port), nil))
	case *agentv1.ServerMessage_SwitchVersion:
		out, err := a.M.Run(ctx, "switch-version", b.SwitchVersion.Version)
		if err != nil {
			fail("INVALID", "switch-version failed: "+strings.TrimSpace(out))
			return
		}
		a.log.Info("engine version switched; instances pick it up on their next restart", "version", b.SwitchVersion.Version)
		sendResult(&agentv1.CommandResult{Status: agentv1.CommandResult_OK, Message: "current -> " + b.SwitchVersion.Version})
	case *agentv1.ServerMessage_UpgradeAgent:
		a.handleAgentUpgrade(ctx, client, b.UpgradeAgent, sendResult)
	default:
		fail("UNSUPPORTED", "command not supported by this agent version")
	}
}

func (a *Agent) logResult(what string, port uint32, err error) {
	if err != nil {
		a.log.Warn(what+" failed", "port", port, "err", err)
	} else {
		a.log.Info(what+" done", "port", port)
	}
}

// licenseWritable：尚未收到 License 时由控制台负责拦截；收到后按 agent 侧独立校验结果执行。
func (a *Agent) licenseWritable() (bool, string) {
	a.mu.Lock()
	raw := a.licRaw
	a.mu.Unlock()
	if raw == "" {
		return true, ""
	}
	return license.AgentCheck(raw, license.BuiltinKeyRing(), time.Now())
}

func newID() string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// errUnused keeps fmt imported for helpers shared with upgrade.go.
var _ = fmt.Sprintf
