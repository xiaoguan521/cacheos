// Package instance 管理本机 sycache 实例（CacheOS 规格书 6.2–6.4）：创建、启停、参数状态机、复制、状态采集与漂移检测。
//
// 访问实例只经 Unix Socket、以 sycache-agent 管理账号认证（权限见 AgentRules）；
// 需要 root 的动作（systemd 启停、版本切换）只经 sycache-wrapper。
// 管理账号口令由 agent 生成，只存在本机 agent 状态目录（0600），不上报控制台、不写日志。
package instance

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"

	agentv1 "github.com/xiaoguan521/cacheos/api/gen/agent/v1"
	"github.com/xiaoguan521/cacheos/internal/agent/collect"
	"github.com/xiaoguan521/cacheos/internal/agent/resp"
	"github.com/xiaoguan521/cacheos/internal/paramcat"
	"github.com/xiaoguan521/cacheos/internal/webcli"
)

// Layout 是本机目录布局（生产值见 DefaultLayout）。
type Layout struct {
	Root     string // /home/atwasoft/sycache
	RunDir   string // /run/sycache
	LogDir   string // /var/log/sycache
	StateDir string // agent 状态目录下的 instances 子目录
}

// DefaultLayout 返回生产布局。
func DefaultLayout(agentState string) Layout {
	return Layout{Root: "/home/atwasoft/sycache", RunDir: "/run/sycache", LogDir: "/var/log/sycache", StateDir: filepath.Join(agentState, "instances")}
}

func (l Layout) InstanceDir(port uint32) string {
	return filepath.Join(l.Root, "instances", strconv.Itoa(int(port)))
}
func (l Layout) Socket(port uint32) string {
	return filepath.Join(l.RunDir, fmt.Sprintf("sycache-%d.sock", port))
}
func (l Layout) ACLPath(port uint32) string { return filepath.Join(l.InstanceDir(port), "users.acl") }
func (l Layout) ManifestPath(port uint32) string {
	return filepath.Join(l.InstanceDir(port), "MANIFEST")
}
func (l Layout) LockPath(port uint32) string {
	return filepath.Join(filepath.Dir(l.RunDir), "sycache-agent", fmt.Sprintf("%d.lock", port))
}
func (l Layout) BackupPath(id string) string {
	return filepath.Join(l.StateDir, "backups", id+".rdb")
}
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
	Port           uint32            `json:"port"`
	Kind           agentv1.Kind      `json:"kind"`
	BindAddress    string            `json:"bind_address"`
	Revision       string            `json:"revision"`
	Params         map[string]string `json:"params"`            // 当前参数版本的全部受管参数
	Pending        map[string]string `json:"pending,omitempty"` // 已写入配置、等待重启生效的参数
	ManifestSHA256 string            `json:"manifest_sha256"`
	AdminPassword  string            `json:"admin_password"`
	ReadPassword   string            `json:"read_password,omitempty"`
	WebCLIPassword string            `json:"webcli_password,omitempty"` // Web 命令行专用账号口令（只存本机）
	Modules        []string          `json:"modules,omitempty"`         // 新建时选择的扩展模块（ADR 0002），之后不可变
	CreatedAt      time.Time         `json:"created_at"`
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
	// The in-memory mutex protects concurrent handlers in this process.  The
	// lock file is the second half of the guard: two agent processes must not
	// rewrite the same config or move the same instance directory.
	_ = os.MkdirAll(filepath.Dir(m.L.LockPath(port)), 0o700)
	f, err := os.OpenFile(m.L.LockPath(port), os.O_CREATE|os.O_RDWR, 0o600)
	if err == nil {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_EX)
	}
	return func() {
		if f != nil {
			_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
			_ = f.Close()
		}
		l.Unlock()
	}
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

type manifest struct {
	Revision     string `json:"revision"`
	Kind         string `json:"kind"`
	ConfigSHA256 string `json:"config_sha256"`
	ParamsSHA256 string `json:"params_sha256"`
}

func configSHA256(conf []byte) string {
	h := sha256.Sum256(conf)
	return hex.EncodeToString(h[:])
}

func (m *Manager) writeManifest(st *State, conf []byte) error {
	st.ManifestSHA256 = configSHA256(conf)
	b, _ := json.MarshalIndent(manifest{Revision: st.Revision, Kind: st.Kind.String(), ConfigSHA256: st.ManifestSHA256, ParamsSHA256: Digest(st.Params)}, "", "  ")
	b = append(b, '\n')
	return writeAtomic(m.L.ManifestPath(st.Port), b, 0o600)
}

func validBindAddress(v string) bool {
	v = strings.TrimSpace(v)
	if v == "" || v == "*" || v == "0.0.0.0" || v == "::" || strings.ContainsAny(v, " \t\r\n\"'\\") {
		return false
	}
	if net.ParseIP(v) != nil {
		return true
	}
	return hostRe.MatchString(v)
}

// Dial 以管理账号连接实例。
func (m *Manager) Dial(ctx context.Context, st *State) (*resp.Conn, error) {
	return resp.Dial(ctx, m.L.Socket(st.Port), UserAgent, st.AdminPassword)
}

