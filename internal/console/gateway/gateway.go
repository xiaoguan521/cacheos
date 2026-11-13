// Package gateway 是控制台的 gRPC AgentGateway 服务端（CacheOS 规格书 6.2）。
//
// agent 主动连接到这里；控制台通过已建立的双向流下发签名指令，agent 在心跳中上报主机与实例状态。
package gateway

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/keepalive"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"

	agentv1 "github.com/xiaoguan521/cacheos/api/gen/agent/v1"
	"github.com/xiaoguan521/cacheos/internal/cmdsig"
	"github.com/xiaoguan521/cacheos/internal/console/backupstore"
	"github.com/xiaoguan521/cacheos/internal/console/licsvc"
	"github.com/xiaoguan521/cacheos/internal/console/pki"
	"github.com/xiaoguan521/cacheos/internal/console/store"
	"github.com/xiaoguan521/cacheos/internal/console/store/query"
	"github.com/xiaoguan521/cacheos/internal/paramcat"
	"github.com/xiaoguan521/cacheos/internal/pkg/audit"
)

// Server 实现 agentv1.AgentGatewayServer。
type Server struct {
	agentv1.UnimplementedAgentGatewayServer

	Store       *store.Store
	CA          *pki.CA
	CmdKey      ed25519.PrivateKey
	Lic         *licsvc.Service
	Audit       *audit.Logger
	Log         *slog.Logger
	BackupDir   string            // 远端 RDB 备份的临时落盘目录
	BackupStore backupstore.Store // 本地/NFS 或 S3-compatible 备份后端
	// OnOnline 在主机上线（收到 Hello）后调用。
	OnOnline func(hostID uint)
	// Ready 为 false 时（高可用备控制台）拒绝所有 agent 请求，agent 会重连到 VIP 上的主控制台。
	Ready func() bool

	mu       sync.Mutex
	sessions map[uint]*session
	lastMin  map[uint]int64 // 实例 ID → 最近写入的指标分钟
}

type session struct {
	hostID  uint
	out     chan *agentv1.ServerMessage
	mu      sync.Mutex
	waiters map[string]chan *agentv1.CommandResult
	done    chan struct{}
}

// TLSConfig 返回 gRPC 监听使用的 TLS 配置：Enroll 不要求客户端证书，其余 RPC 在处理时校验。
func (s *Server) TLSConfig(hosts []string) (*tls.Config, error) {
	cert, err := s.CA.ServerCert(hosts)
	if err != nil {
		return nil, err
	}
	pool := x509.NewCertPool()
	pool.AddCert(s.CA.Cert)
	return &tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{cert}, ClientCAs: pool, ClientAuth: tls.VerifyClientCertIfGiven}, nil
}

// GRPCServer 构造 grpc.Server 并注册服务。
func (s *Server) GRPCServer(tlsCfg *tls.Config) *grpc.Server {
	gs := grpc.NewServer(
		grpc.Creds(credentials.NewTLS(tlsCfg)),
		grpc.MaxRecvMsgSize(8<<20),
		grpc.KeepaliveParams(keepalive.ServerParameters{Time: 30 * time.Second, Timeout: 10 * time.Second}),
		grpc.KeepaliveEnforcementPolicy(keepalive.EnforcementPolicy{MinTime: 10 * time.Second, PermitWithoutStream: true}),
	)
	agentv1.RegisterAgentGatewayServer(gs, s)
	return gs
}

var errStandby = status.Error(codes.Unavailable, "standby console: connect to the active console")

func (s *Server) standby() bool { return s.Ready != nil && !s.Ready() }

// DisconnectAll 断开全部 agent（本控制台失去主角色时）。
func (s *Server) DisconnectAll() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, sess := range s.sessions {
		close(sess.done)
		delete(s.sessions, id)
	}
}

func hashToken(t string) string { h := sha256.Sum256([]byte(t)); return hex.EncodeToString(h[:]) }

// NewEnrollToken 生成一次性注册令牌（15 分钟有效），返回明文，库中只存哈希。
func (s *Server) NewEnrollToken(zone, by string) (string, time.Time, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", time.Time{}, err
	}
	tok := "sc_" + hex.EncodeToString(b)
	exp := time.Now().Add(15 * time.Minute)
	err := s.q().EnrollToken.WithContext(bg).Create(&store.EnrollToken{TokenHash: hashToken(tok), Zone: zone, ExpiresAt: exp, CreatedBy: by})
	return tok, exp, err
}

