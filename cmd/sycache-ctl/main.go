// sycache-ctl 是 CacheOS 控制台的只读运维命令行（规格书 P4/P5 运维补齐）。
//
// 它只通过控制台开放 API（/api/v1，Bearer 令牌）查询状态，不直接访问节点，
// 不监听端口，不执行任何外部进程。第一版只包含只读命令；建实例、改参数、切换等
// 写操作需要 TOTP 二次校验，留待后续版本单独设计。
//
// 配置只来自环境变量，令牌不接受命令行参数（会进入 ps 与 shell 历史）：
//
//	CACHEOS_URL    控制台地址，必须是 https://
//	CACHEOS_TOKEN  开放 API 令牌（控制台「API 令牌」页面创建，只读角色即可）
//
// 全局选项：--ca-file <路径>（信任控制台的内部 CA）、--json（原样输出响应）。
package main

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"text/tabwriter"
	"time"
)

const (
	maxResponseBytes = 4 << 20 // 响应上限 4 MiB，超限中止
	requestTimeout   = 15 * time.Second
	redacted         = "[REDACTED]"
)

var idPattern = regexp.MustCompile(`^[1-9][0-9]{0,17}$`)

const usage = `用法：sycache-ctl [--ca-file <路径>] [--json] <命令> [参数]

环境变量：CACHEOS_URL（https://控制台地址）、CACHEOS_TOKEN（API 令牌）

命令：
  status                      控制台总览（主机、单元、实例、问题）
  hosts ls                    主机列表
  units ls                    单元列表
  units get <id>              单元详情与健康问题
  ops ls [--unit-id <id>]     操作记录列表
  ops get <id>                操作记录详情`

// run 是 CLI 入口，便于测试：不直接读取进程环境与标准流。
func run(args []string, getenv func(string) string, stdout, stderr io.Writer) int {
	ctx := &cli{getenv: getenv, stdout: stdout, stderr: stderr}
	if err := ctx.execute(args); err != nil {
		var ue usageError
		if errors.As(err, &ue) {
			fmt.Fprintln(stderr, "错误："+ue.msg)
			fmt.Fprintln(stderr, usage)
			return 2
		}
		fmt.Fprintln(stderr, "错误："+ctx.redact(err.Error()))
		return 1
	}
	return 0
}

func main() {
	os.Exit(run(os.Args[1:], os.Getenv, os.Stdout, os.Stderr))
}

// usageError 表示参数或配置错误，退出码 2。
type usageError struct{ msg string }

func (e usageError) Error() string { return e.msg }

type cli struct {
	getenv  func(string) string
	stdout  io.Writer
	stderr  io.Writer
	token   string
	base    string
	caFile  string
	rawJSON bool
	client  *http.Client
}

func (c *cli) redact(s string) string {
	if c.token != "" {
		s = strings.ReplaceAll(s, c.token, redacted)
	}
	return s
}

func (c *cli) execute(args []string) error {
	// 全局选项必须位于命令之前。未知的全局选项（包括 --token）直接拒绝。
	for len(args) > 0 && strings.HasPrefix(args[0], "-") {
		switch args[0] {
		case "--json":
			c.rawJSON = true
			args = args[1:]
		case "--ca-file":
			if len(args) < 2 {
				return usageError{"--ca-file 需要路径参数"}
			}
			c.caFile = args[1]
			args = args[2:]
		default:
			return usageError{fmt.Sprintf("未知参数 %q（令牌只能通过环境变量 CACHEOS_TOKEN 提供）", args[0])}
		}
	}
	if len(args) == 0 {
		return usageError{"缺少命令"}
	}

	if err := c.loadConfig(); err != nil {
		return err
	}

	switch args[0] {
	case "status":
		return c.status()
	case "hosts":
		if len(args) == 2 && args[1] == "ls" {
			return c.hostsList()
		}
	case "units":
		if len(args) == 2 && args[1] == "ls" {
			return c.unitsList()
		}
		if len(args) == 3 && args[1] == "get" {
			id, err := parseID(args[2])
			if err != nil {
				return err
			}
			return c.unitGet(id)
		}
	case "ops":
		return c.ops(args[1:])
	default:
		return usageError{fmt.Sprintf("未知命令 %q", args[0])}
	}
	return usageError{fmt.Sprintf("未知命令 %q", strings.Join(args, " "))}
}

