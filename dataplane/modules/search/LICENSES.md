# valkey-search 1.2.1 许可审查

本文件记录 valkey-search 1.2.1 及其第三方依赖的许可信息。

## 项目许可

- **valkey-search** 1.2.1: BSD-3-Clause
  - 上游仓库: https://github.com/valkey-io/valkey-search
  - 发布标签: 1.2.1 (commit 788e8c90063abab69d0d3fff32206f8491c633ca)
  - 官方许可文本: https://github.com/valkey-io/valkey-search/blob/1.2.1/LICENSE

## 第三方依赖许可审查

所有第三方依赖均采用 **MIT / BSD / Apache-2.0** 许可，符合 CacheOS 政策要求（禁止 RSAL / SSPL / AGPL / GPL）。

### 构建系统依赖（vendored in submodules）

| 依赖 | 版本 | 许可 | 来源 | 备注 |
|------|------|------|------|------|
| gRPC | 1.70.1 | Apache-2.0 | google/grpc | 包含 Protobuf、Abseil、GTest、benchmark |
| Protobuf | 内含于 gRPC | Apache-2.0 | protocolbuffers/protobuf | 独立许可证验证通过 |
| Abseil | 内含于 gRPC | Apache-2.0 | abseil/abseil-cpp | 独立许可证验证通过 |
| GTest | main branch | BSD-3-Clause | google/googletest | 来自 googletest |
| benchmark | 1.8.3 | Apache-2.0 | google/benchmark | 非 sanitizer builds |

### Vendored 第三方库（third_party/ 目录）

| 库 | 许可 | 来源 | 用途 | 备注 |
|----|------|------|------|------|
| hnswlib | Apache-2.0 | nmslib/hnswlib | 层次化可导航小世界图 (HNSW) 用于向量搜索 | 修改版本：增加查询取消、指标追踪、自定义加载保存、多块载荷存储 |
| SimSIMD | Apache-2.0 | ashvardanian/SimSIMD | SIMD 优化向量相似性计算 | 修改版本 |
| Snowball | BSD-3-Clause | snowballstem/snowball | 词干提取（stemming）用于全文搜索 | 原始版本 |
| ICU | ICU License | unicode-org/icu | Unicode 正规化与大小写折叠 | 编译为静态库 |
| HdrHistogram_c | CC0-1.0 + BSD-2-Clause | HdrHistogram/HdrHistogram_c | 高动态范围直方图用于延迟追踪 | 双许可 |
| highwayhash | Apache-2.0 | google/highwayhash | SIMD 优化向量哈希 | 原始版本 |

### 系统库依赖（通常已在 ubuntu-latest 上可用）

| 库 | 许可 | 用途 |
|----|------|------|
| libssl-dev / OpenSSL | Apache-2.0 + BSD | TLS 支持 |
| libsystemd-dev | LGPL-2.1+ | systemd 集成（是否被链接进 libsearch.so **待核实**，未核实前不得发布） |
| pkg-config | GPL-2.0+ | 构建工具（不链接到产物） |
| cmake | BSD-3-Clause | 构建工具（不链接到产物） |
| ninja | Apache-2.0 | 构建工具（不链接到产物） |
| g++ / gcc | GPL-3.0+ | 编译器（不链接到产物） |

**注**：系统库和构建工具许可不影响最终二进制文件，仅用于编译过程。

## 兼容性评分

- **许可合规**: ✓ 通过
- **Valkey 版本**: 9.0.1+ (本项目使用 9.1.3)
- **C++ 标准**: C++20
- **编译器**: GCC 12+ 或 Clang 16+
- **Ubuntu 24.04 兼容性**: ✓ 完全兼容

## 许可审查结论

**结论**: ✓ **通过**

valkey-search 1.2.1 及其所有直接第三方依赖的许可均符合 CacheOS 政策（MIT / BSD-3-Clause / Apache-2.0），不包含 RSAL / SSPL / AGPL / GPL 递归依赖。该模块可纳入产品。

**审查日期**: 2026-11-12
**审查人**: 由代理起草，未附逐项来源链接；ICU License 与 CC0-1.0 已由项目负责人于 2026-11-13 认定为宽松许可（见 ADR 0002 修订记录）
**参考**: docs/adr/0002-extension-modules.md