// bg 用于不依附请求的写入（例如连接断开后标记离线）。
var bg = context.Background()

func (s *Server) q() *query.Query { return query.Use(s.Store.DB) }

func (s *Server) backupStore() (backupstore.Store, error) {
	if s.BackupStore != nil {
		return s.BackupStore, nil
	}
	if s.BackupDir == "" {
		return nil, errors.New("backup storage is not configured")
	}
	return backupstore.NewLocal(s.BackupDir)
}

func remoteAddr(ctx context.Context) string {
	if p, ok := peer.FromContext(ctx); ok {
		return p.Addr.String()
	}
	return ""
}

func hostOf(addr string) string {
	if i := strings.LastIndexByte(addr, ':'); i > 0 {
		return strings.Trim(addr[:i], "[]")
	}
	return addr
}

// Enroll 处理主机注册。
func (s *Server) Enroll(ctx context.Context, req *agentv1.EnrollRequest) (*agentv1.EnrollResponse, error) {
	if s.standby() {
		return nil, errStandby
	}
	ip := remoteAddr(ctx)
	fail := func(code codes.Code, msg string) (*agentv1.EnrollResponse, error) {
		_ = s.Audit.Write(audit.Entry{Username: "agent:" + req.Hostname, IP: ip, Action: "host.enroll", ObjectType: "host", Result: "failure", Detail: map[string]string{"reason": msg}})
		return nil, status.Error(code, msg)
	}
	snap := s.Lic.Snapshot()
	if !snap.Writable() {
		return fail(codes.FailedPrecondition, "license does not allow registering hosts: "+string(snap.State))
	}
	q := s.q()
	et, hs := q.EnrollToken, q.Host
	tok, err := et.WithContext(ctx).Where(et.TokenHash.Eq(hashToken(req.EnrollToken))).First()
	if err != nil {
		return fail(codes.PermissionDenied, "invalid enroll token")
	}
	if tok.UsedAt != nil || time.Now().After(tok.ExpiresAt) {
		return fail(codes.PermissionDenied, "enroll token expired or already used")
	}
	if snap.Payload != nil && snap.Payload.Limits.Hosts > 0 {
		n, err := hs.WithContext(ctx).Where(hs.Status.Neq("revoked")).Count()
		if err != nil {
			return nil, status.Error(codes.Internal, "count hosts")
		}
		if n >= int64(snap.Payload.Limits.Hosts) {
			return fail(codes.ResourceExhausted, fmt.Sprintf("LICENSE_HOST_LIMIT: %d hosts licensed", snap.Payload.Limits.Hosts))
		}
	}
	name := req.Hostname
	if len(name) > 48 {
		name = name[:48]
	}
	// 名称唯一：同名主机（重装后重新注册）追加序号
	base := name
	for i := 2; ; i++ {
		if n, _ := hs.WithContext(ctx).Where(hs.Name.Eq(name)).Count(); n == 0 {
			break
		}
		name = fmt.Sprintf("%s-%d", base, i)
	}
	host := &store.Host{Name: name, Hostname: req.Hostname, Zone: tok.Zone, Address: hostOf(ip), RemoteAddr: ip, Status: "offline",
		AgentVersion: req.AgentVersion, Fingerprint: req.MachineFingerprint}
	if err := hs.WithContext(ctx).Create(host); err != nil {
		return nil, status.Error(codes.Internal, "create host")
	}
	der, serial, err := s.CA.SignNode(req.CsrDer, strconv.FormatUint(uint64(host.ID), 10))
	if err != nil {
		_, _ = hs.WithContext(bg).Delete(host)
		return fail(codes.InvalidArgument, "bad csr: "+err.Error())
	}
	// 令牌只能用一次：带 used_at IS NULL 条件的更新，并发注册时只有一个能更新成功
	res, err := et.WithContext(ctx).Where(et.ID.Eq(tok.ID), et.UsedAt.IsNull()).Update(et.UsedAt, time.Now())
	if err != nil || res.RowsAffected != 1 {
		_, _ = hs.WithContext(bg).Delete(host)
		return fail(codes.PermissionDenied, "enroll token already used")
	}
	if _, err := hs.WithContext(ctx).Where(hs.ID.Eq(host.ID)).Update(hs.CertSerial, serial.String()); err != nil {
		return nil, status.Error(codes.Internal, "save host certificate serial")
	}
	_ = s.Audit.Write(audit.Entry{Username: "agent:" + req.Hostname, IP: ip, Action: "host.enroll", ObjectType: "host", ObjectID: strconv.Itoa(int(host.ID)),
		Detail: map[string]any{"zone": tok.Zone, "token_created_by": tok.CreatedBy}})
	return &agentv1.EnrollResponse{HostId: strconv.Itoa(int(host.ID)), ClientCertDer: der, CaCertDer: s.CA.Cert.Raw, CommandPubkey: s.CmdKey.Public().(ed25519.PublicKey)}, nil
}

