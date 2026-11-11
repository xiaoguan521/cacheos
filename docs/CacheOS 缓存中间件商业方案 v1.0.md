# CacheOS 缓存中间件商业方案 v1.0

Oct 29, 2026 · @xiaoc

CacheOS 是兼容 Redis 协议的国产化分布式缓存中间件：数据面 sycache 基于 Valkey 9.1 源码只改名、不改逻辑，控制面复用 EdgeTraffic OS v3.1 的 Go + Vue 控制台框架与授权体系。本文沿用 v3.1 的章节结构，可直接交给 AI Coding Agent 按章节实现。

## 1. 定位、对标与硬约束

**产品一句话**：Valkey 是数据面，保持上游源码、只改名为 sycache；我们做控制面，把「建实例 → 改参数 → 扩缩容 → 主备切换 → 备份恢复 → 升级」变成产品能力。业务方的 Jedis、Lettuce、redis-py、go-redis 等客户端不改一行代码即可接入。

**对标产品**

| 厂商 | 产品 | 公开资料中的形态 | CacheOS 对应做法 |
| --- | --- | --- | --- |
| 东方通 | [TongRDS](https://jit.pro/docs/reference/framework/JitStorage/cache/tongrds-cache) | 基于 Redis 协议的缓存服务，客户端接口与 Redis 一致，支持分布式部署 | 同样协议兼容；增加多节点可视化运维 |
| 宝兰德 | [BES CacheDB（BCD）](https://help.jeecg.com/java/JeecgBoot-Use-BCS-CacheDB) | 宣称完全兼容 Redis 协议，支持内存与文件持久化；部署需随包上传 License 文件，生产建议集群 | 同样四种部署形态；License 默认只在控制面校验（见 10 节） |
| 中创 | InforSuite 系列缓存产品 | 本次未检索到公开技术资料 | 待售前补充竞品资料 |

**硬约束（参照 v3.1 逐条映射）**

1. Valkey 源码零逻辑改动，外面套 Web 控制台。
2. 一个控制台管理多台主机、多个实例与集群。
3. CLI 为 `sycache-server` / `sycache-cli` / `sycache-sentinel` / `sycache-benchmark` / `sycache-check-aof` / `sycache-check-rdb`，参数与 valkey-\* / redis-\* 完全一致。
4. 控制台与节点之间通信必须安全；缓存节点通常在应用内网，但仍按「不可信网络」设计。
5. 目录、文件、配置文件名中的 valkey / redis 全部换成 sycache。
6. 操作系统进程、systemd 单元、日志横幅均显示 sycache。
7. **协议兼容优先于改名**：RESP2/RESP3 报文、错误前缀（`MOVED`、`ASK`、`LOADING`、`NOAUTH` 等）、`INFO` 中的 `redis_version` 字段、Lua 中的 `redis.call` 一律保留，否则客户端会失效。
8. 产品须受商业 License 约束（版本、实例数、内存总量、功能、有效期）。

**与 EdgeTraffic OS 的复用关系**：控制台骨架（登录、RBAC、审计、告警、License、gRPC + mTLS agent 通道、wrapper 提权模型）整体复用，作为公司中间件产品线的公共底座；CacheOS 只新增缓存领域模块。

## 2. 上游选型：用 Valkey 9.1，不用 Redis 8

闭源商业包装只能基于 BSD 许可的代码，因此数据面选 Valkey 9.1.x；Redis 7.4 之后的版本都不适合。

| 上游 | 许可证 | 闭源改名再销售 | 结论 |
| --- | --- | --- | --- |
| Redis ≤ 7.2.x | BSD-3-Clause | 允许 | 可用但已停止演进，不选 |
| Redis 7.4.x–7.8.x | RSALv2 或 SSPLv1 | RSAL 禁止作为托管/商业服务提供；SSPL 要求公开整套服务代码 | 不可用 |
| Redis 8.0+ | RSALv2 / SSPLv1 / AGPLv3 三选一 | AGPL 要求向网络用户公开修改后的源码 | 不可用 |
| Valkey 9.1.x | BSD-3-Clause（Linux 基金会） | 允许，保留版权声明即可 | **选用** |

许可证边界见 [Redis 官方许可页](https://redis.io/legal/licenses/)。Valkey 9.1 于 2026-05-19 发布，常规维护到 2029-05，扩展安全支持到 2031-05，当前补丁为 9.1.3（[endoflife.date](https://www.endoflife.date/valkey)）。产品只跟 Valkey 支持期最长的维护线，与 v3.1「只跟 nginx stable」同理。

**随之而来的约束**

- 产品名、包名、文档不得使用 Redis 商标作为产品名称，只能写「兼容 Redis 协议」。
- 不捆绑 RedisJSON、RediSearch、RedisBloom（非 BSD）；如需 JSON / 布隆过滤器 / 检索，后期评估 Valkey 社区模块，实施前逐个核对其许可证。
- 「关于」页与安装包 `LICENSES/` 目录保留 Valkey 与 Redis ≤7.2 原作者的 BSD 版权声明。
- 迁移工具参考 RedisShake（MIT）实现或直接集成，保留 NOTICE；集成前核对其最新许可证。

## 3. 总体架构

系统由五个程序组成：控制台 sycache-console、主机探针 sycache-agent、提权入口 sycache-wrapper、数据面 sycache-server / sycache-sentinel、厂商授权后台（与 EdgeTraffic OS 共用 syapp-license-server）。客户侧部署前四个和一个 PostgreSQL。

&#91;embedded content: 总体架构 · 五个程序、三个网络区\]

业务流量只在应用与实例之间流动，不经过控制台；控制台宕机或断网时，所有实例与哨兵照常服务、照常自动切换。

## 4. 技术栈

控制台、agent、wrapper 的选型与 v3.1 第 3 节完全一致（Go 1.27 + Gin + GORM，Vue 3 + Vite + antdv-next，PostgreSQL / SQLite，gRPC + mTLS，Ed25519 License 签名），这里只列 CacheOS 新增或替换的部分。

| 用途 | 选型 | 许可证 / 备注 |
| --- | --- | --- |
| 数据面 | Valkey 9.1.x 源码 + 改名 patch → sycache | BSD-3；内置 jemalloc |
| TLS 库 | OpenSSL 3.x；国密变体用铜锁 Tongsuo | Apache-2.0；`BUILD_TLS=yes` 编译 |
| agent 访问本机实例 | redis/go-redis v9 | BSD-2；走 Unix Socket，不走网络 |
| 参数目录生成 | 构建期脚本解析 Valkey `src/config.c` | 输出每个参数的类型、默认值、是否可热改，供控制台使用 |
| 数据迁移 | RedisShake（集成或参考实现） | MIT，保留 NOTICE |
| RDB 离线分析 | 自研 Go RDB 解析器（大 Key、过期分布） | 在 agent 侧运行，不上传数据 |
| 指标 | agent 解析 `INFO` / `LATENCY` / `SLOWLOG`，上报控制台；可选本机 `/metrics` | 不引入 Prometheus 栈作为必选 |
| 前端新增组件 | Key 浏览器（虚拟滚动）、槽位分布热力图（ECharts） | — |

**不引入**：Redis Stack 模块、Kubernetes Operator、Twemproxy / Codis 代理层。集群直接使用 Valkey 原生 Cluster，客户端走 cluster-aware 驱动；对只支持单机协议的老应用，二期再评估代理。

## 5. 数据面 sycache

sycache = Valkey 9.1.x 官方源码 + **1 个改名 patch**（`patches/0001-sycache-rename.patch`）+ 构建参数。patch 只允许改字符串字面量和宏的值，禁止改动判断、数据结构、命令语义、RDB/AOF 格式和模块 ABI。下表文件位置为设计意图，实施时以 9.1 源码上 `rename-scan.sh` 的扫描结果为准。

### 5.1 必须改名的位置

| 位置 | 原内容 | 改为 | 用户可见影响 |
| --- | --- | --- | --- |
| `src/Makefile` 的引擎名变量 | `valkey` 前缀，及 redis-\* 兼容软链 | `sycache`，关闭 redis-\* 软链 | 6 个二进制文件名 |
| `src/version.h` 的服务名与标题宏 | `valkey` / `Valkey` | `sycache` / `SyCache` | 启动日志、`--version` |
| `src/asciilogo.h` | Valkey 字符画 | 产品字符画 | 前台启动横幅 |
| `INFO server` 的 `valkey_version` 字段 | 字段名 | `sycache_version`（产品版本）+ sycache\_engine\_version（上游引擎版本），见 12 节 | 监控脚本 |
| `HELLO` 返回的 `server` 字段 | `valkey` | `sycache` | RESP3 握手；保留 `extended-redis-compatibility` 开关可改回 `redis` |
| `valkey-cli` 帮助与用法文本 | `valkey-cli` | `sycache-cli` | `-h` / `--help` |
| 日志与错误文案中的品牌词 | `Valkey is starting`、`MISCONF Valkey is configured…` 等 | 换品牌词，**错误前缀不变** | server 日志、错误提示 |
| `valkey.conf` / `sentinel.conf` | 文件名与注释 | `sycache.conf` / `sycache-sentinel.conf` | 配置文件 |
| 默认 pidfile、日志、目录 | `/var/run/valkey_6379.pid` 等 | `/run/sycache/sycache-6379.pid` | 文件系统 |

**刻意保持原样**（兼容性要求，写入产品说明书）：`INFO` 的 `redis_version` 字段（客户端靠它判断特性）、Lua 的 `redis.call` / `redis.pcall`、模块 API 头文件与符号、RDB/AOF 文件头魔数、集群总线协议、所有错误前缀、源码文件头 BSD 版权声明。

### 5.2 进程显示

进程标题由 `proc-title-template` 拼接，默认 `{title} {listen-addr} {server-mode}`，`{title}` 取自二进制名。改名后 `ps aux` 显示 `sycache-server 0.0.0.0:6379 [cluster]`、`sycache-sentinel *:26379 [sentinel]`；`ps -C sycache-server`、`pgrep sycache` 天然可用，无需额外处理。

### 5.3 构建与产物

```bash
make -j BUILD_TLS=yes USE_SYSTEMD=yes MALLOC=jemalloc \
     ENGINE_NAME=sycache USE_REDIS_SYMLINKS=no \
     OPENSSL_PREFIX=/build/deps/${TLS_LIB}   # openssl-3.x 或 tongsuo-8.x
make PREFIX=/home/atwasoft/sycache install
```

`ENGINE_NAME` 与 `USE_REDIS_SYMLINKS` 为 Valkey Makefile 的现有变量，实施第一步先确认 9.1 源码中的确切名称；若变量不足以覆盖全部二进制名，改名放进 patch。

产物目录：`/home/atwasoft/sycache/{bin,conf,licenses}`、`/home/atwasoft/sycache/instances/<port>/{sycache.conf,data,acl.conf}`、`/var/log/sycache/`、`/run/sycache/`。输出 RPM / DEB / tar.gz、SBOM（Syft），架构 x86\_64 + aarch64，二期加 loongarch64。

### 5.4 系统身份

- 系统用户与组 `sycache`（nologin）；实例进程以 sycache 运行，不需要 root（端口均大于 1024）。
- systemd 模板单元 `sycache@.service`：`Type=notify`，`ExecStart=/home/atwasoft/sycache/bin/sycache-server /home/atwasoft/sycache/instances/%i/sycache.conf`，`LimitNOFILE=65535`，`ProtectSystem=strict`，`ReadWritePaths=/home/atwasoft/sycache/instances/%i /var/log/sycache`。哨兵用 `sycache-sentinel@.service`。
- 主机调优由安装包写入 `/etc/sysctl.d/90-sycache.conf`（`vm.overcommit_memory=1`、`net.core.somaxconn=4096`）并关闭透明大页。
- 符号链接 `/usr/bin/sycache-cli → /home/atwasoft/sycache/bin/sycache-cli`。

### 5.5 CLI 兼容矩阵与信号

| 命令 | 等价于 | 自动化断言 |
| --- | --- | --- |
| `sycache-server [conf] [--opt val]` | `valkey-server` | 参数解析行为一致 |
| `sycache-server --version` / `-v` | 同上 | 输出含 `sycache`，不含 valkey |
| `sycache-server --sentinel` / `sycache-sentinel` | 哨兵模式 | 同上 |
| `sycache-cli`（`-h -p -a --user --tls --cluster …`） | `valkey-cli` / `redis-cli` | 全部选项一致；`--cluster create/add-node/reshard/check` 可用 |
| `sycache-benchmark` | `valkey-benchmark` | 输出格式一致 |
| `sycache-check-aof` / `sycache-check-rdb` | 同名 valkey 工具 | 退出码一致 |
| `SIGTERM` / `SIGINT` | 按配置持久化后优雅退出 | 退出前完成 RDB / AOF 落盘 |

与 nginx 最大的区别：**Valkey 没有 reload 信号**。参数热生效靠 `CONFIG SET` + `CONFIG REWRITE`，不可热改的参数必须重启实例，由第 6 节的滚动流程处理。

### 5.6 CI 门禁

- `scripts/rename-scan.sh`：在上游源码中 `grep -rniE 'valkey|\"redis'`，凡未被 patch 覆盖、也不在「保持原样」白名单中的新出现位置即失败。
- `scripts/golden-rename.sh`：断言 `--version`、`--help`、`ps aux`、`systemctl status`、`INFO server`、`HELLO 3`、启动横幅中不出现 valkey；`strings sycache-server | grep -ci valkey` 只命中白名单。
- **行为等价**：在改名后的产物上跑 Valkey 自带测试集 `./runtest`、`./runtest-cluster`、`./runtest-sentinel`，全部通过。
- **客户端兼容矩阵**：Jedis 5、Lettuce 6、Redisson、Spring Data Redis、redis-py、go-redis v9、StackExchange.Redis、node-redis / ioredis，各跑单机、哨兵、集群、TLS 四种连接的冒烟用例。

## 6. sycache-agent、wrapper 与变更状态机

agent 沿用 v3.1 第 5 节：Go 单二进制、只主动连控制台、不监听端口、注册令牌 + 本机生成密钥 + 内置 CA 签发 30 天证书、每条指令再做 Ed25519 签名与 `cmd_id` 防重放。下面只写缓存场景的差异。

### 6.1 agent 与实例的关系

- 一台主机可运行多个实例，以端口区分（`instances/<port>/`）。agent 通过 **Unix Socket** 连接本机实例，从不走 TCP。
- 每个实例预置一个管理账号 `sycache-ops`，密码由 agent 生成、仅存本机（0600）。其 ACL 只授予运维命令：`INFO CONFIG CLIENT SLOWLOG LATENCY MEMORY CLUSTER REPLICAOF FAILOVER BGSAVE BGREWRITEAOF LASTSAVE DBSIZE SCAN TYPE TTL OBJECT MIGRATE ACL`，**不授予任何读取 value 的命令**（GET、HGETALL 等）。
- 业务数据不经过控制台：Key 浏览器（7 节 M10）需单独授权，走审计，且默认对 value 脱敏。

### 6.2 gRPC 协议增量（`api/proto/agent/v1/cache.proto`）

```protobuf
message ServerMessage {
  // 沿用 v3.1 的 cmd_id / issued_at / signature
  oneof body {
    CreateInstance create = 20;   // 端口、角色、内存上限、参数模板 revision
    ApplyParams params = 21;      // 参数增量 + parent_revision_id 乐观锁
    RestartInstance restart = 22; // 仅单实例；滚动顺序由控制台编排
    Failover failover = 23;       // CLUSTER FAILOVER 或 SENTINEL FAILOVER
    ClusterOp cluster = 24;       // MEET / ADDSLOTS / SETSLOT / MIGRATE 批次
    Backup backup = 25;           // BGSAVE 后上传或本地保留
    Restore restore = 26;
    UpgradeBinary upgrade = 27;   // 切换 /home/atwasoft/sycache/versions/<ver> 软链
    Diagnose diagnose = 28;       // 慢日志、延迟、大 Key 抽样、RDB 离线分析
    LicenseUpdate license = 15;   // 与 v3.1 相同
  }
}
```

### 6.3 参数变更状态机（agent 本地，对应 v3.1 的 5.4）

| 步骤 | 动作 | 失败处理 |
| --- | --- | --- |
| 1 加锁 | `flock /run/sycache-agent/<port>.lock`；校验 `parent_revision_id` 等于本机当前版本 | 拒绝，返回 CONFLICT |
| 2 分类 | 按构建期生成的参数目录，把增量分成「可热改」与「需重启」 | — |
| 3 预检 | 新 `maxmemory` 不低于 `used_memory` 的 1.1 倍；持久化目录剩余空间足够；参数取值在白名单内 | INVALID，不做任何改动 |
| 4 热改 | 逐项 `CONFIG SET`，每项立即 `CONFIG GET` 回读比对 | 已改项按原值逆序回滚 |
| 5 落盘 | `CONFIG REWRITE`；对 `sycache.conf` 计算哈希写入 MANIFEST | 回滚并报警 |
| 6 标记 | 有「需重启」参数时，实例状态置 PENDING\_RESTART，由控制台发起滚动重启 | — |
| 7 上报 | APPLIED，附 `INFO` 前后对比 | — |

### 6.4 滚动重启与升级（控制台编排）

同一复制组或集群分片内严格按「从节点 → 主从切换 → 原主节点」顺序：

1. 重启从节点，等待 `master_link_status:up` 且复制偏移量追平。
2. 对主节点发起手动切换（集群 `CLUSTER FAILOVER`，哨兵 `SENTINEL FAILOVER`），确认新主可写。
3. 重启原主节点，作为从节点重新加入。
4. 每一步超时（默认 120 秒）即暂停整个编排并告警，**不自动继续**。

二进制升级同样走此流程：新版本解压到 `/home/atwasoft/sycache/versions/<ver>/`，切换 `current` 软链后按上面顺序重启，失败时切回原软链。

### 6.5 wrapper 与 sudoers

```
# /etc/sudoers.d/sycache-agent —— 只有这 4 行
sycache-agent ALL=(root) NOPASSWD: /usr/libexec/sycache/sycache-wrapper start *
sycache-agent ALL=(root) NOPASSWD: /usr/libexec/sycache/sycache-wrapper stop *
sycache-agent ALL=(root) NOPASSWD: /usr/libexec/sycache/sycache-wrapper restart *
sycache-agent ALL=(root) NOPASSWD: /usr/libexec/sycache/sycache-wrapper switch-version *
```

参数安全完全由 wrapper 保证：端口必须匹配 `^[0-9]{4,5}$` 且对应目录已存在；版本号必须匹配 `^[0-9]+\.[0-9]+\.[0-9]+(-[a-z0-9]+)?$` 且目录已存在；内部只调用 `systemctl <动作> sycache@<port>` 和原子替换软链；清空环境变量，禁止 `sh -c`。

### 6.6 漂移与离线

- 每次心跳比对 `CONFIG GET *` 摘要与控制台版本，不一致即 DRIFTED（有人用 cli 直接改了参数），由管理员选择「覆盖」或「采纳为新版本」。
- 与控制台断连时实例照常服务；License 过期**不停止任何实例**。

## 7. 部署形态与高可用

控制台以「部署单元」为管理对象，四种形态都由向导一键创建；业务方连接串在详情页直接给出（含 Spring Boot、Jedis、go-redis 示例）。

| 形态 | 最小规模 | 故障切换 | 适用 | 控制台向导要点 |
| --- | --- | --- | --- | --- |
| 单机 | 1 实例 | 无 | 开发测试 | 一步完成 |
| 主从 | 1 主 1–5 从 | 手动（控制台一键） | 读多写少、可容忍人工切换 | 从节点必须在不同主机 |
| 哨兵 | 1 主 ≥1 从 + 3 哨兵 | 自动（哨兵投票） | 中小规模生产，客户端支持哨兵 | 3 个哨兵分布在 3 台主机；`quorum` 默认 2 |
| 集群 | 3 主 3 从 起 | 自动（Cluster 内置） | 大容量、高并发生产 | 16384 槽均分；主从不同机、跨机架；扩缩容走在线迁槽 |

**反亲和规则**（渲染时强校验，不满足拒绝创建）：同一复制组的主从不得在同一主机；哨兵不得与其监控的主节点全部同机；集群中任一主机宕机后，每个分片至少保留一个副本。

**持久化默认值**：生产形态开启 AOF（`appendfsync everysec`）+ RDB 每小时一次；缓存型场景可在模板中关闭持久化。`maxmemory` 默认取实例内存配额，主机级预留 30% 给 fork 写时复制与系统。

**在线迁槽**：控制台计算迁移计划（源槽、目标节点、批量大小），按批下发 `CLUSTER SETSLOT … IMPORTING/MIGRATING` + `MIGRATE`，每批完成后校验 `sycache-cli --cluster check` 等价结果，可暂停与续跑。大 Key 迁移前先提示，避免阻塞。

**控制台自身高可用**：二期与 EdgeTraffic OS 相同，双活 + PostgreSQL 主备 + keepalived VIP。

## 8. 控制台功能模块

共 14 个模块，其中 6 个直接复用 EdgeTraffic OS（标「复用」），8 个为缓存领域新增。所有写操作都进审计，所有影响可用性的操作都需二次 TOTP。

| 模块 | 路由 | 核心功能 | 最低角色 | License 约束 |
| --- | --- | --- | --- | --- |
| M1 登录与初始化（复用） | `/login` `/setup` | 激活 License → 创建超管 → 绑定 TOTP | — | 无 License 只能进激活页 |
| M2 总览 | `/dashboard` | 部署单元健康、内存水位、命中率、QPS、主从延迟、告警、License 余量 | viewer | — |
| M3 主机管理 | `/hosts` | 注册命令、主机资源、内核参数体检（THP、overcommit、somaxconn）、可用端口 | operator | 主机数上限 |
| M4 部署单元 | `/clusters` | 四种形态创建向导、拓扑图（主从 / 分片 / 哨兵）、连接串、启停、删除（需输入名称确认） | operator | 实例数、内存总量、集群模式 |
| M5 参数管理 | `/params` | 参数模板（缓存型、持久型、高并发型）、按参数目录表单化编辑、热改 / 需重启标识、版本 diff、回滚 | developer | — |
| M6 高可用运维 | `/ha` | 一键主从切换、添加 / 下线从节点、哨兵管理、集群扩缩容与迁槽进度、反亲和体检 | releaser | 在线迁槽为企业版 |
| M7 性能诊断 | `/diagnose` | 慢日志聚合、延迟事件、客户端连接分析、大 Key / 热 Key 抽样、RDB 离线分析报告、内存碎片建议 | developer | RDB 离线分析为企业版 |
| M8 备份恢复 | `/backup` | 定时 RDB 备份（本地 / NFS / S3 兼容存储）、保留策略、校验、按时间点恢复到新实例 | operator | 远端存储为企业版 |
| M9 数据迁移 | `/migrate` | 从 Redis / TongRDS / BCD 等在线同步（全量 + 增量）、校验、切换清单 | operator | 企业版 |
| M10 Key 浏览器 | `/keys` | 按前缀浏览、TTL、类型、大小；value 默认脱敏，查看明文需单独授权并记审计；禁止 `KEYS *`，一律 SCAN | developer + 单独授权 | — |
| M11 数据面账号 | `/acl` | 业务 ACL 用户、命令类别与 Key 前缀授权、密码轮换、危险命令默认禁用（FLUSHALL、KEYS、DEBUG、CONFIG） | operator | — |
| M12 监控告警（复用框架） | `/monitor` | 指标曲线、告警规则（内存 > 85%、主从断开、复制延迟、命中率骤降、持久化失败、节点离线），推送邮件 / 企业微信 / 钉钉 / Webhook | viewer | 告警通道数 |
| M13 审计与用户（复用） | `/audit` `/users` | 哈希链审计、RBAC、按部署单元授权、LDAP / OIDC | auditor / admin | LDAP / OIDC 为企业版 |
| M14 授权与系统（复用） | `/system` | License 导入与用量、关于页（含 BSD 归因）、数据面版本库、备份控制台自身 | admin | — |

**变更流程**：参数、拓扑、版本类变更走与 v3.1 发布中心相同的状态流 `DRAFT → PRECHECKED → (可选) APPROVED → APPLYING → APPLIED | PARTIAL | FAILED | ROLLED_BACK`，申请人与审批人不得为同一人；同一部署单元同时只允许一个变更（数据库行锁）。

## 9. 安全与信创

### 9.1 三条链路的安全

| 链路 | 端口 | 默认措施 |
| --- | --- | --- |
| 管理链路 agent → 控制台 | 控制台 gRPC 端口 | 与 v3.1 相同：agent 主动出连、mTLS、指令签名防重放；节点不开管理端口 |
| 业务链路 应用 → 实例 | 6379 起 | `bind` 只绑内网地址；`protected-mode yes`；`default` 用户禁用，业务一律用 ACL 账号；可选 TLS（`tls-port`） |
| 内部链路 复制 / 集群总线 / 哨兵 | 实例端口、实例端口 + 10000、26379 | `tls-replication yes`、`tls-cluster yes`（企业版）；`masterauth` 与集群口令由控制台生成并轮换；nftables 只放行同部署单元主机 |

**基线强制项**（渲染器写入，业务模板不可覆盖）：禁用 `default` 用户；业务账号默认 `-@dangerous -flushall -flushdb -keys -debug -config`；`enable-debug-command no`、`enable-module-command no`；`rename-command` 不再使用，统一用 ACL 表达。

### 9.2 国密

国密构建变体用 Tongsuo 替换 OpenSSL，TLS 1.3 国密套件（RFC 8998）预计只需替换编译依赖并配置 \`tls-ciphersuites\`，需在 PoC 中实测。**SM2 双证书（NTLS）需要调用 Tongsuo 的专有接口，等于修改 TLS 层代码，突破「零逻辑改动」约束**，因此作为单独的「信创版」发行变体立项评估，不进入标准版。

### 9.3 信创适配清单

| 维度 | 目标 |
| --- | --- |
| CPU | x86\_64、鲲鹏 / 飞腾（aarch64）、龙芯（loongarch64，二期，需先验证 jemalloc 支持） |
| 操作系统 | openEuler、银河麒麟、统信 UOS、CentOS / RHEL 兼容系 |
| 控制台数据库 | PostgreSQL、达梦、人大金仓（与 EdgeTraffic OS 同步推进） |
| 资质 | 各 CPU / OS 厂商的兼容性互认证明；等保 2.0 三级所需的审计、口令、传输加密项在 M11–M13 中覆盖 |

## 10. 商业授权与版本划分

授权体系完全复用 v3.1 第 8 节（同一个 syapp-license-server、Ed25519 签名、机器指纹绑定、离线激活），只把产品标识与计量维度换成缓存场景：按「实例数 + 内存总量（GB）」计量，比按主机数更贴近缓存的价值。

**License 载荷增量**

```json
{
  "product": "cacheos",
  "edition": "enterprise",
  "limits": { "hosts": 20, "instances": 120, "memory_gb": 512 },
  "features": ["cluster", "sentinel", "tls", "online_reshard", "migrate", "rdb_analyze", "remote_backup", "ldap_oidc"],
  "not_before": "2026-11-01", "not_after": "2027-10-31",
  "support_until": "2027-10-31"
}
```

**版本划分**

| 能力 | 试用版 | 标准版 | 企业版 | 信创版 |
| --- | --- | --- | --- | --- |
| 有效期 | 30 天 | 按合同 | 按合同 | 按合同 |
| 规模上限 | 1 主机 / 3 实例 / 16 GB | 按合同 | 按合同 | 按合同 |
| 单机、主从、哨兵 | ✓ | ✓ | ✓ | ✓ |
| 原生集群 | — | — | ✓ | ✓ |
| 业务链路 TLS | — | ✓ | ✓ | ✓ |
| 复制 / 集群总线 TLS | — | — | ✓ | ✓ |
| 在线迁槽、数据迁移、RDB 离线分析 | — | — | ✓ | ✓ |
| 远端备份、LDAP / OIDC | — | — | ✓ | ✓ |
| 审计保留 | 7 天 | 90 天 | 365 天 | 365 天 |
| 国密、国产 CPU / OS / 数据库 | — | — | — | ✓ |

**执行点**：只在控制面校验——创建实例、扩容、开启功能时检查配额与特性，超额拒绝并给出用量明细。数据面 sycache-server 不读 License、不联网。License 过期后控制台进入只读（可看监控、可执行主从切换等应急操作，不可新建与扩容），**已运行实例不受影响**，避免授权问题演变成生产事故。

## 11. 迭代计划与验收

按 EdgeTraffic OS 的节奏先做 MVP 给领导演示，再做正式版；P1 与 P2 可并行，因为控制台底座已经存在。

&#91;embedded content: 迭代路线 · 6 个阶段与验收门\]

P4 演示只用一台 Linux 服务器：在同机起 6 个实例组成 3 主 3 从集群 + 一组哨兵，现场演示创建、改参数、杀主节点自动切换、滚动升级和 License 超额拦截。

## 12. 版本号与安全补丁

对外只显示产品自有版本号，与上游 Valkey 版本解耦；**版本号的每一次变化都必须对应真实的代码变化，禁止不修复漏洞、只改版本号规避扫描。** 上游真实版本与已合入补丁记录在映射表和 SBOM 中，随包交付，可审计。规则与 EdgeTraffic OS v3.1 第 4.7 节一致，两条产品线共用同一套流程与工具。

| 项 | 做法 |
| --- | --- |
| 版本格式 | 产品版本 `主.次.补丁`（如 `CacheOS 1.0.0`）；补丁号用于安全修复与缺陷修复 |
| 源码改动点 | `src/version.h` 新增宏 SYCACHE\_VERSION，只替换给人看的版本输出；影响 `--version`、启动日志、`INFO server` 的 `sycache_version``；HELLO 与复制握手保持上游值` |
| 保持原值 | `INFO` 的 `redis_version` 字段保持上游兼容值：客户端靠它判断支持哪些命令；RDB 版本号与模块 API 版本不改 |
| 对应关系 | 仓库内 `versions.yaml`：产品版本 → 上游 Valkey 版本 + 已合入补丁（CVE 编号、上游 commit）；安装包附 `/home/atwasoft/sycache/share/SBOM.spdx.json` 与 `BUILDINFO`，写真实上游版本 |
| 安全修复流程 | Valkey 发布安全公告后 7 天内完成评估：受影响则 backport 修复，发补丁版；不受影响则出书面说明 |
| 补丁规则例外 | 第 5 节「零逻辑改动」的唯一例外：允许原样 backport Valkey 官方的安全修复 commit，每个 CVE 一个文件 `patches/1xxx-cve-YYYY-NNNNN.patch`，单独评审 |
| 对客户 | 每个补丁版发布安全公告与 VEX 文件（CSAF 格式），逐条写明 CVE 在哪个产品版本修复，或说明不受影响；控制台 M14「关于」页对 admin 展示映射关系 |
| CI 门禁 | 每个 CVE 补丁附复现用例，修复前失败、修复后通过；`versions.yaml` 与 SBOM 不一致即阻断；版本号变化但 `patches/` 与上游源码均无变化即阻断 |

**已定（开发中核对源码后修正）**：同一个上游版本宏还用于复制握手 `REPLCONF version`、HELLO、Lua 与 RDB，统一替换会破坏主从协商与客户端判断。因此产品版本只出现在 `--version`、启动横幅与日志、INFO `sycache_version`；HELLO `version`、复制握手、Lua 版本变量、RDB 字段与 INFO `sycache_engine_version` 保持上游值。已验证 sycache 与上游 Valkey 可混合复制、RDB 双向互读。详见代码仓库 `docs/adr/0001-rename-boundaries.md`。

## 参考来源

- [Redis 官方许可证说明](https://redis.io/legal/licenses/)
- [Valkey 版本与支持周期（endoflife.date）](https://www.endoflife.date/valkey)
- [TongRDS 缓存接入说明](https://jit.pro/docs/reference/framework/JitStorage/cache/tongrds-cache)
- [JeecgBoot 使用宝兰德 BCS CacheDB](https://help.jeecg.com/java/JeecgBoot-Use-BCS-CacheDB)
- 内部：EdgeTraffic OS 设计规格书 v3.1
