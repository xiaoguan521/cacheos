# CacheOS 快速开始

本文带你从零完成第一套 CacheOS 缓存实例：安装控制台、初始化管理员、导入授权与数据面包、接入缓存主机、创建实例，并用客户端连接。

命令、参数、环境变量与默认值均来自仓库源码（`deploy/`、`cmd/sycache-console`、`cmd/sycache-agent`、`web/src/views/`、`internal/console/api/`）。控制台界面上的具体文案与按钮位置以控制台界面为准。

数据面 **sycache** 兼容 Redis 协议，业务方的 Jedis、Lettuce、redis-py、go-redis 等客户端无需改代码即可接入。

## 1. 前置条件

- **控制台主机**：Linux，使用 systemd，以 root 执行安装脚本。需要一个 `sycache-console` 可执行文件。
- **缓存主机**：Linux，使用 systemd，以 root 执行安装脚本。需要 `sycache-agent` 与 `sycache-wrapper` 两个可执行文件；数据面由控制台导入的签名包提供。
- **交付物**：一份 License 授权文件；数据面包（`sycache` 或 `sycache-agent`）及其厂商签名文件（`.sig`）。
- **网络**：缓存主机需要能主动访问控制台的 gRPC 端口（默认 `9443`，见下文 `CACHEOS_GRPC_LISTEN`）。agent 只主动连接控制台，不监听任何端口。

## 2. 安装控制台

在控制台主机上执行（参数为控制台二进制路径，可选的第二个参数为环境配置文件）：

```bash
sudo deploy/install-console.sh /path/to/sycache-console /path/to/console.env
```

脚本会创建 `sycache-console` 系统用户，安装 `/usr/bin/sycache-console` 与 systemd 单元，数据目录为 `/var/lib/sycache-console`。`console.env` 可省略；此时单元读取 `/etc/cacheos/console.env`（不存在则使用默认值）。

控制台从环境变量读取配置（命令行参数同名，优先级以命令行为准）：

| 环境变量 | 默认值 | 说明 |
| --- | --- | --- |
| `CACHEOS_LISTEN` | `127.0.0.1:8443` | Web 控制台监听地址。默认只允许本机访问 |
| `CACHEOS_TLS_CERT` / `CACHEOS_TLS_KEY` | 空 | Web 控制台的 TLS 证书与私钥（可选） |
| `CACHEOS_TLS_AUTO` | 关闭 | 设为 `1` 时使用控制台内置 Web CA 自动签发 HTTPS 证书 |
| `CACHEOS_WEB_HOSTS` | 空 | 自动证书包含的主机名或 IP，逗号分隔；默认取 gRPC 主机名并加上 `localhost` |
| `CACHEOS_GRPC_LISTEN` | `0.0.0.0:9443` | agent 连接的 gRPC 监听地址 |
| `CACHEOS_GRPC_HOSTS` | 空 | gRPC 服务端证书包含的主机名或 IP，逗号分隔；agent 用它们连接 |
| `CACHEOS_GRPC_ADVERTISE` | 空 | 写入主机接入命令的控制台地址 `host:port` |
| `CACHEOS_DATA` | `/var/lib/sycache-console` | 数据目录（数据库、CA、安装 ID） |
| `CACHEOS_DB` | 空 | 数据库：SQLite 文件路径或 `postgres://` DSN，默认 `<数据目录>/console.db` |

一个供参考的 `console.env`（主机名请替换为你的环境中的值）：

```bash
CACHEOS_LISTEN=0.0.0.0:8443
CACHEOS_TLS_AUTO=1
CACHEOS_GRPC_HOSTS=console.example.internal
CACHEOS_GRPC_ADVERTISE=console.example.internal:9443
```

启动控制台：

```bash
sudo systemctl enable --now sycache-console
```

然后用浏览器打开 `https://<控制台地址>:8443`。若控制台未启用 HTTPS 却监听在非本机地址，启动日志会提示登录凭据以明文传输。

## 3. 首次初始化

首次打开控制台会进入初始化页面（`/setup`）。

**第 1 步：创建管理员**

- 用户名默认为 `admin`，只能包含小写字母、数字和连字符，长度 2–63 位。
- 密码至少 8 位，并需要再次输入确认。
- 提交后页面会显示双因素认证（TOTP）密钥和 `otpauth` 链接。请把密钥导入身份验证器 App。之后登录以及创建、切换、删除、备份恢复等影响可用性的操作，都需要输入 6 位验证码。

**第 2 步：导入授权**

