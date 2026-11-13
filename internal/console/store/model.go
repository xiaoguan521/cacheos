package store

//go:generate go run ./gen -out ./query

import "time"

// 模型定义（CacheOS 规格书 7、8 节）。修改任何字段或索引都必须在 migrations.go 中新增一条迁移
// （TestModelsMatchMigrations 会检查）。账号、令牌、告警、审计、授权、系统键值属于控制面基础能力。

// 角色（规格书 8 节 M13）。developer 可改参数与浏览 Key（需单独授权），releaser 可做切换与扩缩容，operator 可建删部署单元。
const (
	RoleAdmin     = "admin"
	RoleOperator  = "operator"
	RoleReleaser  = "releaser"
	RoleDeveloper = "developer"
	RoleAuditor   = "auditor"
	RoleViewer    = "viewer"
)

type User struct {
	ID           uint   `gorm:"primaryKey" json:"id"`
	Username     string `gorm:"uniqueIndex;size:64" json:"username"`
	PasswordHash string `json:"-"`
	Role         string `gorm:"size:16" json:"role"`
	CreatedAt    time.Time
	LastLoginAt  *time.Time `json:"last_login_at"`

	// 账号安全（等保 2.0 三级）
	Source             string     `gorm:"size:16" json:"source"` // local | ldap | oidc（空视为 local）
	Disabled           bool       `json:"disabled"`
	MustChangePassword bool       `json:"must_change_password"`
	PasswordChangedAt  *time.Time `json:"password_changed_at"`
	PasswordHistory    string     `json:"-"` // 最近几次口令哈希（JSON）
	FailedLogins       int        `json:"failed_logins"`
	LockedUntil        *time.Time `json:"locked_until"`
	KeyReveal          bool       `json:"key_reveal"` // Key 浏览器查看明文的单独授权（规格书 8 节 M10）
	CliExec            bool       `json:"cli_exec"`   // Web 命令行的单独授权（CLAUDE.md「agent 与安全」唯一例外）
}

// Host 是一台已注册 agent 的缓存主机（规格书 8 节 M3）。
type Host struct {
	ID             uint       `gorm:"primaryKey" json:"id"`
	Name           string     `gorm:"uniqueIndex;size:64" json:"name"`
	Hostname       string     `gorm:"size:255" json:"hostname"`
	Zone           string     `gorm:"size:64" json:"zone"`     // 机架 / 可用区，用于反亲和（同一分片的主从不同 zone）
	Address        string     `gorm:"size:255" json:"address"` // 实例对外通告与复制使用的地址，默认取 agent 连接来源
	RemoteAddr     string     `gorm:"size:255" json:"remote_addr"`
	CertSerial     string     `gorm:"size:64" json:"-"`
	Fingerprint    string     `gorm:"size:64" json:"-"`
	Status         string     `gorm:"index;size:16" json:"status"` // online | offline | revoked | maintenance
	AgentVersion   string     `gorm:"size:32" json:"agent_version"`
	Arch           string     `gorm:"size:16" json:"arch"`
	OSRelease      string     `gorm:"column:os_release;size:128" json:"os_release"`
	Kernel         string     `gorm:"size:128" json:"kernel"`
	CPUCores       uint32     `gorm:"column:cpu_cores" json:"cpu_cores"`
	MemTotalBytes  uint64     `json:"mem_total_bytes"`
	MemUsedBytes   uint64     `json:"mem_used_bytes"`
	CPUPercent     float64    `gorm:"column:cpu_percent" json:"cpu_percent"`
	DiskFreeBytes  uint64     `json:"disk_free_bytes"`
	EngineCurrent  string     `gorm:"size:32" json:"engine_current"`
	EngineVersions string     `json:"engine_versions"`        // 逗号分隔
	Modules        string     `gorm:"size:64" json:"modules"` // 已安装的扩展模块（可选模块包），逗号分隔，agent 在 Hello / Heartbeat 里上报
	Tuning         string     `json:"-"`                      // HostTuning JSON
	LastSeenAt     *time.Time `json:"last_seen_at"`
	CreatedAt      time.Time  `json:"created_at"`
}

