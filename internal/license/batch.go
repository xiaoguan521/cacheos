package license

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// 授权后台与离线签名机之间交换的两种文件（规格书 8.8.1）。
//
//   SignRequest  sign-request-<批次>.json：后台导出，内容是待签的 License payload 和 / 或 CRL，不含任何密钥。
//   SignedBatch  signed-<批次>.json：     离线签名机输出，只含签名结果；后台导入时逐条验签并核对与请求一致。

const (
	SignRequestFormat = "etos-sign-request-v1"
	SignedBatchFormat = "etos-signed-batch-v1"
	MaxBatchLicenses  = 500
)

var (
	batchRe     = regexp.MustCompile(`^B[0-9]{4,10}$`)
	licenseIDRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{2,63}$`)
	kidRe       = regexp.MustCompile(`^etos-[a-z0-9-]{3,40}$`)
	editions    = map[Edition]bool{EditionTrial: true, EditionStandard: true, EditionXinchuang: true, EditionEnterprise: true}
)

// SignRequest 是一个待签名批次。
type SignRequest struct {
	Format    string    `json:"format"`
	Batch     string    `json:"batch"`
	KID       string    `json:"kid"`
	CreatedAt time.Time `json:"created_at"`
	CreatedBy string    `json:"created_by"`
	Licenses  []Payload `json:"licenses,omitempty"`
	CRL       *CRL      `json:"crl,omitempty"`
}

// SignedBatch 是离线签名机的输出。
type SignedBatch struct {
	Format        string          `json:"format"`
	Batch         string          `json:"batch"`
	KID           string          `json:"kid"`
	RequestSHA256 string          `json:"request_sha256"`
	SignedAt      time.Time       `json:"signed_at"`
	Licenses      []SignedLicense `json:"licenses,omitempty"`
	CRL           string          `json:"crl,omitempty"`
}

// SignedLicense 是一条签好的 License。
type SignedLicense struct {
	LicenseID string `json:"license_id"`
	Raw       string `json:"raw"`
}

// Digest 是文件内容的 SHA-256（十六进制）。签名结果记录它，后台导入时据此确认对应的是哪一份请求。
func Digest(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// ParseSignRequest 严格解析签名请求（拒绝未知字段）并校验内容。
func ParseSignRequest(b []byte, now time.Time) (*SignRequest, error) {
	var r SignRequest
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&r); err != nil {
		return nil, fmt.Errorf("签名请求格式错误：%w", err)
	}
	if err := r.Validate(now); err != nil {
		return nil, err
	}
	return &r, nil
}

// Validate 检查批次的每一条内容；离线签名机签名前、后台导出前都调用它。
func (r *SignRequest) Validate(now time.Time) error {
	if r.Format != SignRequestFormat {
		return fmt.Errorf("不支持的文件格式 %q", r.Format)
	}
	if !batchRe.MatchString(r.Batch) {
		return fmt.Errorf("批次号 %q 不合法（应为 B 加 4–10 位数字）", r.Batch)
	}
	if !kidRe.MatchString(r.KID) {
		return fmt.Errorf("密钥编号 %q 不合法", r.KID)
	}
	if len(r.Licenses) == 0 && r.CRL == nil {
		return errors.New("批次为空")
	}
	if len(r.Licenses) > MaxBatchLicenses {
		return fmt.Errorf("一个批次最多 %d 条", MaxBatchLicenses)
	}
	seen := map[string]bool{}
	for i := range r.Licenses {
		p := &r.Licenses[i]
		where := fmt.Sprintf("第 %d 条（%s）", i+1, p.LicenseID)
		switch {
		case p.V != 1:
			return fmt.Errorf("%s：版本号必须为 1", where)
		case p.KID != r.KID:
			return fmt.Errorf("%s：密钥编号 %q 与批次 %q 不一致", where, p.KID, r.KID)
		case !licenseIDRe.MatchString(p.LicenseID):
			return fmt.Errorf("%s：License 编号不合法", where)
		case seen[p.LicenseID]:
			return fmt.Errorf("%s：License 编号重复", where)
		case strings.TrimSpace(p.Customer.Name) == "":
			return fmt.Errorf("%s：缺少客户名称", where)
		case !editions[p.Edition]:
			return fmt.Errorf("%s：未知版本 %q", where, p.Edition)
		case len(p.Features) == 0:
			return fmt.Errorf("%s：没有功能项", where)
		case !strings.HasPrefix(p.Binding.InstallationID, "inst_"):
			return fmt.Errorf("%s：缺少安装 ID", where)
		case p.Binding.Type != "installation" && p.Binding.Type != "installation_only":
			return fmt.Errorf("%s：绑定方式 %q 不合法", where, p.Binding.Type)
		case p.Binding.Type == "installation" && len(p.Binding.FPHashes) < 3:
			return fmt.Errorf("%s：主机指纹绑定至少需要 3 个指纹", where)
		case !p.ExpiresAt.After(p.NotBefore):
			return fmt.Errorf("%s：到期日早于生效日", where)
		case p.Limits.Hosts < 0 || p.Limits.Instances < 0 || p.Limits.MemoryGB < 0 || p.GraceDays < 0 || p.GraceDays > 90:
			return fmt.Errorf("%s：数量或宽限天数不合法", where)
		}
		seen[p.LicenseID] = true
	}
	if c := r.CRL; c != nil {
		if c.V != 1 || c.KID != r.KID || c.Serial == 0 {
			return errors.New("吊销列表：版本、密钥编号或序号不合法")
		}
		for _, rl := range c.RevokedLicenses {
			if !licenseIDRe.MatchString(rl.LicenseID) {
				return fmt.Errorf("吊销列表：License 编号 %q 不合法", rl.LicenseID)
			}
		}
		for _, k := range c.RevokedKIDs {
			if k == r.KID {
				return errors.New("吊销列表不能吊销签它的密钥本身")
			}
		}
	}
	_ = now
	return nil
}

// VerifySignedBatch 是后台导入签名结果时的核对：对应同一份请求、每条签名有效、
// 签名内容与请求中批准的 payload 完全一致、条目一一对应。
func VerifySignedBatch(req *SignRequest, reqRaw []byte, s *SignedBatch, keys KeyRing) error {
	if s.Format != SignedBatchFormat {
		return fmt.Errorf("不支持的文件格式 %q", s.Format)
	}
	if s.Batch != req.Batch || s.KID != req.KID {
		return errors.New("签名结果与签名请求的批次或密钥编号不一致")
	}
	if s.RequestSHA256 != Digest(reqRaw) {
		return errors.New("签名结果对应的不是这份签名请求（SHA-256 不一致）")
	}
	if len(s.Licenses) != len(req.Licenses) {
		return fmt.Errorf("签名结果有 %d 条，请求有 %d 条", len(s.Licenses), len(req.Licenses))
	}
	want := map[string][]byte{}
	for i := range req.Licenses {
		b, _ := json.Marshal(&req.Licenses[i])
		want[req.Licenses[i].LicenseID] = b
	}
	for _, sl := range s.Licenses {
		p, err := Verify(sl.Raw, keys)
		if err != nil {
			return fmt.Errorf("%s：%w", sl.LicenseID, err)
		}
		got, _ := json.Marshal(p)
		w, ok := want[sl.LicenseID]
		if !ok || p.LicenseID != sl.LicenseID || !bytes.Equal(got, w) {
			return fmt.Errorf("%s：签名内容与批准的草稿不一致", sl.LicenseID)
		}
		delete(want, sl.LicenseID)
	}
	if len(want) != 0 {
		return errors.New("签名结果缺少部分条目")
	}
	if req.CRL != nil || s.CRL != "" {
		if req.CRL == nil || s.CRL == "" {
			return errors.New("吊销列表缺失或多余")
		}
		c, err := VerifyCRL(s.CRL, keys)
		if err != nil {
			return err
		}
		got, _ := json.Marshal(c)
		w, _ := json.Marshal(req.CRL)
		if !bytes.Equal(got, w) {
			return errors.New("吊销列表签名内容与请求不一致")
		}
	}
	return nil
}
