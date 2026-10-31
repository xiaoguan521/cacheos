# 客户端兼容矩阵

`make clients` 用真实客户端库连接 sycache 的单机、集群、哨兵与 TLS（双向认证）拓扑。
客户端源码按 `clients.lock` 从 GitHub 拉取并校验提交哈希，缓存在 `~/.cache/cacheos-clients`（`CLIENTS_CACHE` 可改），之后离线运行。

| 客户端 | 版本 | 覆盖 | 状态 |
| --- | --- | --- | --- |
| redis-py | 6.4.0 | RESP2 / RESP3、事务管道、EVALSHA、发布订阅、集群、哨兵、TLS | 自动化，`make clients` |
| go-redis | 9.7.3 | RESP3（默认）/ RESP2、事务管道、EVALSHA、集群、哨兵、TLS | 自动化，`make clients`（需要 Go） |
| Jedis / Lettuce / Redisson / Spring Data Redis | — | 同上 | 待办：需要 Maven 仓库，在有外网或内网 Nexus 的环境执行 |
| StackExchange.Redis、node-redis / ioredis | — | 同上 | 待办 |

新增客户端：在 `clients.lock` 登记 tag 与提交哈希，在对应语言目录写用例，覆盖项与上表一致。
