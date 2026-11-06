// sycache-wrapper 由 root 拥有、经 sudoers 授权给 sycache-agent 调用（CacheOS 规格书 6.2）。
//
//	sycache-wrapper start|stop|restart <端口>
//	sycache-wrapper switch-version <版本>
//
// switch-version 在目标版本尚未安装时，会读取 agent 状态目录中同版本的
// 已下载包和签名，验签后安装到 /opt/sycache/versions/<版本> 再切换。
//
// 构建：CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" ./cmd/sycache-wrapper
package main

import (
	"fmt"
	"os"
	"os/exec"

	"github.com/xiaoguan521/cacheos/internal/wrapper"
)

func main() {
	p := wrapper.Default()
	cmd, err := p.Build(os.Args[1:])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(wrapper.ExitUsage)
	}
	if cmd.Switch != "" {
		if err := p.SwitchVersion(cmd.Switch); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		fmt.Println("ok")
		return
	}
	c := exec.Command(cmd.Path, cmd.Args...) // 切片执行，不经过 shell
	c.Env = cmd.Env
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr
	if err := c.Run(); err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			os.Exit(ee.ExitCode())
		}
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
