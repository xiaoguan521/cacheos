# sycache-ctl 运维命令行（只读，第一版）

`sycache-ctl` 通过控制台的开放 API（`/api/v1`）查询集群状态，适合放进脚本、巡检任务和告警排查流程。它不直接访问节点，不监听端口，也不执行任何外部进程。

## 准备

1. 在控制台「API 令牌」页面创建令牌。只需要只读角色（viewer），令牌过期后重新创建。
2. 如果控制台使用内部 CA，把 CA 证书保存到本地文件，例如 `/etc/sycache/ca.pem`。

## 配置

只通过环境变量配置，令牌不接受命令行参数，避免出现在进程列表和 shell 历史中。

```bash
export CACHEOS_URL=https://console.example.internal:8443
export CACHEOS_TOKEN=etos_xxxxxxxxxxxxxxxx
```

## 命令

```bash
sycache-ctl --ca-file /etc/sycache/ca.pem status
sycache-ctl --ca-file /etc/sycache/ca.pem hosts ls
sycache-ctl --ca-file /etc/sycache/ca.pem units ls
sycache-ctl --ca-file /etc/sycache/ca.pem units get 3
sycache-ctl --ca-file /etc/sycache/ca.pem ops ls --unit-id 3
sycache-ctl --ca-file /etc/sycache/ca.pem ops get 9
```

加 `--json` 可以原样输出控制台返回的 JSON，方便 `jq` 处理：

```bash
sycache-ctl --ca-file /etc/sycache/ca.pem --json units ls | jq '.[] | select(.health != "ok")'
```

## 退出码

| 退出码 | 含义 |
| --- | --- |
| 0 | 成功 |
| 1 | 请求失败、服务端返回错误、响应超过 4 MiB |
| 2 | 参数或配置错误（包括未知命令、ID 格式不正确、非 https 地址） |

## 安全约束

- 只接受 `https://` 地址。
- 不跟随重定向，避免令牌被转发到其他地址。
- ID 只接受正整数，参数不拼接到 URL 路径之外。
- 输出中出现的令牌会被替换为 `[REDACTED]`。
- 响应大小上限 4 MiB，超限时中止，不输出部分内容。

## 暂不支持

建实例、改参数、切换、备份恢复等写操作需要 TOTP 二次校验，第一版没有提供。遇到这些命令会返回「未知命令」，请在控制台完成。

## 测试

```bash
go test ./cmd/sycache-ctl/
```
