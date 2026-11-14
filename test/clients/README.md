# 客户端兼容矩阵

`make clients` 用真实客户端库连接 sycache 的单机、集群、哨兵与 TLS（双向认证）拓扑。
客户端源码按 `clients.lock` 从 GitHub 拉取并校验提交哈希，缓存在 `~/.cache/cacheos-clients`（`CLIENTS_CACHE` 可改），之后离线运行。

| 客户端 | 版本 | 覆盖 | 状态 |
| --- | --- | --- | --- |
| redis-py | 6.4.0 | RESP2 / RESP3、事务管道、EVALSHA、发布订阅、集群、哨兵、TLS | 自动化，`make clients` |
| go-redis | 9.7.3 | RESP3（默认）/ RESP2、事务管道、EVALSHA、集群、哨兵、TLS | 自动化，`make clients`（需要 Go） |
| Jedis / Lettuce / Redisson / Spring Data Redis | 5.2.0 / 6.5.5 / 3.45.1 / 3.4.5 | 同上 | 自动化，`JAVA_CLIENTS=1 make clients` |
| node-redis / ioredis | 4.7.1 / 5.4.1 | 单机、RESP2/3、Lua、集群、哨兵、双向 TLS | 自动化，`NODE_CLIENTS=1 make clients` |
| StackExchange.Redis | 2.8.31 | 单机、集群、哨兵、双向 TLS | 自动化，`DOTNET_CLIENTS=1 make clients` |

新增客户端：在 `clients.lock` 登记 tag 与提交哈希，在对应语言目录写用例，覆盖项与上表一致。

## 对已发布版本验证

Actions → `release-clients` → Run workflow，在 `release_tags` 填要验证的标签（默认 `v0.1.0,v0.2.1`）。
它不编译，只下载 Release 里的 tar 包（并校验 SHA256SUMS），解开后跑与 `make clients` 相同的矩阵：x86_64 跑 Python、Go、Java、Node、.NET，aarch64 跑 Python、Go、Node。
