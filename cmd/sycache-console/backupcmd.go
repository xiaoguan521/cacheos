package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/xiaoguan521/cacheos/internal/console/backup"
	"github.com/xiaoguan521/cacheos/internal/console/store"
	"github.com/xiaoguan521/cacheos/internal/pkg/audit"
)

// 子命令：sycache-console backup | restore（规格书 P6-6）。
//
// 口令只从文件或环境变量 CACHEOS_BACKUP_PASSPHRASE 读取，不接受命令行参数（会出现在 ps 与 shell 历史中）。
func backupMain(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "用法：sycache-console backup|restore [参数]")
		return 2
	}
	var err error
	switch args[0] {
	case "backup":
		err = cmdBackup(args[1:], stdout)
	case "restore":
		err = cmdRestore(args[1:], stdout)
	default:
		fmt.Fprintf(stderr, "未知子命令 %q\n", args[0])
		return 2
	}
	if err != nil {
		fmt.Fprintln(stderr, "错误：", err)
		return 1
	}
	return 0
}

type commonFlags struct {
	dataDir, dsn, passFile *string
}

func addCommon(fs *flag.FlagSet) commonFlags {
	return commonFlags{
		dataDir:  fs.String("data", envOr("CACHEOS_DATA", "/var/lib/sycache-console"), "数据目录"),
		dsn:      fs.String("db", envOr("CACHEOS_DB", ""), "数据库：SQLite 文件路径或 postgres:// DSN（默认 <data>/console.db）"),
		passFile: fs.String("passphrase-file", "", "备份口令文件（也可用环境变量 CACHEOS_BACKUP_PASSPHRASE）"),
	}
}

func (c commonFlags) open() (*store.Store, error) {
	dsn := *c.dsn
	if dsn == "" {
		dsn = filepath.Join(*c.dataDir, "console.db")
	}
	return store.Open(dsn)
}

func (c commonFlags) passphrase() ([]byte, error) {
	var p []byte
	if *c.passFile != "" {
		b, err := os.ReadFile(*c.passFile)
		if err != nil {
			return nil, err
		}
		p = bytes.TrimRight(b, "\r\n")
	} else if v := os.Getenv("CACHEOS_BACKUP_PASSPHRASE"); v != "" {
		p = []byte(v)
	} else {
		return nil, errors.New("需要 -passphrase-file 或环境变量 CACHEOS_BACKUP_PASSPHRASE")
	}
	if len(p) < 12 {
		return nil, errors.New("备份口令至少 12 个字符（备份包含节点 CA 私钥）")
	}
	return p, nil
}

func cmdBackup(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("backup", flag.ContinueOnError)
	c := addCommon(fs)
	out := fs.String("out", "", "备份文件路径（必填）")
	metrics := fs.Bool("metrics", false, "包含流量统计（体积较大）")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *out == "" {
		return errors.New("需要 -out")
	}
	pass, err := c.passphrase()
	if err != nil {
		return err
	}
	st, err := c.open()
	if err != nil {
		return fmt.Errorf("database: %w", err)
	}
	// 先写临时文件，完整写完再改名：中断不会留下半个备份
	tmp := *out + ".partial"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	sum, err := backup.Create(f, st, *c.dataDir, pass, backup.Options{Metrics: *metrics})
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, *out); err != nil {
		return err
	}
	_ = (&audit.Logger{DB: st.DB}).Write(audit.Entry{Username: "cli", Action: "system.backup", ObjectType: "backup",
		ObjectID: filepath.Base(*out), Result: "ok", Detail: sum.Tables})
	fmt.Fprintf(stdout, "备份完成：%s\n%s", *out, describe(sum))
	return nil
}

func cmdRestore(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("restore", flag.ContinueOnError)
	c := addCommon(fs)
	in := fs.String("in", "", "备份文件路径（必填）")
	force := fs.Bool("force", false, "覆盖数据目录中已有的 CA / 密钥文件")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *in == "" {
		return errors.New("需要 -in")
	}
	pass, err := c.passphrase()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(*c.dataDir, 0o700); err != nil {
		return err
	}
	st, err := c.open()
	if err != nil {
		return fmt.Errorf("database: %w", err)
	}
	f, err := os.Open(*in)
	if err != nil {
		return err
	}
	defer f.Close()
	sum, err := backup.Restore(f, st, *c.dataDir, pass, *force)
	if err != nil {
		return err
	}
	_ = (&audit.Logger{DB: st.DB}).Write(audit.Entry{Username: "cli", Action: "system.restore", ObjectType: "backup",
		ObjectID: filepath.Base(*in), Result: "ok", Detail: map[string]any{"from_version": sum.Version, "created_at": sum.CreatedAt}})
	fmt.Fprintf(stdout, "恢复完成（备份于 %s，版本 %s）\n%s", sum.CreatedAt.Format("2006-01-02 15:04:05Z"), sum.Version, describe(sum))
	fmt.Fprintln(stdout, "提示：启动控制台后节点会自动重连；控制台地址变化时需同步 -grpc-hosts。")
	return nil
}

func describe(s *backup.Summary) string {
	var b strings.Builder
	for _, k := range []string{"node_groups", "nodes", "sites", "upstreams", "certificates", "revisions", "audit_logs", "metric_minutes"} {
		fmt.Fprintf(&b, "  %-15s %d\n", k, s.Tables[k])
	}
	fmt.Fprintf(&b, "  files           %s\n", strings.Join(s.Files, ", "))
	return b.String()
}