// hostFromPeer 从已验证的客户端证书识别主机，并核对证书序列号（吊销即失效）。
func (s *Server) hostFromPeer(ctx context.Context) (*store.Host, error) {
	p, ok := peer.FromContext(ctx)
	if !ok {
		return nil, status.Error(codes.Unauthenticated, "no peer")
	}
	ti, ok := p.AuthInfo.(credentials.TLSInfo)
	if !ok || len(ti.State.VerifiedChains) == 0 {
		return nil, status.Error(codes.Unauthenticated, "client certificate required")
	}
	leaf := ti.State.VerifiedChains[0][0]
	idStr, err := pki.NodeID(leaf)
	if err != nil {
		return nil, status.Error(codes.Unauthenticated, err.Error())
	}
	id, _ := strconv.Atoi(idStr)
	h, err := store.First[store.Host](s.Store, id)
	if err != nil {
		return nil, status.Error(codes.PermissionDenied, "unknown host")
	}
	if h.Status == "revoked" || h.CertSerial != leaf.SerialNumber.String() {
		return nil, status.Error(codes.PermissionDenied, "host revoked")
	}
	return h, nil
}

// Connect 处理 agent 的长连接。
func (s *Server) Connect(stream agentv1.AgentGateway_ConnectServer) error {
	if s.standby() {
		return errStandby
	}
	ctx := stream.Context()
	host, err := s.hostFromPeer(ctx)
	if err != nil {
		return err
	}
	sess := &session{hostID: host.ID, out: make(chan *agentv1.ServerMessage, 16), waiters: map[string]chan *agentv1.CommandResult{}, done: make(chan struct{})}
	s.mu.Lock()
	if s.sessions == nil {
		s.sessions = map[uint]*session{}
	}
	if old := s.sessions[host.ID]; old != nil {
		close(old.done)
	}
	s.sessions[host.ID] = sess
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		if s.sessions[host.ID] == sess {
			delete(s.sessions, host.ID)
			hs := s.q().Host // 连接已断开，流的 ctx 已取消，用 bg
			_, _ = hs.WithContext(bg).Where(hs.ID.Eq(host.ID), hs.Status.Eq("online")).Update(hs.Status, "offline")
			s.Log.Info("host offline", "host", host.ID)
		}
		s.mu.Unlock()
	}()

	errc := make(chan error, 2)
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-sess.done:
				errc <- errors.New("superseded by a newer connection")
				return
			case m := <-sess.out:
				if err := stream.Send(m); err != nil {
					errc <- err
					return
				}
			}
		}
	}()
	go func() {
		for {
			m, err := stream.Recv()
			if err != nil {
				errc <- err
				return
			}
			s.onMessage(ctx, host.ID, sess, m)
		}
	}()
	return <-errc
}

