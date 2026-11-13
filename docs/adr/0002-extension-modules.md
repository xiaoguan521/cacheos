# ADR 0002：扩展数据类型模块纳入

- 状态：已采纳（2026-11-12：JSON、布隆过滤器、搜索三个模块均纳入；valkey-search 固定 1.2.1；规则已写入 CLAUDE.md）
- 关联：规格书 2、5 节；ADR 0001（改名边界）；CLAUDE.md（许可与品牌）

## 背景

Phase 2 目标是对标 Redis 8 的 JSON、布隆过滤器、搜索/向量能力，使用 Valkey 社区维护的 BSD 许可模块而非 Redis 官方产品级模块（后者采用 RSAL / SSPL 许可，符合开源定义但不适合企业版产品）。本 ADR 明确纳入的模块、许可审查结果、改名流程边界、构建打包思路与授权影响。

## 许可审查结果

| 模块 | 官方仓库 | 最新发布 | 主 LICENSE | 版本要求 | 兼容性评分 | 结论 | 备注 |
| --- | --- | --- | --- | --- | --- | --- | --- |
| valkey-json | [valkey-io/valkey-json](https://github.com/valkey-io/valkey-json) | 1.0.3（2026-08-31） | BSD-3-Clause | Valkey ≥8.0（推测） | 高 | **通过** | 依赖 RapidJSON (MIT)；C++ 模块；无第三方 GPL/AGPL 依赖 |
| valkey-bloom | [valkey-io/valkey-bloom](https://github.com/valkey-io/valkey-bloom) | 1.0.1（2026-02-24） | BSD-3-Clause | Valkey 8.0/8.1（明确） | 中 | **暂缓** | Rust 模块；仅支持 8.0/8.1，未声称 9.1 兼容；后续可按需升级 |
| valkey-search | [valkey-io/valkey-search](https://github.com/valkey-io/valkey-search) | 1.3.0-rc2（2026-10-26） + 1.2.1 稳定 | BSD-3-Clause | 1.2.1: ≥9.0.1；1.3.0-rc2: ≥9.1.0 | 高 | **通过** | C++ 模块；1.3.0-rc2 明确支持 Valkey 9.1；预发布需风险评估 |

### 许可详情

**valkey-json RapidJSON 依赖：**
- RapidJSON (https://github.com/Tencent/rapidjson) 采用 MIT 许可，头文件唯一库，无外部 C++ 依赖
- 许可证验证：https://github.com/Tencent/rapidjson 仓库明确标识 "Licensed under the MIT License"

**valkey-bloom Rust 依赖：**
根据 Cargo.toml（已验证），主要依赖：
- `valkey-module` / `valkey-module-macros`（Valkey 官方，BSD-3-Clause 同源）
- `bloomfilter` (3.0.1)：需二次验证许可（MIT / Apache-2.0）
- `lazy_static` / `serde` / `bincode` / `libc`：均为 MIT / Apache-2.0 生态标准库

**valkey-search C++ 依赖：**
根据 README，CMake 依赖为系统开发工具（GCC/Clang、OpenSSL、systemd 开发库等）；无已知专有或 GPL 依赖。OpenSSL 采用 Apache-2.0 / BSD，systemd 采用 LGPL-2.1+（仅头文件集成）。

### 未核实项

1. valkey-json 与 valkey-search 模块 ABI 是否以 `valkeymodule.h` 为基础（假设是，待代码审查确认）
2. valkey-bloom 在 Valkey 9.1 上是否可编译（版本声称为 8.x，可能需小改动或标准库更新）
3. 三个模块在 9.1.3 数据面上的集成测试覆盖（功能正确性、RDB 序列化向前兼容）

## 决策

### 决策：三个模块均纳入

- **valkey-json** 1.0.3：JSON 文档存储与 JSONPath 查询。
- **valkey-search** 固定 1.2.1（稳定，兼容 Valkey ≥9.0.1）：向量搜索与二级索引。不使用 1.3.0-rc2；1.3.0 正式版发布后另立评审再升级。
- **valkey-bloom** 1.0.1：布隆过滤器。上游仅声明支持 Valkey 8.0/8.1，纳入以下列门禁为前提：
  1. 在 Valkey 9.1 上编译并通过其全部测试与本仓库模块验收（见 CLAUDE.md「扩展数据类型模块」）；
  2. 逐一核对其 Rust 依赖（含 `bloomfilter` 等 crate）许可，仅限 MIT / BSD / Apache-2.0；
  3. 若需适配，只能以 `dataplane/patches/` 下可复现 patch 的形式提供，不得手改。
  任一门禁未通过，则该模块不进入发布包，不影响另外两个模块。

建议实施顺序：valkey-json → valkey-search → valkey-bloom。

## 改名流程与边界

沿用 ADR 0001 的改名原则：「给人看的字」改名，「给程序读的字」保留上游值。

### 各模块的改名适用性

| 变更点 | 改名范围 | 模块适用 | 规则 |
| --- | --- | --- | --- |
| 二进制模块文件名 | `valkey-json.so` → `sycache-json.so` 等 | json、search | 不改；保持 `valkey-json.so` 原名（模块加载器按字面匹配） |
| 命令名 | `JSON.*`、`FT.*` 等 | json、search | **不改**；命令语义由数据面协议保证，客户端按字面调用 |
| INFO 模块字段 | `info modules` 中模块名 | json、search、bloom | 模块 ABI 字段保留上游值（如 `name: "json"`）；补充 sycache 品牌字段走控制面 |
| 模块加载日志 | "loaded module" 后的模块名 | json、search、bloom | 保留上游值 |
| RDB 数据类型 ID | 模块的内部类型识别码 | json、search、bloom | **不改**；RDB 格式由模块定义，互通与向前兼容依赖它 |
| 产品级 UI 展示 | 控制台中模块能力的分类名 | json、search | sycache 品牌化：「JSON 存储」「向量搜索」等（不涉及代码） |
| 文档与品牌文案 | 规格书、README、关于页 | json、search、bloom | 沿用 「sycache 集成 Valkey JSON 模块」表述（见品牌规则） |

**核心原则：**
- 模块命令、RDB 格式、模块 ABI 符号保持不变 → sycache 用户可用标准客户端
- 产品层面的品牌化（文案、UI、打包描述）由控制面与部署文档承载

## 构建与打包思路

### 编译与模块独立性

```
dataplane/modules/
├── json/
│   ├── Makefile / build.sh （valkey-json 上游改造）
│   └── patch/ （暂无）
├── search/
│   ├── Makefile / build.sh （valkey-search 上游改造）
│   └── patch/ （暂无）
└── (bloom/ - 后续)

make 阶段：
  1. dataplane/build-sycache.sh 调用 sycache-server 编译
  2. 顺序编译 modules/{json,search}/
  3. 将 *.so 输出到 dataplane/modules/dist/
```

### 加载与模块白名单

- sycache.conf 新增 `modules` 配置段（或通过 CONFIG SET）
- 默认不加载任何模块（白名单机制）
- 用户/operator 显式启用：`loadmodule /path/to/sycache-json.so`
- 打包时可选：安装包含 `--enable-modules=json,search` 变体

### 版本与 SBOM

- dataplane/versions.yaml 新增 `modules` 章节：

```yaml
current: 0.1.0

releases:
  - product: 0.1.0
    upstream: valkey 9.1.3
    modules:
      - name: json
        version: 1.0.3
        upstream_url: https://github.com/valkey-io/valkey-json/releases/tag/v1.0.3
      - name: search
        version: 1.2.1
        upstream_url: https://github.com/valkey-io/valkey-search/releases/tag/v1.2.1
    cves: []
```

- SBOM 中增补模块及其 RapidJSON 等依赖的 SPDX 记录

## 授权与 ACL 影响

### 管理账号权限审查

根据 CLAUDE.md agent 与安全规则，新增命令需评估是否落入管理禁令：

| 命令类别 | JSON 模块 | Search 模块 | 结论 |
| --- | --- | --- | --- |
| 数据写入 (`JSON.SET`、`FT.CREATE` 等) | 数据面命令，无问题 | 数据面命令，无问题 | 允许 ACL 中 `+@write` 涵盖 |
| 执行系统命令 | 无 | 无 | 允许 |
| 读取 value | JSON.GET（数据读取，非管理命令） | FT.GET（数据读取） | 允许用户 ACL，不限于管理账号 |

### Web 命令行（ExecCommand）拒绝清单

新增命令无需特别限制（JSON/Search 都是数据操作，无执行系统命令能力）。现有拒绝清单保持不变。

### ACL 测试用例

添加到 `test/compat/acl_*` 套件：
- `JSON.*` 命令在不同 ACL 等级下的行为
- `FT.*` 索引创建、查询、删除的权限隔离

## 构建与 CI 验证策略

### 本地验收（`make ci`）

新增检查项：
1. 模块构建：`make modules` 通过，输出 `.so` 文件
2. 模块加载：sycache-server 启动时成功 `loadmodule` 各模块
3. 功能测试：
   - valkey-json: 基础 `JSON.SET/GET/DEL` 与 JSONPath 查询
   - valkey-search: 向量索引创建、增删查，与上游 1.2.1 Tcl 套件对标
4. RDB 往返：模块数据写入后 `BGSAVE`，重启后数据完整
5. 复制与持久化：主从同步、哨兵切换、集群迁槽中的模块数据一致性

### GitHub Actions 流程

- 模块构建与单元测试：PR 路径触发（`dataplane/modules/**`）
- 完整集成与上游套件：手动或 `v*` 标签触发（保持现状）

### 上游兼容性追踪

- 记录 valkey-json 1.0.3、valkey-search 1.2.1/1.3.0 与 Valkey 9.1.3 的已知不兼容项（如有）到 `test/upstream/known-diff.txt`
- 模块官方更新时走 ADR 小版本或评审流程

## 风险与未决问题

### 已识别风险

1. **valkey-bloom 兼容性**：版本声称仅 8.0/8.1，9.1 编译/运行未验证。
   - 缓解：纳入前必须通过上述三项门禁，未通过则不进入发布包。

2. **预发布 valkey-search 1.3.0-rc2**：RC 版本可能有突破性变化。
   - 缓解：建议产品版 0.1.0 基于 1.2.1 LTS；1.3.0 GA 后可升级。

3. **第三方 Rust 依赖许可**：`bloomfilter` 等 crate 许可未逐一验证。
   - 缓解：bloom 纳入前逐一核对 Rust 依赖许可（门禁 2）；json 无 Rust 依赖，search 为 C++。

4. **RDB 格式演进**：模块新增命令可能改变 RDB 数据类型 ID。
   - 缓解：与模块上游同步版本固定；迁移工具需按模块版本检测并转换。

### 未决问题

1. 模块参数热配置：CONFIG SET 对某些模块参数（如 JSON 最大路径深度、Search 索引粒度）是否生效，需代码审查。
2. 模块内存计数：sycache INFO memory 中是否包含模块数据的 `used_memory`；否则需补充模块级别的 memory audit。
3. 集群槽迁移中的模块数据：Search 索引是否支持 DUMP/RESTORE 或需要通过 SCAN 重建。
4. 许可证作为产品交付物：SBOM、NOTICE、安装包内是否需包含各模块的 LICENSE 文本。

## 拟议的 CLAUDE.md 规则扩展

### 新增章节：扩展数据类型模块

#### 模块许可与纳入

- 新增依赖模块必须采用 BSD-3-Clause / Apache-2.0 / MIT 许可（同主产品需求）。
- 第三方 C/C++/Rust 依赖的许可必须经过审查，不得包含 RSAL / SSPL / AGPL / GPL 递归依赖。
- 纳入决策与许可审查结果必须记录于 docs/adr/000X-extension-modules.md；每个模块版本变化需补充已知兼容性问题到 `test/upstream/known-diff.txt`。

#### 改名与协议

- 模块命令名、RDB 数据类型 ID、模块 ABI 符号保留上游值，不改。
- 模块加载日志、INFO modules 字段保留上游名字，不改。
- 产品文档和 UI 中对模块能力的描述可品牌化（如「sycache JSON 存储」），但严禁改动代码中的字符串字面量和模块内部标识符。
- 模块 patch（如适配 Valkey 9.1）走与数据面同一流程：`dataplane/patches/000X-module-compat-*.patch`，可复现且与规则同提交。

#### ACL 与安全

- 模块新增命令纳入 sycache agent 的管理账号 ACL 评估（同 CLAUDE.md 现有规则）。
- 如模块涉及脚本执行或危险操作，需单独加入 Web 命令行（ExecCommand）的拒绝清单。
- 模块 module-level 的参数与配置必须支持 CONFIG REWRITE 持久化（无模块 reload 能力下）。

#### 测试与验收

- 模块首次纳入需通过的检查项：
  1. `make modules` 编译成功，.so 输出可用。
  2. sycache-server `--loadmodule` 加载成功，无崩溃或内存泄漏。
  3. 模块基础功能（创建、读写、删除数据）单测通过。
  4. RDB 往返测试：模块数据 BGSAVE → 重启 → 数据完整。
  5. 复制与集群：主从同步、哨兵切换、集群迁槽中的模块数据一致性。
  6. 客户端兼容性：标准 Redis 客户端能调用模块命令并获取预期结果。

- 每个 PR 需附可运行的模块测试命令（同 CLAUDE.md 现有规则）。

#### 版本与 SBOM

- dataplane/versions.yaml 新增 `modules` 章节，记录纳入的模块及版本。
- SBOM 中必须包含模块及其直接第三方依赖的许可证。
- GitHub Actions 发布产物需包含各模块的 LICENSE 文本副本。

---

**后续步骤：**
1. ~~本 ADR 架构评审与批准~~ 已完成。
2. ~~更新 CLAUDE.md~~ 已完成。
3. 启动 dataplane/modules/ 目录初始化，按 json → search → bloom 顺序适配。
4. 本地 `make ci` 增补模块验证流程。

## 修订记录

- 2026-11-13：项目负责人认定 ICU License 与 CC0-1.0 属于与 MIT / BSD / Apache-2.0 同等宽松的许可，允许出现在模块的第三方依赖中（valkey-search 1.2.1 的 ICU、HdrHistogram_c 因此不再阻塞纳入）。CLAUDE.md 对应条款已同步。
- 2026-11-13：CI 的 readelf 检查确认 libsearch.so 动态链接 libsystemd.so.0（LGPL-2.1+）；sycache-server 发布构建（USE_SYSTEMD=yes）本就依赖它。项目负责人认定“动态链接 LGPL 系统库”允许，条件：仅限动态链接、发布包附许可文本、其余 GPL/LGPL 库仍须另行评审。CLAUDE.md 已同步，CI 检查对 libsystemd 放行。
- 仍待补齐：各依赖许可的逐项来源链接。
- 2026-11-13：项目负责人认定 ISC 许可与 MIT 同等宽松，允许出现在模块的第三方依赖中（valkey-bloom 的 `bloomfilter` 3.0.2、构建期的 `libloading` 0.8.9 为 ISC）。bloom 的 Rust 依赖许可检查由 dataplane/modules/cargo-license-check.py 在构建时执行，白名单为 MIT / Apache-2.0 / BSD-2-Clause / BSD-3-Clause / ICU / CC0-1.0 / ISC。
- 2026-11-13：模块命令 ACL / Web 命令行评估（CI 产物 `acl-matrix-<module>.md`，脚本 test/modules/acl-matrix.py）。结论：agent 与 reader 内置账号只列显式命令，三个模块的全部命令均被拒绝；Web 命令行账号为“允许全部减去危险类别”，模块的 admin 类命令已被 ACL 拒绝，但 `JSON.DEBUG`、`BF.LOAD`（写类、可整体载入数据）未带 admin 标志而被放行。处理：`JSON.DEBUG`、`BF.LOAD`、`FT._DEBUG`、`FT._LIST`、`FT.INTERNAL_UPDATE` 加入 `internal/webcli` 拒绝清单并有用例（agent 侧第二层校验）。限制：模块命令在模块未加载时不存在，静态 ACL 规则不能点名它们（SETUSER 会报未知命令），因此 ACL 侧无法为这两条追加 `-` 规则；待“按实例启用模块”的控制台设计落地后，按已加载模块动态渲染 ACL。
- 2026-11-13：集群槽位迁移验证（test/modules/cluster-smoke.py，modules.yml 步骤）：两个集群主节点加载模块，用 SETSLOT + MIGRATE 迁移一个槽位，json（JSON 文档）与 bloom（布隆过滤器）数据在目标节点读取一致，源节点返回 MOVED，均通过。valkey-search 未纳入：索引是节点本地的，集群模式（跨分片查询、索引随槽位迁移）须另行设计，在此之前 search 仅支持单机与主从。
- 2026-11-13：许可文本与 SBOM 打包（dataplane/modules/collect-licenses.py，modules.yml 步骤，单测 test/modules/collect-licenses-test.py 随 `make modules-check` 运行）：每个模块的构建产物旁输出 `licenses/<模块>/`（上游 LICENSE、third_party 各库、bloom 全部 Rust crate 的许可文本；search 另附 libsystemd 的 LGPL-2.1 文本）与 `sbom-<模块>.spdx.json`（模块、直接依赖、动态链接的 libsystemd）。模块根目录无许可文本直接失败。尚未并入 sycache 安装包的 tar/RPM/DEB：模块默认不加载，如何随包分发（独立模块包或并入主包）待控制台“启用模块”的设计一并决定。`docs/NOTICE` 已加模块归因。
- 2026-11-13：控制台启用模块的方案（决定：只在新建实例时选择，不做事后启用与热加载；`FT.CREATE`/`FT.DROPINDEX` 属数据操作，Web 命令行放行）。
  1. 模块是实例的不可变属性：`CreateInstance` 带模块名列表（白名单 json / search / bloom），agent 记入实例状态并渲染 `loadmodule <Root>/modules/<名称>.so`；控制台只下发名称，路径由 agent 拼出，不接收路径。`enable-module-command` 保持 `no`，运行时不可 `MODULE LOAD`。
  2. 组合约束（控制台与 agent 各校验一次）：只支持 server 实例（哨兵不可）；search 不支持集群；模块名重复、未知名称、带路径的名称一律拒绝。主从各节点模块须一致，新增从节点沿用主节点的列表。
  3. 模块文件随版本分发为 `<Root>/modules/<名称>.so`（发布包内由 `valkey-<名称>.so` 重命名，文件名属于给人看的部分；模块命令名、ABI、INFO 字段保持上游值）。wrapper 的 switch-version 将校验已启用模块的文件存在，升级时新版本缺少已启用模块则拒绝切换。
  4. ACL：agent / reader 账号只列显式命令，不变；Web 命令行账号按已加载模块追加 `-<命令>`（来自 `internal/webcli.DeniedCommands` 中该模块前缀的条目），模块没加载时不追加（否则 ACL SETUSER 因未知命令失败）。CI 的 ACL 矩阵用同一规则在真实实例上验证服务端接受并实际拒绝。业务账号仍只能用类别规则 `+@json` / `+@search` / `+@bloom`。
  5. 控制台：创建实例时多选模块（search 与集群互斥并提示）；实例详情展示已启用模块与版本；创建与模块选择写审计。
  实施拆分：本批为 agent 侧（实例状态、配置与 ACL 渲染、校验）；协议字段 `modules`、控制台 API 与界面、wrapper 校验、安装包分发为后续独立 PR。
- 2026-11-13：协议与 agent 创建流程接入模块选择：`CreateInstance.modules`（字段 15，agent.proto）。agent 在写任何文件、启动任何进程之前校验（白名单、哨兵、search+集群），并确认 `<Root>/modules/<名称>.so` 已安装，缺失则拒绝创建；通过后记入实例状态并渲染 loadmodule 与 webcli ACL。pb.go 由 protoc 36.2 + protoc-gen-go v1.36.12 重新生成（头部 protoc 版本行保持 `(unknown)`，与既有生成文件一致）。剩余：控制台 API 与界面、wrapper 校验、安装包分发。
- 2026-11-13：控制台接入模块选择：部署单元新增 `modules` 列（迁移 202611130001，逗号分隔、规范顺序 json,search,bloom），创建单元时由 `orch.NormalizeUnitModules` 校验（与 agent 同一套规则），只下发给数据实例，哨兵进程不带；扩缩容新增节点沿用单元的模块；创建审计记录模块；前端创建表单增加“扩展数据类型”多选（search 在集群形态下禁用），详情页展示。另补一处：业务账号即使选了 `+@all`，也会在其规则之后追加已加载模块的拒绝规则（JSON.DEBUG、BF.LOAD 等，与 Web 命令行同一份清单），避免绕过。尚未做：控制台预检“目标主机是否已安装所选模块”（目前由 agent 创建时拒绝）、wrapper 校验、安装包分发。
- 2026-11-13：模块随安装包分发（取代此前“wrapper 校验模块文件 sha256”的设想）。布局：`modules/<名>.so`、`licenses/modules/<名>/`、`share/modules/<名>.spdx.json`，随产品版本放在版本目录里，实例经 `<Root>/current/modules/<名>.so` 加载，因此 `current` 切换后实例下次重启自动用新版本对应的模块；产品包（tar/RPM/DEB）与签名的升级包是同一份目录树，完整性由既有的包签名覆盖，不另设哈希。`dataplane/packaging/install-modules.sh` 负责安装（先校验后安装，缺 .so / 许可文本 / SBOM 或名称不在白名单则整体失败），`build-sycache.sh -M <模块产物目录>`（`make sycache MODULES_DIST=…`）调用它，BUILDINFO 增加 `modules=名称:版本,…`，产品 SBOM 列出所装模块。不带 `-M` 时包内不含模块。切换版本前的校验放在 agent 而非 wrapper（模块清单在 agent 的实例状态里，wrapper 保持最小权限、不读实例状态）：agent 汇总本机实例在用的模块，已安装版本检查 `versions/<版本>/modules/<名>.so`，升级包则在落盘前扫描包内的 `modules/<名>.so`，缺任何一个就拒绝切换。尚未做：RPM/DEB 对模块 .so 的 NEEDED 依赖映射（search 依赖 libsystemd，其余待核对）、发布流水线（v* 标签）实际带上模块、模块包体积评估（search 较大）。
- 2026-11-13：模块的体积与平台兼容实测（CI 产物 ubuntu-24.04 构建，`module-abi-<名>.txt`）。体积（剥离后）：json 0.5 MB、bloom 2.7 MB、search 20.9 MB（未剥离 147 MB，因此 `install-modules.sh` 安装时剥离）。符号版本：json 与 search 需要 GLIBC 2.38 与 GLIBCXX 3.4.32，bloom 需要 GLIBC 2.34，search 另依赖 libssl.so.3 与 libsystemd.so.0。**结论：直接用 ubuntu 构建的模块不能随发布包承诺的 glibc 2.17 兼容范围（CentOS 7、麒麟 V10、统信 UOS、openEuler）分发。** 为避免悄悄发布加载不了的模块，`install-modules.sh` 增加闸门：设置 `MAX_GLIBC`（兼容构建容器为 2.17）时，模块需要的 glibc 或 libstdc++（`MAX_GLIBCXX`，默认 3.4.19）符号版本更高即失败。发布流水线（v* 标签）暂不带模块，待决定：（A）在兼容构建容器里构建模块（json、bloom 可行性高；search 需要 GCC 12+ / C++20 与 gRPC，容器里的 devtoolset-10 不够）；（B）模块作为独立的可选包，只面向 glibc ≥ 2.38 的发行版，控制台按主机 glibc 版本限制启用。
- 2026-11-13：项目负责人决定采用方案 B——模块做成独立可选包 `sycache-modules`，只面向 glibc ≥ 2.38 的发行版。实现：`dataplane/packaging/build-modules-package.sh` 生成 tar.gz / DEB / RPM（装在主包目录树的 `modules/`、`licenses/modules/`、`share/modules/`，与主包文件不重叠；依赖 `sycache >= 同版本`，库依赖由模块的 NEEDED 与符号版本推导，不认识的库或 GLIBCXX 版本直接失败；RPM 用自动依赖分析）；`modules.yml` 验证包结构，发布流水线 `binary-packages.yml` 增加 `modules-packages` job（ubuntu-24.04、只出 x86_64，aarch64 的模块构建未验证）并纳入 GitHub Release；`docs/release-assets.md` 说明平台限制与卸载注意事项。主包安装路径不变；模块包不装时，控制台创建带模块的单元由 agent 拒绝（“module … is not installed on this host”）。后续：agent 上报主机已装模块，控制台据此限制放置与勾选；aarch64 模块构建；升级包（签名的版本包）如何带模块仍按“切换前覆盖校验”处理。
- 2026-11-13：主机上报已安装模块，控制台据此放置。agent 在 Hello 与 Heartbeat 里上报 `modules`（`current/modules/<名>.so` 的普通文件，白名单内，规范顺序；装上或卸载模块包后无需重启 agent），控制台存入主机的 `modules` 列（迁移 202611130002）。创建单元与扩容选主机时，缺少所选模块的主机不是候选：选不出主机时返回 ErrNoHosts 并提示缺哪个模块、需安装 sycache-modules 包；指定主机时说明具体哪台缺什么。创建表单里没有在线主机装有的模块不可选并标注，已装的显示台数；主机列表显示每台主机的扩展模块。哨兵形态下，参与同一次放置的全部主机（含哨兵进程所在主机）都要求装有所选模块，这是对放置逻辑的最小改动，后续可改为只约束数据实例。
- 2026-11-13：客户端兼容测试（`test/clients/module_clients_test.py`，redis-py 6.4.0 与 go-redis 9.7.3 的自带模块 API：JSON / BF / FT；单机 RESP2 与 RESP3，原生集群按槽位路由，search 不测集群；`modules.yml` 的 “Client compatibility” 步骤）。用例逐操作独立，一次运行列出全部结果；与 Redis 官方模块的差异登记在 `test/clients/modules-known-diff.txt`，对应用例必须按已知方式失败，差异消失会报错提醒更新。已发现的差异：（1）json × redis-py：RESP3 下 `JSON.NUMINCRBY` 回复 JSON 文本（`"[3]"`）而非数组；（2）bloom × go-redis：`BF.INFO` 完整形式多返回 `Error rate` 等键、单字段形式只回复整数而非 `[名称, 值]`，go-redis 的 `BFInfo` / `BFInfoCapacity` 均解析失败，其余 BF 命令与 redis-py 不受影响。json、bloom 通过。Java / Node / .NET 客户端尚未覆盖。
- 2026-11-13：许可审查来源链接补齐。`dataplane/modules/{json,search,bloom}/LICENSES.md` 逐项列出组件、版本、许可、来源链接与是否链入产物，核对依据是上游 `THIRD_PARTY_NOTICES`、固定提交处的许可文本、gRPC v1.70.1 各子模块的许可文本，以及对 CI 构建的 `libsearch.so` 按符号名检查哪些组件真正静态链入。bloom 的表由 `collect-licenses.py --markdown` 根据 `cargo metadata --locked` 生成，CI 重新生成并比对（106 个 crate，均有 SPDX 表达式与来源链接）；发布的 crate 不带许可文本时，同一提交的兄弟 crate 继承根目录许可（valkey-module-macros 系列），仍缺的由入库的 `extra-licenses/<名>-<版本>/` 补上（relative-path 1.9.3，原样取自上游并写明来源），补不上则收集脚本失败。`make modules-check` 检查每个模块都有 LICENSES.md 且含上游链接、版本与锁定提交。
  **本次审查发现的三件事**：（1）search 静态链入的 Zlib（经 gRPC）与 c-ares（MIT 风格声明）不在 CLAUDE.md 许可白名单内，此前没有门禁覆盖 C/C++ 依赖，需项目负责人认定后同步 CLAUDE.md 与 ADR，认定前 search 的许可结论为“有条件通过”；（2）search 构建时从 `master` 取 highwayhash、从 `main` 取 googletest，构建输入未固定、产物不可复现，应通过 `dataplane/patches` 下可复现的补丁固定到提交；（3）json 构建期拉取的 RapidJSON 源码树里含 JSON License 的测试数据（`bin/jsonchecker/`），不参与编译、不进发布包，已在 LICENSES.md 注明。gRPC 各子模块的许可文本目前未被收集脚本自动打进发布包，随（2）的补丁一并解决。
- 2026-11-13：项目负责人认定 Zlib 许可与 c-ares 的 MIT 风格声明（麻省理工 1998 年的“可免费使用、复制、修改、分发”声明）属于与 MIT / BSD / Apache-2.0 同等宽松的许可，允许出现在模块的第三方依赖中（valkey-search 经 gRPC 静态链入 zlib 与 c-ares，因此其许可结论由“有条件通过”改为“通过”）。CLAUDE.md 对应条款已同步；`cargo-license-check.py` 的白名单加入 `Zlib`（bloom 的一个依赖 crate 的许可表达式含 `OR Zlib`，原本就能满足）。仍待办：highwayhash（master）与 googletest（main）的构建输入固定，以及 gRPC 各子模块许可文本的随包收集。
- 2026-11-13：valkey-search 构建输入固定与 gRPC 许可文本。（1）上游 `submodules/CMakeLists.txt` 按分支克隆 highwayhash（`master`）与 googletest（`main`），构建不可复现。现以 patch 固定到 commit：highwayhash `f8381f3331d9c56a9792f9b4a35f61c41108c39e`（2024-04-18）、googletest `845e54d32d86ee1bb872ebb5b1c755e6e3a94c34`（2026-11-12，核对日 2026-11-13，仅测试用）；克隆后 checkout 并核对 HEAD，不一致即配置失败。（2）**模块 patch 的位置**：放在 `dataplane/modules/patches/<模块>/NNNN-*.patch`，而不是上文写的 `dataplane/patches/`——后者由 `scripts/check-versions.sh` 要求与 `versions.yaml` 的 sycache patch 清单、BUILDINFO、`patch_sha256` 一一对应，模块 patch 混进去会污染产品版本的 patch 记录；其余要求不变（只能由脚本生成、可复现、与规则同一提交）。patch 由 `dataplane/modules/gen-search-patch.sh` 生成，`--check` 模式（modules.yml 在 search 构建前运行）核对已入库文件与从锁定标签重新生成的结果逐字节一致。`build-modules.sh` 在下载后、编译前调用 `dataplane/modules/apply-patches.sh`：patch 应用失败即该模块构建失败，源码目录写入 patch 摘要标记，缓存复用时一致则跳过、不一致则失败；patch 文件纳入 modules.yml / binary-packages.yml 的构建缓存键。单测 `test/modules/apply-patches-test.sh` 随 `make modules-check` 运行。gRPC 仍按标签 v1.70.1 克隆（commit 5e099002c1600c580ebe1e6741f8ff8b182ffea4，已在 collect-licenses.py 核对），benchmark 为 v1.8.3，未纳入本次 patch。（3）`collect-licenses.py` 为 search 收集静态链接的 gRPC 及子模块（abseil-cpp、protobuf、c-ares、zlib、upb、utf8_range、address_sorting、envoy-api、xds，范围与 LICENSES.md 中核对为“链入”的一致，不含未链入的 re2）的许可文本到 `licenses/search/grpc/`，SBOM 以 STATIC_LINK 列出；缺任何一个许可文本即失败，modules.yml 的包布局步骤核对这些文件在安装包内。Zlib 已由前一条认定为宽松许可。
- 2026-11-13：v0.2.0 发布流水线首次实跑 `modules-packages` 成功（x86_64 的 tar.gz / DEB / RPM 随 Release 发布）。补两项验证：① `modules.yml` 增加 `release_tag` 输入，用已发布资产在 Ubuntu 24.04、Debian 13、Fedora 40 的干净容器里装主包 + 模块包，加载三个模块并执行基本命令，卸载模块包后主包保留，另在 Ubuntu 22.04（glibc 2.35）验证模块包因依赖被拒绝（`test/modules/verify-release-install.sh`）；② 模块包增加 aarch64：`modules.yml` 新增 `arch` 输入，发布流水线 `modules-packages` 按架构矩阵在原生 ARM 运行器上构建。第二阶段验收项据此勾选。
