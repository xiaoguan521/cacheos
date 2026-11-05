package license

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// EditionFeatures 是各版本包含的功能（签发时据此填写 features）。
var EditionFeatures = map[Edition][]Feature{
	EditionTrial:    {FeatureCore, FeatureSentinel},
	EditionStandard: {FeatureCore, FeatureAPIToken, FeatureSentinel, FeatureTLS},
	EditionEnterprise: {FeatureCore, FeatureAPIToken, FeatureSentinel, FeatureTLS, FeatureCluster, FeatureTLSInternal,
		FeatureReshard, FeatureMigrate, FeatureRDBAnalyze, FeatureRemoteBkp, FeatureSSO, FeatureAuditExport, FeatureAlertIM,
		FeatureHA, FeatureApproval},
	EditionXinchuang: {FeatureCore, FeatureAPIToken, FeatureSentinel, FeatureTLS, FeatureCluster, FeatureTLSInternal,
		FeatureReshard, FeatureMigrate, FeatureRDBAnalyze, FeatureRemoteBkp, FeatureSSO, FeatureAuditExport, FeatureAlertIM,
		FeatureHA, FeatureApproval, FeatureGMTLS},
}

// ActivationRequest 是客户控制台导出的离线激活申请（activation-request.json）中签发需要的部分。
type ActivationRequest struct {
	InstallationID string   `json:"installation_id"`
	FPHashes       []string `json:"fp_hashes"`
}

// IssueOptions 描述一张待签发的 License（授权后台、签发命令共用）。
type IssueOptions struct {
	KID                        string
	LicenseID                  string
	CustomerID                 string
	CustomerName               string
	Edition                    Edition
	Hosts, Instances, MemoryGB int
	Days                       int       // ExpiresAt 为零时使用
	ExpiresAt                  time.Time // 指定到期日（覆盖 Days）
	Maintenance                time.Time // 维保截止，零值表示同到期日
	GraceDays                  int
	Binding                    string // installation | installation_only
	Request                    ActivationRequest
	Now                        time.Time
}

// BuildPayload 按选项生成 payload（不签名）。
func BuildPayload(o IssueOptions) (*Payload, error) {
	feats, ok := EditionFeatures[o.Edition]
	if !ok {
		return nil, fmt.Errorf("未知版本 %q", o.Edition)
	}
	if strings.TrimSpace(o.CustomerName) == "" {
		return nil, errors.New("缺少客户名称")
	}
	if !strings.HasPrefix(o.Request.InstallationID, "inst_") {
		return nil, errors.New("激活申请中没有安装 ID")
	}
	if o.Binding == "" {
		o.Binding = "installation"
	}
	if o.Binding == "installation" && len(o.Request.FPHashes) < 3 {
		return nil, errors.New("主机指纹绑定需要激活申请文件（容器部署请用 installation_only）")
	}
	now := o.Now.UTC().Truncate(time.Second)
	exp := o.ExpiresAt.UTC()
	if exp.IsZero() {
		exp = now.Add(time.Duration(o.Days) * 24 * time.Hour)
	}
	maint := o.Maintenance.UTC()
	if maint.IsZero() {
		maint = exp
	}
	notBefore := now
	if exp.Before(now) {
		notBefore = exp.Add(-30 * 24 * time.Hour) // 演示用的已过期授权
	}
	id := o.LicenseID
	if id == "" {
		id = fmt.Sprintf("LIC-%s-%04d", now.Format("20060102"), now.Unix()%10000)
	}
	p := &Payload{
		V: 1, KID: o.KID, LicenseID: id,
		Customer: Customer{ID: o.CustomerID, Name: o.CustomerName},
		Edition:  o.Edition, Features: feats,
		Limits:   Limits{Hosts: o.Hosts, Instances: o.Instances, MemoryGB: o.MemoryGB, AuditDays: 365},
		IssuedAt: now, NotBefore: notBefore, ExpiresAt: exp, MaintenanceUntil: maint, GraceDays: o.GraceDays,
		Binding: Binding{Type: o.Binding, InstallationID: o.Request.InstallationID},
		Product: Product{Name: ProductName, MinVersion: "0.0.0", MaxVersion: "1.*"},
	}
	if o.Binding == "installation" {
		p.Binding.FPHashes = o.Request.FPHashes
	}
	return p, nil
}
