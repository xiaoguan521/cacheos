// Package wrapper 实现 sycache-wrapper：sycache-agent 唯一的提权入口（CacheOS 规格书 6.2、6.4）。
//
// 安全要点：
//   - 只接受 4 个子命令：start|stop|restart <端口>、switch-version <版本>；
//   - 端口必须匹配 ^[0-9]{4,5}$、在 1024–65535 之间，且 /home/atwasoft/sycache/instances/<端口> 是真实目录（非软链）并含实例配置；
//   - 版本必须匹配 ^[0-9]+\.[0-9]+\.[0-9]+(-[a-z0-9.]+)?$，且已安装版本的
//     /home/atwasoft/sycache/versions/<版本>/bin/sycache-server 解析后仍在 /home/atwasoft/sycache 之内；
//     未安装时只接受 agent 状态目录中待安装的同版本包，验签并安全解包后再切换；
//   - systemctl 用切片方式执行，清空环境变量，绝不经过 shell；参数只出现在 systemd 单元名中；
//   - 不提供 enable/disable/kill/daemon-reload，不读写实例配置，不接触数据文件。
package wrapper

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"

	"github.com/xiaoguan521/cacheos/internal/agent/engineupdate"
	"github.com/xiaoguan521/cacheos/internal/pkgsig"
)

// ExitUsage 是参数非法时的退出码（与 sysexits.h 的 EX_USAGE 一致）。
const ExitUsage = 64

// ErrUsage 表示调用参数不被允许。
var ErrUsage = errors.New("usage: sycache-wrapper start|stop|restart <port> | switch-version <version>")

var (
	portRe    = regexp.MustCompile(`^[0-9]{4,5}$`)
	versionRe = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+(-[a-z0-9.]+)?$`)
)

// SafeEnv 是执行 systemctl 时使用的全部环境变量。
var SafeEnv = []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin"}

// Paths 是 wrapper 涉及的路径（生产值见 Default；测试可替换）。
type Paths struct {
	Root          string // /home/atwasoft/sycache
	Systemctl     string
	AgentStateDir string // /var/lib/sycache-agent；仅用于待安装包暂存区
}

// Default 是生产环境的路径。
func Default() Paths {
	return Paths{Root: "/home/atwasoft/sycache", Systemctl: "/usr/bin/systemctl", AgentStateDir: "/var/lib/sycache-agent"}
}

// Command 是一次经过校验、可直接执行的调用。
type Command struct {
	Switch string   // 非空表示切换版本（由 wrapper 自身完成，不调用外部命令）
	Path   string   // systemctl
	Args   []string // 不含 argv[0]
	Env    []string
}

// Build 校验 argv（不含程序名）并返回要执行的命令。任何不在白名单内的输入都返回 ErrUsage。
func (p Paths) Build(argv []string) (Command, error) {
	if len(argv) != 2 {
		return Command{}, ErrUsage
	}
	switch argv[0] {
	case "start", "stop", "restart":
		unit, err := p.unit(argv[1])
		if err != nil {
			return Command{}, ErrUsage
		}
		return Command{Path: p.Systemctl, Args: []string{argv[0], unit}, Env: SafeEnv}, nil
	case "switch-version":
		if !versionRe.MatchString(argv[1]) || (p.checkVersion(argv[1]) != nil && !p.pendingVersion(argv[1])) {
			return Command{}, ErrUsage
		}
		return Command{Switch: argv[1]}, nil
	}
	return Command{}, ErrUsage
}

// unit 校验端口与实例目录，返回 systemd 单元名。
func (p Paths) unit(port string) (string, error) {
	if !portRe.MatchString(port) {
		return "", ErrUsage
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 1024 || n > 65535 || strconv.Itoa(n) != port {
		return "", ErrUsage
	}
	dir := filepath.Join(p.Root, "instances", port)
	fi, err := os.Lstat(dir)
	if err != nil || !fi.IsDir() { // Lstat：软链不算目录
		return "", ErrUsage
	}
	if regular(filepath.Join(dir, "sycache-sentinel.conf")) {
		return "sycache-sentinel@" + port + ".service", nil
	}
	if regular(filepath.Join(dir, "sycache.conf")) {
		return "sycache@" + port + ".service", nil
	}
	return "", ErrUsage
}

func regular(path string) bool {
	fi, err := os.Lstat(path)
	return err == nil && fi.Mode().IsRegular()
}

// checkVersion 校验版本号与版本目录：解析软链后必须仍在 Root 之内。
func (p Paths) checkVersion(v string) error {
	if !versionRe.MatchString(v) {
		return ErrUsage
	}
	root, err := filepath.EvalSymlinks(p.Root)
	if err != nil {
		return ErrUsage
	}
	dir, err := filepath.EvalSymlinks(filepath.Join(p.Root, "versions", v))
	if err != nil || (dir != root && !strings.HasPrefix(dir, root+string(filepath.Separator))) {
		return ErrUsage
	}
	if fi, err := os.Stat(filepath.Join(dir, "bin", "sycache-server")); err != nil || !fi.Mode().IsRegular() {
		return ErrUsage
	}
	return nil
}

// SwitchVersion 原子切换 current 软链到 versions/<版本>。不重启任何实例：滚动重启由控制台编排（规格书 6.4）。
func (p Paths) SwitchVersion(v string) error {
	if err := p.checkVersion(v); err != nil {
		pkgPath, sigPath, ok := p.pendingPaths(v)
		if !ok {
			return err
		}
		data, readErr := os.ReadFile(pkgPath)
		if readErr != nil {
			return fmt.Errorf("install-version: read package: %w", readErr)
		}
		sig, readErr := os.ReadFile(sigPath)
		if readErr != nil {
			return fmt.Errorf("install-version: read signature: %w", readErr)
		}
		if _, installErr := engineupdate.Install(p.Root, data, sig, runtime.GOARCH, pkgsig.Keys()); installErr != nil {
			return installErr
		}
		// Only remove the agent-owned staging files after atomic publication.
		_ = os.Remove(pkgPath)
		_ = os.Remove(sigPath)
	}
	if err := p.checkVersion(v); err != nil {
		return err
	}
	tmp := filepath.Join(p.Root, "current.tmp")
	_ = os.Remove(tmp)
	if err := os.Symlink("versions/"+v, tmp); err != nil {
		return fmt.Errorf("switch-version: %w", err)
	}
	if err := os.Rename(tmp, filepath.Join(p.Root, "current")); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("switch-version: %w", err)
	}
	return nil
}

func (p Paths) pendingVersion(v string) bool {
	_, _, ok := p.pendingPaths(v)
	return ok
}

func (p Paths) pendingPaths(v string) (pkgPath, sigPath string, ok bool) {
	if p.AgentStateDir == "" || !versionRe.MatchString(v) {
		return "", "", false
	}
	base := filepath.Join(p.AgentStateDir, "upgrade")
	pkgPath = filepath.Join(base, v+".tar.gz")
	sigPath = filepath.Join(base, v+".sig")
	for _, name := range []string{pkgPath, sigPath} {
		fi, err := os.Lstat(name)
		if err != nil || !fi.Mode().IsRegular() {
			return "", "", false
		}
	}
	return pkgPath, sigPath, true
}