func (s *Server) onMessage(ctx context.Context, hostID uint, sess *session, m *agentv1.AgentMessage) {
	now := time.Now()
	hs := s.q().Host
	switch b := m.Body.(type) {
	case *agentv1.AgentMessage_Hello:
		h := b.Hello
		agentVer, arch := ParseAgentVersion(h.AgentVersion)
		tuning, _ := protojson.Marshal(h.GetTuning())
		_, _ = hs.WithContext(ctx).Where(hs.ID.Eq(hostID)).UpdateSimple(hs.Status.Value("online"), hs.AgentVersion.Value(agentVer), hs.Arch.Value(arch),
			hs.Hostname.Value(h.Hostname), hs.OSRelease.Value(h.OsRelease), hs.Kernel.Value(h.Kernel), hs.CPUCores.Value(h.CpuCores),
			hs.MemTotalBytes.Value(h.MemTotalBytes), hs.EngineCurrent.Value(h.EngineCurrent), hs.EngineVersions.Value(strings.Join(h.EngineVersions, ",")), hs.Modules.Value(strings.Join(h.Modules, ",")),
			hs.Tuning.Value(string(tuning)), hs.LastSeenAt.Value(now), hs.RemoteAddr.Value(remoteAddr(ctx)))
		s.ingest(ctx, hostID, h.Instances, now)
		s.Log.Info("host online", "host", hostID, "instances", len(h.Instances), "engine", h.EngineCurrent)
		go func() {
			s.PushLicense(hostID)
			if s.OnOnline != nil {
				s.OnOnline(hostID)
			}
		}()
	case *agentv1.AgentMessage_Heartbeat:
		hb := b.Heartbeat
		_, _ = hs.WithContext(ctx).Where(hs.ID.Eq(hostID)).UpdateSimple(hs.Status.Value("online"), hs.CPUPercent.Value(hb.CpuPercent),
			hs.MemUsedBytes.Value(hb.MemUsedBytes), hs.DiskFreeBytes.Value(hb.DataDiskFreeBytes), hs.Modules.Value(strings.Join(hb.Modules, ",")), hs.LastSeenAt.Value(now))
		s.ingest(ctx, hostID, hb.Instances, now)
	case *agentv1.AgentMessage_Result:
		sess.mu.Lock()
		ch := sess.waiters[b.Result.CmdId]
		delete(sess.waiters, b.Result.CmdId)
		sess.mu.Unlock()
		if ch != nil {
			ch <- b.Result
		}
	}
}

// StateName 把协议中的实例状态转为库中的字符串。
func StateName(st agentv1.State) string {
	switch st {
	case agentv1.State_STATE_RUNNING:
		return "running"
	case agentv1.State_STATE_STOPPED:
		return "stopped"
	case agentv1.State_STATE_LOADING:
		return "loading"
	case agentv1.State_STATE_FAILED:
		return "failed"
	case agentv1.State_STATE_PENDING_RESTART:
		return "pending_restart"
	}
	return "unknown"
}

