// Package engineupdate 安全安装 sycache 数据面升级包。
//
// 升级包是 build-sycache.sh 生成的完整安装 tar.gz。安装时只把
// home/atwasoft/sycache/ 下的内容放入 versions/<version>/；包内的 systemd、sysctl
// 和 logrotate 文件属于系统安装阶段，不能借升级流程覆盖系统路径。
package engineupdate

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/ed25519"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/xiaoguan521/cacheos/internal/pkgsig"
)

const (
	MaxPackage   int64 = 128 << 20
	maxExpanded  int64 = 512 << 20
	maxEntries         = 10000
	enginePrefix       = "home/atwasoft/sycache"
)

var ErrRejected = errors.New("engine package rejected")

// Verify 校验数据面包的名称、架构、签名和包内容摘要。
func Verify(data, sigJSON []byte, arch string, keys map[string]ed25519.PublicKey) (*pkgsig.Sig, error) {
	if int64(len(data)) > MaxPackage {
		return nil, fmt.Errorf("%w: package too large", ErrRejected)
	}
	sig, err := pkgsig.Parse(sigJSON)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrRejected, err)
	}
	if sig.Name != "sycache" || sig.Arch != arch {
		return nil, fmt.Errorf("%w: signature is for %s %s", ErrRejected, sig.Name, sig.Arch)
	}
	if err := pkgsig.Verify(sig, data, keys); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrRejected, err)
	}
	return sig, nil
}

// Install 验签并把数据面包原子安装为 root/versions/<version>。
// 调用方必须是有权限写 root/versions 的 wrapper；不会覆盖已存在版本。
func Install(root string, data, sigJSON []byte, arch string, keys map[string]ed25519.PublicKey) (string, error) {
	sig, err := Verify(data, sigJSON, arch, keys)
	if err != nil {
		return "", err
	}
	versions := filepath.Join(root, "versions")
	if err := os.MkdirAll(versions, 0o755); err != nil {
		return "", fmt.Errorf("%w: create versions directory: %v", ErrRejected, err)
	}
	dst := filepath.Join(versions, sig.Version)
	if _, err := os.Lstat(dst); err == nil {
		return "", fmt.Errorf("%w: version %s is already installed", ErrRejected, sig.Version)
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("%w: inspect destination: %v", ErrRejected, err)
	}
	tmp, err := os.MkdirTemp(versions, ".install-"+sig.Version+"-")
	if err != nil {
		return "", fmt.Errorf("%w: create staging directory: %v", ErrRejected, err)
	}
	keep := false
	defer func() {
		if !keep {
			_ = os.RemoveAll(tmp)
		}
	}()
	if err := extract(data, tmp); err != nil {
		return "", err
	}
	if err := validateInstalledTree(tmp, sig.Version); err != nil {
		return "", err
	}
	share := filepath.Join(tmp, "share")
	if err := os.MkdirAll(share, 0o755); err != nil {
		return "", fmt.Errorf("%w: create share directory: %v", ErrRejected, err)
	}
	sigPath := filepath.Join(share, "ETOS-PKG.sig")
	if fi, err := os.Lstat(sigPath); err == nil && !fi.Mode().IsRegular() {
		return "", fmt.Errorf("%w: package signature path is not regular", ErrRejected)
	}
	if err := os.WriteFile(sigPath, sigJSON, 0o644); err != nil {
		return "", fmt.Errorf("%w: save package signature: %v", ErrRejected, err)
	}
	if err := os.Chmod(sigPath, 0o644); err != nil {
		return "", fmt.Errorf("%w: chmod package signature: %v", ErrRejected, err)
	}
	if err := os.Rename(tmp, dst); err != nil {
		return "", fmt.Errorf("%w: publish version: %v", ErrRejected, err)
	}
	keep = true
	return sig.Version, nil
}

func extract(data []byte, dst string) error {
	zr, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("%w: invalid gzip: %v", ErrRejected, err)
	}
	defer zr.Close()
	return extractTar(zr, dst)
}

