# 规格书索引

- 设计规格书：[CacheOS 缓存中间件商业方案 v1.0](https://claude.ai/artifact/EXSjwWRU3s5Dsyfh221yNV)
- 共用底座：[EdgeTraffic OS 设计规格书 v3.1](https://claude.ai/artifact/6nhk8LuTBm3hqjBD1MryCp)（控制台、agent、wrapper、License 体系）

## 进度

| 阶段 | 内容 | 状态 | 验收门 |
| --- | --- | --- | --- |
| P1 | 数据面 sycache：改名 patch、构建打包、品牌门禁 | 已完成（麒麟 / 统信实机验证待环境） | Valkey 测试集与客户端兼容矩阵全部通过 |
| P2 | 控制台、agent 与 wrapper | 开发中；阻断项已修复，规格缺口与 Linux 实机验收待完成 | 可建单机与主从，参数变更、启停、切换与审计可用 |
| P3 | 哨兵与集群 | 未开始 | 杀主节点演练：自动切换，压测客户端无失败 |
| P4 | MVP 演示 | 未开始 | 领导演示通过 |
| P5 | 企业版能力 | 未开始 | 从 Redis 迁移演练后全量数据校验一致 |
| P6 | 信创版 | 未开始 | 取得 CPU / OS 厂商兼容性互认证明 |

### P1 已完成

- 上游锁定 Valkey 9.1.3（`dataplane/UPSTREAM`），产品版本映射 `dataplane/versions.yaml`
- 改名规则 `dataplane/rename/rename.py` → 生成唯一 patch，可复现（`make patch-check`）；改名边界见 ADR 0001
- 构建 `dataplane/build-sycache.sh`：TLS、jemalloc、systemd、剥离符号并单独输出调试包、BUILDINFO、SPDX SBOM、第三方许可证
- `-S` 兼容构建：OpenSSL 3.5 LTS 静态链接（本机已验证），`MAX_GLIBC` 校验 glibc 符号版本
- 安装包：DEB（本机已验证安装、建用户、卸载保护）、RPM（`build-rpm.sh`，在兼容构建容器内打包）、透明大页设置单元
- 门禁：改名扫描、黄金验收（含二进制字符串）、品牌、版本一致性（`scripts/check-versions.sh`）
- 行为等价与互通 `test/compat`：单机、主从、集群、哨兵切换、双向 TLS 与 TLS 复制、与上游 Valkey 混合复制、RDB 双向互读
- 客户端矩阵 `test/clients`：redis-py 6.4（RESP2/3）、go-redis 9.7（RESP3/2），覆盖单机、集群、哨兵、TLS

本地一条命令完成全部校验：`make ci UPSTREAM_SRC=<上游源码>`。GitHub Actions 只在手动触发或推送 v* 标签时运行。

### P1 验证结果（2026-10-31，p1-verify 工作流）

| 项目 | 结果 |
| --- | --- |
| 兼容构建 x86_64 / aarch64 | 通过：glibc 2.17 容器、OpenSSL 3.5.9 静态链接，产出 tar.gz / RPM / DEB / SBOM |
| 安装冒烟（安装、以 sycache 用户运行主从与持久化、卸载） | 11/11 通过：CentOS 7、Rocky 8/9、openEuler 22.03/24.03、Ubuntu 20.04/22.04/24.04、Debian 12；aarch64 含 openEuler 22.03、Rocky 8、Ubuntu 22.04 |
| 上游 Tcl 测试集（全量） | 通过：4 个失败均为断言了被改名的字串，已登记 `test/upstream/known-diff.txt` |
| 客户端矩阵 | 通过：redis-py 17 项、go-redis 11 项（两架构）；Jedis / Lettuce / Redisson / Spring Data Redis 20 项（x86_64） |

### P1 遗留

- [ ] 麒麟 V10、统信 UOS 实机安装验证（无公开镜像；openEuler 与 Rocky 8 已作为近似验证）
- [ ] known-diff 中两个用例（集群关机丢槽、双通道复制 RDB 加载）因等待改名后的日志而未走到后续断言：P3 集群演练时补一个等价用例

### P2 逐章核对与遗留

2026-11-05 按规格书逐章核对后的状态。P2 不标记为“主体完成”，直到以下遗留项和 Linux 实机验收完成。

- [x] agent 提权链：agent 单元去掉了所有会让 systemd 强制启用 NoNewPrivileges 的选项（显式写 `NoNewPrivileges=no` 无法抵消），并有打包测试防止回退；wrapper 所需的 `/opt/sycache` 写入、`/run/sycache` runtime 目录已声明；wrapper 仍是唯一提权入口。待 Linux 实机确认 `grep NoNewPrivs /proc/$(pidof sycache-agent)/status` 为 0
- [x] agent / console 安装：agent 用户、组和目录由安装脚本创建；新增 console 用户、状态目录和配置目录安装脚本
- [x] 6.3 / 6.4 数据安全门：创建与参数变更检查 maxmemory 至少高于 `used_memory` 10%，持久化目录按重写双副本检查剩余空间；滚动重启等待复制链路为 up，且从节点已处理偏移（`slave_repl_offset`）追到主节点 `master_repl_offset` 的快照（两端分别探测，快照只取一次）
- [ ] 8 节 M1：初始化绑定 TOTP，并对登录、切换、删除等影响可用性的操作执行二次 TOTP 校验
- [ ] 8 节变更流程：实现 `DRAFT → PRECHECKED → APPROVED → APPLYING`，且申请人与审批人不得为同一人
- [ ] 6.3 第 5 步：`CONFIG REWRITE` 后计算 `sycache.conf` 哈希并写入 MANIFEST
- [ ] 9.1：业务端口 `bind` 只绑定主机内网地址；当前协议还需要补充安全传递并校验 bind 地址的字段
- [ ] Linux 安装包、systemd sandbox、浏览器端到端验收：当前开发机缺少 `dpkg-deb`、`rpmbuild`、systemd 与 Playwright，需在目标环境执行