func (c *cli) loadConfig() error {
	c.base = strings.TrimRight(c.getenv("CACHEOS_URL"), "/")
	if c.base == "" {
		return usageError{"未设置环境变量 CACHEOS_URL"}
	}
	u, err := url.Parse(c.base)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return usageError{"CACHEOS_URL 必须是 https:// 开头的控制台地址"}
	}
	c.token = c.getenv("CACHEOS_TOKEN")
	if c.token == "" {
		return usageError{"未设置环境变量 CACHEOS_TOKEN"}
	}
	if strings.ContainsAny(c.token, " \t\r\n") {
		return usageError{"CACHEOS_TOKEN 格式不正确"}
	}

	tlsCfg := &tls.Config{MinVersion: tls.VersionTLS12}
	if c.caFile != "" {
		pemBytes, err := os.ReadFile(c.caFile)
		if err != nil {
			return usageError{"无法读取 --ca-file：" + err.Error()}
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pemBytes) {
			return usageError{"--ca-file 中没有有效的证书"}
		}
		tlsCfg.RootCAs = pool
	}
	c.client = &http.Client{
		Timeout:   requestTimeout,
		Transport: &http.Transport{TLSClientConfig: tlsCfg, Proxy: nil},
		// 不跟随重定向，避免令牌被转发到其他地址。
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	return nil
}

func parseID(s string) (string, error) {
	if !idPattern.MatchString(s) {
		return "", usageError{fmt.Sprintf("无效的 ID %q：只接受正整数", s)}
	}
	return s, nil
}