func extractTar(r io.Reader, dst string) error {
	tr := tar.NewReader(r)
	seen := make(map[string]bool)
	var entries int
	var expanded int64
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("%w: invalid tar: %v", ErrRejected, err)
		}
		entries++
		if entries > maxEntries {
			return fmt.Errorf("%w: too many archive entries", ErrRejected)
		}
		clean, err := safeTarPath(h.Name)
		if err != nil {
			return err
		}
		if clean == "." || clean == enginePrefix {
			continue
		}
		if !strings.HasPrefix(clean, enginePrefix+"/") {
			// The complete release archive also contains system packaging
			// files. They are intentionally ignored, never extracted.
			continue
		}
		rel := strings.TrimPrefix(clean, enginePrefix+"/")
		if rel == "" || rel == "." {
			continue
		}
		target := filepath.Join(dst, filepath.FromSlash(rel))
		if seen[rel] {
			return fmt.Errorf("%w: duplicate archive entry %q", ErrRejected, h.Name)
		}
		seen[rel] = true
		switch h.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return fmt.Errorf("%w: create directory %q: %v", ErrRejected, rel, err)
			}
			if err := os.Chmod(target, cleanMode(h.Mode, true)); err != nil {
				return fmt.Errorf("%w: chmod directory %q: %v", ErrRejected, rel, err)
			}
		case tar.TypeReg, tar.TypeRegA:
			if h.Size < 0 || h.Size > maxExpanded-expanded {
				return fmt.Errorf("%w: expanded file too large %q", ErrRejected, rel)
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return fmt.Errorf("%w: create parent for %q: %v", ErrRejected, rel, err)
			}
			f, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, cleanMode(h.Mode, false))
			if err != nil {
				return fmt.Errorf("%w: create file %q: %v", ErrRejected, rel, err)
			}
			n, copyErr := io.CopyN(f, tr, h.Size)
			closeErr := f.Close()
			if copyErr != nil || closeErr != nil || n != h.Size {
				return fmt.Errorf("%w: read file %q", ErrRejected, rel)
			}
			expanded += n
			if err := os.Chmod(target, cleanMode(h.Mode, false)); err != nil {
				return fmt.Errorf("%w: chmod file %q: %v", ErrRejected, rel, err)
			}
		case tar.TypeSymlink:
			link, err := safeLinkTarget(rel, h.Linkname)
			if err != nil {
				return err
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return fmt.Errorf("%w: create symlink parent %q: %v", ErrRejected, rel, err)
			}
			if err := os.Symlink(link, target); err != nil {
				return fmt.Errorf("%w: create symlink %q: %v", ErrRejected, rel, err)
			}
		default:
			return fmt.Errorf("%w: unsupported archive entry %q", ErrRejected, h.Name)
		}
	}
	return nil
}

func safeTarPath(name string) (string, error) {
	name = strings.TrimPrefix(name, "./")
	if name == "" {
		return ".", nil
	}
	for _, part := range strings.Split(name, "/") {
		if part == ".." {
			return "", fmt.Errorf("%w: path traversal in %q", ErrRejected, name)
		}
	}
	clean := path.Clean(name)
	if clean == "." || strings.HasPrefix(clean, "/") || strings.HasPrefix(clean, "../") || clean == ".." {
		return "", fmt.Errorf("%w: unsafe archive path %q", ErrRejected, name)
	}
	return clean, nil
}

func safeLinkTarget(rel, link string) (string, error) {
	if link == "" || strings.HasPrefix(link, "/") {
		return "", fmt.Errorf("%w: unsafe symlink target %q", ErrRejected, link)
	}
	for _, part := range strings.Split(link, "/") {
		if part == ".." {
			return "", fmt.Errorf("%w: unsafe symlink target %q", ErrRejected, link)
		}
	}
	resolved := path.Clean(path.Join(path.Dir(rel), link))
	if resolved == ".." || strings.HasPrefix(resolved, "../") {
		return "", fmt.Errorf("%w: symlink escapes version directory", ErrRejected)
	}
	return link, nil
}

func cleanMode(mode int64, dir bool) os.FileMode {
	m := os.FileMode(mode) & 0o777
	if dir {
		m |= 0o755
	} else if m == 0 {
		m = 0o644
	}
	return m
}

func validateInstalledTree(root, version string) error {
	for _, rel := range []string{"bin/sycache-server", "share/BUILDINFO"} {
		fi, err := os.Stat(filepath.Join(root, rel))
		if err != nil || !fi.Mode().IsRegular() {
			return fmt.Errorf("%w: package has no regular %s", ErrRejected, rel)
		}
	}
	b, err := os.ReadFile(filepath.Join(root, "share/BUILDINFO"))
	if err != nil {
		return fmt.Errorf("%w: read BUILDINFO: %v", ErrRejected, err)
	}
	productVersion := ""
	for _, line := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(line, "product_version=") {
			productVersion = strings.TrimSpace(strings.TrimPrefix(line, "product_version="))
			break
		}
	}
	if productVersion != version {
		return fmt.Errorf("%w: BUILDINFO product_version %q does not match %q", ErrRejected, productVersion, version)
	}
	return nil
}

// PackageModules 列出升级包携带的扩展模块：只认 <引擎前缀>/modules/<名称>.so 的普通文件。
// 切换版本前用它确认新版本带齐了本机实例在用的模块；不解包、不落盘。
func PackageModules(data []byte) (map[string]bool, error) {
	zr, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("%w: invalid gzip: %v", ErrRejected, err)
	}
	defer zr.Close()
	tr := tar.NewReader(zr)
	out := map[string]bool{}
	for n := 0; ; n++ {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return out, nil
		}
		if err != nil || n > maxEntries {
			return nil, fmt.Errorf("%w: invalid tar", ErrRejected)
		}
		clean, err := safeTarPath(h.Name)
		if err != nil || h.Typeflag != tar.TypeReg && h.Typeflag != tar.TypeRegA {
			continue
		}
		rel, ok := strings.CutPrefix(clean, enginePrefix+"/modules/")
		if !ok || strings.Contains(rel, "/") || !strings.HasSuffix(rel, ".so") {
			continue
		}
		out[strings.TrimSuffix(rel, ".so")] = true
	}
}
