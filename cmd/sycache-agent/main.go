// sycache-agent：部署在每台缓存主机上的探针（CacheOS 规格书 6.2）。
// 只主动连接控制台，不监听任何端口；以 sycache-agent 用户运行（属 sycache 组），经 Unix Socket 管理本机实例。
//
//	sycache-agent enroll -server H:P -token T -ca-fingerprint F
//	sycache-agent run    [-server H:P]
//	sycache-agent version
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/xiaoguan521/cacheos/internal/agent/conn"
	"github.com/xiaoguan521/cacheos/internal/agent/instance"
	"github.com/xiaoguan521/cacheos/internal/agent/selfupdate"
	"github.com/xiaoguan521/cacheos/internal/pkgsig"
	"github.com/xiaoguan521/cacheos/internal/version"
)

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	cmd, args := os.Args[1], os.Args[2:]
	fs := flag.NewFlagSet(cmd, flag.ExitOnError)
	server := fs.String("server", envOr("SYCACHE_CONSOLE", ""), "控制台 gRPC 地址 host:port")
	stateDir := fs.String("state-dir", "/var/lib/sycache-agent", "身份与状态目录")
	root := fs.String("root", "/home/atwasoft/sycache", "数据面安装目录")
	wrapper := fs.String("wrapper", envOr("SYCACHE_WRAPPER", "sudo -n /usr/libexec/sycache/sycache-wrapper"), "提权入口（按空格切分，不经过 shell）")
	token := fs.String("token", "", "一次性注册令牌（enroll）")
	caFP := fs.String("ca-fingerprint", "", "控制台 CA 证书 sha256 指纹（enroll）")
	_ = fs.Parse(args)

	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	layout := instance.DefaultLayout(*stateDir)
	layout.Root = *root
	cfg := conn.Config{Server: *server, StateDir: *stateDir, Layout: layout, Wrapper: strings.Fields(*wrapper)}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	switch cmd {
	case "version", "-version", "--version":
		fmt.Println(version.String("agent"))
	case "enroll":
		if cfg.Server == "" || *token == "" || *caFP == "" {
			fatal(log, "enroll", fmt.Errorf("-server, -token and -ca-fingerprint are required"))
		}
		// 每个令牌只能用一次；新身份只在控制台接受后才替换旧身份，失败时旧身份保持不变。
		a := conn.New(cfg, log)
		had := a.Enrolled()
		if err := a.Enroll(ctx, *token, *caFP); err != nil {
			if had {
				log.Error("enroll failed; existing identity kept unchanged", "err", err)
				os.Exit(1)
			}
			fatal(log, "enroll", err)
		}
		if had {
			log.Info("re-enrolled: previous identity replaced; a running agent picks it up on its next reconnect")
		}
	case "run":
		// agent 自升级：已安装且签名、哈希校验通过的新版本优先（只 exec 一次）
		selfupdate.MaybeExec(*stateDir, version.Version, runtime.GOARCH, pkgsig.Keys())
		if cfg.Server == "" {
			cfg.Server = serverFromState(*stateDir)
		}
		a := conn.New(cfg, log)
		if err := a.Run(ctx); err != nil && ctx.Err() == nil {
			fatal(log, "run", err)
		}
	default:
		usage()
	}
}

func serverFromState(dir string) string {
	b, err := os.ReadFile(filepath.Join(dir, "node.json"))
	if err != nil {
		return ""
	}
	var st struct {
		Server string `json:"server"`
	}
	if json.Unmarshal(b, &st) != nil {
		return ""
	}
	return st.Server
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func fatal(log *slog.Logger, what string, err error) {
	log.Error(what+" failed", "err", err)
	time.Sleep(100 * time.Millisecond)
	os.Exit(1)
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: sycache-agent enroll -server H:P -token T -ca-fingerprint F | run | version")
	os.Exit(2)
}
