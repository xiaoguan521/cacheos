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
- 改名规则 `dataplane/rename/rename.py` → 生成唯一 patch，CI 校验可复现（`make patch-check`）
- 构建 `dataplane/build-sycache.sh`：TLS、jemalloc、systemd、剥离符号并单独输出调试包、BUILDINFO、第三方许可证
- 改名扫描 `scripts/rename-scan.sh`、黄金验收 `scripts/golden-rename.sh`（含二进制字符串门禁）
- 行为等价与上游互通 `test/compat/compat_test.py`：单机、主从、集群、哨兵切换、与上游 Valkey 混合复制、RDB 双向互读

### P1 待办

- [ ] 上游 Tcl 测试集首轮结果评审，形成 `test/upstream/known-diff.txt`（CI 任务 upstream-suite）
- [ ] 客户端兼容矩阵：Jedis、Lettuce、Redisson、Spring Data Redis、go-redis、StackExchange.Redis、node-redis / ioredis
- [ ] RPM / DEB 打包与安装脚本（创建 sycache 用户、关闭透明大页）
- [ ] glibc 2.17 构建环境（覆盖 CentOS 7、麒麟 V10、统信 UOS、openEuler）与 aarch64
- [ ] SBOM（Syft）与 `scripts/check-versions.sh`