// get 发起只读请求，返回原始响应体。令牌只放在 Authorization 头里。
func (c *cli) get(path string, query url.Values) ([]byte, error) {
	u := c.base + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	req, err := http.NewRequest(http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/json")

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("请求失败：%v", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return nil, fmt.Errorf("读取响应失败：%v", err)
	}
	if len(body) > maxResponseBytes {
		return nil, errors.New("响应超过 4 MiB 上限，已中止")
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		var apiErr struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		}
		if json.Unmarshal(body, &apiErr) == nil && apiErr.Code != "" {
			return nil, fmt.Errorf("HTTP %d %s：%s", resp.StatusCode, apiErr.Code, apiErr.Message)
		}
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return body, nil
}

// emit 在 --json 时原样输出，否则交给表格函数。
func (c *cli) emit(body []byte, render func([]byte) error) error {
	if c.rawJSON {
		_, err := fmt.Fprintln(c.stdout, strings.TrimSpace(string(body)))
		return err
	}
	return render(body)
}

func (c *cli) table(header string, rows []string) {
	w := tabwriter.NewWriter(c.stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, header)
	for _, r := range rows {
		fmt.Fprintln(w, r)
	}
	_ = w.Flush()
}

func (c *cli) status() error {
	body, err := c.get("/api/v1/dashboard", nil)
	if err != nil {
		return err
	}
	return c.emit(body, func(b []byte) error {
		var d struct {
			Hosts       int      `json:"hosts"`
			HostsOnline int      `json:"hosts_online"`
			Units       int      `json:"units"`
			Instances   int      `json:"instances"`
			Problems    []string `json:"problems"`
		}
		if err := json.Unmarshal(b, &d); err != nil {
			return fmt.Errorf("响应格式不正确：%v", err)
		}
		fmt.Fprintf(c.stdout, "主机在线 %d/%d\n单元 %d\n实例 %d\n", d.HostsOnline, d.Hosts, d.Units, d.Instances)
		if len(d.Problems) == 0 {
			fmt.Fprintln(c.stdout, "问题：无")
			return nil
		}
		fmt.Fprintln(c.stdout, "问题：")
		for _, p := range d.Problems {
			fmt.Fprintln(c.stdout, "  - "+p)
		}
		return nil
	})
}

func (c *cli) hostsList() error {
	body, err := c.get("/api/v1/hosts", nil)
	if err != nil {
		return err
	}
	return c.emit(body, func(b []byte) error {
		var hosts []struct {
			ID           uint   `json:"id"`
			Name         string `json:"name"`
			Status       string `json:"status"`
			Online       bool   `json:"online"`
			AgentVersion string `json:"agent_version"`
			Zone         string `json:"zone"`
		}
		if err := json.Unmarshal(b, &hosts); err != nil {
			return fmt.Errorf("响应格式不正确：%v", err)
		}
		rows := make([]string, 0, len(hosts))
		for _, h := range hosts {
			rows = append(rows, fmt.Sprintf("%d\t%s\t%s\t%t\t%s\t%s", h.ID, h.Name, h.Status, h.Online, h.Zone, h.AgentVersion))
		}
		c.table("ID\t名称\t状态\t在线\t可用区\tAgent 版本", rows)
		return nil
	})
}

func (c *cli) unitsList() error {
	body, err := c.get("/api/v1/units", nil)
	if err != nil {
		return err
	}
	return c.emit(body, func(b []byte) error {
		var units []struct {
			ID       uint   `json:"id"`
			Name     string `json:"name"`
			Mode     string `json:"mode"`
			Shards   uint32 `json:"shards"`
			Replicas uint32 `json:"replicas"`
			MemoryMB uint32 `json:"memory_mb"`
			Health   string `json:"health"`
		}
		if err := json.Unmarshal(b, &units); err != nil {
			return fmt.Errorf("响应格式不正确：%v", err)
		}
		rows := make([]string, 0, len(units))
		for _, u := range units {
			rows = append(rows, fmt.Sprintf("%d\t%s\t%s\t%d\t%d\t%d\t%s", u.ID, u.Name, u.Mode, u.Shards, u.Replicas, u.MemoryMB, u.Health))
		}
		c.table("ID\t名称\t形态\t分片\t副本\t内存(MB)\t健康", rows)
		return nil
	})
}

func (c *cli) unitGet(id string) error {
	body, err := c.get("/api/v1/units/"+id, nil)
	if err != nil {
		return err
	}
	return c.emit(body, func(b []byte) error {
		var u struct {
			ID       uint     `json:"id"`
			Name     string   `json:"name"`
			Mode     string   `json:"mode"`
			Health   string   `json:"health"`
			Problems []string `json:"problems"`
		}
		if err := json.Unmarshal(b, &u); err != nil {
			return fmt.Errorf("响应格式不正确：%v", err)
		}
		fmt.Fprintf(c.stdout, "ID %d  %s  形态 %s  健康 %s\n", u.ID, u.Name, u.Mode, u.Health)
		for _, p := range u.Problems {
			fmt.Fprintln(c.stdout, "  - "+p)
		}
		return nil
	})
}

func (c *cli) ops(args []string) error {
	if len(args) == 0 {
		return usageError{"ops 需要子命令：ls 或 get"}
	}
	switch args[0] {
	case "ls":
		fs := flag.NewFlagSet("ops ls", flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		unitID := fs.String("unit-id", "", "只显示该单元的操作")
		if err := fs.Parse(args[1:]); err != nil {
			return usageError{"ops ls 参数错误：" + err.Error()}
		}
		query := url.Values{}
		if *unitID != "" {
			id, err := parseID(*unitID)
			if err != nil {
				return err
			}
			query.Set("unit_id", id)
		}
		body, err := c.get("/api/v1/operations", query)
		if err != nil {
			return err
		}
		return c.emit(body, func(b []byte) error {
			var ops []struct {
				ID        uint   `json:"id"`
				UnitID    uint   `json:"unit_id"`
				Kind      string `json:"kind"`
				Status    string `json:"status"`
				CreatedBy string `json:"created_by"`
			}
			if err := json.Unmarshal(b, &ops); err != nil {
				return fmt.Errorf("响应格式不正确：%v", err)
			}
			rows := make([]string, 0, len(ops))
			for _, op := range ops {
				rows = append(rows, fmt.Sprintf("%d\t%d\t%s\t%s\t%s", op.ID, op.UnitID, op.Kind, op.Status, op.CreatedBy))
			}
			c.table("ID\t单元\t类型\t状态\t发起人", rows)
			return nil
		})
	case "get":
		if len(args) != 2 {
			return usageError{"用法：ops get <id>"}
		}
		id, err := parseID(args[1])
		if err != nil {
			return err
		}
		body, err := c.get("/api/v1/operations/"+id, nil)
		if err != nil {
			return err
		}
		return c.emit(body, func(b []byte) error {
			var op struct {
				ID     uint   `json:"id"`
				Kind   string `json:"kind"`
				Status string `json:"status"`
				Note   string `json:"note"`
				Error  string `json:"error"`
			}
			if err := json.Unmarshal(b, &op); err != nil {
				return fmt.Errorf("响应格式不正确：%v", err)
			}
			fmt.Fprintf(c.stdout, "ID %d  类型 %s  状态 %s\n", op.ID, op.Kind, op.Status)
			if op.Note != "" {
				fmt.Fprintln(c.stdout, "备注："+op.Note)
			}
			if op.Error != "" {
				fmt.Fprintln(c.stdout, "错误："+op.Error)
			}
			return nil
		})
	default:
		return usageError{fmt.Sprintf("未知子命令 %q", "ops "+args[0])}
	}
}
