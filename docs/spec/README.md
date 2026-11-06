# 规格书索引

- 设计规格书：[CacheOS 缓存中间件商业方案 v1.0](https://claude.ai/artifact/EXSjwWRU3s5Dsyfh221yNV)
- 共用底座：[EdgeTraffic OS 设计规格书 v3.1](https://claude.ai/artifact/6nhk8LuTBm3hqjBD1MryCp)（控制台、agent、wrapper、License 体系）

## 进度

| 阶段 | 内容 | 状态 | 验收门 |
| --- | --- | --- | --- |
| P1 | 数据面 sycache：改名 patch、构建打包、品牌门禁 | 已完成（麒麟 / 统信实机验证待环境） | Valkey 测试集与客户端兼容矩阵全部通过 |
| P2 | 控制台、agent 与 wrapper | 主体开发与本地浏览器验收已完成；Linux 安装包、systemd sandbox 与目标实机验收待完成 | 可建单机与主从，参数变更、启停、切换、审计、TOTP 与备份可用 |
| P3 | 哨兵与集群 | 核心编排、在线迁槽计划、每批/最终校验、暂停续跑及副本/分片扩缩容已落地；反亲和单测、本机杀主演示与认证迁槽集成已通过；跨机反亲和、压测与 Linux 实机演练待完成 | 杀主节点演练：自动切换，压测客户端无失败 |
| P4 | MVP 演示 | 单机数据面演示脚本、控制台数据面升级与扩缩容闭环已落地；参数 / License 演示待完成 | 领导演示通过 |
| P5 | 企业版能力 | 诊断聚合、Key 元数据 / 受控查看（虚拟滚动）、RDB 备份恢复、RDB 离线分析、在线迁移全量 + 原生复制流增量入口、认证集群迁槽与 S3 兼容备份后端已落地；S3 兼容自动化往返已通过；跨厂商/生产规模真实演练待完成 | 从 Redis 迁移演练后全量数据校验一致 |
| P6 | 信创版 | Tongsuo 构建入口、aarch64 构建 smoke 与 glibc 2.17 兼容构建路径已有；CPU / OS / 数据库互认证与国密 PoC 待完成 | 取得 CPU / OS 厂商兼容性互认证明 |

### P1 已完成

- 上游锁定 Valkey 9.1.3（`dataplane/UPSTREAM`），产品版本映射 `dataplane/versions.yaml`
- 改名规则 `dataplane/rename/rename.py` → 生成唯一 patch，可复现（`make patch-check`）；改名边界见 ADR 0001
- 构建 `dataplane/build-sycache.sh`：TLS、jemalloc、systemd、剥离符号并单独输出调试包、BUILDINFO、SPDX SBOM、第三方许可证
- `-S` 兼容构建：OpenSSL 3.5 LTS 静态链接（本机已验证），`MAX_GLIBC` 校验 glibc 符号版本
- 安装包：DEB（本机已验证安装、建用户、卸载保护）、RPM（`build-rpm.sh`，在兼容构建容器内打包）、透明大页设置单元
- 门禁：改名扫描、黄金验收（含二进制字符串）、品牌、版本一致性（`scripts/check-versions.sh`）
- 发布构建：`.github/workflows/binary-packages.yml` 在 `v*` 标签或手动触发时，为 x86_64 / aarch64 生成并上传 tar.gz、RPM、DEB、调试符号包、SBOM、BUILDINFO 与 SHA256SUMS
- 行为等价与互通 `test/compat`：单机、主从、集群、哨兵切换、双向 TLS 与 TLS 复制、与上游 Valkey 混合复制、RDB 双向互读
- 客户端矩阵 `test/clients`：redis-py 6.4（RESP2/3）、go-redis 9.7（RESP3/2）、Jedis / Lettuce / Redisson / Spring Data Redis、node-redis / ioredis、StackExchange.Redis，覆盖单机、集群、哨兵、TLS

本地一条命令完成全部校验：`make ci UPSTREAM_SRC=<上游源码>`。GitHub Actions 只在手动触发或推送 v* 标签时运行。

### P1 验证结果（2026-10-31 历史 CI + 2026-11-06 同架构复核）

| 项目 | 结果 |
| --- | --- |
| 兼容构建 x86_64 / aarch64 | 通过：glibc 2.17 容器、OpenSSL 3.5.9 静态链接，产出 tar.gz / RPM / DEB / SBOM |
| 安装冒烟（安装、以 sycache 用户运行主从与持久化、卸载） | 11/11 通过：CentOS 7、Rocky 8/9、openEuler 22.03/24.03、Ubuntu 20.04/22.04/24.04、Debian 12；aarch64 含 openEuler 22.03、Rocky 8、Ubuntu 22.04 |
| 上游 Tcl 功能测试集与专用入口 | 历史 CI 全量通过：主套件 5257 passed / 0 failed、`runtest-cluster` 853 passed / 0 failed、`runtest-sentinel` `GOOD! No errors.`；2026-11-06 修复后的同机复跑功能失败数为 0，另有 1 次上游 `unit/cluster/manual-takeover` 执行器偶发 exception，单独重跑 21/21 通过；品牌断言由 `test/upstream/apply-brand-expectations.sh` 在临时测试副本中同步到 sycache；宿主负载敏感延迟断言关闭，未保留 known-diff |
| 客户端矩阵 | 通过：redis-py 17 项、go-redis 11 项（两架构）；Jedis / Lettuce / Redisson / Spring Data Redis 20 项（x86_64）；node-redis / ioredis 8 项、StackExchange.Redis 4 项（同架构容器复核） |

### P1 遗留

- [ ] 麒麟 V10、统信 UOS 实机安装验证（无公开镜像；openEuler 与 Rocky 8 已作为近似验证）

### P2 逐章核对与遗留

2026-11-06 按规格书逐章核对后的状态。P2 代码主体已完成，但不标记为“已验收”，直到以下遗留项和 Linux 实机验收完成。

- [x] agent 提权链：agent 单元去掉了所有会让 systemd 强制启用 NoNewPrivileges 的选项（显式写 `NoNewPrivileges=no` 无法抵消），并有打包测试防止回退；wrapper 所需的 `/opt/sycache` 写入、`/run/sycache` runtime 目录已声明；wrapper 仍是唯一提权入口。待 Linux 实机确认 `grep NoNewPrivs /proc/$(pidof sycache-agent)/status` 为 0
- [x] agent / console 安装：agent 用户、组和目录由安装脚本创建；新增 console 用户、状态目录和配置目录安装脚本
- [x] 6.3 / 6.4 数据安全门：创建与参数变更检查 maxmemory 至少高于 `used_memory` 10%，持久化目录按重写双副本检查剩余空间；滚动重启等待复制链路为 up，且从节点已处理偏移（`slave_repl_offset`）追到主节点 `master_repl_offset` 的快照（两端分别探测，快照只取一次）
- [x] 8 节 M1：初始化绑定 TOTP，并对登录、创建、切换、删除、备份恢复等影响可用性的操作执行二次 TOTP 校验
- [x] 8 节变更流程：实现 `PRECHECKED → (可选) APPROVED → APPLYING`，申请人与审批人不得为同一人；滚动重启超时进入 `PAUSED`，操作记录支持跨进程重建与续跑
- [x] 6.3 第 5 步：`CONFIG REWRITE` 后计算 `sycache.conf` 哈希并写入实例 `MANIFEST`
- [x] 9.1：业务端口 `bind` 已强制拒绝通配地址并通过正式 protobuf `bind_address` 字段传递、在 agent 侧再次校验；Linux 实机链路验证待完成
- [x] P3 核心编排：哨兵三节点、集群分片 / 副本、槽位初始化、Cluster/Sentinel 类型化操作与主从切换
- [x] P5 核心接口：多实例诊断聚合、慢日志 / 大 Key / LFU 热 Key 元数据抽样、SCAN Key 元数据、虚拟滚动与受控单 Key 明文查看、RDB 备份 / 恢复、控制台分片上传与定时策略
- [x] P5 核心补齐：agent 侧 RDB 元数据离线分析、SCAN + DUMP/RESTORE 全量迁移、普通实例原生复制流增量与控制台最终切换入口、集群按槽按批迁移计划
- [x] P5 跨主机恢复链路：控制台备份经 mTLS 分片流式下发到目标 agent，目标端 SHA-256 校验通过后原子落盘，避免大 RDB 进入 agent 内存
- [x] P5 备份后端：控制台文件目录（可放在 NFS 挂载点）与 S3-compatible SigV4 后端；定时策略会删除已过期的本地/远端对象
- [x] P3/P4 运维补齐：迁槽每批后检查槽归属、结束后检查 16384 槽覆盖；控制台以 ECharts 展示槽位分布；PAUSED 操作可由 releaser 续跑；新增 `scripts/demo-local.sh` 单机 3 主 3 从 + 3 哨兵演示
- [x] M6 高可用运维补齐：`/units/:id/scale` 支持主从 / 哨兵副本增减与集群分片扩缩；扩容自动选在线主机并做分片反亲和，集群扩缩在线迁槽，缩容先摘除节点再进入 agent 回收区；`/units/:id/sentinel` 支持 monitor / set / reset / remove，统一进入可暂停续跑的审计操作
- [x] 本机数据面集成验收：普通实例复制流完成“等待切换 → 源端停写 → 最终提升”；哨兵账号认证、集群广播地址与带认证的槽位迁移均用真实 sycache-server 进程验证
- [x] P3 扩容放置门禁：新增在线主机容量、磁盘空间、端口分配和分片可用区反亲和单测；`.github/workflows/p2-p6-acceptance.yml` 还会执行本机集群 / 哨兵 / 故障切换演练
- [x] M14 数据面版本库：控制台 admin 可导入厂商签名的 sycache / sycache-agent 包，导入时校验 SHA-256 与 Ed25519，支持列表、删除与审计；agent 下发前再次验签
- [x] M14 关于页与供应链记录：admin 可查看控制台构建信息、Valkey BSD-3-Clause 归因、产品—上游提交 / 补丁映射；`security/vex/<version>.csaf.json` 与 `scripts/check-vex.sh` 已纳入版本门禁
- [x] 6.4 / P4 数据面升级闭环：GitHub Actions 产物进入版本库后，按主机架构预检、agent 分片下载并二次验签，wrapper 安全解包到 `versions/<version>` 后原子切换 `current`，控制台按从节点到主节点滚动重启；切换阶段失败回滚，重启阶段超时进入 PAUSED 并支持跨进程续跑
- [x] P2/P5/P6 自动化入口：浏览器验收脚本支持空数据目录初始化并自动计算 TOTP；新增 P5 S3-compatible 真实 HTTP/SigV4 往返测试；新增 P6 `scripts/tongsuo-build.sh` 与手动 Tongsuo 构建 job
- [ ] P5 验收遗留：RedisShake 级跨厂商/多分片迁移适配与真实数据校验、S3/NFS 真实对象存储演练、在线迁槽实机压测与跨主机备份恢复演练
- [ ] Linux 安装包、systemd sandbox 与目标环境浏览器验收：本地 Playwright 冒烟已通过，仍需目标 Linux 实机确认安装、sandbox、NoNewPrivileges 与真实 agent 链路
- [ ] P6 信创验收：Tongsuo 本机构建 smoke 已通过，仍需 Tongsuo/国密互操作、CPU/OS/数据库组合认证和 PoC 实测
- [ ] 正式发布密钥仪式：`internal/license` 与 `internal/pkgsig` 目前只有演示 / 空公钥环；`go test -tags release` 需在离线签名机完成公钥入库后通过
