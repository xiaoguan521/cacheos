# ADR 0001：改名边界与版本字段

- 状态：已采纳（2026-10-31）
- 关联：规格书 5.1、5.6、12 节

## 结论

改名只碰「给人看的字」，不碰「给程序读的字」。凡是客户端、复制对端、磁盘文件、Lua 脚本或第三方模块会按字面值读取的内容，一律保持上游值。

## 改了什么（dataplane/rename/rename.py）

| 位置 | 上游 | sycache |
| --- | --- | --- |
| 二进制文件名（Makefile `ENGINE_NAME`） | valkey-server 等 6 个 | sycache-server 等 6 个；不生成 redis-* 软链 |
| 按可执行文件名判断运行模式（server.c） | `"valkey-sentinel"`、`"valkey-check-rdb"`、`"valkey-check-aof"` | `"sycache-…"`；redis-* 兼容名保留 |
| 子进程标题（ps 可见） | valkey-rdb-bgsave、valkey-aof-rewrite 等 | sycache-… |
| `SERVER_NAME` / `SERVER_TITLE` | valkey / Valkey | sycache / SyCache（INFO `server_name`、HELLO `server`、日志） |
| INFO 运行模式字段 | 默认 `server_mode` | 保留品牌字段；仅在集群模式补充标准 `redis_mode` 别名，便于 StackExchange.Redis 等客户端发现集群 |
| 日志、帮助、错误文案、LATENCY/MEMORY DOCTOR、COMMAND DOCS | Valkey | SyCache |
| cli 环境变量与配置文件 | VALKEYCLI_*、~/.valkeyclirc | SYCACHECLI_*、~/.sycacheclirc |
| cli / benchmark 的 `-u` 帮助文本 | valkey://… | 展示 redis:// / rediss://（两者本来就支持） |
| 启动横幅 | Valkey 字符画 | SyCache 字符画，格式参数顺序不变 |

## 刻意保留（KEEP）

| 内容 | 原因 |
| --- | --- |
| RDB / AOF 文件头魔数 `VALKEY`、RDB 字段 `valkey-ver` | 磁盘格式。保留后 sycache 与上游 Valkey 可互读数据文件，迁移工具不需要适配（compat_test 已验证双向） |
| 复制握手 `REPLCONF version` 的值 | 主从按版本号协商特性，必须是真实引擎版本（compat_test 已验证与上游混合复制） |
| HELLO 的 `version`、Lua 的 `VALKEY_VERSION` / `VALKEY_VERSION_NUM`、`REDIS_VERSION` | 客户端与用户脚本按它判断能力 |
| INFO `redis_version:7.2.4` | 客户端靠它判断支持哪些命令 |
| INFO `redis_mode` | StackExchange.Redis 等客户端在集群模式用它判断拓扑；单机 INFO 形状保持上游，`server_mode` 继续保留品牌展示 |
| 模块 API 符号 `ValkeyModule_*`、头文件 valkeymodule.h / redismodule.h | 第三方模块按名字链接 |
| ldb 调试命令 `valkey` | 用户在调试会话中输入的命令名 |
| `-u` 参数可解析的 URI 协议名 valkey://、valkeys:// | 已有连接串与迁移脚本依赖它；首轮上游测试集中 URI 用例因改成 sycache:// 而失败，已改回 |
| 所有错误前缀（MOVED、ASK、NOAUTH、CLUSTERDOWN…） | 本来就不含品牌词，测试中断言不变 |
| C 标识符、断言表达式、`__FILE__` 中的源文件名 | 代码本身，改了就是逻辑改动；只在崩溃报告中出现 |

## 版本字段（对规格书 12 节的落地与修正）

规格书 12 节原写「HELLO 的 version 字段改为产品版本」。核对源码后发现同一个 `VALKEY_VERSION` 宏还用于复制握手、Lua 与 RDB，统一替换会破坏协议，因此改为新增宏 `SYCACHE_VERSION`，只替换给人看的位置：

| 输出 | 值 |
| --- | --- |
| `sycache-server --version`、cli / benchmark / check-* 的 `--version` | 产品版本（SYCACHE_VERSION） |
| 启动横幅、启动日志、配置错误与崩溃日志 | 产品版本 |
| INFO `sycache_version`（替换原 `valkey_release_stage` 行） | 产品版本 |
| INFO `sycache_engine_version`（原 `valkey_version`） | 上游引擎版本；Lua 引擎与 cli 读取它判断特性，两处读取方同步改名 |
| HELLO `version`、REPLCONF、RDB `valkey-ver`、Lua | 上游引擎版本 |

`SYCACHE_VERSION` 由构建脚本从 dataplane/versions.yaml 传入；上游真实版本同时写进安装包的 BUILDINFO。
代价：INFO 中不再有 `valkey_release_stage` 字段（上游对 GA 版恒为 "ga"，仅供展示）。

## 已知上游行为（不修）

`sycache-check-rdb --version` 在只带一个参数时会把它当文件名（上游 valkey-check-rdb 相同，参数循环从第 2 个开始）。按「零逻辑改动」原则不修，如需修复走上游。
