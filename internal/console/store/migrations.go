package store

import (
	"time"

	"github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/gorm"
)

// 数据库迁移（gormigrate）。规则：
//   - 已发布的迁移永不修改：每条迁移使用自己内部定义的表结构快照，不引用会继续演进的模型类型；
//   - 修改 model.go 时在末尾追加一条新迁移（ID 用 年月日序号_说明），只做增量变更；
//   - TestModelsMatchMigrations 检查“全部迁移后的表结构”与“当前模型”一致，
//     TestUpgradeFromEveryMigration 检查从任何一个历史版本升级后与全新安装一致（SQLite 与 PostgreSQL）。

func migrations() []*gormigrate.Migration {
	return []*gormigrate.Migration{
		{ID: "202611010001_init", Migrate: migrateInit},
		{ID: "202611110001_user_cli_exec", Migrate: migrateUserCliExec},
	}
}

// ---- 202611110001_user_cli_exec：用户增加 Web 命令行授权（CliExec）----

func migrateUserCliExec(tx *gorm.DB) error {
	type User struct {
		CliExec bool
	}
	return tx.Migrator().AddColumn(&User{}, "CliExec")
}

// Migrate 把数据库升级到最新版本。
func Migrate(db *gorm.DB) error {
	return gormigrate.New(db, gormigrate.DefaultOptions, migrations()).Migrate()
}

// MigrateTo 只升级到指定迁移（测试升级路径使用）。
func MigrateTo(db *gorm.DB, id string) error {
	return gormigrate.New(db, gormigrate.DefaultOptions, migrations()).MigrateTo(id)
}

// MigrationIDs 按顺序返回全部迁移 ID。
func MigrationIDs() []string {
	var ids []string
	for _, m := range migrations() {
		ids = append(ids, m.ID)
	}
	return ids
}

// ---- 202611010001_init：CacheOS 1.0 的全部表（快照）----

