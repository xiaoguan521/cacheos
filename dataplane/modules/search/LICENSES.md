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
| c-ares（gRPC 子模块） | 随 gRPC v1.70.1 | MIT 风格许可（文本为 MIT 麻省理工 1998 年的“可免费使用、复制、修改、分发”声明 + Daniel Stenberg 版权），项目负责人于 2026-11-13 认定为宽松许可 | https://github.com/c-ares/c-ares | 是 |
| zlib（gRPC 子模块） | 随 gRPC v1.70.1 | Zlib，项目负责人于 2026-11-13 认定为宽松许可 | https://github.com/madler/zlib | 是 |
| highwayhash | 固定提交 f8381f3331d9c56a9792f9b4a35f61c41108c39e（2024-04-18，原为 `master`） | Apache-2.0 | https://github.com/google/highwayhash | 是 |

## 仅构建或测试使用，未链入产物

| 组件 | 版本 | 许可 | 来源 | 说明 |
|---|---|---|---|---|
| googletest | 固定提交 845e54d32d86ee1bb872ebb5b1c755e6e3a94c34（2026-11-12，原为 `main`） | BSD-3-Clause | https://github.com/google/googletest | 产物中无其符号 |
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

## 构建输入固定与许可文本随包（2026-11-13）

- highwayhash（原取自 `master`）与 googletest（原取自 `main`）现由 `dataplane/modules/patches/search/0001-pin-build-inputs.patch` 固定到上表提交：
  克隆后 checkout 并核对 HEAD，不一致即配置失败。patch 由 `dataplane/modules/gen-search-patch.sh` 生成（`--check` 在 CI 中核对一致），
  `build-modules.sh` 构建前经 `apply-patches.sh` 应用，应用失败即构建失败。gRPC 固定标签 v1.70.1（提交 5e099002c1600c580ebe1e6741f8ff8b182ffea4），benchmark 固定 v1.8.3。
- `collect-licenses.py` 为 search 收集静态链入的 gRPC 及其子模块（abseil-cpp、protobuf、c-ares、zlib、upb、utf8_range、address_sorting、envoy-api、xds）的许可文本到
  `licenses/modules/search/grpc/`，SBOM 以 STATIC_LINK 列出；文本取自构建所用的同一份 gRPC 检出，缓存命中没有构建目录时按固定标签与提交克隆。
  upb 在 gRPC 检出里没有独立许可文件，由 protobuf 的许可覆盖（SBOM 注明）。

## 结论

**通过**：模块本身与链入产物的全部组件均为白名单内或已认定的宽松许可（含 2026-11-13 认定的 ICU、CC0-1.0、Zlib 与 c-ares 的 MIT 风格声明），不含 RSAL / SSPL / AGPL / GPL 类传染许可；动态链接的 libsystemd 为认定的例外。构建输入已固定、gRPC 许可文本随包。
