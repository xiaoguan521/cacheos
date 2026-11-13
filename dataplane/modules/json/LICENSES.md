# valkey-json 1.0.3 许可审查

核对对象：valkey-json 1.0.3（提交 c839dcccc3570d24f2764c7025ca4cb8698e4ab6）及其进入产物 `libjson.so` 的全部组件。
核对方式（2026-11-13）：上游 `CMakeLists.txt`、`build.sh` 与 `src/rapidjson/README.md`、固定提交处 RapidJSON 的 `license.txt`，
以及 CI 构建出的 `libjson.so` 的 NEEDED 库（只有 libstdc++、libm、libgcc_s、libc）。

## 模块本身

| 项 | 内容 |
|---|---|
| 许可 | BSD-3-Clause |
| 上游 | https://github.com/valkey-io/valkey-json （标签 1.0.3） |
| 许可文本 | https://github.com/valkey-io/valkey-json/blob/1.0.3/LICENSE |

## 链入产物的组件

| 组件 | 版本 | 许可 | 来源 | 说明 |
|---|---|---|---|---|
| RapidJSON | 提交 ebd87cb468fb4cb060b37e579718c4a4125416c1（2024-12-02），构建时由 FetchContent 按此提交拉取（已固定） | MIT | https://github.com/Tencent/rapidjson | 许可文本：https://github.com/Tencent/rapidjson/blob/ebd87cb468fb4cb060b37e579718c4a4125416c1/license.txt 。上游在 `src/rapidjson/` 里带有对 `document.h`、`reader.h` 等头文件的修改版（深度限制、整数范围），许可文本相同 |
| Valkey 模块 API 头文件（`valkeymodule.h`） | 9.1 分支；构建脚本校验该分支提交与 `dataplane/UPSTREAM` 锁定的提交一致 | BSD-3-Clause | https://github.com/valkey-io/valkey | 只用头文件，随模块编译进产物 |

### RapidJSON 许可文本里的第三方条款

RapidJSON 的 `license.txt` 声明：源码以 MIT 许可，**除**其中列出的第三方部分：
- `msinttypes`（BSD 许可）：仅供 Windows / 旧版 MSVC 使用，Linux 构建不包含；
- `bin/jsonchecker/`（**JSON License**，“软件应当用于善而不是恶”）：RapidJSON 上游明确说明这是唯一采用该许可的代码，且只是测试数据。
  该目录**不参与编译，也不进入发布包**；构建时它会随 FetchContent 的源码树一起下载，但只留在源码缓存里。

## 仅测试构建使用

`build.sh --release`（我们的构建方式）把单元测试与集成测试都关闭，下面两项不会被拉取，更不会进入产物：

| 组件 | 许可 | 来源 |
|---|---|---|
| valkey-test-framework（提交由 `VALKEY_TEST_FRAMEWORK_COMMIT` 固定） | BSD-3-Clause | https://github.com/valkey-io/valkey-test-framework |
| googletest（`tst/CMakeLists.txt`） | BSD-3-Clause | https://github.com/google/googletest |

## 动态链接的系统库

libstdc++、libgcc_s（GPL-3.0-or-later WITH GCC-exception-3.1，运行库例外）与 glibc（libc、libm，LGPL-2.1-or-later）：所有二进制共有的系统运行库，发布包不含其二进制。

## 结论

**通过**：进入产物的组件均为 BSD-3-Clause / MIT；JSON License 的代码只在源码缓存里的测试数据中，不参与编译、不进发布包。