- 在页面中导入 License 文件。也可以点击「稍后导入，先进入控制台」跳过，之后在「授权与系统」页面导入。
- 授权决定可用的功能。例如未授权哨兵、集群或业务链路 TLS 时，相应形态或选项无法创建（见常见问题）。

## 4. 导入数据面包

在控制台进入「授权与系统」页面的「数据面版本库」，点击导入，依次选择：

- 二进制包：`sycache` 或 `sycache-agent` 发布包；
- 厂商签名文件（`.sig`）：文件中的名称、版本、架构必须与包内容一致。

导入时控制台会校验 SHA-256 与 Ed25519 签名，只有通过验签的包才会进入版本库。删除版本包不影响已安装的主机。此操作仅管理员可执行。

## 5. 安装 agent 并注册主机

**在缓存主机上安装 agent**（以 root 执行，两个参数分别为 agent 与 wrapper 二进制）：

```bash
sudo deploy/install-agent.sh /path/to/sycache-agent /path/to/sycache-wrapper
```

脚本会创建 `sycache-agent` 用户与目录，安装 `/usr/bin/sycache-agent`、`/usr/libexec/sycache/sycache-wrapper` 与 systemd 单元。agent 只能通过 wrapper 完成启停、重启和切换版本。

**生成接入命令**：在控制台进入「主机」页面，点击「生成接入命令」。生成的命令形如：

```bash
sudo -u sycache-agent sycache-agent enroll -server '<控制台gRPC地址>' -token '<令牌>' -ca-fingerprint '<CA指纹>' && sudo systemctl enable --now sycache-agent
```

在缓存主机上执行该命令。注册令牌是一次性的，并带有过期时间（页面返回 `expires_at`）；过期或已使用后，请重新生成。注册完成后，主机出现在「主机」列表中。实例只会分配到在线或维护状态的主机。

## 6. 创建第一个实例

进入「部署单元」页面，点击「创建部署单元」，填写：

- **名称**与说明；
- **形态**：单机、主从、哨兵或集群；
- **每个数据实例内存（MiB）**；
- 主从形态可设置每个分片的从节点数；哨兵形态有监控名与 quorum 字段；集群形态有分片数（最少 3）与每分片从节点数；
- **数据面 TLS** 开关与**参数模板**。

创建为异步操作，进展可在「操作记录」页面查看。实例就绪后，在部署单元详情的实例列表中可以看到每个实例的地址。

## 7. 创建业务账号并连接

实例的默认用户 `default` 是关闭的，不能用于业务连接。请先在部署单元详情的「业务账号」区域创建业务账号，填写名称、可访问的 Key 范围、命令类别；口令留空时由控制台自动生成。

**使用 sycache-cli 连接**（在已安装数据面包的主机上执行，`sycache-cli` 已安装到 `/usr/bin`）：

```bash
sycache-cli -p <实例端口>
```

连接时的主机与账号口令参数，以 `sycache-cli --help` 的输出为准。若部署单元开启了数据面 TLS，客户端也需要启用 TLS。

**使用 Redis 兼容客户端连接**：使用对应语言的 Redis 兼容客户端，填写实例地址、端口，以及上面创建的业务账号的用户名和口令即可。

## 常见问题

**1. 控制台只能在本机访问。**
默认监听地址是 `127.0.0.1:8443`。需要从其他主机访问时，设置 `CACHEOS_LISTEN`（例如 `0.0.0.0:8443`），并同时配置 HTTPS（`CACHEOS_TLS_AUTO=1` 或 `CACHEOS_TLS_CERT` / `CACHEOS_TLS_KEY`）。

**2. 浏览器提示证书不受信任。**
使用 `CACHEOS_TLS_AUTO=1` 时，证书由控制台内置的 Web CA 签发。请从控制台的 `/web-ca.crt` 下载该 CA 证书，导入浏览器的受信任根证书中（只需一次）。

**3. 创建部署单元时提示授权不包含某形态或业务链路 TLS。**
控制台返回 `LICENSE_FEATURE_DENIED`，提示「当前授权不包含 … 形态」或「当前授权不包含业务链路 TLS」。请导入包含相应功能的 License，或改选单机 / 主从形态，并关闭数据面 TLS。

**4. 接入命令执行失败，提示令牌无效。**
注册令牌是一次性的，并有过期时间。请回到「主机」页面重新生成接入命令。

**5. 客户端连接后认证失败。**
默认用户 `default` 是关闭的。请使用「业务账号」中创建的账号的用户名和口令连接；账号可访问的 Key 范围与命令类别以创建时的设置为准。