func (m *Manager) dialReader(ctx context.Context, st *State) (*resp.Conn, error) {
	if st.ReadPassword == "" {
		return nil, errf("UNSUPPORTED", "instance has no separate read session")
	}
	return resp.Dial(ctx, m.L.Socket(st.Port), UserReader, st.ReadPassword)
}

// Create 创建实例：校验参数 → 建目录、写配置与账号文件 → 经 wrapper 启动 → 等待就绪。
func (m *Manager) Create(ctx context.Context, c *agentv1.CreateInstance) (*agentv1.InstanceStatus, error) {
	port := c.GetPort()
	if port < 1024 || port > 65535 {
		return nil, errf("INVALID", "port %d out of range", port)
	}
	if c.GetClusterEnabled() && port > 55535 {
		return nil, errf("INVALID", "cluster instance port %d is too high; cluster bus port must stay within 65535", port)
	}
	defer m.lock(port)()
	if _, err := m.Load(port); err == nil {
		return nil, errf("CONFLICT", "instance %d already exists", port)
	}
	if _, err := os.Lstat(m.L.InstanceDir(port)); err == nil {
		return nil, errf("CONFLICT", "directory for port %d already exists", port)
	}
	rawParams := map[string]string{}
	for k, v := range c.GetParams() {
		rawParams[k] = v
	}
	bind := strings.TrimSpace(c.GetBindAddress())
	// Accept the temporary field during a rolling upgrade from a pre-1.0.0
	// console, but all new commands use the typed protobuf field above.
	if bind == "" {
		bind = strings.TrimSpace(rawParams["__cacheos_bind"])
	}
	delete(rawParams, "__cacheos_bind")
	if bind == "" {
		bind = "127.0.0.1"
	}
	if !validBindAddress(bind) {
		return nil, errf("INVALID", "bind address must be a concrete internal host or IP, got %q", bind)
	}
	params, err := paramcat.NormalizeAll(rawParams)
	if err != nil {
		return nil, errf("INVALID", "%v", err)
	}
	if _, declared := params["maxmemory"]; declared {
		maxMemory, err := MaxMemoryBytes(params)
		if err != nil {
			return nil, errf("INVALID", "%v", err)
		}
		if err := ValidateMaxMemory(maxMemory, 0); err != nil {
			return nil, errf("INVALID", "%v", err)
		}
		if ParamsNeedPersistence(params) {
			free := collect.DiskFree(filepath.Join(m.L.Root, "instances"))
			if err := ValidatePersistenceDisk(free, maxMemory, 0); err != nil {
				return nil, errf("INVALID", "%v", err)
			}
		}
	} else if ParamsNeedPersistence(params) {
		return nil, errf("INVALID", "maxmemory is required for persistence-enabled instances")
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
	if err := ValidateModules(c.GetModules(), kind, c.GetClusterEnabled()); err != nil {
		return nil, errf("INVALID", "%v", err)
	}
	for _, name := range c.GetModules() { // 先确认模块文件已随版本安装，缺文件时不创建实例
		if fi, err := os.Stat(m.L.ModulePath(name)); err != nil || !fi.Mode().IsRegular() {
			return nil, errf("INVALID", "module %s is not installed on this host", name)
		}
	}
	st := &State{Port: port, Kind: kind, BindAddress: bind, Modules: c.GetModules(), Revision: c.GetRevisionId(), Params: params, AdminPassword: randomSecret(), ReadPassword: randomSecret(), WebCLIPassword: randomSecret(), CreatedAt: time.Now().UTC()}
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
	if err := m.writeManifest(st, []byte(conf)); err != nil {
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
	effective := make(map[string]string, len(st.Params)+len(want))
	for k, v := range st.Params {
		effective[k] = v
	}
	for k, v := range want {
		effective[k] = v
	}
	maxBytes, maxErr := MaxMemoryBytes(effective)
	if maxErr != nil && (ParamsNeedPersistence(effective) || want["maxmemory"] != "") {
		return nil, errf("INVALID", "%v", maxErr)
	}
	if maxErr == nil && (maxBytes != 0 && ParamsNeedPersistence(effective) || want["maxmemory"] != "") {
		info, err := c.Do(ctx, "INFO", "memory")
		if err != nil {
			return nil, errf("INTERNAL", "read used_memory: %v", err)
		}
		usedMemory := atou(resp.ParseInfo(info.Text())["used_memory"])
		if want["maxmemory"] != "" {
			if err := ValidateMaxMemory(maxBytes, usedMemory); err != nil {
				return nil, errf("INVALID", "%v", err)
			}
		}
		if ParamsNeedPersistence(effective) {
			free := collect.DiskFree(filepath.Join(m.L.Root, "instances"))
			if err := ValidatePersistenceDisk(free, maxBytes, usedMemory); err != nil {
				return nil, errf("INVALID", "%v", err)
			}
		}
	}

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
	if conf, err := os.ReadFile(m.L.ConfPath(st.Port, st.Kind)); err == nil {
		if err := m.writeManifest(st, conf); err != nil {
			return nil, err
		}
	} else {
		return nil, err
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

// ClusterOp executes one allow-listed cluster primitive.  The console keeps
// the topology state machine; the agent only exposes typed, single-step
// commands and never accepts an arbitrary command string.
//
// SECURITY: req.PeerPassword 只用于 MIGRATE AUTH2，不得写入任何日志或返回值。
// 调用方 (agent.go) 只记录 port + error，不记录完整 proto；debug 级别亦然。
func (m *Manager) ClusterOp(ctx context.Context, req *agentv1.ClusterOp) ([]byte, *agentv1.InstanceStatus, error) {
	defer m.lock(req.GetPort())()
	st, err := m.Load(req.GetPort())
	if err != nil {
		return nil, nil, err
	}
	if st.Kind != agentv1.Kind_KIND_SERVER {
		return nil, nil, errf("UNSUPPORTED", "cluster operations require a server instance")
	}
	c, err := m.Dial(ctx, st)
	if err != nil {
		return nil, nil, errf("INTERNAL", "connect: %v", err)
	}
	defer c.Close()
	var args []string
	switch req.GetOp() {
	case agentv1.ClusterOp_MEET:
		if !validBindAddress(req.GetPeerHost()) || req.GetPeerPort() == 0 || req.GetPeerPort() > 65535 {
			return nil, nil, errf("INVALID", "invalid cluster peer address")
		}
		args = []string{"CLUSTER", "MEET", req.GetPeerHost(), strconv.Itoa(int(req.GetPeerPort()))}
	case agentv1.ClusterOp_ADD_SLOTS:
		for _, r := range req.GetSlots() {
			if r.GetStart() > r.GetEnd() || r.GetEnd() > 16383 {
				return nil, nil, errf("INVALID", "invalid slot range %d-%d", r.GetStart(), r.GetEnd())
			}
			for n := r.GetStart(); n <= r.GetEnd(); n++ {
				args = append(args, strconv.Itoa(int(n)))
			}
		}
		if len(args) == 0 {
			return nil, nil, errf("INVALID", "slot range is required")
		}
		args = append([]string{"CLUSTER", "ADDSLOTS"}, args...)
	case agentv1.ClusterOp_REPLICATE:
		if !clusterNodeIDRe.MatchString(req.GetNodeId()) {
			return nil, nil, errf("INVALID", "invalid cluster node id")
		}
		args = []string{"CLUSTER", "REPLICATE", req.GetNodeId()}
	case agentv1.ClusterOp_SETSLOT_IMPORTING, agentv1.ClusterOp_SETSLOT_MIGRATING, agentv1.ClusterOp_SETSLOT_NODE:
		if req.GetSlot() > 16383 {
			return nil, nil, errf("INVALID", "invalid slot %d", req.GetSlot())
		}
		op := map[agentv1.ClusterOp_Op]string{agentv1.ClusterOp_SETSLOT_IMPORTING: "IMPORTING", agentv1.ClusterOp_SETSLOT_MIGRATING: "MIGRATING", agentv1.ClusterOp_SETSLOT_NODE: "NODE"}[req.GetOp()]
		args = []string{"CLUSTER", "SETSLOT", strconv.Itoa(int(req.GetSlot())), op}
		if op != "" && op != "MIGRATING" && op != "IMPORTING" {
			if !clusterNodeIDRe.MatchString(req.GetNodeId()) {
				return nil, nil, errf("INVALID", "invalid cluster node id")
			}
			args = append(args, req.GetNodeId())
		} else if op == "MIGRATING" || op == "IMPORTING" {
			if !clusterNodeIDRe.MatchString(req.GetNodeId()) {
				return nil, nil, errf("INVALID", "invalid cluster node id")
			}
			args = append(args, req.GetNodeId())
		}
	case agentv1.ClusterOp_MIGRATE_SLOT:
		if !validBindAddress(req.GetPeerHost()) || req.GetPeerPort() == 0 || req.GetPeerPort() > 65535 || req.GetSlot() > 16383 {
			return nil, nil, errf("INVALID", "invalid migration target")
		}
		if req.GetPeerUser() != "" || req.GetPeerPassword() != "" {
			if req.GetPeerUser() != UserCluster || req.GetPeerPassword() == "" || strings.ContainsAny(req.GetPeerPassword(), "\r\n") {
				return nil, nil, errf("INVALID", "invalid migration target credentials")
			}
		}
		batch := req.GetBatch()
		if batch == 0 || batch > 10000 {
			batch = 100
		}
		timeout := req.GetTimeoutMs()
		if timeout == 0 {
			timeout = 10000
		}
		if timeout > 600000 {
			return nil, nil, errf("INVALID", "migration timeout is too large")
		}
		v, err := c.Do(ctx, "CLUSTER", "GETKEYSINSLOT", strconv.Itoa(int(req.GetSlot())), strconv.Itoa(int(batch)))
		if err != nil {
			return nil, nil, err
		}
		keys := v.Strings()
		if len(keys) > 0 {
			args = []string{"MIGRATE", req.GetPeerHost(), strconv.Itoa(int(req.GetPeerPort())), "", "0", strconv.Itoa(int(timeout)), "REPLACE"}
			if req.GetPeerUser() != "" {
				args = append(args, "AUTH2", req.GetPeerUser(), req.GetPeerPassword())
			}
			args = append(args, "KEYS")
			args = append(args, keys...)
			if _, err := c.Do(ctx, args...); err != nil {
				return nil, nil, err
			}
		}
		return []byte(fmt.Sprintf(`{"slot":%d,"migrated":%d}`, req.GetSlot(), len(keys))), m.Status(ctx, st.Port), nil
	case agentv1.ClusterOp_FORGET:
		if !clusterNodeIDRe.MatchString(req.GetNodeId()) {
			return nil, nil, errf("INVALID", "invalid cluster node id")
		}
		args = []string{"CLUSTER", "FORGET", req.GetNodeId()}
	case agentv1.ClusterOp_NODES:
		v, err := c.Do(ctx, "CLUSTER", "NODES")
		if err != nil {
			return nil, nil, err
		}
		return []byte(v.Text()), m.Status(ctx, st.Port), nil
	case agentv1.ClusterOp_RESET:
		args = []string{"CLUSTER", "RESET", "SOFT"}
	default:
		return nil, nil, errf("UNSUPPORTED", "cluster operation %v", req.GetOp())
	}
	if _, err := c.Do(ctx, args...); err != nil {
		return nil, nil, err
	}
	return nil, m.Status(ctx, st.Port), nil
}

var clusterNodeIDRe = regexp.MustCompile(`^[0-9a-f]{40}$`)

// SentinelOp exposes the small command set required to manage a monitored
// master.  Option names are fixed so arbitrary Sentinel configuration cannot
// be injected through the control plane.
func (m *Manager) SentinelOp(ctx context.Context, req *agentv1.SentinelOp) (*agentv1.InstanceStatus, error) {
	defer m.lock(req.GetPort())()
	st, err := m.Load(req.GetPort())
	if err != nil {
		return nil, err
	}
	if st.Kind != agentv1.Kind_KIND_SENTINEL {
		return nil, errf("UNSUPPORTED", "sentinel operations require a sentinel instance")
	}
	c, err := m.Dial(ctx, st)
	if err != nil {
		return nil, errf("INTERNAL", "connect: %v", err)
	}
	defer c.Close()
	name := req.GetMasterName()
	if !userRe.MatchString(name) {
		return nil, errf("INVALID", "invalid sentinel master name")
	}
	if req.GetAuthPass() != "" && strings.ContainsAny(req.GetAuthPass(), " \t\r\n\"'\\") {
		return nil, errf("INVALID", "auth_pass contains forbidden characters")
	}
	var args []string
	switch req.GetOp() {
	case agentv1.SentinelOp_MONITOR:
		if !validBindAddress(req.GetMasterHost()) || req.GetMasterPort() == 0 || req.GetMasterPort() > 65535 || req.GetQuorum() == 0 {
			return nil, errf("INVALID", "invalid sentinel monitor")
		}
		args = []string{"SENTINEL", "MONITOR", name, req.GetMasterHost(), strconv.Itoa(int(req.GetMasterPort())), strconv.Itoa(int(req.GetQuorum()))}
	case agentv1.SentinelOp_REMOVE:
		args = []string{"SENTINEL", "REMOVE", name}
	case agentv1.SentinelOp_SET:
		allowed := map[string]bool{"down-after-milliseconds": true, "failover-timeout": true, "parallel-syncs": true, "auth-user": true, "auth-pass": true}
		keys := make([]string, 0, len(req.GetOptions()))
		for k := range req.GetOptions() {
			if !allowed[k] || strings.ContainsAny(req.GetOptions()[k], "\r\n") {
				return nil, errf("INVALID", "sentinel option %q is not allowed", k)
			}
			keys = append(keys, k)
		}
		sort.Strings(keys)
		if len(keys) == 0 {
			return nil, errf("INVALID", "sentinel options are required")
		}
		args = []string{"SENTINEL", "SET", name}
		for _, k := range keys {
			args = append(args, k, req.GetOptions()[k])
		}
	case agentv1.SentinelOp_RESET:
		args = []string{"SENTINEL", "RESET", name}
	default:
		return nil, errf("UNSUPPORTED", "sentinel operation %v", req.GetOp())
	}
	if _, err := c.Do(ctx, args...); err != nil {
		return nil, err
	}
	return m.Status(ctx, st.Port), nil
}

// Diagnose returns bounded, metadata-only diagnostic JSON.  Values are never
// read; BIGKEYS and key browser use TYPE/TTL/MEMORY USAGE only.
func (m *Manager) Diagnose(ctx context.Context, req *agentv1.Diagnose) ([]byte, error) {
	defer m.lock(req.GetPort())()
	st, err := m.Load(req.GetPort())
	if err != nil {
		return nil, err
	}
	if req.GetWhat() == agentv1.Diagnose_RDB_ANALYZE {
		payload, err := marshalRDBReport(m.rdbPath(st), time.Now())
		if err != nil {
			return nil, errf("INTERNAL", "RDB analyze: %v", err)
		}
		return payload, nil
	}
	c, err := m.Dial(ctx, st)
	if err != nil {
		return nil, errf("INTERNAL", "connect: %v", err)
	}
	defer c.Close()
	n := req.GetSample()
	if n == 0 || n > 1000 {
		n = 100
	}
	var report any
	// extra 是报告之外的附加字段（慢日志聚合、内存建议），旧字段 report 的形状保持不变。
	extra := map[string]any{}
	switch req.GetWhat() {
	case agentv1.Diagnose_SLOWLOG:
		v, err := c.Do(ctx, "SLOWLOG", "GET", strconv.Itoa(slowlogDiagSample))
		if err != nil {
			return nil, err
		}
		report = slowlog(v)
		extra["aggregate"] = aggregateSlowlog(slowlogRecords(v))
	case agentv1.Diagnose_LATENCY:
		v, err := c.Do(ctx, "LATENCY", "LATEST")
		if err != nil {
			return nil, err
		}
		report = v
	case agentv1.Diagnose_MEMORY:
		v, err := c.Do(ctx, "INFO", "memory")
		if err != nil {
			return nil, err
		}
		info := resp.ParseInfo(v.Text())
		report = info
		extra["advice"] = MemoryAdvice(info)
	case agentv1.Diagnose_CLIENTS:
		v, err := c.Do(ctx, "CLIENT", "LIST")
		if err != nil {
			return nil, err
		}
		lines := strings.Split(strings.TrimSpace(v.Text()), "\n")
		if len(lines) > int(n) {
			lines = lines[:n]
		}
		report = lines
	case agentv1.Diagnose_BIGKEYS:
		return m.scanMetadata(ctx, c, "0", "*", n)
	default:
		return nil, errf("UNSUPPORTED", "diagnostic %v", req.GetWhat())
	}
	payload := map[string]any{"kind": req.GetWhat().String(), "sample": n, "report": report}
	for k, v := range extra {
		payload[k] = v
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	return b, nil
}

// Web 命令行的时间与返回大小上限（CLAUDE.md「每条命令有超时与返回大小上限，超限截断并标明」）。
const (
	webCLITimeout    = 5 * time.Second
	webCLIMaxPayload = 256 << 10
	webCLISlack      = 2 << 10 // 为 JSON 外壳与结构开销预留
)

// ExecCommand 执行 Web 命令行的一条数据命令（CLAUDE.md「agent 与安全」中唯一的例外）。
//
// 流程：实例须由本 agent 管理 → argv 校验（与控制台共用 webcli.Check，拒绝清单在发送前再查一次）
// → 以专用账号 sycache-webcli 经 Unix Socket 连接 → 发送 → 读回复 → 序列化。连接用完即关，不跨请求复用。
// 引擎返回的错误（含 NOPERM）作为正常结果（reply.type = error）回传，不视为 agent 故障。
// 返回值第二项为回复类型，供调用方记录结果状态；参数与回复内容不得写入日志。
func (m *Manager) ExecCommand(ctx context.Context, req *agentv1.ExecCommand) ([]byte, string, error) {
	port := req.GetPort()
	defer m.lock(port)()
	st, err := m.Load(port)
	if err != nil {
		return nil, "", err
	}
	argv := req.GetArgv()
	if _, err := webcli.Check(argv); err != nil {
		var we *webcli.Error
		if errors.As(err, &we) {
			return nil, "", errf(we.Code, "%s", we.Msg)
		}
		return nil, "", err
	}
	if st.Kind == agentv1.Kind_KIND_SENTINEL {
		return nil, "", errf("UNSUPPORTED", "web cli runs only on data instances")
	}
	if st.WebCLIPassword == "" {
		return nil, "", errf("UNSUPPORTED", "instance has no web cli account; instances created before Web 命令行 need to be recreated")
	}
	ctx, cancel := context.WithTimeout(ctx, webCLITimeout)
	defer cancel()
	c, err := resp.Dial(ctx, m.L.Socket(port), UserWebCLI, st.WebCLIPassword)
	if err != nil {
		return nil, "", err
	}
	defer c.Close()
	v, err := c.Do(ctx, argv...)
	var engineErr *resp.Error
	switch {
	case err == nil:
	case errors.As(err, &engineErr):
		// 引擎错误：v 已是 '-' 类型的应答，原样作为结果
	case errors.Is(err, os.ErrDeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded):
		return nil, "", errf("TIMEOUT", "command did not finish within %s", webCLITimeout)
	default:
		return nil, "", err
	}
	payload, kind, err := encodeReply(v)
	if err != nil {
		return nil, "", err
	}
	return payload, kind, nil
}

// replyBudget 控制回复序列化的字节预算，超限时截断并置 truncated。
type replyBudget struct {
	left      int
	truncated bool
}

// jsonRuneSize 估算一个字符在 JSON 中的最大长度（转义按最坏情况计）。
func jsonRuneSize(r rune) int {
	switch {
	case r == utf8.RuneError || r < 0x20 || r == '"' || r == '\\' || r == '<' || r == '>' || r == '&' || r == 0x2028 || r == 0x2029:
		return 6
	default:
		return utf8.RuneLen(r)
	}
}

// text 把 RESP 字符串转为 JSON 文本：合法 UTF-8 原样输出；二进制内容改为 base64 并标明编码。
func (b *replyBudget) text(s string) (value, encoding string) {
	if utf8.ValidString(s) {
		var out strings.Builder
		for _, r := range s {
			n := jsonRuneSize(r)
			if n > b.left {
				b.truncated = true
				break
			}
			b.left -= n
			out.WriteRune(r)
		}
		return out.String(), ""
	}
	enc := base64.StdEncoding.EncodeToString([]byte(s))
	n := min(len(enc), max(b.left, 0))
	if n < len(enc) {
		b.truncated = true
	}
	b.left -= n
	return enc[:n], "base64"
}

// node 把一个 RESP 应答转为可序列化的结构，类型包括 simple、error、integer、bulk、array、null。
func (b *replyBudget) node(v resp.Value) map[string]any {
	const overhead = 32
	b.left -= overhead
	switch v.Kind {
	case '+', '-', '$':
		kind := map[byte]string{'+': "simple", '-': "error", '$': "bulk"}[v.Kind]
		if v.Null {
			return map[string]any{"type": "null"}
		}
		value, enc := b.text(v.Str)
		n := map[string]any{"type": kind, "value": value}
		if enc != "" {
			n["encoding"] = enc
		}
		return n
	case ':':
		b.left -= 24
		return map[string]any{"type": "integer", "value": v.Int}
	case '*':
		if v.Null {
			return map[string]any{"type": "null"}
		}
		items := make([]any, 0, len(v.Array))
		for _, e := range v.Array {
			if b.left <= overhead {
				b.truncated = true
				break
			}
			items = append(items, b.node(e))
		}
		return map[string]any{"type": "array", "value": items}
	}
	return map[string]any{"type": "unknown"}
}

// replyKind 返回应答的顶层类型名。
func replyKind(v resp.Value) string {
	switch v.Kind {
	case '+':
		return "simple"
	case '-':
		return "error"
	case ':':
		return "integer"
	case '$':
		if v.Null {
			return "null"
		}
		return "bulk"
	case '*':
		if v.Null {
			return "null"
		}
		return "array"
	}
	return "unknown"
}

// encodeReply 把应答序列化为 {"reply": ..., "truncated": bool}，整体不超过 256 KiB。
func encodeReply(v resp.Value) ([]byte, string, error) {
	b := &replyBudget{left: webCLIMaxPayload - webCLISlack}
	node := b.node(v)
	out, err := json.Marshal(map[string]any{"reply": node, "truncated": b.truncated})
	if err != nil {
		return nil, "", err
	}
	kind := replyKind(v)
	if len(out) > webCLIMaxPayload {
		// 预算已计入结构开销，正常不会走到这里；兜底返回只含类型的截断标记。
		out, err = json.Marshal(map[string]any{"reply": map[string]any{"type": kind}, "truncated": true})
		if err != nil {
			return nil, "", err
		}
	}
	return out, kind, nil
}

var keyPatternRe = regexp.MustCompile(`^[A-Za-z0-9:_.\/-]+\*$`)

func (m *Manager) BrowseKeys(ctx context.Context, req *agentv1.BrowseKeys) ([]byte, error) {
	defer m.lock(req.GetPort())()
	st, err := m.Load(req.GetPort())
	if err != nil {
		return nil, err
	}
	pattern := req.GetPattern()
	if !keyPatternRe.MatchString(pattern) {
		return nil, errf("INVALID", "pattern must be a literal prefix ending in *")
	}
	key := req.GetKey()
	if req.GetReveal() && key == "" {
		return nil, errf("INVALID", "value reveal requires a single key")
	}
	var c *resp.Conn
	if req.GetReveal() {
		c, err = m.dialReader(ctx, st)
	} else {
		c, err = m.Dial(ctx, st)
	}
	if err != nil {
		return nil, err
	}
	defer c.Close()
	count := req.GetCount()
	if count == 0 || count > 200 {
		count = 100
	}
	if key != "" {
		if !keyPatternRe.MatchString(key+"*") || strings.ContainsAny(key, "*\r\n") {
			return nil, errf("INVALID", "key contains unsupported characters")
		}
		if prefix := strings.TrimSuffix(pattern, "*"); !strings.HasPrefix(key, prefix) {
			return nil, errf("INVALID", "key is outside the requested prefix")
		}
		t, err := c.Do(ctx, "TYPE", key)
		if err != nil {
			return nil, err
		}
		ttl, _ := c.Do(ctx, "PTTL", key)
		sz, _ := c.Do(ctx, "MEMORY", "USAGE", key)
		item := map[string]any{"key": key, "type": t.Text(), "ttl_ms": ttl.Int, "bytes": sz.Int}
		if req.GetReveal() {
			value, err := readValue(ctx, c, key, t.Text())
			if err != nil {
				return nil, err
			}
			item["value"] = value
		}
		return json.Marshal(map[string]any{"cursor": "0", "items": []map[string]any{item}})
	}
	return m.scanMetadata(ctx, c, req.GetCursor(), pattern, count)
}

func readValue(ctx context.Context, c *resp.Conn, key, typ string) (any, error) {
	var args []string
	switch typ {
	case "string":
		args = []string{"GET", key}
	case "hash":
		args = []string{"HGETALL", key}
	case "list":
		args = []string{"LRANGE", key, "0", "127"}
	case "set":
		args = []string{"SMEMBERS", key}
	case "zset":
		args = []string{"ZRANGE", key, "0", "127", "WITHSCORES"}
	case "stream":
		args = []string{"XRANGE", key, "-", "+", "COUNT", "100"}
	default:
		return map[string]any{"type": typ, "message": "value preview is unavailable for this type"}, nil
	}
	v, err := c.Do(ctx, args...)
	if err != nil {
		return nil, err
	}
	return jsonValue(v, 0), nil
}

func jsonValue(v resp.Value, depth int) any {
	if v.Null {
		return nil
	}
	if depth > 8 {
		return "[truncated]"
	}
	if v.Kind == ':' {
		return v.Int
	}
	if len(v.Array) == 0 {
		return truncateValue(v.Str, 4096)
	}
	out := make([]any, 0, len(v.Array))
	for _, child := range v.Array {
		out = append(out, jsonValue(child, depth+1))
	}
	return out
}

func truncateValue(v string, max int) string {
	if len(v) <= max {
		return v
	}
	return v[:max] + "…"
}

func (m *Manager) scanMetadata(ctx context.Context, c *resp.Conn, cursor, pattern string, count uint32) ([]byte, error) {
	if cursor == "" {
		cursor = "0"
	}
	if _, err := strconv.ParseUint(cursor, 10, 64); err != nil {
		return nil, errf("INVALID", "cursor must be numeric")
	}
	v, err := c.Do(ctx, "SCAN", cursor, "MATCH", pattern, "COUNT", strconv.Itoa(int(count)))
	if err != nil {
		return nil, err
	}
	if len(v.Array) < 2 {
		return nil, errf("INTERNAL", "invalid SCAN response")
	}
	cursor = v.Array[0].Str
	keys := v.Array[1].Strings()
	items := make([]map[string]any, 0, len(keys))
	for _, key := range keys {
		t, err := c.Do(ctx, "TYPE", key)
		if err != nil {
			continue
		}
		ttl, _ := c.Do(ctx, "PTTL", key)
		sz, _ := c.Do(ctx, "MEMORY", "USAGE", key)
		freq, _ := c.Do(ctx, "OBJECT", "FREQ", key)
		idle, _ := c.Do(ctx, "OBJECT", "IDLETIME", key)
		item := map[string]any{"key": key, "type": t.Text(), "ttl_ms": ttl.Int, "bytes": sz.Int}
		if freq.Kind == ':' {
			item["lfu_freq"] = freq.Int
		}
		if idle.Kind == ':' {
			item["idle_seconds"] = idle.Int
		}
		items = append(items, item)
	}
	sort.SliceStable(items, func(i, j int) bool {
		ib, _ := items[i]["bytes"].(int64)
		jb, _ := items[j]["bytes"].(int64)
		if ib != jb {
			return ib > jb
		}
		return items[i]["key"].(string) < items[j]["key"].(string)
	})
	return json.Marshal(map[string]any{"cursor": cursor, "items": items})
}

var backupIDRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

func (m *Manager) rdbPath(st *State) string {
	name := "dump.rdb"
	if b, err := os.ReadFile(m.L.ConfPath(st.Port, st.Kind)); err == nil {
		for _, line := range strings.Split(string(b), "\n") {
			f := strings.Fields(line)
			if len(f) == 2 && f[0] == "dbfilename" && backupIDRe.MatchString(f[1]) {
				name = f[1]
				break
			}
		}
	}
	return filepath.Join(m.L.InstanceDir(st.Port), name)
}

// Backup creates a local, content-addressed copy of the latest RDB.  The
// console may subsequently stream this file through the typed backup channel;
// the agent itself never exposes arbitrary paths.
func (m *Manager) Backup(ctx context.Context, req *agentv1.Backup) ([]byte, error) {
	defer m.lock(req.GetPort())()
	st, err := m.Load(req.GetPort())
	if err != nil {
		return nil, err
	}
	c, err := m.Dial(ctx, st)
	if err != nil {
		return nil, errf("INTERNAL", "connect: %v", err)
	}
	if _, err := c.Do(ctx, "BGSAVE"); err != nil && !resp.IsError(err, "BACKGROUND_SAVE_IN_PROGRESS") && !strings.Contains(strings.ToLower(err.Error()), "background save already in progress") {
		c.Close()
		return nil, err
	}
	c.Close()
	deadline := time.Now().Add(120 * time.Second)
	var rdb string
	for time.Now().Before(deadline) {
		rdb = m.rdbPath(st)
		if fi, err := os.Stat(rdb); err == nil && fi.Size() >= 0 {
			// INFO persistence gives the authoritative completion state; a file
			// that already exists is still acceptable when BGSAVE is disabled.
			probe, e := m.Dial(ctx, st)
			if e == nil {
				v, e := probe.Do(ctx, "INFO", "persistence")
				probe.Close()
				if e == nil && resp.ParseInfo(v.Text())["rdb_bgsave_in_progress"] != "1" {
					break
				}
			}
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
	if rdb == "" {
		return nil, errf("TIMEOUT", "RDB backup did not finish")
	}
	if _, err := os.Stat(rdb); err != nil {
		return nil, errf("NOT_FOUND", "RDB file is not available: %v", err)
	}
	if !backupIDRe.MatchString(req.GetBackupId()) {
		return nil, errf("INVALID", "invalid backup id")
	}
	dest := m.L.BackupPath(req.GetBackupId())
	if err := os.MkdirAll(filepath.Dir(dest), 0o700); err != nil {
		return nil, err
	}
	src, err := os.Open(rdb)
	if err != nil {
		return nil, err
	}
	defer src.Close()
	tmp := dest + ".tmp"
	_ = os.Remove(tmp)
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return nil, err
	}
	h := sha256.New()
	if _, err := io.Copy(io.MultiWriter(out, h), src); err != nil {
		out.Close()
		_ = os.Remove(tmp)
		return nil, err
	}
	if err := out.Close(); err != nil {
		_ = os.Remove(tmp)
		return nil, err
	}
	if err := os.Rename(tmp, dest); err != nil {
		_ = os.Remove(tmp)
		return nil, err
	}
	return json.Marshal(map[string]any{"backup_id": req.GetBackupId(), "size": fileSize(dest), "sha256": hex.EncodeToString(h.Sum(nil)), "path": "agent-backup"})
}

// DeleteBackup removes one agent-local backup after the console retention job
// has verified that the corresponding metadata record is expired.
func (m *Manager) DeleteBackup(ctx context.Context, req *agentv1.Backup) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !backupIDRe.MatchString(req.GetBackupId()) {
		return errf("INVALID", "invalid backup id")
	}
	path := m.L.BackupPath(req.GetBackupId())
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func fileSize(path string) int64 {
	fi, _ := os.Stat(path)
	if fi == nil {
		return 0
	}
	return fi.Size()
}

// StoreBackup installs a console-downloaded backup into the agent's private
// backup directory. It retains the byte-slice compatibility API for callers
// that already have the complete backup in memory.
func (m *Manager) StoreBackup(ctx context.Context, id string, data []byte, wantSHA256 string) error {
	return m.StoreBackupStream(ctx, id, wantSHA256, func(w io.Writer) error {
		_, err := w.Write(data)
		return err
	})
}

// StoreBackupStream installs a console-downloaded backup without buffering the
// RDB in agent memory. The temporary file is private and only becomes visible
// at the final rename after the complete content hash has been verified.
func (m *Manager) StoreBackupStream(ctx context.Context, id, wantSHA256 string, fill func(io.Writer) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !backupIDRe.MatchString(id) {
		return errf("INVALID", "invalid backup id")
	}
	if fill == nil {
		return errf("INVALID", "backup stream is nil")
	}
	dir := filepath.Dir(m.L.BackupPath(id))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "."+id+".download-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	cleanup := func() {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
	}
	h := sha256.New()
	if err := fill(io.MultiWriter(tmp, h)); err != nil {
		cleanup()
		return err
	}
	if err := ctx.Err(); err != nil {
		cleanup()
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	got := hex.EncodeToString(h.Sum(nil))
	if wantSHA256 != "" && !strings.EqualFold(wantSHA256, got) {
		_ = os.Remove(tmpPath)
		return errf("INVALID", "backup sha256 mismatch")
	}
	if err := os.Chmod(tmpPath, 0o600); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	if err := os.Rename(tmpPath, m.L.BackupPath(id)); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	return nil
}

// Restore replaces an RDB only while the instance is stopped.  The source is
// restricted to the agent's own backup directory and is hash-checked before
// being installed atomically.
func (m *Manager) Restore(ctx context.Context, req *agentv1.Restore) error {
	defer m.lock(req.GetPort())()
	st, err := m.Load(req.GetPort())
	if err != nil {
		return err
	}
	if m.running(ctx, st) {
		return errf("CONFLICT", "instance %d must be stopped before restore", req.GetPort())
	}
	if !backupIDRe.MatchString(req.GetBackupId()) {
		return errf("INVALID", "invalid backup id")
	}
	srcPath := m.L.BackupPath(req.GetBackupId())
	src, err := os.Open(srcPath)
	if err != nil {
		return errf("NOT_FOUND", "backup %s not found", req.GetBackupId())
	}
	defer src.Close()
	h := sha256.New()
	dest := m.rdbPath(st)
	tmp := dest + ".restore"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(io.MultiWriter(out, h), src); err != nil {
		out.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := out.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if want := strings.TrimSpace(req.GetSha256()); want != "" && !strings.EqualFold(want, hex.EncodeToString(h.Sum(nil))) {
		_ = os.Remove(tmp)
		return errf("INVALID", "backup sha256 mismatch")
	}
	if err := os.Rename(tmp, dest); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
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
