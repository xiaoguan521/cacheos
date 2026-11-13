package instance

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	agentv1 "github.com/xiaoguan521/cacheos/api/gen/agent/v1"
	"github.com/xiaoguan521/cacheos/internal/paramcat"
)

// 保留账号（规格书 7 节 M11）：业务账号不得使用 sycache- 前缀与 default。
const (
	UserAgent    = "sycache-ops"
	UserRepl     = "sycache-repl"
	UserCluster  = "sycache-cluster"
	UserSentinel = "sycache-sentinel"
	UserReader   = "sycache-reader"
	UserWebCLI   = "sycache-webcli"
)

// AgentRules 是 agent 管理账号的权限：只有运维命令，不授予任何读取 value 的命令（CLAUDE.md「agent 与安全」）。
// SCAN / TYPE / TTL / OBJECT / MEMORY USAGE 只返回键名与元数据；MIGRATE 在实例之间搬运数据，不经过 agent。
var AgentRules = []string{
	"~*", "&*", "-@all",
	"+ping", "+info", "+role", "+dbsize", "+lastsave", "+auth", "+hello",
	"+config|get", "+config|set", "+config|rewrite", "+config|resetstat",
	"+replicaof", "+failover", "+cluster", "+migrate", "+readonly",
	"+slowlog", "+latency", "+commandlog", "+memory|stats", "+memory|doctor", "+memory|usage", "+memory|malloc-stats",
	"+client|list", "+client|kill", "+client|setname", "+client|info",
	"+acl|setuser", "+acl|deluser", "+acl|list", "+acl|users", "+acl|save", "+acl|whoami",
	"+bgsave", "+bgrewriteaof", "+shutdown",
	"+scan", "+type", "+ttl", "+pttl", "+object|encoding", "+object|freq", "+object|idletime",
	"+restore", "+del", // migration writes and removes keys on the target; it never reads values
}

// ReplRules：从节点向主节点复制用的账号。
var ReplRules = []string{"-@all", "+psync", "+sync", "+replconf", "+ping", "+auth", "+info"}

// ClusterRules：在线迁槽时由 MIGRATE 目标端认证使用；只允许写入已迁移
// 的序列化键，不授予读取、配置或复制控制权限。
var ClusterRules = []string{"~*", "&*", "-@all", "+restore", "+restore-asking", "+select", "+ping", "+auth"}

// DataSentinelRules：数据节点上供 sycache-sentinel 监控和切换使用的最小权限。
// SENTINEL 子命令只存在于哨兵进程，不能写入普通 server 的 ACL 文件。
var DataSentinelRules = []string{"-@all", "+multi", "+exec", "+slaveof", "+replicaof", "+ping", "+info", "+role",
	"+config|get", "+config|set", "+config|rewrite", "+client|setname", "+client|kill", "+subscribe", "+publish", "+script|kill", "+auth"}

// SentinelRules：哨兵进程自身支持的监控与切换权限。普通 server 上的
// CONFIG/REPLICAOF/MULTI 等命令属于 DataSentinelRules；sentinel 模式不会注册这些命令。
var SentinelRules = []string{"allchannels", "-@all", "+ping", "+info", "+role", "+hello", "+client|setname", "+client|kill",
	"+subscribe", "+psubscribe", "+publish", "+auth",
	"+sentinel|get-master-addr-by-name", "+sentinel|masters", "+sentinel|master", "+sentinel|replicas", "+sentinel|sentinels", "+sentinel|myid",
	"+sentinel|monitor", "+sentinel|remove", "+sentinel|set", "+sentinel|reset", "+sentinel|failover"}

// ReaderRules 只在显式授权的 Key 查看请求中使用。它与 agent 管理账号分离，
// 不包含 CONFIG、SCAN 或任意运维命令；默认不会把值返回给控制台。
var ReaderRules = []string{"~*", "&*", "-@all", "+type", "+ttl", "+pttl", "+memory|usage", "+get", "+hget", "+hgetall", "+lrange", "+smembers", "+zrange", "+xrange"}

// WebCLIRules 是 Web 命令行专用账号 sycache-webcli 的规则（CLAUDE.md「agent 与安全」唯一例外）。
// 与管理账号分离，不放宽管理账号；禁用管理、危险、脚本、阻塞、订阅、事务与连接类命令。
// 同一份拒绝清单在 internal/webcli 中由 agent 发送前再校验一次。
var WebCLIRules = []string{"~*", "resetchannels", "+@all", "-@admin", "-@dangerous", "-@scripting", "-@blocking", "-@pubsub", "-@transaction", "-@connection", "+info", "+dbsize"}