// ingest 保存 agent 上报的实例状态（全量）：更新运行字段、判定漂移、按分钟记录指标；库中有而上报中没有的实例记为 missing。
func (s *Server) ingest(ctx context.Context, hostID uint, list []*agentv1.InstanceStatus, now time.Time) {
	q := s.q()
	in, un, pr := q.Instance, q.Unit, q.ParamRevision
	rows, err := in.WithContext(ctx).Where(in.HostID.Eq(hostID)).Find()
	if err != nil {
		return
	}
	byPort := map[uint32]*agentv1.InstanceStatus{}
	for _, st := range list {
		byPort[st.Port] = st
	}
	revs := map[uint]map[string]string{} // unit → 当前参数版本
	revID := map[uint]string{}
	minute := now.Unix() / 60 * 60
	for _, row := range rows {
		st := byPort[row.Port]
		if st == nil {
			_, _ = in.WithContext(ctx).Where(in.ID.Eq(row.ID)).UpdateSimple(in.State.Value("missing"), in.ReportedAt.Value(now))
			continue
		}
		if _, ok := revs[row.UnitID]; !ok {
			revs[row.UnitID] = map[string]string{}
			if u, err := un.WithContext(ctx).Where(un.ID.Eq(row.UnitID)).First(); err == nil && u.RevisionID != "" {
				revID[row.UnitID] = u.RevisionID
				if r, err := pr.WithContext(ctx).Where(pr.ID.Eq(u.RevisionID)).First(); err == nil {
					m := map[string]string{}
					_ = json.Unmarshal([]byte(r.Params), &m)
					revs[row.UnitID] = m
				}
			}
		}
		drifted := false
		if row.Kind == "server" && st.Config != nil && row.AppliedRev == revID[row.UnitID] && st.State != agentv1.State_STATE_PENDING_RESTART {
			// 只有已应用当前版本、且没有等待重启参数的实例才判定漂移（变更进行中的实例不算）
			for k, want := range revs[row.UnitID] {
				if got, ok := st.Config[k]; ok && !paramcat.Equal(k, got, want) {
					drifted = true
					break
				}
			}
		}
		hit := 0.0
		if t := st.KeyspaceHits + st.KeyspaceMisses; t > 0 {
			hit = float64(st.KeyspaceHits) / float64(t)
		}
		raw, _ := protojson.Marshal(st)
		_, _ = in.WithContext(ctx).Where(in.ID.Eq(row.ID)).UpdateSimple(
			in.State.Value(StateName(st.State)), in.Role.Value(st.Role), in.MasterHost.Value(st.MasterHost), in.MasterPort.Value(st.MasterPort),
			in.LinkStatus.Value(st.MasterLinkStatus), in.ClusterNodeID.Value(st.ClusterNodeId), in.ClusterSlots.Value(st.ClusterSlots),
			in.UsedMemory.Value(st.UsedMemory), in.MaxMemory.Value(st.Maxmemory), in.OpsPerSec.Value(st.OpsPerSec), in.Clients.Value(st.ConnectedClients),
			in.Keys.Value(st.Keys), in.HitRate.Value(hit), in.ReplOffset.Value(st.MasterReplOffset), in.ConfigDigest.Value(st.ConfigDigest),
			in.Drifted.Value(drifted), in.PendingRestart.Value(st.State == agentv1.State_STATE_PENDING_RESTART),
			in.Status.Value(string(raw)), in.Error.Value(st.Error), in.ReportedAt.Value(now))
		s.mu.Lock()
		if s.lastMin == nil {
			s.lastMin = map[uint]int64{}
		}
		fresh := s.lastMin[row.ID] != minute
		s.lastMin[row.ID] = minute
		s.mu.Unlock()
		if fresh && st.State != agentv1.State_STATE_STOPPED && st.State != agentv1.State_STATE_FAILED {
			_ = q.MetricMinute.WithContext(ctx).Create(&store.MetricMinute{InstanceID: row.ID, Minute: minute, OpsPerSec: st.OpsPerSec, UsedMemory: st.UsedMemory,
				Clients: st.ConnectedClients, Hits: st.KeyspaceHits, Misses: st.KeyspaceMisses, NetIn: st.TotalNetInputBytes, NetOut: st.TotalNetOutputBytes})
		}
	}
}

// ErrOffline 表示主机当前没有连接。
var ErrOffline = errors.New("host offline")

// Online 报告主机是否在线。
func (s *Server) Online(hostID uint) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sessions[hostID] != nil
}

// Send 签名并下发一条指令，等待结果。
func (s *Server) Send(ctx context.Context, hostID uint, m *agentv1.ServerMessage, timeout time.Duration) (*agentv1.CommandResult, error) {
	s.mu.Lock()
	sess := s.sessions[hostID]
	s.mu.Unlock()
	if sess == nil {
		return nil, ErrOffline
	}
	id := make([]byte, 12)
	_, _ = rand.Read(id)
	m.CmdId = hex.EncodeToString(id)
	if err := cmdsig.Sign(s.CmdKey, m, time.Now()); err != nil {
		return nil, err
	}
	ch := make(chan *agentv1.CommandResult, 1)
	sess.mu.Lock()
	sess.waiters[m.CmdId] = ch
	sess.mu.Unlock()
	defer func() { sess.mu.Lock(); delete(sess.waiters, m.CmdId); sess.mu.Unlock() }()
	select {
	case sess.out <- m:
	case <-time.After(5 * time.Second):
		return nil, errors.New("send queue full")
	}
	t := time.NewTimer(timeout)
	defer t.Stop()
	select {
	case r := <-ch:
		return r, nil
	case <-t.C:
		return nil, context.DeadlineExceeded
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-sess.done:
		return nil, ErrOffline
	}
}

// PushLicense 向主机推送当前 License。
func (s *Server) PushLicense(hostID uint) {
	if raw := s.Lic.Raw(); raw != "" {
		_, _ = s.Send(context.Background(), hostID, &agentv1.ServerMessage{Body: &agentv1.ServerMessage_License{License: &agentv1.LicenseUpdate{Raw: raw}}}, 10*time.Second)
	}
}

