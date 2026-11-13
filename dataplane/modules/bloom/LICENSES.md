# valkey-bloom 1.0.1 许可审查

本文件由 dataplane/modules/collect-licenses.py 根据 `cargo metadata --locked`（Cargo.lock 已入库）生成，请勿手改；
CI 会重新生成并与本文件比对，依赖变化后须更新。依赖许可另由 dataplane/modules/cargo-license-check.py 在构建时按白名单校验。

- 模块：valkey-bloom 1.0.1，BSD-3-Clause
- 上游：https://github.com/valkey-io/valkey-bloom（提交 43b4e704bc8d6bd5f65529c8bd7e8488f2d54080）
- 许可文本：https://github.com/valkey-io/valkey-bloom/blob/1.0.1/LICENSE
- 依赖 crate（不含模块自身）共 106 个，许可文本随发布包附在 licenses/modules/bloom/crates/ 下

| crate | 版本 | 许可（SPDX 表达式） | 来源 |
|---|---|---|---|
| addr2line | 0.27.1 | Apache-2.0 OR MIT | https://crates.io/crates/addr2line/0.27.1 ; https://github.com/gimli-rs/addr2line |
| adler2 | 2.0.1 | 0BSD OR MIT OR Apache-2.0 | https://crates.io/crates/adler2/2.0.1 ; https://github.com/oyvindln/adler2 |
| aho-corasick | 1.1.5 | Unlicense OR MIT | https://crates.io/crates/aho-corasick/1.1.5 ; https://github.com/BurntSushi/aho-corasick |
| autocfg | 1.5.1 | Apache-2.0 OR MIT | https://crates.io/crates/autocfg/1.5.1 ; https://github.com/cuviper/autocfg |
| backtrace | 0.3.77 | MIT OR Apache-2.0 | https://crates.io/crates/backtrace/0.3.77 ; https://github.com/rust-lang/backtrace-rs |
| bincode | 1.3.3 | MIT | https://crates.io/crates/bincode/1.3.3 ; https://github.com/servo/bincode |
| bindgen | 0.70.1 | BSD-3-Clause | https://crates.io/crates/bindgen/0.70.1 ; https://github.com/rust-lang/rust-bindgen |
| bitflags | 1.3.2 | MIT/Apache-2.0 | https://crates.io/crates/bitflags/1.3.2 ; https://github.com/bitflags/bitflags |
| bitflags | 2.13.2 | MIT OR Apache-2.0 | https://crates.io/crates/bitflags/2.13.2 ; https://github.com/bitflags/bitflags |
| bloomfilter | 3.0.2 | ISC | https://crates.io/crates/bloomfilter/3.0.2 ; https://github.com/jedisct1/rust-bloom-filter |
| bumpalo | 3.20.3 | MIT OR Apache-2.0 | https://crates.io/crates/bumpalo/3.20.3 ; https://github.com/fitzgen/bumpalo |
| cc | 1.11.1 | MIT OR Apache-2.0 | https://crates.io/crates/cc/1.11.1 ; https://github.com/rust-lang/cc-rs |
| cexpr | 0.6.0 | Apache-2.0/MIT | https://crates.io/crates/cexpr/0.6.0 ; https://github.com/jethrogb/rust-cexpr |
| cfg-if | 1.0.5 | MIT OR Apache-2.0 | https://crates.io/crates/cfg-if/1.0.5 ; https://github.com/rust-lang/cfg-if |
| clang-sys | 1.9.1 | Apache-2.0 | https://crates.io/crates/clang-sys/1.9.1 ; https://github.com/KyleMayes/clang-sys |
| either | 1.19.0 | MIT OR Apache-2.0 | https://crates.io/crates/either/1.19.0 ; https://github.com/rayon-rs/either |
| enum-primitive-derive | 0.1.2 | MIT | https://crates.io/crates/enum-primitive-derive/0.1.2 ; https://gitlab.com/cardoe/enum-primitive-derive.git |
| equivalent | 1.0.3 | Apache-2.0 OR MIT | https://crates.io/crates/equivalent/1.0.3 ; https://github.com/indexmap-rs/equivalent |
| find-msvc-tools | 0.1.15 | MIT OR Apache-2.0 | https://crates.io/crates/find-msvc-tools/0.1.15 ; https://github.com/rust-lang/cc-rs |
| futures | 0.3.34 | MIT OR Apache-2.0 | https://crates.io/crates/futures/0.3.34 ; https://github.com/rust-lang/futures-rs |
| futures-channel | 0.3.34 | MIT OR Apache-2.0 | https://crates.io/crates/futures-channel/0.3.34 ; https://github.com/rust-lang/futures-rs |
| futures-core | 0.3.34 | MIT OR Apache-2.0 | https://crates.io/crates/futures-core/0.3.34 ; https://github.com/rust-lang/futures-rs |
| futures-executor | 0.3.34 | MIT OR Apache-2.0 | https://crates.io/crates/futures-executor/0.3.34 ; https://github.com/rust-lang/futures-rs |
| futures-io | 0.3.34 | MIT OR Apache-2.0 | https://crates.io/crates/futures-io/0.3.34 ; https://github.com/rust-lang/futures-rs |
| futures-macro | 0.3.34 | MIT OR Apache-2.0 | https://crates.io/crates/futures-macro/0.3.34 ; https://github.com/rust-lang/futures-rs |
| futures-sink | 0.3.34 | MIT OR Apache-2.0 | https://crates.io/crates/futures-sink/0.3.34 ; https://github.com/rust-lang/futures-rs |
| futures-task | 0.3.34 | MIT OR Apache-2.0 | https://crates.io/crates/futures-task/0.3.34 ; https://github.com/rust-lang/futures-rs |
| futures-timer | 3.0.4 | MIT/Apache-2.0 | https://crates.io/crates/futures-timer/3.0.4 ; https://github.com/async-rs/futures-timer |
| futures-util | 0.3.34 | MIT OR Apache-2.0 | https://crates.io/crates/futures-util/0.3.34 ; https://github.com/rust-lang/futures-rs |
| getrandom | 0.2.17 | MIT OR Apache-2.0 | https://crates.io/crates/getrandom/0.2.17 ; https://github.com/rust-random/getrandom |
| gimli | 0.34.0 | MIT OR Apache-2.0 | https://crates.io/crates/gimli/0.34.0 ; https://github.com/gimli-rs/gimli |
| glob | 0.3.4 | MIT OR Apache-2.0 | https://crates.io/crates/glob/0.3.4 ; https://github.com/rust-lang/glob |
| hashbrown | 0.17.1 | MIT OR Apache-2.0 | https://crates.io/crates/hashbrown/0.17.1 ; https://github.com/rust-lang/hashbrown |
| heck | 0.5.0 | MIT OR Apache-2.0 | https://crates.io/crates/heck/0.5.0 ; https://github.com/withoutboats/heck |
| indexmap | 2.14.2 | Apache-2.0 OR MIT | https://crates.io/crates/indexmap/2.14.2 ; https://github.com/indexmap-rs/indexmap |
| itertools | 0.13.0 | MIT OR Apache-2.0 | https://crates.io/crates/itertools/0.13.0 ; https://github.com/rust-itertools/itertools |
| js-sys | 0.3.107 | MIT OR Apache-2.0 | https://crates.io/crates/js-sys/0.3.107 ; https://github.com/wasm-bindgen/wasm-bindgen/tree/master/crates/js-sys |
| lazy_static | 1.5.1 | MIT OR Apache-2.0 | https://crates.io/crates/lazy_static/1.5.1 ; https://github.com/rust-lang-nursery/lazy-static.rs |
| libc | 0.2.191 | MIT OR Apache-2.0 | https://crates.io/crates/libc/0.2.191 ; https://github.com/rust-lang/libc |
| libloading | 0.8.9 | ISC | https://crates.io/crates/libloading/0.8.9 ; https://github.com/nagisa/rust_libloading/ |
| linkme | 0.3.38 | MIT OR Apache-2.0 | https://crates.io/crates/linkme/0.3.38 ; https://github.com/dtolnay/linkme |
| linkme-impl | 0.3.38 | MIT OR Apache-2.0 | https://crates.io/crates/linkme-impl/0.3.38 ; https://github.com/dtolnay/linkme |
| log | 0.4.35 | MIT OR Apache-2.0 | https://crates.io/crates/log/0.4.35 ; https://github.com/rust-lang/log |
| memchr | 2.8.3 | Unlicense OR MIT | https://crates.io/crates/memchr/2.8.3 ; https://github.com/BurntSushi/memchr |
| memoffset | 0.7.1 | MIT | https://crates.io/crates/memoffset/0.7.1 ; https://github.com/Gilnaa/memoffset |
| minimal-lexical | 0.2.1 | MIT/Apache-2.0 | https://crates.io/crates/minimal-lexical/0.2.1 ; https://github.com/Alexhuszagh/minimal-lexical |
| miniz_oxide | 0.9.1 | MIT OR Zlib OR Apache-2.0 | https://crates.io/crates/miniz_oxide/0.9.1 ; https://github.com/Frommi/miniz_oxide/tree/master/miniz_oxide |
| nix | 0.26.4 | MIT | https://crates.io/crates/nix/0.26.4 ; https://github.com/nix-rust/nix |
| nom | 7.1.3 | MIT | https://crates.io/crates/nom/7.1.3 ; https://github.com/Geal/nom |
| num-traits | 0.1.43 | MIT/Apache-2.0 | https://crates.io/crates/num-traits/0.1.43 ; https://github.com/rust-num/num-traits |
| num-traits | 0.2.19 | MIT OR Apache-2.0 | https://crates.io/crates/num-traits/0.2.19 ; https://github.com/rust-num/num-traits |
| object | 0.40.0 | Apache-2.0 OR MIT | https://crates.io/crates/object/0.40.0 ; https://github.com/gimli-rs/object |
| paste | 1.0.15 | MIT OR Apache-2.0 | https://crates.io/crates/paste/1.0.15 ; https://github.com/dtolnay/paste |
| pin-project-lite | 0.2.17 | Apache-2.0 OR MIT | https://crates.io/crates/pin-project-lite/0.2.17 ; https://github.com/taiki-e/pin-project-lite |
| pin-utils | 0.1.1 | MIT OR Apache-2.0 | https://crates.io/crates/pin-utils/0.1.1 ; https://github.com/rust-lang/pin-utils |
| ppv-lite86 | 0.2.21 | MIT OR Apache-2.0 | https://crates.io/crates/ppv-lite86/0.2.21 ; https://github.com/cryptocorrosion/cryptocorrosion |
| prettyplease | 0.2.37 | MIT OR Apache-2.0 | https://crates.io/crates/prettyplease/0.2.37 ; https://github.com/dtolnay/prettyplease |
| proc-macro-crate | 3.5.0 | MIT OR Apache-2.0 | https://crates.io/crates/proc-macro-crate/3.5.0 ; https://github.com/bkchr/proc-macro-crate |
| proc-macro2 | 1.0.107 | MIT OR Apache-2.0 | https://crates.io/crates/proc-macro2/1.0.107 ; https://github.com/dtolnay/proc-macro2 |
| quote | 0.3.15 | MIT/Apache-2.0 | https://crates.io/crates/quote/0.3.15 ; https://github.com/dtolnay/quote |
| quote | 1.0.47 | MIT OR Apache-2.0 | https://crates.io/crates/quote/1.0.47 ; https://github.com/dtolnay/quote |
| rand | 0.8.8 | MIT OR Apache-2.0 | https://crates.io/crates/rand/0.8.8 ; https://github.com/rust-random/rand |
| rand_chacha | 0.3.1 | MIT OR Apache-2.0 | https://crates.io/crates/rand_chacha/0.3.1 ; https://github.com/rust-random/rand |
| rand_core | 0.6.4 | MIT OR Apache-2.0 | https://crates.io/crates/rand_core/0.6.4 ; https://github.com/rust-random/rand |
| regex | 1.13.1 | MIT OR Apache-2.0 | https://crates.io/crates/regex/1.13.1 ; https://github.com/rust-lang/regex |
| regex-automata | 0.4.18 | MIT OR Apache-2.0 | https://crates.io/crates/regex-automata/0.4.18 ; https://github.com/rust-lang/regex |
| regex-syntax | 0.8.11 | MIT OR Apache-2.0 | https://crates.io/crates/regex-syntax/0.8.11 ; https://github.com/rust-lang/regex |
| relative-path | 1.9.3 | MIT OR Apache-2.0 | https://crates.io/crates/relative-path/1.9.3 ; https://github.com/udoprog/relative-path |
| rstest | 0.23.0 | MIT OR Apache-2.0 | https://crates.io/crates/rstest/0.23.0 ; https://github.com/la10736/rstest |
| rstest_macros | 0.23.0 | MIT OR Apache-2.0 | https://crates.io/crates/rstest_macros/0.23.0 ; https://github.com/la10736/rstest |
| rustc-demangle | 0.1.28 | MIT/Apache-2.0 | https://crates.io/crates/rustc-demangle/0.1.28 ; https://github.com/rust-lang/rustc-demangle |
| rustc-hash | 1.1.0 | Apache-2.0/MIT | https://crates.io/crates/rustc-hash/1.1.0 ; https://github.com/rust-lang-nursery/rustc-hash |
| rustc_version | 0.4.1 | MIT OR Apache-2.0 | https://crates.io/crates/rustc_version/0.4.1 ; https://github.com/djc/rustc-version-rs |
| rustversion | 1.0.23 | MIT OR Apache-2.0 | https://crates.io/crates/rustversion/1.0.23 ; https://github.com/dtolnay/rustversion |
| semver | 1.0.28 | MIT OR Apache-2.0 | https://crates.io/crates/semver/1.0.28 ; https://github.com/dtolnay/semver |
| serde | 1.0.229 | MIT OR Apache-2.0 | https://crates.io/crates/serde/1.0.229 ; https://github.com/serde-rs/serde |
| serde_core | 1.0.229 | MIT OR Apache-2.0 | https://crates.io/crates/serde_core/1.0.229 ; https://github.com/serde-rs/serde |
| serde_derive | 1.0.229 | MIT OR Apache-2.0 | https://crates.io/crates/serde_derive/1.0.229 ; https://github.com/serde-rs/serde |
| serde_syn | 0.1.0 | MIT | https://crates.io/crates/serde_syn/0.1.0 ; https://gitlab.com/samsartor/serde_syn |
| shlex | 1.3.0 | MIT OR Apache-2.0 | https://crates.io/crates/shlex/1.3.0 ; https://github.com/comex/rust-shlex |
| shlex | 2.0.1 | MIT OR Apache-2.0 | https://crates.io/crates/shlex/2.0.1 ; https://github.com/comex/rust-shlex |
| siphasher | 1.0.4 | MIT OR Apache-2.0 | https://crates.io/crates/siphasher/1.0.4 ; https://github.com/jedisct1/rust-siphash |
| slab | 0.4.12 | MIT | https://crates.io/crates/slab/0.4.12 ; https://github.com/tokio-rs/slab |
| strum_macros | 0.26.4 | MIT | https://crates.io/crates/strum_macros/0.26.4 ; https://github.com/Peternator7/strum |
| syn | 0.11.11 | MIT/Apache-2.0 | https://crates.io/crates/syn/0.11.11 ; https://github.com/dtolnay/syn |
| syn | 1.0.109 | MIT OR Apache-2.0 | https://crates.io/crates/syn/1.0.109 ; https://github.com/dtolnay/syn |
| syn | 2.0.119 | MIT OR Apache-2.0 | https://crates.io/crates/syn/2.0.119 ; https://github.com/dtolnay/syn |
| syn | 3.0.10 | MIT OR Apache-2.0 | https://crates.io/crates/syn/3.0.10 ; https://github.com/dtolnay/syn |
| synom | 0.11.3 | MIT/Apache-2.0 | https://crates.io/crates/synom/0.11.3 ; https://github.com/dtolnay/syn |
| toml_datetime | 1.1.3+spec-1.1.0 | MIT OR Apache-2.0 | https://crates.io/crates/toml_datetime/1.1.3+spec-1.1.0 ; https://github.com/toml-rs/toml |
| toml_edit | 0.25.19+spec-1.1.0 | MIT OR Apache-2.0 | https://crates.io/crates/toml_edit/0.25.19+spec-1.1.0 ; https://github.com/toml-rs/toml |
| toml_parser | 1.1.5+spec-1.1.0 | MIT OR Apache-2.0 | https://crates.io/crates/toml_parser/1.1.5+spec-1.1.0 ; https://github.com/toml-rs/toml |
| unicode-ident | 1.0.27 | (MIT OR Apache-2.0) AND Unicode-3.0 | https://crates.io/crates/unicode-ident/1.0.27 ; https://github.com/dtolnay/unicode-ident |
| unicode-xid | 0.0.4 | MIT/Apache-2.0 | https://crates.io/crates/unicode-xid/0.0.4 ; https://github.com/unicode-rs/unicode-xid |
| valkey-module | 0.1.15 | BSD-3-Clause | https://crates.io/crates/valkey-module/0.1.15 ; https://github.com/valkey-io/valkeymodule-rs |
| valkey-module-macros | 0.1.15 | BSD-3-Clause | https://crates.io/crates/valkey-module-macros/0.1.15 ; https://github.com/valkey-io/valkeymodule-rs |
| valkey-module-macros-internals | 0.1.15 | BSD-3-Clause | https://crates.io/crates/valkey-module-macros-internals/0.1.15 ; https://github.com/valkey-io/valkeymodule-rs |
| wasi | 0.11.1+wasi-snapshot-preview1 | Apache-2.0 WITH LLVM-exception OR Apache-2.0 OR MIT | https://crates.io/crates/wasi/0.11.1+wasi-snapshot-preview1 ; https://github.com/bytecodealliance/wasi |
| wasm-bindgen | 0.2.130 | MIT OR Apache-2.0 | https://crates.io/crates/wasm-bindgen/0.2.130 ; https://github.com/wasm-bindgen/wasm-bindgen |
| wasm-bindgen-macro | 0.2.130 | MIT OR Apache-2.0 | https://crates.io/crates/wasm-bindgen-macro/0.2.130 ; https://github.com/wasm-bindgen/wasm-bindgen/tree/master/crates/macro |
| wasm-bindgen-macro-support | 0.2.130 | MIT OR Apache-2.0 | https://crates.io/crates/wasm-bindgen-macro-support/0.2.130 ; https://github.com/wasm-bindgen/wasm-bindgen/tree/main/crates/macro-support |
| wasm-bindgen-shared | 0.2.130 | MIT OR Apache-2.0 | https://crates.io/crates/wasm-bindgen-shared/0.2.130 ; https://github.com/wasm-bindgen/wasm-bindgen/tree/master/crates/shared |
| windows-link | 0.2.1 | MIT OR Apache-2.0 | https://crates.io/crates/windows-link/0.2.1 ; https://github.com/microsoft/windows-rs |
| winnow | 1.0.5 | MIT | https://crates.io/crates/winnow/1.0.5 ; https://github.com/winnow-rs/winnow |
| zerocopy | 0.8.63 | BSD-2-Clause OR Apache-2.0 OR MIT | https://crates.io/crates/zerocopy/0.8.63 ; https://github.com/google/zerocopy |
| zerocopy-derive | 0.8.63 | BSD-2-Clause OR Apache-2.0 OR MIT | https://crates.io/crates/zerocopy-derive/0.8.63 ; https://github.com/google/zerocopy |
