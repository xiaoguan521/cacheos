# Redis 协议兼容性对比

以下入口都只依赖 Python 3 标准库：

- `cmdcheck.py`：直接连接单机或某个数据节点，执行已有命令用例。
- `sentinel_cmdcheck.py`：对比 Sentinel 命令，从 Sentinel 发现主节点后测试主从复制和常用数据命令。
- `cluster_cmdcheck.py`：对比 Cluster 命令、槽位和重定向，并测试常用数据命令。
- `cluster_transition_check.py`：对本机演示集群执行实际故障转移和单槽迁移。

## 本机演示

先启动 `demo/docker-compose-sentinel.yml` 和 `demo/docker-compose-cluster.yml`，再在仓库根目录运行：

```bash
export CMDCHECK_PASSWORD="$(awk '$1 == "requirepass" {print $2; exit}' demo/redis.conf)"

python3 test/compat/sentinel_cmdcheck.py -v --json demo/sentinel-cmdcheck-results.json
python3 test/compat/cluster_cmdcheck.py -v --json demo/cluster-cmdcheck-results.json
```

不传 `-t` 时，Sentinel 脚本默认对比 `127.0.0.1:26379,26380,26381/redis-ha` 与 `127.0.0.1:27379,27380,27381/sycache-ha`；Cluster 脚本默认对比 `127.0.0.1:7100` 和 `127.0.0.1:8100`。也可以指定其他目标：

```bash
python3 test/compat/sentinel_cmdcheck.py \
  -t redis=127.0.0.1:26379,127.0.0.1:26380,127.0.0.1:26381/redis-ha \
  -t sycache=127.0.0.1:27379,127.0.0.1:27380,127.0.0.1:27381/sycache-ha

python3 test/compat/cluster_cmdcheck.py \
  -t redis=127.0.0.1:7100 -t sycache=127.0.0.1:8100
```

`CMDCHECK_PASSWORD_REDIS`、`CMDCHECK_PASSWORD_SYCACHE` 可以分别覆盖数据节点口令。哨兵口令可用 `CMDCHECK_SENTINEL_PASSWORD` 或 `CMDCHECK_SENTINEL_PASSWORD_REDIS` / `CMDCHECK_SENTINEL_PASSWORD_SYCACHE` 单独指定；未指定时回退到相应数据节点口令。用户名使用 `CMDCHECK_USER[_目标名]` 和 `CMDCHECK_SENTINEL_USER[_目标名]`。口令不要写在命令行参数里。

两个新脚本均支持 `--list`、`--group`、`--only`、`-v`、`--json`、`--timeout` 以及 `--tls` / `--tls-ca` / `--tls-cert` / `--tls-key`。例如 `--group sentinel,replication` 只测试哨兵和主从，`--group cluster` 只测试集群命令。`--list` 不连接服务端。TLS 设置同时用于哨兵与数据节点。

## 覆盖范围与结果

Sentinel 用例实际执行 `SENTINEL MASTER`、`MASTERS`、`REPLICAS`、`SENTINELS`、`CKQUORUM` 等命令，检查哨兵发现的一致性，并向主节点写入随机测试键后从副本读取。

Cluster 用例实际执行 `CLUSTER INFO`、`NODES`、`SLOTS`、`SHARDS`、`KEYSLOT`、`REPLICAS`、`GETKEYSINSLOT` 等命令，检查 `MOVED` 重定向、跨槽错误、`READONLY` / `READWRITE` 和副本读取。常用数据用例使用带相同哈希标签的随机测试键，先通过 `CLUSTER SLOTS` 找到该槽位的主节点，避免把正常的重定向误判为命令不支持；重定向本身有独立用例。

表格和 JSON 都按用例记录 `PASS`、`FAIL`、`UNSUPPORTED`、`NOPERM`、`SKIP`；存在结果差异、失败或权限不足时退出码为 1，参数或连接错误为 2，否则为 0。两端均不支持同一命令不会单独导致非零退出码。输出还列出实际尝试发送过的命令名称，不包含参数；一个组合用例如果中途失败，后续命令可能未执行，不能将用例名称当成完整的执行记录。

哨兵、主节点和副本数量的比较反映当前拓扑，数量不同不一定是命令兼容性问题；`SKIP` 和 `NOPERM` 也应结合目标配置判断。

两个新脚本只写入随机前缀的测试键，并在每个用例结束后用独立连接尝试清理；进程被强制终止或节点不可达时可能遗留键。脚本不会执行 `FLUSHDB`、`FLUSHALL`、`SCRIPT FLUSH`、`CONFIG SET`，也不会修改 Sentinel / Cluster 配置或触发故障切换。`EVAL` 和 `SCRIPT LOAD` 会增加 Lua 脚本缓存，错误口令测试可能产生认证失败日志，发布订阅测试会发送随机测试频道的消息。

因此 `SENTINEL FAILOVER`、`SENTINEL SET`、`CLUSTER FAILOVER`、`CLUSTER SETSLOT` 等有副作用的管理命令不在默认覆盖范围内。针对本机演示拓扑，如需实际验证自动哨兵切换，可单独执行 `python3 demo/topologies/verify-failover.py`；它会短暂停止主节点并改变主从角色。

## 集群故障转移和迁槽

在上述本机演示集群运行且配置好口令后，可以单独执行：

```bash
python3 test/compat/cluster_transition_check.py --execute --only failover --json demo/cluster-failover-results.json
python3 test/compat/cluster_transition_check.py --execute --only migration --json demo/cluster-migration-results.json
```

不传 `--only` 会依次运行两项；未显式传入 `--execute` 时脚本拒绝执行。脚本仅允许操作本机 `demo/docker-compose-cluster.yml` 中的 Redis 和 SyCache 集群，并先检查集群与副本是否健康。

故障转移测试会短暂停止一个主节点，检查副本自动接管及数据读写，再重新启动原节点并尝试切回。迁槽测试会先寻找空槽，只迁移随机生成的测试键，检查 `ASK`、`MOVED` 重定向后将槽迁回；测试结束后尝试清理测试键。测试期间会实际改变集群拓扑和槽位，若进程中断、节点故障或槽中出现其他键，自动恢复可能无法完成，需要人工检查。测试键设置了过期时间，脚本不会清空数据库或主动迁移已有业务键。

注意：原有 `cmdcheck.py` 包含 `SCRIPT FLUSH` 用例；上述安全约束只适用于两个新的模式对比脚本。
