// Package instance 管理本机 sycache 实例（CacheOS 规格书 6.2–6.4）：创建、启停、参数状态机、复制、状态采集与漂移检测。
//
// 访问实例只经 Unix Socket、以 sycache-agent 管理账号认证（权限见 AgentRules）；
// 需要 root 的动作（systemd 启停、版本切换）只经 sycache-wrapper。
// 管理账号口令由 agent 生成，只存在本机 agent 状态目录（0600），不上报控制台、不写日志。
package instance

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	agentv1 "github.com/xiaoguan521/cacheos/api/gen/agent/v1"
	"github.com/xiaoguan521/cacheos/internal/agent/resp"
	"github.com/xiaoguan521/cacheos/internal/paramcat"
)

// Layout 是本机目录布局（生产值见 DefaultLayout）。
type Layout struct {
	Root     string // /opt/sycache
	RunDir   string // /run/sycache
	LogDir   string // /var/log/sycache
	StateDir string // agent 状态目录下的 instances 子目录
}

// DefaultLayout 返回生产布局。
func DefaultLayout(agentState string) Layout {
	return Layout{Root: "/opt/sycache", RunDir: "/run/sycache", LogDir: "/var/log/sycache", StateDir: filepath.Join(agentState, "instances")}
}

func (l Layout) InstanceDir(port uint32) string {
	return filepath.Join(l.Root, "instances", strconv.Itoa(int(port)))
}
func (l Layout) Socket(port uint32) string {
	return filepath.Join(l.RunDir, fmt.Sprintf("sycache-%d.sock", port))
}
func (l Layout) ACLPath(port uint32) string { return filepath.Join(l.InstanceDir(port), "users.acl") }
func (l Layout) ConfPath(port uint32, kind agentv1.Kind) string {
	if kind == agentv1.Kind_KIND_SENTINEL {
		return filepath.Join(l.InstanceDir(port), "sycache-sentinel.conf")
	}
	return filepath.Join(l.InstanceDir(port), "sycache.conf")
}
func (l Layout) statePath(port uint32) string {
	return filepath.Join(l.StateDir, strconv.Itoa(int(port))+".json")
}

// State 是 agent 为每个实例保存的本机状态。
type State struct {
	Port          uint32            `json:"port"`
	Kind          agentv1.Kind      `json:"kind"`
	Revision      string            `json:"revision"`
	Params        map[string]string `json:"params"`            // 当前参数版本的全部受管参数
	Pending       map[string]string `json:"pending,omitempty"` // 已写入配置、等待重启生效的参数
	AdminPassword string            `json:"admin_password"`
	CreatedAt     time.Time         `json:"created_at"`
}

// Runner 执行 sycache-wrapper（按切片调用，不经过 shell）。
type Runner func(ctx context.Context, args ...string) (string, error)

// WrapperRunner 返回以 prefix（如 sudo -n /usr/libexec/sycache/sycache-wrapper）执行的 Runner。
func WrapperRunner(prefix []string) Runner {
	return func(ctx context.Context, args ...string) (string, error) {
		if len(prefix) == 0 {
			return "", errors.New("wrapper not configured")
		}
		argv := append(append([]string(nil), prefix[1:]...), args...)
		out, err := exec.CommandContext(ctx, prefix[0], argv...).CombinedOutput()
		return strings.TrimSpace(string(out)), err
	}
}

// Error 带有协议错误码（CONFLICT | INVALID | TIMEOUT | NOT_FOUND | UNSUPPORTED | INTERNAL）。
type Error struct {
	Code string
	Msg  string
}

func (e *Error) Error() string { return e.Code + ": " + e.Msg }

func errf(code, format string, a ...any) error {
	return &Error{Code: code, Msg: fmt.Sprintf(format, a...)}
}

// Manager 管理本机全部实例。
type Manager struct {
	L      Layout
	Run    Runner
	Active func(ctx context.Context, port uint32, kind agentv1.Kind) bool // systemd 单元是否在运行；为空时只看套接字

	mu    sync.Mutex
	locks map[uint32]*sync.Mutex
}

func (m *Manager) lock(port uint32) func() {
	m.mu.Lock()
	if m.locks == nil {
		m.locks = map[uint32]*sync.Mutex{}
	}
	l := m.locks[port]
	if l == nil {
		l = &sync.Mutex{}
		m.locks[port] = l
	}
	m.mu.Unlock()
	l.Lock()
	return l.Unlock
}

