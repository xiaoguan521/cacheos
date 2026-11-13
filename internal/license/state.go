package license

import (
	"errors"
	"time"
)

// State 是控制台的授权状态（规格书 8.6）。
type State string

const (
	StateUnlicensed State = "UNLICENSED"
	StateValid      State = "VALID"
	StateExpiring   State = "EXPIRING"
	StateGrace      State = "GRACE"
	StateExpired    State = "EXPIRED"
	StateInvalid    State = "INVALID"
)

// ProductName 是本产品在 License 中的产品标识；其他产品的 License 不能激活本产品。
const ProductName = "cacheos"

// ExpiringWindow 是到期提醒的提前量。
const ExpiringWindow = 30 * 24 * time.Hour

// ClockSkewTolerance 是允许的时钟回拨量，超过即判 INVALID（规格书 8.7）。
const ClockSkewTolerance = 24 * time.Hour

// Env 是评估授权所需的本机事实。
type Env struct {
	Now            time.Time
	InstallationID string
	// MaxSeenTime 是本机记录过的最大时间（防时钟回拨）。零值表示未记录。
	MaxSeenTime time.Time
	// FingerprintMatch 报告主机指纹是否满足 5 取 3；installation_only 绑定时忽略。
	FingerprintMatch bool
	// BuildDate 是控制台构建日期，用于 maintenance_until 校验。
	BuildDate time.Time
	// CRL 是当前生效的吊销列表（内置与导入中序号较大的一份），可为 nil。
	CRL *CRL
}

// Snapshot 是业务代码可见的唯一授权视图。
type Snapshot struct {
	State     State
	Reason    string
	Payload   *Payload // State 为 UNLICENSED / INVALID 时可能为 nil
	ExpiresAt time.Time
}

// Writable 报告控制台是否允许新增变更。
// EXPIRED / INVALID / UNLICENSED 下只读；回滚、证书续期、导入 License 由中间件单独放行。
func (s Snapshot) Writable() bool {
	switch s.State {
	case StateValid, StateExpiring, StateGrace:
		return true
	default:
		return false
	}
}

// Feature 报告某功能当前是否可用。
func (s Snapshot) Feature(f Feature) bool {
	return s.Writable() && s.Payload != nil && s.Payload.HasFeature(f)
}

var ErrMaintenanceExpired = errors.New("license: console build is newer than maintenance_until")

// Evaluate 根据 License 原文与本机环境得出授权状态。
// 每次调用都重新验签，不信任任何缓存（规格书 8.6）。
func Evaluate(raw string, keys KeyRing, env Env) Snapshot {
	if raw == "" {
		return Snapshot{State: StateUnlicensed, Reason: "no license imported"}
	}
	p, err := Verify(raw, keys)
	if err != nil {
		return Snapshot{State: StateInvalid, Reason: err.Error()}
	}
	inv := func(reason string) Snapshot {
		return Snapshot{State: StateInvalid, Reason: reason, Payload: p, ExpiresAt: p.ExpiresAt}
	}
	if p.Product.Name != ProductName {
		return inv("license is not issued for " + ProductName)
	}
	if env.CRL.KIDRevoked(p.KID) {
		return inv("signing key revoked")
	}
	if env.CRL.LicenseRevoked(p.LicenseID) {
		return inv("license revoked")
	}
	if p.Binding.InstallationID == "" || p.Binding.InstallationID != env.InstallationID {
		return inv("installation_id mismatch")
	}
	if p.Binding.Type != "installation_only" && !env.FingerprintMatch {
		return inv("host fingerprint mismatch")
	}
	if !env.MaxSeenTime.IsZero() && env.Now.Before(env.MaxSeenTime.Add(-ClockSkewTolerance)) {
		return inv("system clock moved backwards")
	}
	if env.Now.Before(p.NotBefore) {
		return inv(reasonNotYetValid)
	}
	if !env.BuildDate.IsZero() && !p.MaintenanceUntil.IsZero() && env.BuildDate.After(p.MaintenanceUntil) {
		return inv(ErrMaintenanceExpired.Error())
	}

	return Snapshot{State: windowState(p, env.Now), Payload: p, ExpiresAt: p.ExpiresAt}
}

const reasonNotYetValid = "license not yet valid"

// windowState 是有效期规则的唯一实现：按到期时间划分 VALID / EXPIRING / GRACE / EXPIRED。
// Evaluate 与 agent 侧的 AgentCheck 共用它；调用方负责先排除 NotBefore 之前的时间。
func windowState(p *Payload, now time.Time) State {
	grace := time.Duration(p.GraceDays) * 24 * time.Hour
	switch {
	case now.Before(p.ExpiresAt.Add(-ExpiringWindow)):
		return StateValid
	case now.Before(p.ExpiresAt):
		return StateExpiring
	case now.Before(p.ExpiresAt.Add(grace)):
		return StateGrace
	default:
		return StateExpired
	}
}
