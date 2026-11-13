// Package licsvc 是控制台侧的授权服务：安装 ID、主机指纹、时钟回拨检测、License 导入与状态（规格书 8.4–8.6）。
package licsvc

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/xiaoguan521/cacheos/internal/console/store"
	"github.com/xiaoguan521/cacheos/internal/console/store/query"
	"github.com/xiaoguan521/cacheos/internal/license"
	"github.com/xiaoguan521/cacheos/internal/version"
)

// Service 维护当前授权快照。
type Service struct {
	Store   *store.Store
	DataDir string
	Keys    license.KeyRing
	Log     *slog.Logger // 为空时用 slog.Default()

	mu             sync.Mutex
	installationID string
	idMismatch     bool
}

// Init 读取或生成 installation_id（数据库与本地文件各存一份，不一致视为复制部署）。
func (s *Service) Init() error {
	path := filepath.Join(s.DataDir, "installation.json")
	dbID, dbErr := s.Store.GetKV("installation_id")
	var file struct {
		ID string `json:"installation_id"`
	}
	b, fileErr := os.ReadFile(path)
	if fileErr == nil {
		_ = json.Unmarshal(b, &file)
	}
	switch {
	case dbErr != nil && fileErr != nil: // 全新安装
		raw := make([]byte, 16)
		if _, err := rand.Read(raw); err != nil {
			return err
		}
		// 只在库中不存在时写入：高可用下并发首启的控制台得到同一个 ID
		id, err := s.Store.InitKV("installation_id", "inst_"+hex.EncodeToString(raw))
		if err != nil {
			return err
		}
		out, _ := json.Marshal(map[string]string{"installation_id": id})
		if err := os.WriteFile(path, out, 0o600); err != nil {
			return err
		}
		dbID = id
	case dbErr == nil && fileErr != nil:
		out, _ := json.Marshal(map[string]string{"installation_id": dbID})
		_ = os.WriteFile(path, out, 0o600)
	case dbErr != nil && fileErr == nil:
		dbID = file.ID
		_ = s.Store.SetKV("installation_id", dbID)
	default:
		if dbID != file.ID {
			s.idMismatch = true
		}
	}
	s.installationID = dbID
	return nil
}

// InstallationID 返回本安装的 ID。
func (s *Service) InstallationID() string { return s.installationID }

// FingerprintHashes 返回 5 项主机特征各自的加盐哈希（规格书 8.4）。
func FingerprintHashes() []string {
	read := func(p string) string { b, _ := os.ReadFile(p); return strings.TrimSpace(string(b)) }
	mac := ""
	if ifs, err := net.Interfaces(); err == nil {
		for _, i := range ifs {
			if i.Flags&net.FlagLoopback == 0 && len(i.HardwareAddr) > 0 {
				mac = i.HardwareAddr.String()
				break
			}
		}
	}
	cpu := ""
	for _, line := range strings.Split(read("/proc/cpuinfo"), "\n") {
		if strings.HasPrefix(line, "model name") {
			cpu = line
			break
		}
	}
	facts := []string{read("/etc/machine-id"), read("/sys/class/dmi/id/product_uuid"), rootFS(), mac, cpu}
	out := make([]string, len(facts))
	for i, f := range facts {
		h := sha256.Sum256([]byte(fmt.Sprintf("etos-fp-%d:%s", i, f)))
		out[i] = hex.EncodeToString(h[:8])
	}
	return out
}

func rootFS() string {
	b, _ := os.ReadFile("/proc/self/mountinfo")
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Fields(line)
		if len(f) > 4 && f[4] == "/" {
			return f[2] // 设备号 major:minor
		}
	}
	return ""
}

func fingerprintMatch(want []string) bool {
	if len(want) == 0 {
		return false
	}
	local := map[string]bool{}
	for _, h := range FingerprintHashes() {
		local[h] = true
	}
	n := 0
	for _, h := range want {
		if local[h] {
			n++
		}
	}
	return n >= 3
}

func (s *Service) logger() *slog.Logger {
	if s.Log != nil {
		return s.Log
	}
	return slog.Default()
}