// Load 读取实例状态。
func (m *Manager) Load(port uint32) (*State, error) {
	b, err := os.ReadFile(m.L.statePath(port))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, errf("NOT_FOUND", "instance %d is not managed on this host", port)
		}
		return nil, err
	}
	var st State
	if err := json.Unmarshal(b, &st); err != nil {
		return nil, err
	}
	return &st, nil
}

func (m *Manager) save(st *State) error {
	if err := os.MkdirAll(m.L.StateDir, 0o700); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(st, "", "  ")
	return writeAtomic(m.L.statePath(st.Port), b, 0o600)
}

// Ports 返回本机受管实例的端口（升序）。
func (m *Manager) Ports() []uint32 {
	ents, _ := os.ReadDir(m.L.StateDir)
	var out []uint32
	for _, e := range ents {
		if n, err := strconv.Atoi(strings.TrimSuffix(e.Name(), ".json")); err == nil && strings.HasSuffix(e.Name(), ".json") {
			out = append(out, uint32(n))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

func writeAtomic(path string, b []byte, mode os.FileMode) error {
	tmp := fmt.Sprintf("%s.tmp-%d", path, os.Getpid())
	if err := os.WriteFile(tmp, b, mode); err != nil {
		return err
	}
	if err := os.Chmod(tmp, mode); err != nil { // 不受 umask 影响
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

func randomSecret() string {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

// Dial 以管理账号连接实例。
func (m *Manager) Dial(ctx context.Context, st *State) (*resp.Conn, error) {
	return resp.Dial(ctx, m.L.Socket(st.Port), UserAgent, st.AdminPassword)
}

// Create 创建实例：校验参数 → 建目录、写配置与账号文件 → 经 wrapper 启动 → 等待就绪。
func (m *Manager) Create(ctx context.Context, c *agentv1.CreateInstance) (*agentv1.InstanceStatus, error) {
	port := c.GetPort()
	if port < 1024 || port > 65535 {
		return nil, errf("INVALID", "port %d out of range", port)
	}
	defer m.lock(port)()
	if _, err := m.Load(port); err == nil {
		return nil, errf("CONFLICT", "instance %d already exists", port)
	}
	if _, err := os.Lstat(m.L.InstanceDir(port)); err == nil {
		return nil, errf("CONFLICT", "directory for port %d already exists", port)
	}
	params, err := paramcat.NormalizeAll(c.GetParams())
	if err != nil {
		return nil, errf("INVALID", "%v", err)
	}
	for _, u := range c.GetAclUsers() {
		if err := ValidateUser(u); err != nil {
			return nil, errf("INVALID", "%v", err)
		}
	}
	if strings.ContainsAny(c.GetMasterauth(), " \t\r\n\"'\\") {
		return nil, errf("INVALID", "masterauth contains forbidden characters")
	}
	// 写入配置文件的每个字段都要校验，防止借换行注入额外配置
	if r := c.GetReplicaOf(); r != nil && r.GetHost() != "" && (!hostRe.MatchString(r.GetHost()) || r.GetPort() == 0 || r.GetPort() > 65535) {
		return nil, errf("INVALID", "invalid master address")
	}
	if n := c.GetSentinelMonitorName(); n != "" {
		if !userRe.MatchString(n) || !hostRe.MatchString(c.GetSentinelMasterHost()) || c.GetSentinelMasterPort() == 0 ||
			c.GetSentinelMasterPort() > 65535 || c.GetSentinelQuorum() == 0 {
			return nil, errf("INVALID", "invalid sentinel monitor settings")
		}
	}
	kind := c.GetKind()
	if kind == agentv1.Kind_KIND_UNSPECIFIED {
		kind = agentv1.Kind_KIND_SERVER
	}
	st := &State{Port: port, Kind: kind, Revision: c.GetRevisionId(), Params: params, AdminPassword: randomSecret(), CreatedAt: time.Now().UTC()}
	dir := m.L.InstanceDir(port)
	if err := os.Mkdir(dir, 0o2770); err != nil {
		return nil, err
	}
	_ = os.Chmod(dir, 0o2770)
	cleanup := func() { _ = os.RemoveAll(dir) }
	var conf string
	if kind == agentv1.Kind_KIND_SENTINEL {
		conf = m.L.SentinelConf(st, c)
	} else {
		conf = m.L.ServerConf(st, c)
		if err := writeAtomic(m.L.ACLPath(port), []byte(ACLFile(st, c.GetMasterauth(), c.GetAclUsers())), 0o660); err != nil {
			cleanup()
			return nil, err
		}
		if t := c.GetTls(); t != nil && len(t.GetCertPem()) > 0 {
			for name, b := range map[string][]byte{"tls.crt": t.GetCertPem(), "tls.key": t.GetKeyPem(), "ca.crt": t.GetCaPem()} {
				if err := writeAtomic(filepath.Join(dir, name), b, 0o640); err != nil {
					cleanup()
					return nil, err
				}
			}
		}
	}
	if err := writeAtomic(m.L.ConfPath(port, kind), []byte(conf), 0o660); err != nil {
		cleanup()
		return nil, err
	}
	if err := m.save(st); err != nil {
		cleanup()
		return nil, err
	}
	if out, err := m.Run(ctx, "start", strconv.Itoa(int(port))); err != nil {
		return nil, errf("INTERNAL", "start failed: %v %s", err, out)
	}
	return m.waitReady(ctx, st, 60*time.Second)
}

// waitReady 等待实例可经管理账号访问且不在加载数据中。
func (m *Manager) waitReady(ctx context.Context, st *State, timeout time.Duration) (*agentv1.InstanceStatus, error) {
	deadline := time.Now().Add(timeout)
	var last error
	for time.Now().Before(deadline) {
		c, err := m.Dial(ctx, st)
		if err == nil {
			_, err = c.Do(ctx, "PING")
			c.Close()
			if err == nil {
				return m.Status(ctx, st.Port), nil
			}
		}
		last = err
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(300 * time.Millisecond):
		}
	}
	return m.Status(ctx, st.Port), errf("TIMEOUT", "instance %d not ready: %v", st.Port, last)
}

// Action 启动 / 停止 / 重启 / 删除。
func (m *Manager) Action(ctx context.Context, port uint32, a agentv1.InstanceAction_Action) (*agentv1.InstanceStatus, error) {
	defer m.lock(port)()
	st, err := m.Load(port)
	if err != nil {
		return nil, err
	}
	p := strconv.Itoa(int(port))
	switch a {
	case agentv1.InstanceAction_START, agentv1.InstanceAction_RESTART:
		verb := map[agentv1.InstanceAction_Action]string{agentv1.InstanceAction_START: "start", agentv1.InstanceAction_RESTART: "restart"}[a]
		if out, err := m.Run(ctx, verb, p); err != nil {
			return nil, errf("INTERNAL", "%s failed: %v %s", verb, err, out)
		}
		s, err := m.waitReady(ctx, st, 120*time.Second)
		if err != nil {
			return s, err
		}
		if len(st.Pending) > 0 {
			if err := m.verifyPending(ctx, st); err != nil {
				return s, err
			}
		}
		return m.Status(ctx, port), nil
	case agentv1.InstanceAction_STOP:
		if out, err := m.Run(ctx, "stop", p); err != nil {
			return nil, errf("INTERNAL", "stop failed: %v %s", err, out)
		}
		return m.Status(ctx, port), nil
	case agentv1.InstanceAction_DELETE:
		if m.running(ctx, st) {
			return nil, errf("CONFLICT", "instance %d is running; stop it first", port)
		}
		// 数据目录移入回收区保留（以 . 开头，wrapper 的端口校验不会匹配，无法再被启动）
		trash := filepath.Join(m.L.Root, "instances", fmt.Sprintf(".trash-%d-%s", port, time.Now().UTC().Format("20060102T150405")))
		if err := os.Rename(m.L.InstanceDir(port), trash); err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		if err := os.Remove(m.L.statePath(port)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		return &agentv1.InstanceStatus{Port: port, Kind: st.Kind, State: agentv1.State_STATE_STOPPED}, nil
	}
	return nil, errf("UNSUPPORTED", "action %v", a)
}

// verifyPending 重启后回读等待生效的参数，全部生效则清空。
func (m *Manager) verifyPending(ctx context.Context, st *State) error {
	c, err := m.Dial(ctx, st)
	if err != nil {
		return err
	}
	defer c.Close()
	got, err := configGet(ctx, c, keys(st.Pending))
	if err != nil {
		return err
	}
	for k, v := range st.Pending {
		if !Equal(k, got[k], v) {
			return errf("INTERNAL", "%s is %q after restart, want %q", k, got[k], v)
		}
	}
	st.Pending = nil
	return m.save(st)
}

func keys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func configGet(ctx context.Context, c *resp.Conn, names []string) (map[string]string, error) {
	out := map[string]string{}
	if len(names) == 0 {
		return out, nil
	}
	v, err := c.Do(ctx, append([]string{"CONFIG", "GET"}, names...)...)
	if err != nil {
		return nil, err
	}
	s := v.Strings()
	for i := 0; i+1 < len(s); i += 2 {
		out[s[i]] = s[i+1]
	}
	return out, nil
}

// ApplyResult 是一次参数变更的结果。
type ApplyResult struct {
	Status  agentv1.CommandResult_Status
	Applied []string // 已热生效
	Pending []string // 已写入配置、等待重启
	Message string
}

// Apply 参数变更状态机（规格书 6.3）：
//
//	乐观锁（parent_revision）→ 参数目录校验 → 可热改的一次 CONFIG SET（服务端原子）→ 回读比对
//	→ CONFIG REWRITE → 需重启的写入配置文件并记为 pending。
//
// 任何一步失败都把已改的参数 CONFIG SET 回原值并 CONFIG REWRITE，返回 ROLLED_BACK；没有改动则返回 FAILED。
func (m *Manager) Apply(ctx context.Context, req *agentv1.ApplyParams) (*ApplyResult, error) {
	defer m.lock(req.GetPort())()
	st, err := m.Load(req.GetPort())
	if err != nil {
		return nil, err
	}
	if st.Kind != agentv1.Kind_KIND_SERVER {
		return nil, errf("UNSUPPORTED", "parameters of sentinel instances are managed by SentinelOp")
	}
	if st.Revision != req.GetParentRevisionId() {
		return nil, errf("CONFLICT", "instance is at revision %q, change was based on %q", st.Revision, req.GetParentRevisionId())
	}
	want, err := paramcat.NormalizeAll(req.GetParams())
	if err != nil {
		return nil, errf("INVALID", "%v", err)
	}
	var hot, restart []string
	for k := range want {
		if p, _ := paramcat.Lookup(k); p.Restart {
			restart = append(restart, k)
		} else {
			hot = append(hot, k)
		}
	}
	sort.Strings(hot)
	sort.Strings(restart)
	// 复核控制台的判定：控制台认为可热改、agent 认为需重启时拒绝，避免控制台错误地跳过滚动重启
	declared := map[string]bool{}
	for _, k := range req.GetRestartRequired() {
		declared[k] = true
	}
	for _, k := range restart {
		if !declared[k] {
			return nil, errf("INVALID", "%s requires a restart but the change does not declare it", k)
		}
	}

	c, err := m.Dial(ctx, st)
	if err != nil {
		return nil, errf("INTERNAL", "connect: %v", err)
	}
	defer c.Close()

	old, err := configGet(ctx, c, hot)
	if err != nil {
		return nil, errf("INTERNAL", "config get: %v", err)
	}
	res := &ApplyResult{Status: agentv1.CommandResult_OK}
	if len(hot) > 0 {
		args := []string{"CONFIG", "SET"}
		for _, k := range hot {
			args = append(args, k, want[k])
		}
		if _, err := c.Do(ctx, args...); err != nil { // CONFIG SET 多参数是原子的：失败时一个都没改
			return &ApplyResult{Status: agentv1.CommandResult_FAILED, Message: err.Error()}, nil
		}
		got, err := configGet(ctx, c, hot)
		if err != nil {
			return m.rollback(ctx, c, old, "read back: "+err.Error()), nil
		}
		for _, k := range hot {
			if !Equal(k, got[k], want[k]) {
				return m.rollback(ctx, c, old, fmt.Sprintf("%s reads back as %q, want %q", k, got[k], want[k])), nil
			}
		}
	}
	if _, err := c.Do(ctx, "CONFIG", "REWRITE"); err != nil {
		return m.rollback(ctx, c, old, "config rewrite: "+err.Error()), nil
	}
	pending := map[string]string{}
	for k, v := range st.Pending {
		pending[k] = v
	}
	for _, k := range restart {
		pending[k] = want[k]
	}
	// CONFIG REWRITE 会把需重启参数的旧运行值写回配置文件，所以每次 REWRITE 之后重新写入全部 pending
	if len(pending) > 0 {
		if err := m.writePending(st, pending); err != nil {
			return m.rollback(ctx, c, old, "write config: "+err.Error()), nil
		}
	}
	for k, v := range want {
		st.Params[k] = v
	}
	st.Revision = req.GetRevisionId()
	st.Pending = pending
	if len(pending) == 0 {
		st.Pending = nil
	}
	if err := m.save(st); err != nil {
		return nil, err
	}
	res.Applied = hot
	res.Pending = keys(pending)
	if len(pending) > 0 {
		res.Status = agentv1.CommandResult_PENDING_RESTART
	}
	return res, nil
}

func (m *Manager) writePending(st *State, pending map[string]string) error {
	path := m.L.ConfPath(st.Port, st.Kind)
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return writeAtomic(path, []byte(SetConfLines(string(b), pending)), 0o660)
}

func (m *Manager) rollback(ctx context.Context, c *resp.Conn, old map[string]string, why string) *ApplyResult {
	if len(old) == 0 {
		return &ApplyResult{Status: agentv1.CommandResult_FAILED, Message: why}
	}
	args := []string{"CONFIG", "SET"}
	for _, k := range keys(old) {
		args = append(args, k, old[k])
	}
	if _, err := c.Do(ctx, args...); err != nil {
		return &ApplyResult{Status: agentv1.CommandResult_FAILED, Message: why + "; rollback failed: " + err.Error()}
	}
	_, _ = c.Do(ctx, "CONFIG", "REWRITE")
	return &ApplyResult{Status: agentv1.CommandResult_ROLLED_BACK, Message: why}
}

// SyncACL 全量同步业务账号：计算差异后 ACL SETUSER / DELUSER，再 ACL SAVE。
func (m *Manager) SyncACL(ctx context.Context, req *agentv1.SyncAcl) (map[string]string, error) {
	defer m.lock(req.GetPort())()
	st, err := m.Load(req.GetPort())
	if err != nil {
		return nil, err
	}
	for _, u := range req.GetUsers() {
		if err := ValidateUser(u); err != nil {
			return nil, errf("INVALID", "%v", err)
		}
	}
	c, err := m.Dial(ctx, st)
	if err != nil {
		return nil, errf("INTERNAL", "connect: %v", err)
	}
	defer c.Close()
	v, err := c.Do(ctx, "ACL", "USERS")
	if err != nil {
		return nil, err
	}
	want := map[string]bool{}
	for _, u := range req.GetUsers() {
		want[u.GetName()] = true
		if _, err := c.Do(ctx, append([]string{"ACL", "SETUSER", u.GetName()}, UserArgs(u)...)...); err != nil {
			return nil, errf("INVALID", "user %s: %v", u.GetName(), err)
		}
	}
	removed := 0
	for _, name := range v.Strings() {
		if name == "default" || strings.HasPrefix(name, "sycache-") || want[name] {
			continue
		}
		if _, err := c.Do(ctx, "ACL", "DELUSER", name); err != nil {
			return nil, err
		}
		removed++
	}
	if _, err := c.Do(ctx, "ACL", "SAVE"); err != nil {
		return nil, err
	}
	return map[string]string{"users": strconv.Itoa(len(want)), "removed": strconv.Itoa(removed)}, nil
}

// SetReplication 调整复制关系：REPLICAOF host port 或 REPLICAOF NO ONE，之后 CONFIG REWRITE 持久化。
func (m *Manager) SetReplication(ctx context.Context, req *agentv1.SetReplication) (*agentv1.InstanceStatus, error) {
	defer m.lock(req.GetPort())()
	st, err := m.Load(req.GetPort())
	if err != nil {
		return nil, err
	}
	c, err := m.Dial(ctx, st)
	if err != nil {
		return nil, errf("INTERNAL", "connect: %v", err)
	}
	defer c.Close()
	if a := req.GetMasterauth(); a != "" {
		if strings.ContainsAny(a, " \t\r\n\"'\\") {
			return nil, errf("INVALID", "masterauth contains forbidden characters")
		}
		if _, err := c.Do(ctx, "CONFIG", "SET", "masteruser", UserRepl, "masterauth", a); err != nil {
			return nil, err
		}
	}
	if r := req.GetReplicaOf(); r != nil && r.GetHost() != "" {
		if !hostRe.MatchString(r.GetHost()) || r.GetPort() == 0 || r.GetPort() > 65535 {
			return nil, errf("INVALID", "invalid master address")
		}
		_, err = c.Do(ctx, "REPLICAOF", r.GetHost(), strconv.Itoa(int(r.GetPort())))
	} else {
		_, err = c.Do(ctx, "REPLICAOF", "NO", "ONE")
	}
	if err != nil {
		return nil, err
	}
	if _, err := c.Do(ctx, "CONFIG", "REWRITE"); err != nil {
		return nil, err
	}
	if len(st.Pending) > 0 {
		_ = m.writePending(st, st.Pending)
	}
	return m.Status(ctx, st.Port), nil
}

// Failover 主从切换（规格书 7 节 M6）。
func (m *Manager) Failover(ctx context.Context, req *agentv1.Failover) (*agentv1.InstanceStatus, error) {
	defer m.lock(req.GetPort())()
	st, err := m.Load(req.GetPort())
	if err != nil {
		return nil, err
	}
	c, err := m.Dial(ctx, st)
	if err != nil {
		return nil, errf("INTERNAL", "connect: %v", err)
	}
	defer c.Close()
	timeout := req.GetTimeoutMs()
	if timeout == 0 {
		timeout = 10000
	}
	switch req.GetMode() {
	case agentv1.Failover_PROMOTE:
		if _, err := c.Do(ctx, "REPLICAOF", "NO", "ONE"); err != nil {
			return nil, err
		}
	case agentv1.Failover_COORDINATED:
		// FAILOVER 在主节点上执行：暂停写入，等从节点追平后互换角色（不丢数据）；超时自动放弃
		if _, err := c.Do(ctx, "FAILOVER", "TIMEOUT", strconv.Itoa(int(timeout))); err != nil {
			return nil, err
		}
		deadline := time.Now().Add(time.Duration(timeout)*time.Millisecond + 5*time.Second)
		for time.Now().Before(deadline) {
			v, err := c.Do(ctx, "INFO", "replication")
			if err == nil {
				info := resp.ParseInfo(v.Text())
				if info["role"] == "slave" {
					break
				}
				if info["master_failover_state"] == "no-failover" && info["role"] == "master" {
					return nil, errf("TIMEOUT", "failover aborted: replica did not catch up in time")
				}
			}
			time.Sleep(200 * time.Millisecond)
		}
	case agentv1.Failover_CLUSTER:
		_, err = c.Do(ctx, "CLUSTER", "FAILOVER")
	case agentv1.Failover_CLUSTER_FORCE:
		_, err = c.Do(ctx, "CLUSTER", "FAILOVER", "FORCE")
	case agentv1.Failover_SENTINEL:
		if !userRe.MatchString(req.GetSentinelMasterName()) {
			return nil, errf("INVALID", "invalid master name")
		}
		_, err = c.Do(ctx, "SENTINEL", "FAILOVER", req.GetSentinelMasterName())
	default:
		return nil, errf("UNSUPPORTED", "failover mode %v", req.GetMode())
	}
	if err != nil {
		return nil, err
	}
	if st.Kind == agentv1.Kind_KIND_SERVER {
		_, _ = c.Do(ctx, "CONFIG", "REWRITE")
		if len(st.Pending) > 0 {
			_ = m.writePending(st, st.Pending)
		}
	}
	return m.Status(ctx, st.Port), nil
}

func (m *Manager) running(ctx context.Context, st *State) bool {
	if m.Active != nil {
		return m.Active(ctx, st.Port, st.Kind)
	}
	_, err := os.Stat(m.L.Socket(st.Port))
	return err == nil
}

// SystemdActive 用 systemctl is-active 判断单元状态（无需 root）。
func SystemdActive(ctx context.Context, port uint32, kind agentv1.Kind) bool {
	unit := fmt.Sprintf("sycache@%d.service", port)
	if kind == agentv1.Kind_KIND_SENTINEL {
		unit = fmt.Sprintf("sycache-sentinel@%d.service", port)
	}
	return exec.CommandContext(ctx, "systemctl", "is-active", "--quiet", unit).Run() == nil
}