func migrateInit(tx *gorm.DB) error {
	type User struct {
		ID                 uint   `gorm:"primaryKey"`
		Username           string `gorm:"uniqueIndex;size:64"`
		PasswordHash       string
		Role               string `gorm:"size:16"`
		CreatedAt          time.Time
		LastLoginAt        *time.Time
		Source             string `gorm:"size:16"`
		Disabled           bool
		MustChangePassword bool
		PasswordChangedAt  *time.Time
		PasswordHistory    string
		FailedLogins       int
		LockedUntil        *time.Time
		KeyReveal          bool
	}
	type Host struct {
		ID             uint   `gorm:"primaryKey"`
		Name           string `gorm:"uniqueIndex;size:64"`
		Hostname       string `gorm:"size:255"`
		Zone           string `gorm:"size:64"`
		Address        string `gorm:"size:255"`
		RemoteAddr     string `gorm:"size:255"`
		CertSerial     string `gorm:"size:64"`
		Fingerprint    string `gorm:"size:64"`
		Status         string `gorm:"index;size:16"`
		AgentVersion   string `gorm:"size:32"`
		Arch           string `gorm:"size:16"`
		OSRelease      string `gorm:"column:os_release;size:128"`
		Kernel         string `gorm:"size:128"`
		CPUCores       uint32 `gorm:"column:cpu_cores"`
		MemTotalBytes  uint64
		MemUsedBytes   uint64
		CPUPercent     float64 `gorm:"column:cpu_percent"`
		DiskFreeBytes  uint64
		EngineCurrent  string `gorm:"size:32"`
		EngineVersions string
		Tuning         string
		LastSeenAt     *time.Time
		CreatedAt      time.Time
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
	type Unit struct {
		ID           uint   `gorm:"primaryKey"`
		Name         string `gorm:"uniqueIndex;size:64"`
		Mode         string `gorm:"size:16"`
		Description  string
		MemoryMB     uint32 `gorm:"column:memory_mb"`
		Shards       uint32
		Replicas     uint32
		TemplateID   uint
		TLS          bool   `gorm:"column:tls"`
		RevisionID   string `gorm:"size:16"`
		EngineVer    string `gorm:"size:32"`
		Status       string `gorm:"size:16"`
		MasterAuth   string
		SentinelName string `gorm:"size:64"`
		Quorum       uint32
		CreatedBy    string `gorm:"size:64"`
		CreatedAt    time.Time
		UpdatedAt    time.Time
	}
	type Instance struct {
		ID             uint   `gorm:"primaryKey"`
		UnitID         uint   `gorm:"index"`
		HostID         uint   `gorm:"uniqueIndex:idx_inst_host_port"`
		Port           uint32 `gorm:"uniqueIndex:idx_inst_host_port"`
		Kind           string `gorm:"size:16"`
		Shard          uint32
		Desired        string `gorm:"size:16"`
		State          string `gorm:"size:24"`
		Role           string `gorm:"size:16"`
		MasterHost     string `gorm:"size:255"`
		MasterPort     uint32
		LinkStatus     string `gorm:"size:8"`
		ClusterNodeID  string `gorm:"size:64"`
		ClusterSlots   string
		UsedMemory     uint64
		MaxMemory      uint64
		OpsPerSec      uint64
		Clients        uint64
		Keys           uint64
		HitRate        float64
		ReplOffset     int64
		ConfigDigest   string `gorm:"size:80"`
		AppliedRev     string `gorm:"size:16"`
		Drifted        bool
		PendingRestart bool
		Status         string
		Error          string
		ReportedAt     *time.Time
		CreatedAt      time.Time
	}
	type ParamTemplate struct {
		ID          uint   `gorm:"primaryKey"`
		Name        string `gorm:"uniqueIndex;size:64"`
		Description string
		Params      string
		Builtin     bool
		UpdatedBy   string `gorm:"size:64"`
		UpdatedAt   time.Time
	}
	type ParamRevision struct {
		Seq       uint   `gorm:"primaryKey"`
		ID        string `gorm:"uniqueIndex;size:16"`
		UnitID    uint   `gorm:"index"`
		ParentID  string `gorm:"size:16"`
		Params    string
		Digest    string `gorm:"size:64"`
		Note      string
		CreatedBy string `gorm:"size:64"`
		CreatedAt time.Time
	}
	type Operation struct {
		ID         uint   `gorm:"primaryKey"`
		UnitID     uint   `gorm:"index"`
		Kind       string `gorm:"size:24"`
		Status     string `gorm:"index;size:16"`
		Request    string
		Steps      string
		Note       string
		Error      string
		CreatedBy  string `gorm:"size:64"`
		ApprovedBy string `gorm:"size:64"`
		CreatedAt  time.Time
		StartedAt  *time.Time
		FinishedAt *time.Time
	}
	type UnitLock struct {
		UnitID      uint `gorm:"primaryKey"`
		OperationID uint
		LockedAt    time.Time
	}
	type AclAccount struct {
		ID          uint   `gorm:"primaryKey"`
		UnitID      uint   `gorm:"uniqueIndex:idx_acl_unit_name"`
		Name        string `gorm:"uniqueIndex:idx_acl_unit_name;size:64"`
		PassHashes  string
		KeyPatterns string
		Categories  string
		Enabled     bool
		UpdatedBy   string `gorm:"size:64"`
		UpdatedAt   time.Time
	}
	type Backup struct {
		ID         string `gorm:"primaryKey;size:32"`
		UnitID     uint   `gorm:"index"`
		InstanceID uint
		Shard      uint32
		Location   string `gorm:"size:16"`
		Path       string
		Size       int64
		SHA256     string `gorm:"column:sha256;size:64"`
		Status     string `gorm:"size:16"`
		Trigger    string `gorm:"size:16"`
		Error      string
		CreatedBy  string `gorm:"size:64"`
		CreatedAt  time.Time
		ExpiresAt  *time.Time
	}
	type BackupPolicy struct {
		UnitID    uint `gorm:"primaryKey"`
		Enabled   bool
		Cron      string `gorm:"size:64"`
		KeepDays  int
		Upload    bool
		LastRunAt *time.Time
		UpdatedBy string `gorm:"size:64"`
		UpdatedAt time.Time
	}
	type MigrationJob struct {
		ID         uint   `gorm:"primaryKey"`
		UnitID     uint   `gorm:"index"`
		SourceAddr string `gorm:"size:255"`
		SourceAuth string
		Mode       string `gorm:"size:16"`
		Status     string `gorm:"size:16"`
		Progress   string
		Error      string
		CreatedBy  string `gorm:"size:64"`
		CreatedAt  time.Time
		FinishedAt *time.Time
	}
	type EnginePackage struct {
		ID         uint   `gorm:"primaryKey"`
		Name       string `gorm:"uniqueIndex:idx_pkg;size:32"`
		Version    string `gorm:"uniqueIndex:idx_pkg;size:32"`
		Arch       string `gorm:"uniqueIndex:idx_pkg;size:16"`
		SHA256     string `gorm:"column:sha256;size:64"`
		Size       int64
		KID        string `gorm:"column:kid;size:40"`
		Sig        string
		Data       []byte
		UploadedBy string `gorm:"size:64"`
		CreatedAt  time.Time
	}
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
	type APIToken struct {
		ID         uint   `gorm:"primaryKey"`
		Name       string `gorm:"size:64"`
		Username   string `gorm:"index;size:64"`
		Role       string `gorm:"size:16"`
		TokenHash  string `gorm:"uniqueIndex;size:64"`
		Prefix     string `gorm:"size:16"`
		ExpiresAt  *time.Time
		LastUsedAt *time.Time
		LastUsedIP string `gorm:"size:64"`
		Revoked    bool
		CreatedBy  string `gorm:"size:64"`
		CreatedAt  time.Time
	}
	type AlertChannel struct {
		ID        uint   `gorm:"primaryKey"`
		Name      string `gorm:"uniqueIndex;size:64"`
		Type      string `gorm:"size:16"`
		URL       string
		SecretEnc string
		SMTPUser  string `gorm:"column:smtp_user"`
		MailFrom  string
		MailTo    string
		Enabled   bool
		CreatedBy string `gorm:"size:64"`
		CreatedAt time.Time
		UpdatedAt time.Time
	}
	type AlertRule struct {
		ID         uint   `gorm:"primaryKey"`
		Kind       string `gorm:"uniqueIndex;size:32"`
		Enabled    bool
		Threshold  float64
		ChannelIDs string
		UpdatedBy  string `gorm:"size:64"`
		UpdatedAt  time.Time
	}
	type AlertEvent struct {
		ID         uint   `gorm:"primaryKey"`
		Kind       string `gorm:"size:32"`
		ObjectKey  string `gorm:"index;size:128"`
		Message    string
		Status     string `gorm:"index;size:16"`
		FiredAt    time.Time
		ResolvedAt *time.Time
		Notify     string
	}
	type AuditLog struct {
		ID         uint      `gorm:"primaryKey"`
		TS         time.Time `gorm:"index"`
		Username   string
		IP         string
		Action     string
		ObjectType string
		ObjectID   string
		Detail     string
		Result     string
		PrevHash   string
		Hash       string
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
	return tx.AutoMigrate(&User{}, &Host{}, &EnrollToken{}, &Unit{}, &Instance{}, &ParamTemplate{}, &ParamRevision{}, &Operation{}, &UnitLock{}, &AclAccount{}, &Backup{}, &BackupPolicy{}, &MigrationJob{}, &EnginePackage{}, &MetricMinute{}, &APIToken{}, &AlertChannel{}, &AlertRule{}, &AlertEvent{}, &AuditLog{}, &LicenseStore{}, &SystemKV{})
}

var _ = time.Time{}
