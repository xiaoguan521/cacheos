# valkey-search 1.2.1 许可审查

核对对象：valkey-search 1.2.1（提交 788e8c90063abab69d0d3fff32206f8491c633ca）及其进入产物 `libsearch.so` 的全部组件。
核对方式（2026-11-13）：上游仓库的 `THIRD_PARTY_NOTICES`、源码树里的许可文本、gRPC v1.70.1 各子模块的许可文本，
以及对 CI 构建出的 `libsearch.so`（未剥离）按符号名检查哪些组件真正被静态链接。表中“链入”一列即该检查的结论。

## 模块本身

| 项 | 内容 |
|---|---|
| 许可 | BSD-3-Clause |
| 上游 | https://github.com/valkey-io/valkey-search （标签 1.2.1） |
| 许可文本 | https://github.com/valkey-io/valkey-search/blob/1.2.1/LICENSE |
| vmsdk（仓库内 `vmsdk/`） | BSD-3-Clause，随模块一起发布，许可文本 https://github.com/valkey-io/valkey-search/blob/1.2.1/vmsdk/LICENSE |

## 随源码仓库提供（`third_party/`，上游 `THIRD_PARTY_NOTICES` 逐项登记）

| 组件 | 版本 | 许可 | 来源 | 许可文本（1.2.1 标签） | 链入 |
|---|---|---|---|---|---|
| HdrHistogram_c | 未标注 | CC0-1.0（上游同时标注 BSD-2-Clause，双许可） | https://github.com/HdrHistogram/HdrHistogram_c | third_party/hdrhistogram_c/LICENSE | 是 |
| hnswlib（上游已修改） | 未标注 | Apache-2.0 | https://github.com/nmslib/hnswlib | third_party/hnswlib/LICENSE | 是 |
| SimSIMD（上游已修改） | 未标注 | Apache-2.0 | https://github.com/ashvardanian/SimSIMD | third_party/simsimd/LICENSE | 是 |
| Snowball | v3.0.1 | BSD-3-Clause | https://github.com/snowballstem/snowball | third_party/snowball/COPYING | 是 |
| ICU | 76.1 | ICU License | https://github.com/unicode-org/icu | third_party/icu/LICENSE | 是（静态库） |

ICU License 与 CC0-1.0 已由项目负责人于 2026-11-13 认定为宽松许可（见 ADR 0002 修订记录）。

## 构建时拉取并静态链入（`submodules/CMakeLists.txt`）

| 组件 | 版本 | 许可 | 来源 | 链入 |
|---|---|---|---|---|
| gRPC | v1.70.1 | Apache-2.0 | https://github.com/grpc/grpc | 是 |
| abseil-cpp（gRPC 子模块） | 随 gRPC v1.70.1 | Apache-2.0 | https://github.com/abseil/abseil-cpp | 是 |
| protobuf（gRPC 子模块） | 随 gRPC v1.70.1 | BSD-3-Clause | https://github.com/protocolbuffers/protobuf | 是 |
| upb（gRPC 仓库内 `third_party/upb`） | 随 gRPC v1.70.1 | BSD-3-Clause（文件头注明 BSD-style） | https://github.com/protocolbuffers/protobuf/tree/main/upb | 是 |
| utf8_range（gRPC 仓库内） | 随 gRPC v1.70.1 | MIT | https://github.com/protocolbuffers/utf8_range | 是 |
| address_sorting（gRPC 仓库内） | 随 gRPC v1.70.1 | BSD-3-Clause（WIDE Project） | https://github.com/grpc/grpc/tree/v1.70.1/third_party/address_sorting | 是 |
| envoy data-plane-api / cncf xds（gRPC 子模块，xDS 协议定义） | 随 gRPC v1.70.1 | Apache-2.0 | https://github.com/envoyproxy/data-plane-api 、 https://github.com/cncf/xds | 是 |
| c-ares（gRPC 子模块） | 随 gRPC v1.70.1 | MIT 风格许可（文本为 MIT 麻省理工 1998 年的“可免费使用、复制、修改、分发”声明 + Daniel Stenberg 版权），**不在白名单内，待认定** | https://github.com/c-ares/c-ares | 是 |
| zlib（gRPC 子模块） | 随 gRPC v1.70.1 | Zlib，**不在白名单内，待认定** | https://github.com/madler/zlib | 是 |
| highwayhash | **master（未固定）**；2026-11-13 核对时为 f8381f3331d9c56a9792f9b4a35f61c41108c39e（2024-04-18） | Apache-2.0 | https://github.com/google/highwayhash | 是 |

## 仅构建或测试使用，未链入产物

| 组件 | 版本 | 许可 | 来源 | 说明 |
|---|---|---|---|---|
| googletest | **main（未固定）**；2026-11-13 核对时为 845e54d32d86ee1bb872ebb5b1c755e6e3a94c34 | BSD-3-Clause | https://github.com/google/googletest | 产物中无其符号 |
| benchmark | v1.8.3 | Apache-2.0 | https://github.com/google/benchmark | 产物中无其符号 |
| re2（gRPC 子模块） | 随 gRPC v1.70.1 | BSD-3-Clause | https://github.com/google/re2 | 产物中未发现其符号 |
| BoringSSL（gRPC 子模块） | — | — | https://github.com/google/boringssl | 未使用：gRPC 链接系统 OpenSSL，产物中无其符号 |

## 动态链接的系统库（发布包不含其二进制）

| 库 | 许可 | 说明 |
|---|---|---|
| OpenSSL（libssl.so.3、libcrypto.so.3） | Apache-2.0 | 系统自带 |
| libsystemd（libsystemd.so.0） | LGPL-2.1-or-later | 项目负责人认定动态链接允许；发布包附其许可文本（licenses/modules/search/libsystemd-LGPL-2.1.txt） |
| glibc（libc、libm、libmvec、ld-linux） | LGPL-2.1-or-later | 所有二进制共有的系统运行库 |
| libstdc++、libgcc_s | GPL-3.0-or-later WITH GCC-exception-3.1 | 系统运行库，运行库例外允许动态或静态使用而不传染 |

## 需要项目负责人处理的事项

1. **Zlib 与 c-ares 的许可**：二者静态链入产物，许可不在 CLAUDE.md 白名单（MIT / BSD / Apache-2.0 / ICU / CC0-1.0 / ISC）内。Zlib 是常见的宽松许可；c-ares 的许可文本是 MIT 风格的宽松声明。需要项目负责人像此前对 ICU、CC0-1.0、ISC 那样明确认定，并同步 CLAUDE.md 与 ADR。**认定前本审查结论为“有条件”。**
2. **构建输入未固定**：highwayhash 取自 `master`、googletest 取自 `main`，每次构建拿到的代码可能不同，产物不可复现。highwayhash 已两年多没有变更、googletest 不进产物，实际风险低，但按“上游版本锁定”的原则应通过 `dataplane/patches` 下可复现的补丁固定到提交。
3. 许可文本与 SBOM 的随包分发见 `dataplane/modules/collect-licenses.py`（自动收集 `third_party/` 下各库的许可文本；gRPC 各子模块的许可文本目前**未**自动收集，需随固定补丁一并解决）。

## 结论

**有条件通过**：模块本身与直接依赖的许可均为宽松许可，不含 RSAL / SSPL / AGPL / GPL 类传染许可；
上述第 1 项认定后方可视为完全符合 CLAUDE.md，第 2、3 项是改进项。
