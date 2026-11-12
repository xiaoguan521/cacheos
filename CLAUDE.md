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

## 扩展数据类型模块（ADR 0002）
- 仅允许纳入 ADR 0002 列出的 Valkey 社区模块：valkey-json 1.0.3、valkey-search 1.2.1、valkey-bloom 1.0.1；新增或升级模块版本须先更新 ADR。
- 模块及其全部第三方依赖（含 C/C++/Rust crate）必须是 MIT / BSD / Apache-2.0 许可，不得含 RSAL / SSPL / AGPL / GPL；许可审查结果记入 ADR。
- 模块命令名、RDB 数据类型 ID、模块 ABI 符号、INFO modules 字段与加载日志中的模块名保持上游值；改名只适用于给人看的文案，沿用「数据面」改名边界。
- 为适配 Valkey 9.1 的改动只能是 `dataplane/patches/` 下可复现的 patch，与规则同一提交，禁止手改上游模块源码。
- 模块默认不加载，由白名单显式 loadmodule；模块参数持久化走 CONFIG SET + CONFIG REWRITE。
- 模块新增命令必须评估对 agent 管理账号 ACL 与 Web 命令行拒绝清单的影响；涉及脚本执行或危险操作的命令加入拒绝清单并有用例。
- 每个模块首次纳入须本地通过：编译、loadmodule、基础功能、RDB 往返、主从 / 哨兵 / 集群一致性、客户端兼容；valkey-bloom 另须先通过 Valkey 9.1 兼容与 Rust 依赖许可门禁，否则不进入发布包。
- dataplane/versions.yaml 增加 modules 章节；SBOM 含模块及直接依赖许可，发布产物附各模块 LICENSE 文本，docs/NOTICE 保留模块归因。

## agent 与安全（P2 起，沿用 EdgeTraffic OS 规则）
- agent 只主动连接控制台，不得监听任何端口；通过 Unix Socket 访问本机实例。
- agent 的管理账号不得授予读取 value 的命令；协议中不得加入「执行系统命令 / shell」「读写任意文件」类指令。
- 唯一例外是 Web 命令行（ExecCommand）：只把数据命令经 Unix Socket 发给指定实例，不触达操作系统。必须同时满足：
  - 使用独立的内置账号，不复用也不放宽管理账号；该账号的 ACL 禁用管理类、危险类、脚本、阻塞、订阅、事务与连接类命令。
  - agent 侧在发送前按同一份拒绝清单再校验一次；ACL 与 agent 校验各有测试，拒绝清单中的每条命令都有用例。
  - 操作人须由管理员单独授权（与明文查看 Key 同级），且每次请求经过二次验证。
  - 每条命令写审计：操作人、部署单元、实例、命令名、参数个数、结果状态；参数值与返回值不得写入审计和日志。
  - 单条命令有超时与返回大小上限，超限截断并标明。
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
- GitHub Actions 默认手动触发或在 v* 标签时运行；仅允许 PR 按路径自动触发轻量门禁（dataplane.yml、p2-p6-acceptance.yml）。发布构建、容器安装冒烟、上游 Tcl 测试集、铜锁变体只手动或 v* 标签触发，不随 PR 或日常提交触发；能在本地验证的一律在本地验证。
- 上游 Tcl 测试集（CI 任务 upstream-suite）新增失败必须解释，并登记到 test/upstream/known-diff.txt。
