# CacheOS —— 编码规则（违反任一条即拒绝合并）

完整设计见 docs/spec/README.md 中的规格书链接。每个任务只引用规格书对应章节。

## 上游与许可
- 数据面只基于 Valkey（BSD-3-Clause），版本锁定在 dataplane/UPSTREAM。
- 不得引入 Redis 7.4 及以后的任何代码（RSAL / SSPL / AGPL），不得捆绑 RedisJSON、RediSearch、RedisBloom 等非 BSD 模块。
- 新增依赖必须是 MIT / BSD / Apache-2.0 许可；产品名称中不得使用 Redis 商标，只能写「兼容 Redis 协议」。

## 数据面（规格书 5 节，docs/adr/0001-rename-boundaries.md）
- sycache = Valkey 发布标签源码 + dataplane/patches/0001-sycache-rename.patch。
- patch 只能由 dataplane/rename/rename.py 生成（scripts/gen-patch.sh），禁止手改；规则与 patch 同一提交，`make patch-check` 必须通过。
- 改名只允许改字符串字面量与宏的值；禁止改动判断、结构体、函数签名、命令语义、RDB/AOF 格式、复制协议与模块 ABI。
- 给程序读的字保持上游值：RDB/AOF 魔数与字段、REPLCONF version、HELLO version、Lua 版本变量、INFO redis_version、模块 API 符号、错误前缀。
- 唯一例外：原样 backport 上游官方安全修复，每个 CVE 一个 dataplane/patches/1xxx-cve-YYYY-NNNNN.patch，单独评审，附复现用例。
- 产品版本只来自 dataplane/versions.yaml；版本号变化必须对应真实代码变化，禁止只改版本号规避扫描（规格书 12 节）。
- Valkey 没有 reload 信号：参数热生效用 CONFIG SET + CONFIG REWRITE，不可热改的参数走滚动重启（规格书 6.3、6.4）。

## agent 与安全（P2 起，沿用 EdgeTraffic OS 规则）
- agent 只主动连接控制台，不得监听任何端口；通过 Unix Socket 访问本机实例。
- agent 的管理账号不得授予读取 value 的命令；协议中不得加入「执行命令」「读写任意文件」类指令。
- 所有外部命令用 exec.Command 切片调用，禁止 sh -c。
- wrapper 只接受子命令 start|stop|restart|switch-version；端口只接受 ^[0-9]{4,5}$ 且实例目录已存在，版本只接受 ^[0-9]+\.[0-9]+\.[0-9]+(-[a-z0-9.]+)?$ 且版本目录已存在；每个子命令都必须有注入测试。
- 私钥、凭据、实例口令、License 签名私钥不得出现在代码、日志、前端响应中。
- 授权失效不得停止任何实例（规格书 10 节）。

## 品牌
- 产品代码、界面文案、日志、打包与部署文件中不得出现 valkey（范围见 scripts/brand-check.sh）。
- 关于页与 docs/NOTICE 必须保留 "based on Valkey (BSD-3-Clause)" 归因与上游版权声明。

## 交付要求
- 先写失败用例再写实现；每个 PR 附可运行的测试命令。
- 数据面改动：本地 `make ci` 全部通过（patch、品牌、构建、版本一致性、黄金验收、互通、客户端矩阵）。
- GitHub Actions 只手动触发或在 v* 标签时运行，不要为日常提交触发；能在本地验证的一律在本地验证。
- 上游 Tcl 测试集（CI 任务 upstream-suite）新增失败必须解释，并登记到 test/upstream/known-diff.txt。