// raw 返回最近导入的 License 原文；库中没有时返回 ""，读库失败返回错误（不能当作“未授权”）。
func (s *Service) raw() (string, error) {
	l := query.Use(s.Store.DB).LicenseStore
	rows, err := l.WithContext(context.Background()).Order(l.ID.Desc()).Limit(1).Find()
	if err != nil {
		return "", err
	}
	if len(rows) == 0 {
		return "", nil
	}
	return rows[0].Raw, nil
}

// Raw 返回当前 License 原文（推送给 agent 用）；读库失败时返回空并记日志。
func (s *Service) Raw() string {
	raw, err := s.raw()
	if err != nil {
		s.logger().Error("license: read stored license failed", "err", err)
	}
	return raw
}

// env 收集评估所需的本机事实；任何一项读不出来都返回错误，调用方据此拒绝新增操作。
func (s *Service) env(now time.Time) (license.Env, error) {
	maxSeen := time.Time{}
	switch v, err := s.Store.GetKV("max_seen_time"); {
	case errors.Is(err, store.ErrNotFound):
	case err != nil:
		return license.Env{}, fmt.Errorf("clock check unavailable: read max_seen_time: %w", err)
	default:
		t, perr := time.Parse(time.RFC3339, v)
		if perr != nil {
			return license.Env{}, fmt.Errorf("clock check unavailable: max_seen_time unreadable: %w", perr)
		}
		maxSeen = t
	}
	crl, err := s.crl()
	if err != nil {
		return license.Env{}, err
	}
	bd, _ := time.Parse("2006-01-02", version.BuildDate)
	if bd.Year() < 2000 {
		bd = time.Time{} // 开发构建不做维保校验
	}
	return license.Env{Now: now, InstallationID: s.installationID, MaxSeenTime: maxSeen, BuildDate: bd, CRL: crl}, nil
}

const crlKV = "license_crl"

// crl 返回当前生效的吊销列表：内置与已导入中序号较大的一份（CRL 是累积的）。
// 已导入的列表读不出或验签失败时返回错误：吊销信息不可知，不能当作“没有吊销”。
func (s *Service) crl() (*license.CRL, error) {
	var imported *license.CRL
	switch raw, err := s.Store.GetKV(crlKV); {
	case errors.Is(err, store.ErrNotFound):
	case err != nil:
		return nil, fmt.Errorf("revocation list unavailable: %w", err)
	case raw != "":
		c, verr := license.VerifyCRL(raw, s.Keys)
		if verr != nil {
			return nil, fmt.Errorf("revocation list unreadable (license_crl): %w", verr)
		}
		imported = c
	}
	return license.NewerCRL(license.BuiltinCRL(s.Keys), imported), nil
}

// CRLInfo 描述当前生效的吊销列表（授权页展示）。
type CRLInfo struct {
	Serial   uint64    `json:"serial"`
	IssuedAt time.Time `json:"issued_at"`
	Revoked  int       `json:"revoked"`
}

// CRLInfo 返回当前吊销列表的摘要；没有吊销列表时返回 nil。
func (s *Service) CRLInfo() *CRLInfo {
	c, err := s.crl()
	if err != nil {
		s.logger().Error("license: revocation list unusable", "key", crlKV, "err", err)
		return nil
	}
	if c == nil {
		return nil
	}
	return &CRLInfo{Serial: c.Serial, IssuedAt: c.IssuedAt, Revoked: len(c.RevokedLicenses)}
}

// ErrCRLNotNewer 表示导入的吊销列表不比当前的新。
var ErrCRLNotNewer = errors.New("吊销列表不比当前的新（序号未增加）")

// ImportCRL 校验并保存吊销列表；只接受序号更大的列表，防止用旧列表“撤销吊销”。
func (s *Service) ImportCRL(raw string) (*license.CRL, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.importCRL(raw)
}

func (s *Service) importCRL(raw string) (*license.CRL, error) {
	raw = strings.TrimSpace(raw)
	c, err := license.VerifyCRL(raw, s.Keys)
	if err != nil {
		return nil, err
	}
	// 已存的列表损坏时不拿它比序号，允许用新的有效列表覆盖修复
	if cur, cerr := s.crl(); cerr != nil {
		s.logger().Error("license: stored revocation list unusable, replacing", "key", crlKV, "err", cerr)
	} else if cur != nil && c.Serial <= cur.Serial {
		return nil, fmt.Errorf("%w：当前序号 %d，导入的序号 %d", ErrCRLNotNewer, cur.Serial, c.Serial)
	}
	if err := s.Store.SetKV(crlKV, raw); err != nil {
		return nil, err
	}
	return c, nil
}