// Disconnect 断开主机的连接（吊销时调用）。
func (s *Server) Disconnect(hostID uint) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if sess := s.sessions[hostID]; sess != nil {
		close(sess.done)
		delete(s.sessions, hostID)
	}
}

// ParseAgentVersion 拆分 agent 上报的“版本 linux/架构”。
func ParseAgentVersion(v string) (version, arch string) {
	f := strings.Fields(v)
	if len(f) == 2 && strings.HasPrefix(f[1], "linux/") {
		a := strings.TrimPrefix(f[1], "linux/")
		if a == "amd64" || a == "arm64" || a == "loong64" {
			return f[0], a
		}
	}
	return v, ""
}

// FetchPackage 分片下发版本库中的升级包：只有已验签入库的包；签名随指令下发，agent 使用前再校验。
func (s *Server) FetchPackage(req *agentv1.FetchPackageRequest, stream agentv1.AgentGateway_FetchPackageServer) error {
	if s.standby() {
		return errStandby
	}
	host, err := s.hostFromPeer(stream.Context())
	if err != nil {
		return err
	}
	if req.GetKind() == "backup" {
		q := s.q()
		b, err := q.Backup.WithContext(stream.Context()).Where(q.Backup.ID.Eq(req.GetName()), q.Backup.Location.Neq("local"), q.Backup.Status.Eq("done")).First()
		if err != nil || b.Path == "" {
			return status.Error(codes.NotFound, "backup not found")
		}
		// A host may download only backups belonging to a unit it is already
		// enrolled to run; mTLS identity alone is not enough for cross-tenant
		// backup access.
		if n, err := q.Instance.WithContext(stream.Context()).Where(q.Instance.HostID.Eq(host.ID), q.Instance.UnitID.Eq(b.UnitID)).Count(); err != nil || n == 0 {
			return status.Error(codes.PermissionDenied, "host is not part of the backup unit")
		}
		storage, err := s.backupStore()
		if err != nil {
			return status.Error(codes.FailedPrecondition, err.Error())
		}
		f, err := storage.Open(stream.Context(), b.Path)
		if err != nil {
			return status.Error(codes.NotFound, "backup file is unavailable")
		}
		defer f.Close()
		const chunk = 256 << 10
		buf := make([]byte, chunk)
		var off int64
		for {
			n, readErr := f.Read(buf)
			if n > 0 {
				data := append([]byte(nil), buf[:n]...)
				if err := stream.Send(&agentv1.PackageChunk{Data: data, Offset: off, Last: readErr == io.EOF}); err != nil {
					return err
				}
				off += int64(n)
			}
			if readErr == io.EOF {
				if off == 0 {
					return stream.Send(&agentv1.PackageChunk{Offset: 0, Last: true})
				}
				return nil
			}
			if readErr != nil {
				return readErr
			}
		}
	}
	p := s.q().EnginePackage
	pkg, err := p.WithContext(stream.Context()).Where(p.Name.Eq(req.Name), p.Version.Eq(req.Version), p.Arch.Eq(req.Arch)).First()
	if err != nil {
		return status.Error(codes.NotFound, "package not found in the version library")
	}
	const chunk = 256 << 10
	for off := 0; ; off += chunk {
		end := min(off+chunk, len(pkg.Data))
		if err := stream.Send(&agentv1.PackageChunk{Data: pkg.Data[off:end], Offset: int64(off), Last: end == len(pkg.Data)}); err != nil {
			return err
		}
		if end == len(pkg.Data) {
			return nil
		}
	}
}