type EnrollToken struct {
	ID        uint   `gorm:"primaryKey"`
	TokenHash string `gorm:"uniqueIndex;size:64"`
	Zone      string `gorm:"size:64"`
	ExpiresAt time.Time
	UsedAt    *time.Time
	CreatedBy string `gorm:"size:64"`
	CreatedAt time.Time
}

// 部署形态（规格书 7 节）
const (
	ModeStandalone  = "standalone"
	ModeReplication = "replication"
	ModeSentinel    = "sentinel"
	ModeCluster     = "cluster"
)

// Unit 是一个部署单元：单机、主从、哨兵或集群（规格书 8 节 M4）。
type Unit struct {
	ID          uint   `gorm:"primaryKey" json:"id"`
	Name        string `gorm:"uniqueIndex;size:64" json:"name"`
	Mode        string `gorm:"size:16" json:"mode"`
	Description string `json:"description"`
	// 规格
	MemoryMB   uint32 `gorm:"column:memory_mb" json:"memory_mb"` // 每个数据实例的 maxmemory
	Shards     uint32 `json:"shards"`                            // 集群分片数；其余形态为 1
	Replicas   uint32 `json:"replicas"`                          // 每个分片的从节点数
	TemplateID uint   `json:"template_id"`
	TLS        bool   `gorm:"column:tls" json:"tls"`
	Modules    string `gorm:"size:64" json:"modules"` // 扩展模块（ADR 0002），逗号分隔，如 "json,bloom"；创建时确定，之后不可改
	// 版本与状态
	RevisionID   string    `gorm:"size:16" json:"revision_id"` // 当前参数版本
	EngineVer    string    `gorm:"size:32" json:"engine_version"`
	Status       string    `gorm:"size:16" json:"status"` // creating | running | degraded | changing | stopped | failed | deleting
	MasterAuth   string    `json:"-"`                     // 复制 / 集群口令（secretbox 加密）
	SentinelName string    `gorm:"size:64" json:"sentinel_name"`
	Quorum       uint32    `json:"quorum"`
	CreatedBy    string    `gorm:"size:64" json:"created_by"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// Instance 是部署单元中的一个进程（数据实例或哨兵）。
type Instance struct {
	ID      uint   `gorm:"primaryKey" json:"id"`
	UnitID  uint   `gorm:"index" json:"unit_id"`
	HostID  uint   `gorm:"uniqueIndex:idx_inst_host_port" json:"host_id"`
	Port    uint32 `gorm:"uniqueIndex:idx_inst_host_port" json:"port"`
	Kind    string `gorm:"size:16" json:"kind"`         // server | sentinel
	Shard   uint32 `json:"shard"`                       // 集群分片序号（从 0 开始）；主从 / 哨兵形态为 0
	Desired string `gorm:"size:16" json:"desired_role"` // master | replica | sentinel（编排期望）
	// agent 上报
	State          string     `gorm:"size:24" json:"state"` // running | stopped | loading | failed | pending_restart | missing | unknown
	Role           string     `gorm:"size:16" json:"role"`
	MasterHost     string     `gorm:"size:255" json:"master_host"`
	MasterPort     uint32     `json:"master_port"`
	LinkStatus     string     `gorm:"size:8" json:"master_link_status"`
	ClusterNodeID  string     `gorm:"size:64" json:"cluster_node_id"`
	ClusterSlots   string     `json:"cluster_slots"`
	UsedMemory     uint64     `json:"used_memory"`
	MaxMemory      uint64     `json:"maxmemory"`
	OpsPerSec      uint64     `json:"ops_per_sec"`
	Clients        uint64     `json:"connected_clients"`
	Keys           uint64     `json:"keys"`
	HitRate        float64    `json:"hit_rate"`
	ReplOffset     int64      `json:"repl_offset"`
	ConfigDigest   string     `gorm:"size:80" json:"config_digest"`
	AppliedRev     string     `gorm:"size:16" json:"applied_revision"`
	Drifted        bool       `json:"drifted"`
	PendingRestart bool       `json:"pending_restart"`
	Status         string     `json:"-"` // 最近一次 InstanceStatus（JSON）
	Error          string     `json:"error"`
	ReportedAt     *time.Time `json:"reported_at"`
	CreatedAt      time.Time  `json:"created_at"`
}

// ParamTemplate 是参数模板（规格书 8 节 M5）：缓存型、持久型、高并发型等。
type ParamTemplate struct {
	ID          uint      `gorm:"primaryKey" json:"id"`
	Name        string    `gorm:"uniqueIndex;size:64" json:"name"`
	Description string    `json:"description"`
	Params      string    `json:"-"` // JSON map
	Builtin     bool      `json:"builtin"`
	UpdatedBy   string    `gorm:"size:64" json:"updated_by"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// ParamRevision 是部署单元不可变的参数版本（全量）。
type ParamRevision struct {
	Seq       uint      `gorm:"primaryKey" json:"-"`
	ID        string    `gorm:"uniqueIndex;size:16" json:"id"` // p + 8 位序号
	UnitID    uint      `gorm:"index" json:"unit_id"`
	ParentID  string    `gorm:"size:16" json:"parent_id"`
	Params    string    `json:"-"` // JSON map（全量受管参数）
	Digest    string    `gorm:"size:64" json:"digest"`
	Note      string    `json:"note"`
	CreatedBy string    `gorm:"size:64" json:"created_by"`
	CreatedAt time.Time `json:"created_at"`
}

// 变更状态（规格书 8 节变更流程）
const (
	OpDraft      = "DRAFT"
	OpPrechecked = "PRECHECKED"
	OpApproved   = "APPROVED"
	OpApplying   = "APPLYING"
	OpApplied    = "APPLIED"
	OpPartial    = "PARTIAL"
	OpFailed     = "FAILED"
	OpRolledBack = "ROLLED_BACK"
	OpPaused     = "PAUSED"
)

// Operation 是一次对部署单元的变更：创建、参数、切换、重启、升级、扩缩容、迁槽、备份、恢复、删除。
// 同一部署单元同时只允许一个进行中的变更（LockUnit 用行锁保证）。
type Operation struct {
	ID         uint       `gorm:"primaryKey" json:"id"`
	UnitID     uint       `gorm:"index" json:"unit_id"`
	Kind       string     `gorm:"size:24" json:"kind"`
	Status     string     `gorm:"index;size:16" json:"status"`
	Request    string     `json:"-"` // 请求参数 JSON
	Steps      string     `json:"-"` // 步骤与结果 JSON
	Note       string     `json:"note"`
	Error      string     `json:"error"`
	CreatedBy  string     `gorm:"size:64" json:"created_by"`
	ApprovedBy string     `gorm:"size:64" json:"approved_by"`
	CreatedAt  time.Time  `json:"created_at"`
	StartedAt  *time.Time `json:"started_at"`
	FinishedAt *time.Time `json:"finished_at"`
}

// UnitLock 是部署单元的变更互斥锁（一行一个单元，带 operation id）。
type UnitLock struct {
	UnitID      uint      `gorm:"primaryKey" json:"unit_id"`
	OperationID uint      `json:"operation_id"`
	LockedAt    time.Time `json:"locked_at"`
}

// AclAccount 是部署单元的业务账号（规格书 8 节 M11）。口令只存 sha256（ACL #hash 语法），明文只在创建时返回一次。
type AclAccount struct {
	ID          uint      `gorm:"primaryKey" json:"id"`
	UnitID      uint      `gorm:"uniqueIndex:idx_acl_unit_name" json:"unit_id"`
	Name        string    `gorm:"uniqueIndex:idx_acl_unit_name;size:64" json:"name"`
	PassHashes  string    `json:"-"`            // 逗号分隔的 sha256（轮换期间允许两个）
	KeyPatterns string    `json:"key_patterns"` // 逗号分隔，如 "app:*"
	Categories  string    `json:"categories"`   // 逗号分隔，如 "+@read,+@write,-@dangerous"
	Enabled     bool      `json:"enabled"`
	UpdatedBy   string    `gorm:"size:64" json:"updated_by"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// Backup 是一次 RDB 备份（规格书 8 节 M8，P5）。
type Backup struct {
	ID         string     `gorm:"primaryKey;size:32" json:"id"`
	UnitID     uint       `gorm:"index" json:"unit_id"`
	InstanceID uint       `json:"instance_id"`
	Shard      uint32     `json:"shard"`
	Location   string     `gorm:"size:16" json:"location"` // local | console | s3
	Path       string     `json:"-"`                       // 控制台存储路径（location=console）
	Size       int64      `json:"size"`
	SHA256     string     `gorm:"column:sha256;size:64" json:"sha256"`
	Status     string     `gorm:"size:16" json:"status"`  // running | done | failed | expired
	Trigger    string     `gorm:"size:16" json:"trigger"` // manual | schedule
	Error      string     `json:"error"`
	CreatedBy  string     `gorm:"size:64" json:"created_by"`
	CreatedAt  time.Time  `json:"created_at"`
	ExpiresAt  *time.Time `json:"expires_at"`
}

// BackupPolicy 是部署单元的定时备份策略。
type BackupPolicy struct {
	UnitID    uint       `gorm:"primaryKey" json:"unit_id"`
	Enabled   bool       `json:"enabled"`
	Cron      string     `gorm:"size:64" json:"cron"` // 简化：HH:MM 每日
	KeepDays  int        `json:"keep_days"`
	Upload    bool       `json:"upload"`
	LastRunAt *time.Time `json:"last_run_at"`
	UpdatedBy string     `gorm:"size:64" json:"updated_by"`
	UpdatedAt time.Time  `json:"updated_at"`
}

// MigrationJob 是一次在线数据迁移（规格书 8 节 M9，P5）：源端为任意兼容 Redis 协议的实例。
type MigrationJob struct {
	ID         uint       `gorm:"primaryKey" json:"id"`
	UnitID     uint       `gorm:"index" json:"unit_id"`
	SourceAddr string     `gorm:"size:255" json:"source_addr"`
	SourceAuth string     `json:"-"`                     // 加密
	Mode       string     `gorm:"size:16" json:"mode"`   // full | full_incr
	Status     string     `gorm:"size:16" json:"status"` // queued | running | awaiting_cutover | done | partial | failed
	Progress   string     `json:"progress"`              // JSON：已迁移键数、增量偏移、校验结果
	Error      string     `json:"error"`
	CreatedBy  string     `gorm:"size:64" json:"created_by"`
	CreatedAt  time.Time  `json:"created_at"`
	FinishedAt *time.Time `json:"finished_at"`
}

// EnginePackage 是版本库中的一个厂商签名升级包（sycache 引擎或 agent）。导入时验签，包内容存库。
type EnginePackage struct {
	ID         uint      `gorm:"primaryKey" json:"id"`
	Name       string    `gorm:"uniqueIndex:idx_pkg;size:32" json:"name"` // sycache | sycache-agent
	Version    string    `gorm:"uniqueIndex:idx_pkg;size:32" json:"version"`
	Arch       string    `gorm:"uniqueIndex:idx_pkg;size:16" json:"arch"`
	SHA256     string    `gorm:"column:sha256;size:64" json:"sha256"`
	Size       int64     `json:"size"`
	KID        string    `gorm:"column:kid;size:40" json:"kid"`
	Sig        string    `json:"-"`
	Data       []byte    `json:"-"`
	UploadedBy string    `gorm:"size:64" json:"uploaded_by"`
	CreatedAt  time.Time `json:"created_at"`
}

// MetricMinute 是实例的分钟级指标（agent 心跳上报后由控制台按分钟落库）。
type MetricMinute struct {
	ID         uint  `gorm:"primaryKey"`
	InstanceID uint  `gorm:"index:idx_metric,priority:2"`
	Minute     int64 `gorm:"index:idx_metric,priority:1"`
	OpsPerSec  uint64
	UsedMemory uint64
	Clients    uint64
	Hits       uint64
	Misses     uint64
	NetIn      uint64
	NetOut     uint64
}

// APIToken 是开放 API 的访问令牌。只保存哈希，明文只在创建时返回一次。
type APIToken struct {
	ID         uint       `gorm:"primaryKey" json:"id"`
	Name       string     `gorm:"size:64" json:"name"`
	Username   string     `gorm:"index;size:64" json:"username"`
	Role       string     `gorm:"size:16" json:"role"`
	TokenHash  string     `gorm:"uniqueIndex;size:64" json:"-"`
	Prefix     string     `gorm:"size:16" json:"prefix"`
	ExpiresAt  *time.Time `json:"expires_at"`
	LastUsedAt *time.Time `json:"last_used_at"`
	LastUsedIP string     `gorm:"size:64" json:"last_used_ip"`
	Revoked    bool       `json:"revoked"`
	CreatedBy  string     `gorm:"size:64" json:"created_by"`
	CreatedAt  time.Time  `json:"created_at"`
}

// AlertChannel 是告警通道。密钥类字段加密存储，接口从不返回。
type AlertChannel struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	Name      string    `gorm:"uniqueIndex;size:64" json:"name"`
	Type      string    `gorm:"size:16" json:"type"` // webhook | wecom | dingtalk | email
	URL       string    `json:"url"`
	SecretEnc string    `json:"-"`
	SMTPUser  string    `gorm:"column:smtp_user" json:"smtp_user"`
	MailFrom  string    `json:"mail_from"`
	MailTo    string    `json:"mail_to"`
	Enabled   bool      `json:"enabled"`
	CreatedBy string    `gorm:"size:64" json:"created_by"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// AlertRule 是内置告警规则的配置（规格书 8 节 M12）。
type AlertRule struct {
	ID         uint      `gorm:"primaryKey" json:"id"`
	Kind       string    `gorm:"uniqueIndex;size:32" json:"kind"`
	Enabled    bool      `json:"enabled"`
	Threshold  float64   `json:"threshold"`
	ChannelIDs string    `json:"channel_ids"`
	UpdatedBy  string    `gorm:"size:64" json:"updated_by"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// AlertEvent 是一次告警（同一对象同一规则同时只有一条 firing）。
type AlertEvent struct {
	ID         uint       `gorm:"primaryKey" json:"id"`
	Kind       string     `gorm:"size:32" json:"kind"`
	ObjectKey  string     `gorm:"index;size:128" json:"object_key"`
	Message    string     `json:"message"`
	Status     string     `gorm:"index;size:16" json:"status"`
	FiredAt    time.Time  `json:"fired_at"`
	ResolvedAt *time.Time `json:"resolved_at"`
	Notify     string     `json:"notify"`
}

type AuditLog struct {
	ID         uint      `gorm:"primaryKey" json:"id"`
	TS         time.Time `gorm:"index" json:"ts"`
	Username   string    `json:"username"`
	IP         string    `json:"ip"`
	Action     string    `json:"action"`
	ObjectType string    `json:"object_type"`
	ObjectID   string    `json:"object_id"`
	Detail     string    `json:"detail"`
	Result     string    `json:"result"`
	PrevHash   string    `json:"prev_hash"`
	Hash       string    `json:"hash"`
}

type LicenseStore struct {
	ID         uint `gorm:"primaryKey"`
	Raw        string
	ImportedBy string
	ImportedAt time.Time
}

type SystemKV struct {
	Key   string `gorm:"primaryKey;size:64"`
	Value string
}

// Models 返回全部模型（gorm/gen 生成查询代码与迁移守卫测试使用）。
func Models() []any {
	return []any{&User{}, &Host{}, &EnrollToken{}, &Unit{}, &Instance{}, &ParamTemplate{}, &ParamRevision{},
		&Operation{}, &UnitLock{}, &AclAccount{}, &Backup{}, &BackupPolicy{}, &MigrationJob{}, &EnginePackage{},
		&MetricMinute{}, &APIToken{}, &AlertChannel{}, &AlertRule{}, &AlertEvent{}, &AuditLog{}, &LicenseStore{}, &SystemKV{}}
}
