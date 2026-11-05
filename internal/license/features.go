package license

// Feature 是 License 中 features 数组的取值（规格书 8.2）。
// 版本名只用于展示；功能是否可用只看 License 里列出的 features。
type Feature string

const (
	FeatureCore        Feature = "core"         // 单机、主从、参数管理、监控告警、备份到本机
	FeatureAPIToken    Feature = "api_token"    // 开放 API 令牌
	FeatureSSO         Feature = "sso"          // LDAP / OIDC
	FeatureAuditExport Feature = "audit_export" // 审计导出
	FeatureAlertIM     Feature = "alert_im"     // 企业微信 / 钉钉告警通道
	FeatureHA          Feature = "ha"           // 控制台双活
	FeatureSentinel    Feature = "sentinel"     // 哨兵形态
	FeatureCluster     Feature = "cluster"      // 原生集群形态
	FeatureTLS         Feature = "tls"          // 业务链路 TLS
	FeatureTLSInternal Feature = "tls_internal" // 复制 / 集群总线 TLS
	FeatureReshard     Feature = "online_reshard"
	FeatureMigrate     Feature = "migrate"       // 在线数据迁移
	FeatureRDBAnalyze  Feature = "rdb_analyze"   // RDB 离线分析
	FeatureRemoteBkp   Feature = "remote_backup" // 备份上传到控制台
	FeatureApproval    Feature = "change_approval"
	FeatureGMTLS       Feature = "gm_tls" // 国密（信创版）
)

// Edition 是展示用的版本名。
type Edition string

const (
	EditionTrial      Edition = "trial"
	EditionStandard   Edition = "standard"
	EditionXinchuang  Edition = "xinchuang" // 信创版：企业版 + 国密 + 国产 CPU / OS / 数据库
	EditionEnterprise Edition = "enterprise"
)
