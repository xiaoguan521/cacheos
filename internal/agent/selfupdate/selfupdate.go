// Package selfupdate 是 agent 自升级（规格书 12.6：agent 自升级使用同一种签名包，只重启 agent 进程）。
//
// 新版本安装在状态目录 agent/<版本>/ 下，连同升级包与签名一起保存；/usr/bin/sycache-agent 启动时（MaybeExec）
// 再次校验签名与二进制哈希，只有版本更新且校验通过才 exec 到新版本。agent 以普通用户运行，自升级不涉及提权；
// 未签名或被改动的二进制永远不会被执行。
package selfupdate

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/ed25519"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/xiaoguan521/cacheos/internal/pkgsig"
)

const (
	maxBinary = 96 << 20
	binEntry  = "bin/sycache-agent"
	envGuard  = "SYCACHE_AGENT_SELFUPDATE_EXEC"
)

var ErrRejected = errors.New("agent package rejected")

func dir(state, ver string) string { return filepath.Join(state, "agent", ver) }

// extract 校验签名并取出 bin/sycache-agent。
func extract(data, sigJSON []byte, arch string, keys map[string]ed25519.PublicKey) (*pkgsig.Sig, []byte, error) {
	sig, err := pkgsig.Check(data, sigJSON, "sycache-agent", arch, keys)
	if err != nil {
		return nil, nil, err
	}
	zr, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, nil, fmt.Errorf("%w: %v", ErrRejected, err)
	}
	tr := tar.NewReader(zr)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, nil, fmt.Errorf("%w: %v", ErrRejected, err)
		}
		if strings.TrimPrefix(h.Name, "./") == binEntry && h.Typeflag == tar.TypeReg && h.Size <= maxBinary {
			b, err := io.ReadAll(io.LimitReader(tr, maxBinary))
			return sig, b, err
		}
	}
	return nil, nil, fmt.Errorf("%w: package has no %s", ErrRejected, binEntry)
}

// Install 校验并安装新版本，切换 agent/current 指向它。
func Install(state string, data, sigJSON []byte, arch string, keys map[string]ed25519.PublicKey) (string, error) {
	sig, bin, err := extract(data, sigJSON, arch, keys)
	if err != nil {
		return "", err
	}
	d := dir(state, sig.Version)
	tmp := d + ".tmp"
	_ = os.RemoveAll(tmp)
	if err := os.MkdirAll(tmp, 0o700); err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(tmp, "package.tar.gz"), data, 0o600); err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(tmp, "package.sig"), sigJSON, 0o600); err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(tmp, "sycache-agent"), bin, 0o700); err != nil {
		return "", err
	}
	_ = os.RemoveAll(d)
	if err := os.Rename(tmp, d); err != nil {
		return "", err
	}
	link := filepath.Join(state, "agent", "current")
	_ = os.Remove(link + ".new")
	if err := os.Symlink(sig.Version, link+".new"); err != nil {
		return "", err
	}
	return sig.Version, os.Rename(link+".new", link)
}

// newer 比较 N.N.N[-N]。
func newer(a, b string) bool {
	pa, pb := parts(a), parts(b)
	for i := range pa {
		if pa[i] != pb[i] {
			return pa[i] > pb[i]
		}
	}
	return false
}

func parts(v string) [4]int {
	var out [4]int
	main, rel, _ := strings.Cut(v, "-")
	for i, s := range strings.SplitN(main, ".", 3) {
		out[i], _ = strconv.Atoi(s)
	}
	out[3], _ = strconv.Atoi(rel)
	return out
}

// Staged 返回已安装且校验通过、比 running 新的版本的二进制路径；没有则返回空串。
// 已有候选版本却校验失败（被改动或损坏）时，在 log 中记警告，避免静默忽略；log 可为 nil。
func Staged(state, running, arch string, keys map[string]ed25519.PublicKey, log *slog.Logger) string {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	ver, err := os.Readlink(filepath.Join(state, "agent", "current"))
	if err != nil || !pkgsig.VersionRe.MatchString(ver) || (pkgsig.VersionRe.MatchString(running) && !newer(ver, running)) {
		return ""
	}
	d := dir(state, ver)
	reject := func(why string, err error) string {
		log.Warn("installed agent version ignored; not executing it", "version", ver, "reason", why, "err", err)
		return ""
	}
	data, err1 := os.ReadFile(filepath.Join(d, "package.tar.gz"))
	sigJSON, err2 := os.ReadFile(filepath.Join(d, "package.sig"))
	bin, err3 := os.ReadFile(filepath.Join(d, "sycache-agent"))
	if err := errors.Join(err1, err2, err3); err != nil {
		return reject("installed files unreadable", err)
	}
	sig, want, err := extract(data, sigJSON, arch, keys)
	if err != nil {
		return reject("package verification failed", err)
	}
	if sig.Version != ver {
		return reject("signed version differs from install directory", nil)
	}
	if sha256.Sum256(want) != sha256.Sum256(bin) {
		return reject("binary differs from the signed package", nil)
	}
	return filepath.Join(d, "sycache-agent")
}

// MaybeExec 在启动时切换到已安装的新版本（只执行一次，避免循环）。返回时表示继续运行当前二进制。
func MaybeExec(state, running, arch string, keys map[string]ed25519.PublicKey, log *slog.Logger) {
	if os.Getenv(envGuard) != "" {
		return
	}
	bin := Staged(state, running, arch, keys, log)
	if bin == "" {
		return
	}
	env := append(os.Environ(), envGuard+"=1")
	_ = syscall.Exec(bin, append([]string{bin}, os.Args[1:]...), env)
}