// UploadBackup receives a bounded, agent-produced RDB stream.  The first
// chunk carries the logical backup id and port; the final chunk carries the
// whole-file hash.  The server never accepts a caller-provided filesystem path.
func (s *Server) UploadBackup(stream agentv1.AgentGateway_UploadBackupServer) error {
	if s.standby() {
		return errStandby
	}
	host, err := s.hostFromPeer(stream.Context())
	if err != nil {
		return err
	}
	var id string
	var port uint32
	var tmp *os.File
	var path string
	var storage backupstore.Store
	h := sha256.New()
	var size int64
	cleanup := func() {
		if tmp != nil {
			_ = tmp.Close()
		}
		if path != "" {
			_ = os.Remove(path)
		}
	}
	defer func() {
		if tmp != nil {
			_ = tmp.Close()
		}
	}()
	for {
		chunk, err := stream.Recv()
		if err == io.EOF {
			cleanup()
			return status.Error(codes.InvalidArgument, "backup stream ended before the last chunk")
		}
		if err != nil {
			cleanup()
			return err
		}
		if tmp == nil {
			id, port = chunk.GetBackupId(), chunk.GetPort()
			if len(id) == 0 || len(id) > 64 || strings.ContainsAny(id, "/\\\r\n") || port < 1024 || port > 65535 {
				return status.Error(codes.InvalidArgument, "invalid backup metadata")
			}
			storage, err = s.backupStore()
			if err != nil {
				return status.Error(codes.FailedPrecondition, "backup storage is not configured")
			}
			if err := os.MkdirAll(s.BackupDir, 0o700); err != nil {
				return status.Error(codes.Internal, "create backup storage")
			}
			tmp, err = os.CreateTemp(s.BackupDir, ".upload-*.rdb")
			if err != nil {
				return status.Error(codes.Internal, "open backup storage")
			}
			path = tmp.Name()
		}
		if len(chunk.GetData()) > 512<<10 {
			cleanup()
			return status.Error(codes.InvalidArgument, "backup chunk is too large")
		}
		if _, err := tmp.Write(chunk.GetData()); err != nil {
			cleanup()
			return err
		}
		h.Write(chunk.GetData())
		size += int64(len(chunk.GetData()))
		if !chunk.GetLast() {
			continue
		}
		got := hex.EncodeToString(h.Sum(nil))
		if want := chunk.GetSha256(); want != "" && !strings.EqualFold(want, got) {
			cleanup()
			return status.Error(codes.DataLoss, "backup sha256 mismatch")
		}
		if err := tmp.Close(); err != nil {
			cleanup()
			return err
		}
		tmp = nil
		q := s.q()
		in, err := q.Instance.WithContext(stream.Context()).Where(q.Instance.HostID.Eq(host.ID), q.Instance.Port.Eq(port)).First()
		if err != nil {
			cleanup()
			return status.Error(codes.NotFound, "instance for backup not found")
		}
		ref, err := storage.PutFile(stream.Context(), id, path, size, got)
		if err != nil {
			cleanup()
			return status.Error(codes.Internal, "store backup: "+err.Error())
		}
		path = ""
		b := &store.Backup{ID: id, UnitID: in.UnitID, InstanceID: in.ID, Location: storage.Kind(), Path: ref, Size: size, SHA256: got, Status: "done", Trigger: "manual", CreatedBy: "agent:" + host.Name, CreatedAt: time.Now()}
		if err := q.Backup.WithContext(stream.Context()).Save(b); err != nil {
			_ = storage.Delete(stream.Context(), ref)
			return status.Error(codes.Internal, "record backup")
		}
		return stream.SendAndClose(&agentv1.UploadResult{Size: size, Sha256: got})
	}
}

// RemoveBackup deletes the stored bytes for a retention-expired backup. Local
// agent backups are removed through the typed command; remote console/S3
// backups are removed through the configured storage backend.
func (s *Server) RemoveBackup(ctx context.Context, b *store.Backup) error {
	if b == nil {
		return errors.New("backup is nil")
	}
	if b.Location != "local" {
		storage, err := s.backupStore()
		if err != nil {
			return err
		}
		return storage.Delete(ctx, b.Path)
	}
	in, err := s.q().Instance.WithContext(ctx).Where(s.q().Instance.ID.Eq(b.InstanceID)).First()
	if err != nil {
		// The source instance may already have been deleted; the local file is
		// then unreachable through the agent and the metadata can still expire.
		return nil
	}
	r, err := s.Send(ctx, in.HostID, &agentv1.ServerMessage{Body: &agentv1.ServerMessage_Backup{Backup: &agentv1.Backup{Port: in.Port, BackupId: b.ID, Delete: true}}}, time.Minute)
	if err != nil {
		return err
	}
	if r.Status != agentv1.CommandResult_OK {
		return fmt.Errorf("delete backup: %s", r.Message)
	}
	return nil
}
