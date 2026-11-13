package api

import (
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"github.com/xiaoguan521/cacheos/internal/console/store"
	"github.com/xiaoguan521/cacheos/internal/pkgsig"
)

// Agent 的升级下载上限也是 128 MiB；控制台导入沿用同一上限，避免把异常上传
// 直接读入数据库前占满控制台内存。签名文件是小 JSON，单独设置较小的上限。
const (
	maxEnginePackage          = pkgsig.MaxPackage
	maxEngineSignature  int64 = 128 << 10
	maxEngineRequest    int64 = maxEnginePackage + maxEngineSignature + 2<<20
	maxEnginePackageRow       = 200
)

type enginePackageView struct {
	ID         uint      `json:"id"`
	Name       string    `json:"name"`
	Version    string    `json:"version"`
	Arch       string    `json:"arch"`
	Variant    string    `json:"variant"`
	SHA256     string    `json:"sha256"`
	Size       int64     `json:"size"`
	KID        string    `json:"kid"`
	UploadedBy string    `json:"uploaded_by"`
	CreatedAt  time.Time `json:"created_at"`
}

func packageView(p *store.EnginePackage) enginePackageView {
	v := enginePackageView{
		ID: p.ID, Name: p.Name, Version: p.Version, Arch: p.Arch,
		SHA256: p.SHA256, Size: p.Size, KID: p.KID,
		UploadedBy: p.UploadedBy, CreatedAt: p.CreatedAt,
	}
	if sig, err := pkgsig.Parse([]byte(p.Sig)); err == nil {
		v.Variant = sig.Variant
	}
	return v
}

func (s *Server) listEnginePackages(c *gin.Context) {
	pq := s.q().EnginePackage
	packages, err := pq.WithContext(c.Request.Context()).Order(pq.CreatedAt.Desc()).Limit(maxEnginePackageRow).Find()
	if err != nil {
		failErr(c, err)
		return
	}
	out := make([]enginePackageView, 0, len(packages))
	for _, p := range packages {
		out = append(out, packageView(p))
	}
	c.JSON(http.StatusOK, out)
}

func readPackagePart(header *multipart.FileHeader, max int64) ([]byte, error) {
	if header == nil {
		return nil, errors.New("missing upload")
	}
	if header.Size > max {
		return nil, errors.New("upload too large")
	}
	f, err := header.Open()
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > max {
		return nil, errors.New("upload too large")
	}
	return b, nil
}

func (s *Server) importEnginePackage(c *gin.Context) {
	// ParseMultipartForm may spill the files to a temporary file, while this
	// limit still bounds the complete request including multipart framing.
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxEngineRequest)
	pkgHeader, err := c.FormFile("package")
	if err != nil {
		fail(c, http.StatusBadRequest, "PACKAGE_REQUIRED", "请上传二进制包文件 package")
		return
	}
	sigHeader, err := c.FormFile("signature")
	if err != nil {
		fail(c, http.StatusBadRequest, "SIGNATURE_REQUIRED", "请上传厂商签名文件 signature")
		return
	}
	data, err := readPackagePart(pkgHeader, maxEnginePackage)
	if err != nil {
		fail(c, http.StatusBadRequest, "PACKAGE_INVALID", err.Error())
		return
	}
	sigData, err := readPackagePart(sigHeader, maxEngineSignature)
	if err != nil {
		fail(c, http.StatusBadRequest, "SIGNATURE_INVALID", err.Error())
		return
	}
	sig, err := pkgsig.Parse(sigData)
	if err != nil {
		fail(c, http.StatusBadRequest, "SIGNATURE_INVALID", err.Error())
		return
	}
	if err := pkgsig.Verify(sig, data, pkgsig.Keys()); err != nil {
		fail(c, http.StatusBadRequest, "SIGNATURE_INVALID", err.Error())
		return
	}
	fields := map[string]string{
		"name":    strings.TrimSpace(c.PostForm("name")),
		"version": strings.TrimSpace(c.PostForm("version")),
		"arch":    strings.TrimSpace(c.PostForm("arch")),
		"variant": strings.TrimSpace(c.PostForm("variant")),
	}
	expected := map[string]string{"name": sig.Name, "version": sig.Version, "arch": sig.Arch, "variant": sig.Variant}
	for field, got := range fields {
		if got != "" && got != expected[field] {
			fail(c, http.StatusBadRequest, "PACKAGE_METADATA_MISMATCH", field+" 与签名文件不一致")
			return
		}
	}

	pq := s.q().EnginePackage
	ctx := c.Request.Context()
	if existing, findErr := pq.WithContext(ctx).Where(pq.Name.Eq(sig.Name), pq.Version.Eq(sig.Version), pq.Arch.Eq(sig.Arch)).First(); findErr == nil {
		fail(c, http.StatusConflict, "PACKAGE_EXISTS", "相同名称、版本和架构的包已存在")
		_ = existing
		return
	} else if !errors.Is(findErr, gorm.ErrRecordNotFound) {
		failErr(c, findErr)
		return
	}
	p := &store.EnginePackage{
		Name: sig.Name, Version: sig.Version, Arch: sig.Arch,
		SHA256: sig.SHA256, Size: int64(len(data)), KID: sig.KID,
		Sig: string(sigData), Data: data, UploadedBy: c.GetString("user"), CreatedAt: time.Now(),
	}
	if err := pq.WithContext(ctx).Create(p); err != nil {
		// The pre-check is for a useful error; the unique index remains the
		// authority when two administrators upload concurrently.
		if existing, findErr := pq.WithContext(ctx).Where(pq.Name.Eq(sig.Name), pq.Version.Eq(sig.Version), pq.Arch.Eq(sig.Arch)).First(); findErr == nil && existing != nil {
			fail(c, http.StatusConflict, "PACKAGE_EXISTS", "相同名称、版本和架构的包已存在")
			return
		}
		failErr(c, err)
		return
	}
	s.audit(c, "engine_package.import", "engine_package", fmt.Sprint(p.ID), map[string]any{
		"name": sig.Name, "version": sig.Version, "arch": sig.Arch, "variant": sig.Variant,
		"sha256": sig.SHA256, "size": len(data), "kid": sig.KID,
	})
	c.JSON(http.StatusCreated, packageView(p))
}

func (s *Server) deleteEnginePackage(c *gin.Context) {
	id, ok := parseID(c)
	if !ok {
		return
	}
	p, err := store.First[store.EnginePackage](s.Store, id)
	if err != nil {
		failErr(c, err)
		return
	}
	if _, err := s.q().EnginePackage.WithContext(c.Request.Context()).Where(s.q().EnginePackage.ID.Eq(id)).Delete(p); err != nil {
		failErr(c, err)
		return
	}
	s.audit(c, "engine_package.delete", "engine_package", fmt.Sprint(id), map[string]any{
		"name": p.Name, "version": p.Version, "arch": p.Arch, "sha256": p.SHA256,
	})
	c.Status(http.StatusNoContent)
}
