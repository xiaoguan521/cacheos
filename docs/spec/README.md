# 规格书索引

- 设计规格书：[CacheOS 缓存中间件商业方案 v1.0](https://claude.ai/artifact/EXSjwWRU3s5Dsyfh221yNV)
- 共用底座：[EdgeTraffic OS 设计规格书 v3.1](https://claude.ai/artifact/6nhk8LuTBm3hqjBD1MryCp)（控制台、agent、wrapper、License 体系）

## 进度

| 阶段 | 内容 | 状态 | 验收门 |
| --- | --- | --- | --- |
| P1 | 数据面 sycache：改名 patch、构建打包、品牌门禁 | 进行中 | Valkey 测试集与客户端兼容矩阵全部通过 |
| P2 | 控制台与 agent（复用 EdgeTraffic 底座） | 未开始 | 可建单机与主从，参数热改与一键切换可用 |
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

### P1 待办

- [ ] 兼容构建在 glibc 2.17 容器中实跑（`make compat-build`，需要 docker）：产出 RPM / DEB，并在麒麟 V10、统信 UOS、openEuler、CentOS 7 上安装验证
- [ ] aarch64（鲲鹏 / 飞腾）构建与验证
- [ ] 上游 Tcl 测试集首轮结果评审，填写 `test/upstream/known-diff.txt`（`make upstream-suite`，需要 tclsh）
- [ ] Java 客户端矩阵：Jedis、Lettuce、Redisson、Spring Data Redis（需要 Maven 仓库）