// refuse 是读不到授权事实时的结果：拒绝新增操作并写日志；运行中的实例不受影响（规格书 10 节）。
func (s *Service) refuse(err error) license.Snapshot {
	s.logger().Error("license: facts unreadable, new operations refused", "err", err)
	return license.Snapshot{State: license.StateInvalid, Reason: err.Error()}
}

func (s *Service) evaluate(raw string, now time.Time) license.Snapshot {
	env, err := s.env(now)
	if err != nil {
		return s.refuse(err)
	}
	if p, err := license.Verify(raw, s.Keys); err == nil {
		env.FingerprintMatch = fingerprintMatch(p.Binding.FPHashes)
	}
	snap := license.Evaluate(raw, s.Keys, env)
	if s.idMismatch && snap.State != license.StateUnlicensed {
		snap.State, snap.Reason = license.StateInvalid, "installation id mismatch between database and data dir"
	}
	return snap
}

// current 读取已存 License 并评估；调用方须持有 s.mu。
func (s *Service) current(now time.Time) license.Snapshot {
	raw, err := s.raw()
	if err != nil {
		return s.refuse(fmt.Errorf("license store unreadable: %w", err))
	}
	return s.evaluate(raw, now)
}

// Snapshot 每次都重新验签计算当前状态（规格书 8.6）。
func (s *Service) Snapshot() license.Snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.current(time.Now())
}

// Tick 记录已见最大时间，供时钟回拨检测；由定时任务每小时调用。
func (s *Service) Tick(now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch v, err := s.Store.GetKV("max_seen_time"); {
	case errors.Is(err, store.ErrNotFound):
	case err != nil:
		s.logger().Error("license: read max_seen_time failed", "err", err)
	default:
		if t, perr := time.Parse(time.RFC3339, v); perr != nil {
			s.logger().Error("license: max_seen_time unreadable, overwriting", "err", perr)
		} else if !now.After(t) {
			return
		}
	}
	if err := s.Store.SetKV("max_seen_time", now.UTC().Format(time.RFC3339)); err != nil {
		s.logger().Error("license: write max_seen_time failed", "err", err)
	}
}

// Import 校验并保存新的 License；INVALID 的 License 不会被保存。
// 授权文件中可以附带吊销列表（每行一项），会先于 License 导入，因此已吊销的 License 导入不进来。
func (s *Service) Import(bundle, user string) (license.Snapshot, error) {
	raw, crls := license.SplitBundle(bundle)
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, c := range crls {
		if _, err := s.importCRL(c); err != nil && !errors.Is(err, ErrCRLNotNewer) {
			return license.Snapshot{State: license.StateInvalid, Reason: err.Error()}, fmt.Errorf("revocation list rejected: %w", err)
		}
	}
	if raw == "" {
		return s.current(time.Now()), nil // 只导入了吊销列表
	}
	snap := s.evaluate(raw, time.Now())
	if snap.State == license.StateInvalid || snap.State == license.StateUnlicensed {
		return snap, errors.New("license rejected: " + snap.Reason)
	}
	if err := query.Use(s.Store.DB).LicenseStore.WithContext(context.Background()).Create(&store.LicenseStore{Raw: raw, ImportedBy: user, ImportedAt: time.Now()}); err != nil {
		return snap, err
	}
	return snap, nil
}

// ActivationRequest 生成离线激活申请（规格书 8.5）。
type ActivationRequest struct {
	InstallationID string    `json:"installation_id"`
	FPHashes       []string  `json:"fp_hashes"`
	Product        string    `json:"product"`
	Version        string    `json:"version"`
	Hosts          int64     `json:"hosts"`
	CreatedAt      time.Time `json:"created_at"`
}

func (s *Service) ActivationRequest() ActivationRequest {
	h := query.Use(s.Store.DB).Host
	n, _ := h.WithContext(context.Background()).Where(h.Status.Neq("revoked")).Count()
	return ActivationRequest{InstallationID: s.installationID, FPHashes: FingerprintHashes(), Product: version.Product,
		Version: version.Version, Hosts: n, CreatedAt: time.Now().UTC()}
}
