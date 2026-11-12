# CacheOS 部署手册

本手册面向运维人员，结构参照 Redis 官方「安装 / 运维（Install / Administration）」文档：先说明安装方式，再讲配置、启停、验证、持久化与备份、升级、排障。

数据面 **sycache** 兼容 Redis 协议，命令、配置项、`INFO` 输出、RDB/AOF 格式与上游保持一致，业务客户端不需要改代码。

> 本手册的命令、路径、参数均取自本仓库当前代码（`deploy/`、`dataplane/packaging/`、`cmd/`、`test/install/smoke.sh`）。
> 带 **【未经项目 CI 验证】** 标记的内容是参考做法，项目目前没有对应的发布物或自动化测试，上线前请先在测试环境走通。

---

## 目录

1. [部署形态与选型](#1-部署形态与选型)
2. [前置条件与主机准备](#2-前置条件与主机准备)
3. [获取安装包并校验](#3-获取安装包并校验)
   - [3.1 发版与打 tag 构建（维护者）](#31-发版与打-tag-构建维护者)
4. [形态 A：独立部署（仅数据面，不带控制台）](#4-形态-a独立部署仅数据面不带控制台)
5. [形态 B：带控制台部署（生产推荐）](#5-形态-b带控制台部署生产推荐)
6. [形态 C：Docker 部署](#6-形态-cdocker-部署)
7. [验证命令](#7-验证命令)
8. [配置说明](#8-配置说明)
9. [持久化与备份恢复](#9-持久化与备份恢复)
10. [升级与回滚](#10-升级与回滚)
11. [日常运维](#11-日常运维)
12. [常见问题处理](#12-常见问题处理)
13. [附：与 Redis 手册的对照](#13-附与-redis-手册的对照)

---

## 1. 部署形态与选型

| 形态 | 组件 | 适用场景 | 能做什么 | 不能做什么 |
| --- | --- | --- | --- | --- |
| **A 独立部署** | 仅 `sycache` 数据面包 | 开发测试、单机小业务、已有自研运维体系 | 与手工部署 Redis 一样，自己写配置、自己用 systemd 管理 | 没有统一界面、变更编排、主备切换编排、备份恢复、审计 |
| **B 带控制台部署** | 控制台 + 每台缓存主机装 agent / wrapper + `sycache` 数据面 | 生产环境 | 建实例、改参数、扩缩容、主备切换、备份恢复、升级、审计 | — |
| **C Docker** | GHCR 的 `sycache` / `sycache-console` 镜像 | 测试、演示、控制台容器化 | 见第 6 节 | agent 依赖 systemd 与 root wrapper，**不支持容器内运行**，容器内的实例无法被控制台纳管 |

实例形态（独立部署需自行搭建，控制台部署由控制台创建）：单机、主从、哨兵、原生集群。

**组件对应关系**

| 程序 | 安装位置 | 说明 |
| --- | --- | --- |
| `sycache-server` / `sycache-sentinel` | `/home/atwasoft/sycache/bin/` | 数据面服务端、哨兵 |
| `sycache-cli` / `sycache-benchmark` / `sycache-check-rdb` / `sycache-check-aof` | 同上 | 运维工具，参数与上游一致 |
| `sycache-agent` | `/usr/bin/sycache-agent` | 缓存主机上的代理，只主动连控制台，不监听任何端口 |
| `sycache-wrapper` | `/usr/libexec/sycache/sycache-wrapper` | agent 唯一的提权入口，只接受 `start|stop|restart|switch-version` |
| `sycache-console` | `/usr/bin/sycache-console` | Web 控制台、gRPC 网关、内置 CA |
| `sycache-ctl` | 任意运维机 | 只读命令行，见 [`ctl.md`](ctl.md) |

---

## 2. 前置条件与主机准备

### 2.1 系统要求

- Linux，x86_64 或 aarch64；使用 systemd。
- 数据面二进制只依赖 glibc ≥ 2.17 与 `libsystemd.so.0`，已在 CentOS 7、openEuler、Debian/Ubuntu 等发行版上做过安装冒烟（见 `test/install/smoke.sh`）。
- 安装与配置需要 root。

### 2.2 端口

| 端口 | 方向 | 用途 | 默认 |
| --- | --- | --- | --- |
| 实例端口（如 `7001`） | 客户端 → 缓存主机 | 业务访问；控制台创建的实例端口以控制台显示为准 | 控制台分配，格式 4–5 位数字 |
| 集群总线端口 | 缓存主机之间 | 集群形态使用，为数据端口 + 10000（上游约定） | — |
| `8443` | 运维人员 → 控制台 | Web 控制台 / API | 仅监听 `127.0.0.1` |
| `9443` | 缓存主机 → 控制台 | agent 的 gRPC 连接 | `0.0.0.0:9443` |

防火墙只需放行：运维网段 → 控制台 8443、缓存主机 → 控制台 9443、业务网段 → 实例端口。缓存主机无需对控制台开放任何入站端口。

### 2.3 内核与系统调优

DEB/RPM 安装后会自动应用以下设置，tar.gz 安装或容器场景请手工设置（与 Redis 官方「Administration」章节的建议一致）：

```bash
# /usr/lib/sysctl.d/90-sycache.conf 的内容
sudo sysctl -w vm.overcommit_memory=1        # fork 持久化（BGSAVE / AOF 重写）需要
sudo sysctl -w net.core.somaxconn=4096       # 配合 tcp-backlog

# 透明大页设为 madvise（包内的 sycache-thp.service 会开机自动设置）
echo madvise | sudo tee /sys/kernel/mm/transparent_hugepage/enabled
echo madvise | sudo tee /sys/kernel/mm/transparent_hugepage/defrag
```

另外：

- 文件描述符：systemd 单元已设置 `LimitNOFILE=65535`；手工启动请 `ulimit -n 65535`。
- 容量规划：开启持久化的实例，内存按「数据量 × 2」预留，应对 fork 时的写时复制。
- 不要启用 swap 给缓存实例使用；`maxmemory` 要小于容器 / cgroup 内存上限。

---

## 3. 获取安装包并校验

数据面安装包来自 GitHub Release（每个 `v*` 标签发布），详见 [`release-assets.md`](release-assets.md)。

```bash
export VERSION=0.1.0                          # 不带 v
export BASE="https://github.com/xiaoguan521/cacheos/releases/download/v${VERSION}"

curl -fLO "${BASE}/SHA256SUMS"
curl -fLO "${BASE}/sycache_${VERSION}-1_amd64.deb"      # Debian/Ubuntu x86_64
# curl -fLO "${BASE}/sycache-${VERSION}-1.x86_64.rpm"   # RHEL / openEuler x86_64
# curl -fLO "${BASE}/sycache-${VERSION}-x86_64.tar.gz"  # 通用归档
# ARM64 把 amd64 / x86_64 换成 arm64 / aarch64

sha256sum -c SHA256SUMS --ignore-missing      # 必须看到 OK 再安装
```

> 注意：当前 Release 只发布数据面 `sycache` 包。控制台 `sycache-console`、`sycache-agent`、`sycache-wrapper` 三个二进制不在 Release 资产内，请使用交付渠道提供的文件，或从源码构建（见 5.1）。

---

## 3.1 发版与打 tag 构建（维护者）

发版由 `.github/workflows/binary-packages.yml` 完成，**只在 `v*` 标签或 Actions 手动触发时运行**，不随 PR 或日常提交触发。

**发版步骤**

```bash
# 1. dataplane/versions.yaml 的 current 必须等于要发布的版本，且对应真实代码变化
# 2. 本地先跑完整校验
make ci UPSTREAM_SRC=/tmp/valkey-src
# 3. 打标签（标签去掉 v 必须等于 versions.yaml 的 current，否则构建在校验步骤失败）
git tag v0.1.0 && git push origin v0.1.0
```

预发布版本用带连字符的版本号（如 `v1.0.0-rc1`），Release 会标记为 prerelease，且不成为 latest。

**工作流做了什么**

| 阶段 | 内容 | 产物 |
| --- | --- | --- |
| `publish-builder` | x86_64、aarch64 各构建一次 glibc 2.17 兼容**编译环境**镜像并推送 GHCR | `ghcr.io/xiaoguan521/cacheos/build-env:sha-<提交>-<架构>`；v* 标签时另加 `el7-<架构>` |
| `build` | 拉取并校验上游源码哈希，在该编译环境内构建，运行 VEX、改名黄金验收、版本一致性门禁，生成校验和 | tar.gz、DEB、RPM、调试符号包、SPDX SBOM、BUILDINFO、SHA256SUMS（Actions artifact，保留 90 天） |
| `publish-builder-manifest` | 合并多架构清单 | `build-env:sha-<提交>`；v* 标签时另加 `build-env:el7` |
| `publish-images` | 每个架构用 `build` 产出的 tar.gz 构建**数据面运行时镜像**（`dataplane/docker/Dockerfile`），并从源码构建**控制台镜像**（`deploy/docker/console.Dockerfile`）；起容器做冒烟（数据面 ping / set / get，控制台 `/healthz`）通过后才推送 | `sycache:sha-<提交>-<架构>`、`sycache-console:sha-<提交>-<架构>` |
| `publish-images-manifest` | 合并多架构清单 | `sha-<提交>`；v* 标签时另加 `<版本>`，正式版再加 `latest` |
| `publish-release`（仅 v* 标签） | 创建 GitHub Release，上传全部资产并重新生成全量 `SHA256SUMS` | 见 [`release-assets.md`](release-assets.md) |

**GHCR 镜像一览**（`ghcr.io/xiaoguan521/cacheos/` 下，路径全小写）

| 镜像 | 用途 | 标签 |
| --- | --- | --- |
| `sycache` | 数据面运行时镜像：`sycache-server`、`sycache-sentinel`、`sycache-cli` 等 | `<版本>`、`latest`（仅正式版）、`sha-<提交>` |
| `sycache-console` | 控制台运行时镜像 | 同上 |
| `build-env` | glibc 2.17 **编译环境**，只用于编译，不能运行服务 | `el7`、`el7-<架构>`、`sha-<提交>` |

- 手动触发（非标签）只推送不可变的 `sha-` 标签，不会改动 `<版本>` 与 `latest`。
- 镜像在每个架构的原生 runner 上构建，`<版本>` 标签是 x86_64 + aarch64 的多架构清单。
- 预发布版本（带连字符）不更新 `latest`。
- **本节工作流是新增的，尚未在真实 v* 标签上跑过**；首次发版后请先核对 Actions 日志与 `docker pull` 结果。
- agent、wrapper 依赖 systemd 与 root，**不做成镜像**。

**使用已发布的编译环境，跳过本机构建**

```bash
CACHEOS_BUILD_IMAGE=ghcr.io/xiaoguan521/cacheos/build-env:el7 make compat-build
```

GHCR 包首次发布后，需要在各包的 Package settings 里设为 Public（`build-env`、`sycache`、`sycache-console` 各设一次），匿名用户才能 `docker pull`。

**本地构建（等价于工作流的 build 阶段）**

```bash
make compat-build            # 需要 docker，产出 dist/ 下的 tar.gz / RPM / DEB
```

---

## 4. 形态 A：独立部署（仅数据面，不带控制台）

### 4.1 安装

**DEB**

```bash
sudo apt install "./sycache_${VERSION}-1_amd64.deb"
```

**RPM**

```bash
sudo dnf install "./sycache-${VERSION}-1.x86_64.rpm"      # 老系统用 yum
```

**tar.gz**（生产优先用 DEB/RPM，以便保留安装与升级记录）

```bash
sudo tar -C / -xzf "sycache-${VERSION}-x86_64.tar.gz"
sudo groupadd -r sycache
sudo useradd -r -g sycache -d /home/atwasoft/sycache -s /sbin/nologin sycache
sudo install -d -o sycache -g sycache -m 2770 /home/atwasoft/sycache/instances
sudo install -d -o sycache -g sycache -m 0750 /var/log/sycache
sudo ln -sfn /home/atwasoft/sycache/bin/sycache-cli /usr/bin/sycache-cli
```

安装包会完成：

- 创建系统用户和组 `sycache`；
- 程序安装到 `/home/atwasoft/sycache/`（`bin/`、`share/BUILDINFO`、`versions/`、`current`）；
- 创建实例目录 `/home/atwasoft/sycache/instances`、日志目录 `/var/log/sycache`；
- 安装 systemd 模板单元 `sycache@.service`、`sycache-sentinel@.service`、`sycache-thp.service`；
- 安装 logrotate 配置（每日轮转，保留 14 份）与 sysctl 配置；
- 创建 `/usr/bin/sycache-cli` 指向当前版本。

**安装不会启动任何实例**。

确认安装结果：

```bash
sycache-server --version      # 应显示 SyCache server v=<版本> ...
id sycache
ls /home/atwasoft/sycache/bin
```

### 4.2 创建并启动单机实例（systemd 方式，推荐）

systemd 模板单元的实例名就是端口号，配置文件固定在 `instances/<端口>/sycache.conf`。以端口 `6379` 为例：

```bash
sudo install -d -o sycache -g sycache -m 0750 /home/atwasoft/sycache/instances/6379

sudo -u sycache tee /home/atwasoft/sycache/instances/6379/sycache.conf >/dev/null <<'EOF'
bind 0.0.0.0
port 6379
protected-mode yes
dir /home/atwasoft/sycache/instances/6379
logfile /var/log/sycache/6379.log
pidfile /run/sycache/sycache-6379.pid
daemonize no
supervised systemd
maxmemory 2gb
maxmemory-policy allkeys-lru
appendonly yes
appendfsync everysec
save 3600 1 300 100 60 10000
# 必须设置口令或 ACL，否则 protected-mode 会拒绝外部连接
requirepass CHANGE_ME_TO_A_LONG_RANDOM_PASSWORD
EOF

sudo chmod 0600 /home/atwasoft/sycache/instances/6379/sycache.conf
sudo systemctl enable --now sycache@6379
sudo systemctl status sycache@6379
```

> 单元以 `User=sycache`、`ProtectSystem=strict` 运行，仅 `instances/<端口>` 与 `/var/log/sycache` 可写。配置里的 `dir`、`logfile`、`pidfile` 必须落在这些目录（pidfile 在 `/run/sycache/`）内。
> 单元使用 `Type=notify`，加载大数据集时 `TimeoutStartSec=infinity`，`status` 会停在 activating 直到加载完成。

完整的默认配置模板随包安装：`/home/atwasoft/sycache/conf/sycache.conf.default` 与 `sycache-sentinel.conf.default`，可复制后按需修改（参数默认值与上游一致）。

### 4.3 手工启动（不用 systemd，仅限测试）

与 `test/install/smoke.sh` 一致：

```bash
B=/home/atwasoft/sycache/bin
sudo install -d -o sycache -g sycache /home/atwasoft/sycache/instances/7001
sudo -u sycache $B/sycache-server --port 7001 --daemonize yes \
  --dir /home/atwasoft/sycache/instances/7001 \
  --logfile /var/log/sycache/7001.log \
  --pidfile /home/atwasoft/sycache/instances/7001/pid
```

### 4.4 搭建主从

在从节点配置中加入（需要时同时配置 `masterauth`）：

```conf
replicaof 10.0.0.11 6379
masterauth CHANGE_ME_TO_A_LONG_RANDOM_PASSWORD
```

或运行时执行：

```bash
sycache-cli -h 10.0.0.12 -p 6379 -a '口令' replicaof 10.0.0.11 6379
```

验证复制见 7.3。

### 4.5 搭建哨兵（三个及以上，奇数个）

每个哨兵主机创建 `instances/26379/sycache-sentinel.conf`（哨兵必须能写自己的配置文件，因此路径放在可写的实例目录内）：

```conf
port 26379
daemonize no
dir /home/atwasoft/sycache/instances/26379
logfile /var/log/sycache/26379.log
sentinel monitor mymaster 10.0.0.11 6379 2
sentinel auth-pass mymaster CHANGE_ME_TO_A_LONG_RANDOM_PASSWORD
sentinel down-after-milliseconds mymaster 5000
sentinel failover-timeout mymaster 60000
```

```bash
sudo install -d -o sycache -g sycache -m 0750 /home/atwasoft/sycache/instances/26379
sudo systemctl enable --now sycache-sentinel@26379
sycache-cli -p 26379 sentinel master mymaster
```

### 4.6 搭建原生集群

至少 3 个主节点（建议 3 主 3 从）。每个节点的配置加入：

```conf
cluster-enabled yes
cluster-config-file nodes-6379.conf
cluster-node-timeout 15000
```

所有节点启动后，在任意一台执行：

```bash
sycache-cli --cluster create \
  10.0.0.11:6379 10.0.0.12:6379 10.0.0.13:6379 \
  10.0.0.14:6379 10.0.0.15:6379 10.0.0.16:6379 \
  --cluster-replicas 1 -a '口令'
```

> 使用 `sycache-cli --cluster` 搭建集群的参数与上游一致，但项目 CI 没有专门覆盖这一手工流程，**【未经项目 CI 验证】**；需要受支持的集群部署请用形态 B 由控制台创建。

---

## 5. 形态 B：带控制台部署（生产推荐）

整体流程：**装控制台 → 初始化 → 导入授权与数据面包 → 各缓存主机装 agent 并注册 → 在控制台创建实例**。

### 5.1 准备二进制与交付物

- 二进制：`sycache-console`（控制台主机）、`sycache-agent` 与 `sycache-wrapper`（每台缓存主机）。
- 交付物：License 授权文件；数据面包及厂商签名文件（`.sig`）。
- 部署脚本在仓库 `deploy/` 目录：`install-console.sh`、`install-agent.sh` 以及对应的 systemd 单元和 sudoers 文件。

从源码构建时（需要 Go 与前端构建环境，前端先构建，控制台才会内嵌 Web 界面）：

```bash
git clone https://github.com/xiaoguan521/cacheos.git && cd cacheos
(cd web && pnpm install --offline && pnpm run build)
mkdir -p out
go build -o out/ ./cmd/sycache-console ./cmd/sycache-agent ./cmd/sycache-wrapper ./cmd/sycache-ctl
./out/sycache-console --version
```

### 5.2 安装控制台

在控制台主机上，以 root 执行：

```bash
sudo deploy/install-console.sh /path/to/sycache-console /path/to/console.env
```

脚本完成：创建 `sycache-console` 用户和组；安装 `/usr/bin/sycache-console` 与 systemd 单元；创建数据目录 `/var/lib/sycache-console`（0700）；创建 `/etc/cacheos/`（0750）并安装环境文件。`console.env` 参数可省略，之后手工编辑 `/etc/cacheos/console.env`。

**最小生产配置**（`/etc/cacheos/console.env`，权限 0640，属主 root:sycache-console）：

```bash
CACHEOS_LISTEN=0.0.0.0:8443
CACHEOS_TLS_AUTO=1
CACHEOS_GRPC_LISTEN=0.0.0.0:9443
CACHEOS_GRPC_HOSTS=console.example.internal
CACHEOS_GRPC_ADVERTISE=console.example.internal:9443
```

要点：

- `CACHEOS_GRPC_HOSTS` 必须包含 agent 访问控制台时使用的域名或 IP，否则 agent 校验证书失败。
- `CACHEOS_GRPC_ADVERTISE` 会写进「生成接入命令」里，务必是缓存主机能解析并访问的 `host:port`。
- 监听非本机地址时务必启用 HTTPS（`CACHEOS_TLS_AUTO=1`，或 `CACHEOS_TLS_CERT` + `CACHEOS_TLS_KEY`），否则登录凭据明文传输，启动日志会告警。
- 用 PostgreSQL 代替默认的 SQLite：`CACHEOS_DB=postgres://user:pass@host:5432/cacheos`。多台控制台共用同一 PostgreSQL 时，必须共用同一套 CA 与 `secret.key`，否则启动被拒绝。

完整环境变量：

| 变量 | 默认 | 说明 |
| --- | --- | --- |
| `CACHEOS_LISTEN` | `127.0.0.1:8443` | Web / API 监听地址 |
| `CACHEOS_TLS_CERT` / `CACHEOS_TLS_KEY` | 空 | 自带证书 |
| `CACHEOS_TLS_AUTO` | 关 | `1` = 内置 Web CA 自动签发 HTTPS 证书 |
| `CACHEOS_WEB_HOSTS` | 空 | 自动证书的主机名 / IP，默认取 gRPC 主机名加 `localhost` |
| `CACHEOS_GRPC_LISTEN` | `0.0.0.0:9443` | agent 连接入口 |
| `CACHEOS_GRPC_HOSTS` | 空 | gRPC 证书包含的主机名 / IP |
| `CACHEOS_GRPC_ADVERTISE` | 空 | 接入命令里的控制台地址 |
| `CACHEOS_DATA` | `/var/lib/sycache-console` | 数据目录（数据库、CA、`secret.key`） |
| `CACHEOS_DB` | `<数据目录>/console.db` | SQLite 路径或 `postgres://` DSN |
| `CACHEOS_BACKUP_DIR` | `<数据目录>/backups` | 实例备份目录，可指向 NFS 挂载点 |
| `CACHEOS_BACKUP_S3_ENDPOINT` / `_BUCKET` / `_REGION` / `_ACCESS_KEY` / `_SECRET_KEY` / `_PREFIX` | 空 | 备份存到 S3 兼容存储；密钥只从环境读取，不要写进命令行 |

启动：

```bash
sudo systemctl enable --now sycache-console
sudo systemctl status sycache-console
curl -k https://127.0.0.1:8443/healthz        # 期望 HTTP 200，见第 7 节
```

> `/var/lib/sycache-console`（数据库、`pki/` 内置 CA、`secret.key`）是控制台的全部状态，**必须备份**，丢失后已注册的 agent 与加密数据无法恢复。

### 5.3 初始化控制台

浏览器打开 `https://<控制台地址>:8443`，进入 `/setup`：

1. **创建管理员**：用户名小写字母、数字、连字符，2–63 位；密码至少 8 位。提交后显示 TOTP 密钥，立刻导入身份验证器，之后敏感操作（创建、切换、删除、备份恢复等）都要 6 位验证码。
2. **导入 License**：可稍后在「授权与系统」页面导入。未授权的形态（哨兵、集群）或功能（业务链路 TLS）无法创建。

若使用 `CACHEOS_TLS_AUTO=1`，浏览器提示证书不受信任时，下载 `https://<控制台>:8443/web-ca.crt` 并导入受信任根证书。

### 5.4 导入数据面包

控制台「授权与系统」→「数据面版本库」→ 导入：选择数据面包及对应 `.sig` 签名文件。控制台校验 SHA-256 与 Ed25519 签名，通过后才入库。

### 5.5 缓存主机：安装 agent 并注册

每台缓存主机，以 root 执行：

```bash
sudo deploy/install-agent.sh /path/to/sycache-agent /path/to/sycache-wrapper
```

脚本完成：创建 `sycache`、`sycache-agent` 用户与目录；安装 agent、wrapper、systemd 单元，以及仅允许 agent 以 root 调用 wrapper 四个子命令的 sudoers 规则（`/etc/sudoers.d/sycache-agent`）。**不要放宽该 sudoers 规则**。

然后在控制台「主机」页面点击「生成接入命令」，把输出的命令在缓存主机上执行：

```bash
sudo -u sycache-agent sycache-agent enroll \
  -server '<控制台gRPC地址>' -token '<令牌>' -ca-fingerprint '<CA指纹>' \
  && sudo systemctl enable --now sycache-agent
```

- 令牌一次性、有过期时间；失败或过期就重新生成。
- `-ca-fingerprint` 用来确认连接的是真控制台，不要跳过。
- 同时还需在缓存主机上安装数据面包（`sycache`，见 4.1 的 DEB/RPM），因为实例二进制与 `sycache-cli` 来自它；版本切换由控制台通过 wrapper 完成。
- 主机出现在「主机」列表，状态在线后即可分配实例。

### 5.6 创建实例并连接

「部署单元」→「创建部署单元」：选择形态（单机 / 主从 / 哨兵 / 集群）、内存、副本数、是否数据面 TLS、参数模板。创建为异步操作，在「操作记录」查看进度。

实例默认用户 `default` 是**关闭**的，先在部署单元详情的「业务账号」创建账号（口令留空则自动生成），再连接：

```bash
sycache-cli -h <主机> -p <实例端口> --user <账号> -a '<口令>' ping
```

开启数据面 TLS 的实例，客户端需同时启用 TLS（`--tls`，并按需配 `--cacert`）。

### 5.7 控制台高可用（可选）

多台控制台共用同一 PostgreSQL，并共用同一套 CA 与 `secret.key`。需要授权中包含高可用功能。`/healthz` 在主控制台返回 200、备控制台或数据库不可用时返回 503，可直接给 keepalived / 负载均衡做探测。

### 5.8 运维命令行 `sycache-ctl`

只读，用 API 令牌访问控制台，适合脚本与巡检，用法见 [`ctl.md`](ctl.md)。

---

## 6. 形态 C：Docker 部署

**要点（先读）**

- 镜像由 `v*` 标签的发布工作流构建并推送到 GHCR（见 3.1）：
  `ghcr.io/xiaoguan521/cacheos/sycache:<版本>` 与 `ghcr.io/xiaoguan521/cacheos/sycache-console:<版本>`，支持 amd64 / arm64。
- agent 通过 systemd 与 root wrapper 管理本机实例，**不能在容器内运行**，所以容器里的实例**不会被控制台纳管**。容器形态适合：测试、演示、控制台容器化、不需要控制台纳管的独立实例。生产的受管实例请用第 5 节。
- 工作流含容器冒烟测试，但新增的镜像发布流程尚未在真实标签上跑过，首次使用请先在测试环境验证。

### 6.1 拉取镜像

```bash
export VERSION=0.1.0
docker pull ghcr.io/xiaoguan521/cacheos/sycache:${VERSION}
docker pull ghcr.io/xiaoguan521/cacheos/sycache-console:${VERSION}
```

如提示未授权，说明包还是私有：在 GitHub Package settings 设为 Public，或先 `docker login ghcr.io`（用户名 + 具有 `read:packages` 的 PAT）。

### 6.2 数据面容器（独立实例）

镜像内容：

- 程序在 `/home/atwasoft/sycache`，并链接到 `/usr/local/bin`：`sycache-server`、`sycache-sentinel`、`sycache-cli`、`sycache-benchmark`、`sycache-check-rdb`、`sycache-check-aof`。
- 以非 root 用户 `sycache`（uid/gid 999）运行，数据目录 `/data`（`VOLUME`），端口 `6379`。
- 默认命令：`--port 6379 --bind 0.0.0.0 --dir /data --appendonly yes --protected-mode yes`。**自定义参数时会替换整个默认命令**，请把需要的参数补全。
- 前台运行，不要加 `--daemonize yes`。

```bash
docker volume create sycache-data

docker run -d --name sycache \
  -p 6379:6379 \
  -v sycache-data:/data \
  --restart unless-stopped \
  ghcr.io/xiaoguan521/cacheos/sycache:${VERSION} \
  --port 6379 --bind 0.0.0.0 --dir /data --appendonly yes \
  --requirepass 'CHANGE_ME_TO_A_LONG_RANDOM_PASSWORD' \
  --maxmemory 1gb --maxmemory-policy allkeys-lru

docker exec -it sycache sycache-cli -a 'CHANGE_ME_TO_A_LONG_RANDOM_PASSWORD' ping
```

用配置文件启动（推荐，口令不出现在进程列表）：

```bash
docker run -d --name sycache -p 6379:6379 \
  -v sycache-data:/data \
  -v $PWD/sycache.conf:/etc/sycache/sycache.conf:ro \
  ghcr.io/xiaoguan521/cacheos/sycache:${VERSION} /etc/sycache/sycache.conf
```

配置文件里要写 `dir /data`、`bind 0.0.0.0`、`daemonize no`，且不要用 `supervised systemd`。

容器内常用工具：

```bash
docker exec sycache sycache-server --version
docker exec -it sycache sycache-cli -a '口令'
docker exec sycache sycache-check-rdb /data/dump.rdb
```

注意：

- 镜像的 `HEALTHCHECK` 把 `PONG` 与 `NOAUTH` 都视为健康，所以设置了口令也能正常探测。
- 宿主机仍需设置 `vm.overcommit_memory=1`（容器共享宿主内核，见 2.3）。
- `--maxmemory` 必须小于 `docker run --memory` 的限制，否则可能被 OOM kill；开启持久化再预留 fork 的写时复制余量。
- 主从、哨兵、集群在容器里要让对端能访问到：使用 `--network host`，或设置 `replica-announce-ip`、`cluster-announce-ip`、`cluster-announce-port` 等上游参数。
- 日志默认输出到标准输出，用 `docker logs sycache` 查看。

### 6.3 控制台容器

镜像内容：非 root 用户 `sycache-console`（uid/gid 10001）；默认 `CACHEOS_LISTEN=0.0.0.0:8443`、`CACHEOS_GRPC_LISTEN=0.0.0.0:9443`、`CACHEOS_DATA=/var/lib/sycache-console`（`VOLUME`）；环境变量与 5.2 一致。

```bash
docker volume create sycache-console-data

docker run -d --name sycache-console \
  -p 8443:8443 -p 9443:9443 \
  -v sycache-console-data:/var/lib/sycache-console \
  -e CACHEOS_TLS_AUTO=1 \
  -e CACHEOS_GRPC_HOSTS=console.example.internal \
  -e CACHEOS_GRPC_ADVERTISE=console.example.internal:9443 \
  --restart unless-stopped \
  ghcr.io/xiaoguan521/cacheos/sycache-console:${VERSION}

curl -k https://127.0.0.1:8443/healthz
docker logs sycache-console | tail -20
```

要点：

- 对外监听非本机地址时务必启用 HTTPS（`CACHEOS_TLS_AUTO=1` 或自带证书）。
- `CACHEOS_GRPC_HOSTS` 必须包含 agent 访问控制台的域名 / IP；`CACHEOS_GRPC_ADVERTISE` 是写进接入命令的地址（容器映射出的 `host:9443`）。
- **数据卷必须持久化并备份**（`console.db`、`pki/`、`secret.key`）；丢失后所有 agent 需要重新注册。
- 外部 PostgreSQL：`-e CACHEOS_DB='postgres://user:pass@host:5432/cacheos'`。
- 缓存主机上的 agent 仍按 5.5 安装在宿主机，并连接到容器映射出的 `9443`。
- 备份目录或 S3 变量同 5.2；本地目录备份需额外挂载卷并设置 `CACHEOS_BACKUP_DIR`。
- 升级：换新标签重新 `docker run`，数据卷保持不变。升级前先备份数据卷。

### 6.4 docker compose 示例（控制台 + PostgreSQL）

```yaml
services:
  postgres:
    image: postgres:16
    environment:
      POSTGRES_DB: cacheos
      POSTGRES_USER: cacheos
      POSTGRES_PASSWORD: ${PG_PASSWORD:?set PG_PASSWORD}
    volumes: ["pgdata:/var/lib/postgresql/data"]
    restart: unless-stopped
  console:
    image: ghcr.io/xiaoguan521/cacheos/sycache-console:${VERSION:?set VERSION}
    depends_on: [postgres]
    ports: ["8443:8443", "9443:9443"]
    environment:
      CACHEOS_TLS_AUTO: "1"
      CACHEOS_GRPC_HOSTS: console.example.internal
      CACHEOS_GRPC_ADVERTISE: console.example.internal:9443
      CACHEOS_DB: postgres://cacheos:${PG_PASSWORD}@postgres:5432/cacheos?sslmode=disable
    volumes: ["console-data:/var/lib/sycache-console"]
    restart: unless-stopped
  sycache:
    image: ghcr.io/xiaoguan521/cacheos/sycache:${VERSION}
    command: ["--port","6379","--bind","0.0.0.0","--dir","/data","--appendonly","yes","--requirepass","${SYCACHE_PASSWORD:?set SYCACHE_PASSWORD}"]
    ports: ["6379:6379"]
    volumes: ["sycache-data:/data"]
    restart: unless-stopped
volumes: {pgdata: {}, console-data: {}, sycache-data: {}}
```

> 示例中 PostgreSQL 的 `sslmode=disable` 仅限同一 compose 网络内；跨主机请启用 TLS。
> 本 compose 示例本身未经 CI 验证。

### 6.5 自行构建镜像

不想用 GHCR 时，在本地用同样的 Dockerfile 构建：

```bash
# 控制台（构建上下文为仓库根目录，依赖已 vendor）
docker build -f deploy/docker/console.Dockerfile -t sycache-console:dev .

# 数据面：先按第 3 节下载并校验 tar.gz，放进一个目录作为构建上下文
docker build -f dataplane/docker/Dockerfile \
  --build-arg TARBALL=sycache-${VERSION}-x86_64.tar.gz \
  -t sycache:dev ./dist
```

---

## 7. 验证命令

### 7.1 软件与服务

```bash
sycache-server --version                       # SyCache server v=<版本> ...
sycache-cli --version
systemctl is-active sycache@6379               # active
journalctl -u sycache@6379 -n 50 --no-pager
ss -lntp | grep -E ':6379|:8443|:9443'         # 端口在监听
```

### 7.2 读写与基础信息（对应 Redis 的 `redis-cli ping`）

```bash
sycache-cli -p 6379 -a '口令' ping                      # PONG
sycache-cli -p 6379 -a '口令' set deploy:check ok
sycache-cli -p 6379 -a '口令' get deploy:check          # "ok"
sycache-cli -p 6379 -a '口令' del deploy:check
sycache-cli -p 6379 -a '口令' info server | head -20
sycache-cli -p 6379 -a '口令' info memory | grep -E 'used_memory_human|maxmemory_human'
sycache-cli -p 6379 -a '口令' config get maxmemory
sycache-cli -p 6379 -a '口令' --latency                 # 持续测延迟，Ctrl-C 退出
sycache-cli -p 6379 -a '口令' --bigkeys                 # 采样大 key
```

> `INFO` 里的 `redis_version` 等字段保持上游值，这是为了让客户端、监控与驱动继续识别。

### 7.3 复制

```bash
# 主：role=master，connected_slaves 为从节点数
sycache-cli -h 主 -p 6379 -a '口令' info replication

# 从：master_link_status:up 才算同步完成
sycache-cli -h 从 -p 6379 -a '口令' info replication | grep -E 'role|master_link_status|master_sync_in_progress|slave_repl_offset'

# 主写、从读
sycache-cli -h 主 -a '口令' set smoke:k v
sycache-cli -h 从 -a '口令' get smoke:k
```

### 7.4 哨兵

```bash
sycache-cli -p 26379 sentinel master mymaster
sycache-cli -p 26379 sentinel replicas mymaster
sycache-cli -p 26379 sentinel sentinels mymaster
sycache-cli -p 26379 sentinel get-master-addr-by-name mymaster
```

### 7.5 集群

```bash
sycache-cli -c -h 任一节点 -p 6379 -a '口令' cluster info | head -3     # cluster_state:ok
sycache-cli -h 任一节点 -p 6379 -a '口令' cluster nodes
sycache-cli --cluster check 任一节点:6379 -a '口令'
```

### 7.6 持久化文件检查

```bash
sycache-cli -p 6379 -a '口令' save
cd /home/atwasoft/sycache/instances/6379
sycache-check-rdb dump.rdb                     # 期望输出含 RDB looks OK
sycache-check-aof appendonly.aof               # 或 appenddirname 指定的目录内文件
```

### 7.7 压测（只在测试环境，不要压生产）

```bash
sycache-benchmark -p 6379 -a '口令' -c 50 -n 100000 -t set,get -q
```

### 7.8 控制台与 agent

```bash
curl -k https://<控制台>:8443/healthz          # 200: {"status":"ok","role":"leader","db":"ok",...}
                                                # 503: 备控制台或数据库不可用
sycache-console --version
systemctl is-active sycache-console sycache-agent
journalctl -u sycache-agent -n 50 --no-pager   # 看到与控制台连接成功、无证书错误
sycache-ctl --ca-file /etc/sycache/ca.pem status
sycache-ctl --ca-file /etc/sycache/ca.pem hosts ls     # 主机在线
sycache-ctl --ca-file /etc/sycache/ca.pem units ls     # 部署单元 health 为 ok
```

### 7.9 上线验收清单

| 项目 | 命令 / 方法 | 通过标准 |
| --- | --- | --- |
| 进程 | `systemctl is-active` | active |
| 连通 | `sycache-cli ping` | PONG |
| 读写 | set / get / del | 一致 |
| 认证 | 无口令执行 `ping` | `NOAUTH` 或认证失败 |
| 内存 | `info memory` | `maxmemory` 已设置且合理 |
| 持久化 | `info persistence` | `aof_enabled` / `rdb_last_bgsave_status:ok` |
| 复制 | `info replication` | `master_link_status:up` |
| 故障恢复 | `systemctl kill -s KILL sycache@端口` | 5 秒内被 systemd 拉起 |
| 日志 | `/var/log/sycache/<端口>.log` | 无 ERROR / WARNING 异常 |
| 内核参数 | `sysctl vm.overcommit_memory` | 1 |
| 备份 | 控制台触发一次备份并恢复到测试环境 | 数据一致 |

---

## 8. 配置说明

### 8.1 配置文件与路径

| 内容 | 路径 |
| --- | --- |
| 程序目录 | `/home/atwasoft/sycache/`（`bin/`、`versions/<版本>`、`current` 软链） |
| 实例目录 | `/home/atwasoft/sycache/instances/<端口>/`（配置、数据、RDB/AOF） |
| 实例配置 | `instances/<端口>/sycache.conf`；哨兵为 `sycache-sentinel.conf` |
| 实例日志 | `/var/log/sycache/<端口>.log`（logrotate 每日轮转，保留 14 份） |
| PID / 运行时 | `/run/sycache/` |
| 控制台数据 | `/var/lib/sycache-console/`（`console.db`、`pki/`、`secret.key`、`backups/`） |
| 控制台配置 | `/etc/cacheos/console.env` |
| agent 状态 | `/var/lib/sycache-agent/` |

### 8.2 修改参数

- 独立部署：编辑 `sycache.conf`，运行时用 `CONFIG SET`，再 `CONFIG REWRITE` 落盘。
- 控制台部署：在控制台的「参数模板」或实例参数页修改，由控制台下发。

**sycache 没有 reload 信号**。可热改的参数用 `CONFIG SET` + `CONFIG REWRITE`；不可热改的参数（如 `port`、`cluster-enabled`、`bind`）需要重启，控制台会按滚动重启处理。

```bash
sycache-cli -p 6379 -a '口令' config set maxmemory 4gb
sycache-cli -p 6379 -a '口令' config rewrite
```

### 8.3 安全基线

- 实例必须设置口令或 ACL；`default` 用户不要给业务使用。
- `bind` 只绑定业务网卡，不要对公网暴露；防火墙只放行业务网段。
- 配置文件含口令，权限设为 0600。
- 控制台对外必须启用 HTTPS；管理员必须绑定 TOTP。
- 不要在命令行用 `-a '口令'` 做长期脚本（会出现在进程列表）；改用环境变量 `SYCACHECLI_AUTH`（兼容旧名 `REDISCLI_AUTH`）。
- 授权失效不会停止任何已运行实例。

---

## 9. 持久化与备份恢复

### 9.1 持久化建议

| 场景 | 建议 |
| --- | --- |
| 纯缓存、可回源 | 关闭 AOF，保留较宽松的 RDB，或都关闭 |
| 需要尽量少丢数据 | `appendonly yes` + `appendfsync everysec` |
| 主从 | 主节点开 AOF；从节点至少开 RDB |

### 9.2 独立部署备份

```bash
sycache-cli -p 6379 -a '口令' bgsave
sycache-cli -p 6379 -a '口令' info persistence | grep -E 'rdb_bgsave_in_progress|rdb_last_bgsave_status'
sudo cp /home/atwasoft/sycache/instances/6379/dump.rdb /backup/dump-$(date +%F).rdb
```

恢复：停实例 → 替换 `dump.rdb`（属主 `sycache`）→ 启动实例；若开了 AOF，需同时处理 AOF 文件，否则启动时以 AOF 为准。

### 9.3 控制台备份与恢复

- 实例数据备份：由控制台触发，存放在 `CACHEOS_BACKUP_DIR`（可指向 NFS）或 S3 兼容存储。
- 控制台自身备份：`sycache-console backup` / `sycache-console restore`，备份口令只从文件或 `CACHEOS_BACKUP_PASSPHRASE` 读取，不接受命令行参数。

```bash
export CACHEOS_BACKUP_PASSPHRASE='...'
sudo -E -u sycache-console sycache-console backup --help
sudo -E -u sycache-console sycache-console restore --help
```

具体参数以 `--help` 输出为准。

---

## 10. 升级与回滚

### 10.1 数据面（独立部署）

DEB/RPM 升级**不会重启实例**，也不会改动 `current` 指向的版本：

```bash
sudo apt install "./sycache_<新版本>-1_amd64.deb"      # 或 dnf / yum
# 在业务低峰逐个重启，先从节点后主节点；哨兵或集群形态先确认复制状态
sudo systemctl restart sycache@6379
```

回滚：安装旧版本包，或在版本目录内切回旧版本后重启。保留上一版安装包。

### 10.2 数据面（控制台部署）

在控制台导入新版本包（含签名）→ 对部署单元发起滚动升级。底层通过 wrapper 的 `switch-version` 原子切换 `current` 软链并重启实例，随时可切回旧版本。

### 10.3 控制台与 agent

1. 备份 `/var/lib/sycache-console` 与 `/etc/cacheos`。
2. 替换 `/usr/bin/sycache-console`（再次执行 `install-console.sh` 即可，幂等），`systemctl restart sycache-console`。
3. agent 在已安装且签名通过的新版本存在时会自升级；手工升级再执行一次 `install-agent.sh`，然后 `systemctl restart sycache-agent`。重启 agent **不会**停止缓存实例。

### 10.4 卸载

DEB/RPM 在仍有 `sycache@*` 实例运行时**拒绝卸载**，先停实例（或在控制台停）。

```bash
sudo systemctl stop 'sycache@*' 'sycache-sentinel@*'
sudo apt remove sycache              # 或 sudo rpm -e sycache
```

实例目录与数据不会被自动删除，需要时手工清理。

---

## 11. 日常运维

```bash
# 启停（独立部署）
sudo systemctl start|stop|restart sycache@6379
sudo systemctl status sycache@6379

# 实时日志
tail -f /var/log/sycache/6379.log
journalctl -u sycache@6379 -f

# 慢查询、客户端、内存诊断
sycache-cli -p 6379 -a '口令' slowlog get 10
sycache-cli -p 6379 -a '口令' client list
sycache-cli -p 6379 -a '口令' memory doctor
sycache-cli -p 6379 -a '口令' info commandstats
sycache-cli -p 6379 -a '口令' info stats | grep -E 'evicted_keys|keyspace_(hits|misses)|rejected_connections'
```

**监控**：控制台在 `/api/v1` 下提供 Prometheus 文本指标，使用 API 令牌（最低只读角色）鉴权，可接入现有 Prometheus。重点告警项：内存使用率、`evicted_keys` 增长、`master_link_status`、复制延迟、AOF / RDB 失败、客户端连接数、慢查询、agent 离线。

---

## 12. 常见问题处理

### 12.1 实例与数据面

**Q1. `systemctl start sycache@端口` 一直 activating，或很久才 active。**
原因：实例在加载 RDB/AOF，`Type=notify` 会等到加载完成，且 `TimeoutStartSec=infinity`。
处理：`journalctl -u sycache@端口 -f` 与实例日志看 `Loading` 进度；超大数据集属正常。

**Q2. 启动失败：找不到配置文件 / 没有权限。**
检查：`/home/atwasoft/sycache/instances/<端口>/sycache.conf` 是否存在且属主为 `sycache`；`dir`、`logfile` 是否在 `instances/<端口>` 或 `/var/log/sycache` 内（单元用 `ProtectSystem=strict`，其他路径只读）。

**Q3. 日志出现 `Can't open the log file: Permission denied` 或 RDB 保存失败。**
同上：`ReadWritePaths` 之外的路径不可写。把 `dir` / `logfile` / `pidfile` 放到允许的位置，并确认 `chown -R sycache:sycache` 实例目录。

**Q4. 日志告警 `Memory overcommit must be enabled`，BGSAVE 失败：`Can't save in background: fork: Cannot allocate memory`。**
`sudo sysctl -w vm.overcommit_memory=1`，并写入 `/etc/sysctl.d/90-sycache.conf`（DEB/RPM 已内置）。同时确认内存余量够 fork 时写时复制。

**Q5. 告警 `WARNING you have Transparent Huge Pages (THP) enabled`。**
`systemctl enable --now sycache-thp.service`，或手工 `echo madvise > /sys/kernel/mm/transparent_hugepage/enabled`。

**Q6. 告警 `TCP backlog setting of 511 cannot be enforced`。**
`sudo sysctl -w net.core.somaxconn=4096`。

**Q7. 客户端报 `DENIED Redis is running in protected mode` / 连接被拒绝。**
未设置口令且 `bind` 不是回环地址时，保护模式会拒绝外部连接。设置 `requirepass` 或 ACL，并确认 `bind`、防火墙、安全组。

**Q8. 客户端报 `NOAUTH Authentication required` / `WRONGPASS`。**
补充口令；控制台部署的实例 `default` 用户关闭，必须使用「业务账号」。

**Q9. 报 `OOM command not allowed when used memory > 'maxmemory'`。**
未设置淘汰策略（`noeviction`）且内存满了。处理：调大 `maxmemory`，或设置 `maxmemory-policy allkeys-lru` / `volatile-lru`；排查大 key（`--bigkeys`）与 TTL 缺失。

**Q10. 报 `MISCONF ... unable to persist on disk`。**
BGSAVE 失败导致拒写。查日志（磁盘满、权限、fork 失败），修复后 `sycache-cli bgsave` 成功即自动恢复；不要靠 `stop-writes-on-bgsave-error no` 掩盖。

**Q11. 从节点 `master_link_status:down` 或反复全量同步。**
排查：网络与防火墙；`masterauth` 是否正确；主节点 `repl-backlog-size` 过小；`client-output-buffer-limit replica` 触发断开；从节点磁盘 / 内存。

**Q12. 哨兵不发生切换，或频繁误切换。**
检查 `quorum` 与哨兵数（≥3 且奇数）、`down-after-milliseconds`、哨兵之间与到主从的网络、`sentinel auth-pass`；哨兵配置文件必须可写。

**Q13. 集群 `cluster_state:fail`。**
`cluster nodes` 看是否有 fail 节点、槽位是否全部覆盖；检查总线端口（数据端口 + 10000）是否放行；修复：`sycache-cli --cluster check` 与 `--cluster fix`（先备份）。

**Q14. 升级后能否直接回滚？**
RDB/AOF 格式与上游保持一致，同系列版本间可回滚；跨大版本回滚前先备份并在测试环境验证。

### 12.2 控制台

**Q15. 只能在本机访问控制台。**
默认监听 `127.0.0.1:8443`。设置 `CACHEOS_LISTEN=0.0.0.0:8443` 并启用 HTTPS，重启控制台。

**Q16. 浏览器提示证书不受信任。**
内置 CA 签发的证书需要导入一次根证书：下载 `https://<控制台>:8443/web-ca.crt`。或改用自己的证书（`CACHEOS_TLS_CERT` / `CACHEOS_TLS_KEY`）。

**Q17. 控制台启动失败。**
`journalctl -u sycache-console -n 100`。常见：数据目录权限（须属 `sycache-console`，0700）；`CACHEOS_DB` 无法连接；共用 PostgreSQL 时 CA / `secret.key` 与库中记录不一致（日志会提示 `ha_secretbox_id` / `ha_ca_fingerprint`）；端口被占用。

**Q18. 创建部署单元提示 `LICENSE_FEATURE_DENIED`。**
当前授权不含该形态（哨兵 / 集群）或业务链路 TLS。导入更高规格 License，或改用单机 / 主从并关闭数据面 TLS。

**Q19. 丢了 TOTP 验证器。**
需要另一位管理员重置；只有一位管理员时联系交付方，不要手工改库。

**Q20. 导入数据面包失败。**
`.sig` 必须与包匹配：名称、版本、架构一致；SHA-256 与 Ed25519 签名任一不符即拒绝。重新核对下载文件与 `SHA256SUMS`。

### 12.3 agent 与主机

**Q21. 接入命令提示令牌无效。**
令牌一次性且会过期，回「主机」页面重新生成。

**Q22. `enroll` 报证书或指纹错误。**
确认 `-ca-fingerprint` 与控制台一致；`CACHEOS_GRPC_HOSTS` 是否包含 agent 使用的域名 / IP；缓存主机到控制台 `9443` 是否连通：`nc -zv console.example.internal 9443`。

**Q23. 主机在控制台显示离线。**
`systemctl status sycache-agent`、`journalctl -u sycache-agent`；确认 `9443` 可达、系统时间同步（时间偏差会导致证书校验失败）。agent 离线不影响实例运行。

**Q24. 控制台下发启停失败，日志出现 `sudo: a password is required` 或 wrapper 拒绝。**
检查 `/etc/sudoers.d/sycache-agent` 是否存在（0440）；wrapper 只接受 `start|stop|restart|switch-version`，端口必须是 4–5 位数字且实例目录已存在，版本号必须形如 `1.2.3` 且版本目录已存在。不要改 sudoers 放宽规则。

**Q25. 卸载时提示 `refusing to remove while instances are running`。**
这是保护措施。先在控制台或 `systemctl stop sycache@端口` 停掉实例。

**Q26. 授权过期后实例会停吗？**
不会。授权失效只限制新建 / 变更相关功能，不会停止任何已运行实例。

### 12.4 Docker

**Q27. 容器启动后立刻退出。**
`docker logs <容器>`。多半是误加了 `--daemonize yes`、数据卷权限不对（容器内用户 uid 999 必须能写 `/data`，用绑定目录时 `chown 999:999`）、或自定义参数时漏了 `--bind 0.0.0.0` / `--dir /data`（自定义命令会替换默认命令）。

**Q28. 容器内主从 / 集群地址不对。**
容器内看到的是内网地址。使用 `--network host`，或设置 `replica-announce-ip`、`cluster-announce-ip`、`cluster-announce-port` 等上游参数。

---

## 13. 附：与 Redis 手册的对照

| 项目 | Redis 手册做法 | CacheOS（sycache）做法 |
| --- | --- | --- |
| 服务端程序 | `redis-server` | `sycache-server` |
| 命令行 | `redis-cli` | `sycache-cli`（参数一致） |
| 压测 | `redis-benchmark` | `sycache-benchmark` |
| 校验 RDB / AOF | `redis-check-rdb` / `redis-check-aof` | `sycache-check-rdb` / `sycache-check-aof` |
| 哨兵 | `redis-sentinel` | `sycache-sentinel` |
| 配置文件 | `redis.conf` | `sycache.conf`（参数默认值不变） |
| 安装包 | 官方 apt / yum 仓库 | GitHub Release 的 DEB / RPM / tar.gz |
| 系统服务 | `redis-server.service` | `sycache@<端口>.service`（模板单元，一个端口一个实例） |
| 默认目录 | `/var/lib/redis`、`/var/log/redis` | `/home/atwasoft/sycache/instances/<端口>`、`/var/log/sycache` |
| 运行用户 | `redis` | `sycache` |
| 重载配置 | `CONFIG SET` / `CONFIG REWRITE` / 重启 | 相同；**无 reload 信号** |
| 内核调优 | overcommit、THP、somaxconn | 相同，包内自动设置 |
| 持久化 | RDB / AOF，格式兼容 | 格式与上游一致，可互相读取 |
| 管理界面 | 无（依赖第三方） | 内置控制台（带 TOTP、审计、授权管理） |
| 官方容器镜像 | `redis` 镜像 | `ghcr.io/xiaoguan521/cacheos/sycache`，见第 6 节 |
| `INFO` 版本字段 | `redis_version` | 保持上游值，客户端与监控无需适配 |

> 说明：sycache 是 CacheOS 的数据面，兼容 Redis 协议，基于 Valkey（BSD-3-Clause）源码，归因见 [`NOTICE`](NOTICE)。
