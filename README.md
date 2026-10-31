# CacheOS（缓存中间件）

兼容 Redis 协议的国产化分布式缓存中间件：数据面 **sycache** 基于 Valkey 源码只改名、不改逻辑；
控制面复用 EdgeTraffic OS 的 Go + Vue 控制台底座与授权体系，把「建实例 → 改参数 → 扩缩容 → 主备切换 → 备份恢复 → 升级」变成产品能力。
业务方的 Jedis、Lettuce、redis-py、go-redis 等客户端不改代码即可接入。

- 设计规格书：[CacheOS 缓存中间件商业方案 v1.0](https://claude.ai/artifact/EXSjwWRU3s5Dsyfh221yNV)（索引与进度见 [`docs/spec/`](docs/spec/README.md)）
- 编码规则：[`CLAUDE.md`](CLAUDE.md)（所有编码 Agent 与开发者必读）
- 改名边界：[`docs/adr/0001-rename-boundaries.md`](docs/adr/0001-rename-boundaries.md)

## 组件

| 程序 | 位置 | 职责 | 阶段 |
| --- | --- | --- | --- |
| `sycache-server` / `sycache-sentinel` | 每台缓存主机 | 数据面：单机、主从、哨兵、原生集群 | P1 |
| `sycache-cli` / `sycache-benchmark` / `sycache-check-rdb` / `sycache-check-aof` | 每台缓存主机 | 运维工具，参数与上游一致 | P1 |
| `sycache-agent` | 每台缓存主机 | 主动连接控制台，实例生命周期、参数变更状态机、指标采集 | P2 |
| `sycache-wrapper` | 每台缓存主机 | agent 唯一的提权入口：start / stop / restart / switch-version | P2 |
| `sycache-console` | 客户内网 | Web 控制台、变更编排、内置 CA、审计、License 校验 | P2 |

## 目录

```
dataplane/UPSTREAM          上游 Valkey 版本与提交哈希锁定
dataplane/versions.yaml     产品版本 → 上游版本 + 已合入补丁
dataplane/rename/           改名规则（生成 patch 的唯一来源）
dataplane/patches/          0001 改名 patch（生成物）；1xxx-cve-* 安全补丁
dataplane/build-sycache.sh  构建安装包
dataplane/packaging/        systemd 单元、sysctl、logrotate、默认配置生成
scripts/                    patch 生成与校验、改名扫描、黄金验收、品牌门禁
test/compat/                行为等价与上游互通测试（仅用 Python 标准库）
test/upstream/              上游 Tcl 测试集适配
docs/                       NOTICE、ADR、规格书索引
```

## 开发

依赖：gcc、make、OpenSSL 开发包、python3；可选 libsystemd-dev（`Type=notify`）。

```bash
git clone --branch 9.1.3 https://github.com/valkey-io/valkey.git /tmp/valkey   # 或不传 UPSTREAM_SRC，脚本自动拉取并校验哈希
make patch-check UPSTREAM_SRC=/tmp/valkey   # patch 与规则一致
make sycache     UPSTREAM_SRC=/tmp/valkey   # 构建 dist/sycache-<版本>-<架构>.tar.gz
make golden                                 # 改名黄金验收
make compat                                 # 行为等价测试；加 VALKEY_BIN=<上游 src 目录> 跑混合版本互通
make ci          UPSTREAM_SRC=/tmp/valkey   # 提交前的完整校验（含版本一致性、上游互通、客户端矩阵）
make compat-build                           # glibc 2.17 容器中兼容构建，产出 tar.gz / RPM / DEB（需要 docker）
```

修改改名规则后执行 `make patch UPSTREAM_SRC=…` 重新生成 patch，并把规则与 patch 一起提交。

## 许可

本仓库为专有软件，见 [LICENSE](LICENSE)。数据面基于 Valkey（BSD-3-Clause），归因见 [docs/NOTICE](docs/NOTICE)。