var (
	hostRe = regexp.MustCompile(`^[A-Za-z0-9.:-]{1,253}$`)
	userRe = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_.-]{0,63}$`)
	hashRe = regexp.MustCompile(`^[0-9a-f]{64}$`)
	// 业务账号规则白名单：命令 / 类别的增减、键与频道模式、少数关键字。禁止 >明文口令、nopass、空格与换行。
	ruleRe = regexp.MustCompile(`^([+-]@?[a-z][a-z|-]*|%?R?W?~[A-Za-z0-9:_.*/{}-]+|&[A-Za-z0-9:_.*/-]+|allkeys|resetkeys|allchannels|resetchannels|allcommands|nocommands)$`)
)

// ValidateUser 校验控制台下发的业务账号。
func ValidateUser(u *agentv1.AclUser) error {
	if !userRe.MatchString(u.GetName()) || u.GetName() == "default" || strings.HasPrefix(u.GetName(), "sycache-") {
		return fmt.Errorf("invalid or reserved user name %q", u.GetName())
	}
	if len(u.GetPasswordSha256()) == 0 && u.GetEnabled() {
		return fmt.Errorf("user %s: enabled user needs a password", u.GetName())
	}
	for _, h := range u.GetPasswordSha256() {
		if !hashRe.MatchString(h) {
			return fmt.Errorf("user %s: password must be a sha256 hex digest", u.GetName())
		}
	}
	for _, r := range u.GetRules() {
		if !ruleRe.MatchString(r) {
			return fmt.Errorf("user %s: rule %q not allowed", u.GetName(), r)
		}
	}
	return nil
}

// UserArgs 返回 ACL SETUSER 的参数（不含命令名与用户名）。reset 保证规则是全量而不是累加。
func UserArgs(u *agentv1.AclUser) []string {
	args := []string{"reset"}
	if u.GetEnabled() {
		args = append(args, "on")
	} else {
		args = append(args, "off")
	}
	for _, h := range u.GetPasswordSha256() {
		args = append(args, "#"+h)
	}
	return append(args, u.GetRules()...)
}

func sha(s string) string { h := sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:]) }

// ACLFile 渲染实例的 users.acl：default 关闭，内置账号只存口令哈希。
func ACLFile(st *State, masterauth string, users []*agentv1.AclUser) string {
	var b strings.Builder
	b.WriteString("user default off resetkeys resetchannels -@all\n")
	fmt.Fprintf(&b, "user %s on #%s %s\n", UserAgent, sha(st.AdminPassword), strings.Join(AgentRules, " "))
	if st.ReadPassword != "" {
		fmt.Fprintf(&b, "user %s on #%s %s\n", UserReader, sha(st.ReadPassword), strings.Join(ReaderRules, " "))
	}
	if st.WebCLIPassword != "" {
		fmt.Fprintf(&b, "user %s on #%s %s\n", UserWebCLI, sha(st.WebCLIPassword), strings.Join(WebCLIRulesFor(st.Modules), " "))
	}
	if masterauth != "" {
		fmt.Fprintf(&b, "user %s on #%s %s\n", UserRepl, sha(masterauth), strings.Join(ReplRules, " "))
		fmt.Fprintf(&b, "user %s on #%s %s\n", UserCluster, sha(masterauth), strings.Join(ClusterRules, " "))
		fmt.Fprintf(&b, "user %s on #%s %s\n", UserSentinel, sha(masterauth), strings.Join(DataSentinelRules, " "))
	}
	for _, u := range users {
		fmt.Fprintf(&b, "user %s %s\n", u.GetName(), strings.Join(UserArgs(u)[1:], " "))
	}
	return b.String()
}

// quote 按配置文件语法给值加引号（值已经过 paramcat 校验，不含引号、反斜杠与换行）。
func quote(v string) string {
	if v == "" || strings.ContainsAny(v, " \t") {
		return `"` + v + `"`
	}
	return v
}

// ServerConf 渲染 server 实例配置。编排字段（端口、目录、套接字、复制、集群、TLS）由 agent 生成，参数来自参数目录。
func (l Layout) ServerConf(st *State, c *agentv1.CreateInstance) string {
	p := st.Port
	bind := st.BindAddress
	if bind == "" {
		bind = "127.0.0.1"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "# 由 CacheOS 管理（参数版本 %s）。请在控制台修改参数；手工修改会被识别为配置漂移。\n", st.Revision)
	fmt.Fprintf(&b, "port %d\nbind %s\nprotected-mode yes\nenable-debug-command no\nenable-module-command no\n", p, bind)
	fmt.Fprintf(&b, "unixsocket %s\nunixsocketperm 770\n", l.Socket(p))
	fmt.Fprintf(&b, "dir %s\npidfile %s/sycache-%d.pid\nlogfile %s/sycache-%d.log\n", l.InstanceDir(p), l.RunDir, p, l.LogDir, p)
	fmt.Fprintf(&b, "aclfile %s\n", l.ACLPath(p))
	if r := c.GetReplicaOf(); r != nil && r.GetHost() != "" {
		fmt.Fprintf(&b, "replicaof %s %d\n", r.GetHost(), r.GetPort())
	}
	if c.GetMasterauth() != "" {
		fmt.Fprintf(&b, "masteruser %s\nmasterauth %s\n", UserRepl, quote(c.GetMasterauth()))
	}
	if c.GetClusterEnabled() {
		fmt.Fprintf(&b, "cluster-enabled yes\ncluster-config-file nodes.conf\ncluster-announce-ip %s\ncluster-announce-port %d\ncluster-announce-bus-port %d\n", bind, p, p+10000)
	}
	if t := c.GetTls(); t != nil && len(t.GetCertPem()) > 0 {
		d := l.InstanceDir(p)
		fmt.Fprintf(&b, "tls-port %d\nport 0\n", p) // TLS 实例只开 TLS 端口；agent 走 Unix Socket 不受影响
		fmt.Fprintf(&b, "tls-cert-file %s/tls.crt\ntls-key-file %s/tls.key\ntls-ca-cert-file %s/ca.crt\n", d, d, d)
		fmt.Fprintf(&b, "tls-replication %s\ntls-cluster %s\ntls-auth-clients %s\n", yn(t.GetTlsReplication()), yn(t.GetTlsCluster()), map[bool]string{true: "yes", false: "optional"}[t.GetAuthClients()])
	}
	b.WriteString(l.ModuleConfLines(st.Modules))
	b.WriteString(paramLines(st.Params))
	return b.String()
}

// SentinelConf 渲染哨兵配置。哨兵会自行改写此文件（记录发现的从节点与其他哨兵），之后由 SentinelOp 管理。
func (l Layout) SentinelConf(st *State, c *agentv1.CreateInstance) string {
	p := st.Port
	bind := st.BindAddress
	if bind == "" {
		bind = "127.0.0.1"
	}
	var b strings.Builder
	b.WriteString("# 由 CacheOS 管理。哨兵会改写本文件记录拓扑，请勿手工修改。\n")
	fmt.Fprintf(&b, "port %d\nbind %s\nprotected-mode yes\nenable-debug-command no\nenable-module-command no\n", p, bind)
	fmt.Fprintf(&b, "unixsocket %s\nunixsocketperm 770\n", l.Socket(p))
	fmt.Fprintf(&b, "dir %s\npidfile %s/sycache-sentinel-%d.pid\nlogfile %s/sycache-sentinel-%d.log\n", l.InstanceDir(p), l.RunDir, p, l.LogDir, p)
	// 哨兵不读 aclfile：账号直接写在配置中。客户端只能做服务发现；管理命令只给 agent 账号。
	b.WriteString("user default off resetkeys resetchannels -@all\n")
	fmt.Fprintf(&b, "user %s on #%s ~* &* %s\n", UserAgent, sha(st.AdminPassword), strings.Join(SentinelRules, " "))
	if n := c.GetSentinelMonitorName(); n != "" {
		fmt.Fprintf(&b, "sentinel monitor %s %s %d %d\n", n, c.GetSentinelMasterHost(), c.GetSentinelMasterPort(), c.GetSentinelQuorum())
		if c.GetMasterauth() != "" {
			fmt.Fprintf(&b, "sentinel auth-user %s %s\nsentinel auth-pass %s %s\n", n, UserSentinel, n, quote(c.GetMasterauth()))
		}
	}
	return b.String()
}

func yn(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

func paramLines(params map[string]string) string {
	keys := make([]string, 0, len(params))
	for k := range params {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		fmt.Fprintf(&b, "%s %s\n", k, quote(params[k]))
	}
	return b.String()
}

// SetConfLines 在配置文本中把 key 的行替换为新值（不存在则追加），用于需重启生效的参数。
// save 这类可出现多行的参数先删除全部旧行。
func SetConfLines(conf string, kv map[string]string) string {
	lines := strings.Split(strings.TrimRight(conf, "\n"), "\n")
	keys := make([]string, 0, len(kv))
	for k := range kv {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		out := lines[:0:0]
		placed := false
		for _, line := range lines {
			f := strings.Fields(line)
			if len(f) > 0 && strings.EqualFold(f[0], k) {
				if !placed {
					out = append(out, k+" "+quote(kv[k]))
					placed = true
				}
				continue
			}
			out = append(out, line)
		}
		if !placed {
			out = append(out, k+" "+quote(kv[k]))
		}
		lines = out
	}
	return strings.Join(lines, "\n") + "\n"
}

// Equal 见 paramcat.Equal。
var Equal = paramcat.Equal

// Digest 计算受管参数的摘要（键排序后 k=v 逐行），用于漂移检测与上报。
func Digest(cfg map[string]string) string {
	keys := make([]string, 0, len(cfg))
	for k := range cfg {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	h := sha256.New()
	for _, k := range keys {
		h.Write([]byte(k + "=" + cfg[k] + "\n"))
	}
	return hex.EncodeToString(h.Sum(nil))
}

func atou(s string) uint64 { n, _ := strconv.ParseUint(strings.TrimSpace(s), 10, 64); return n }
func atoi(s string) int64  { n, _ := strconv.ParseInt(strings.TrimSpace(s), 10, 64); return n }
func atof(s string) float64 {
	f, _ := strconv.ParseFloat(strings.TrimSpace(s), 64)
	return f
}
