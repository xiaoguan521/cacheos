#!/usr/bin/env bash
# 生成 / 校验 valkey-search 的构建输入固定 patch（ADR 0002 修订记录）。
# 用法：
#   dataplane/modules/gen-search-patch.sh            重新生成 dataplane/modules/patches/search/0001-pin-build-inputs.patch
#   dataplane/modules/gen-search-patch.sh --check    重新生成到临时文件并与已入库的 patch 逐字节比较（需要网络，CI 里运行）
#
# 做什么：valkey-search 1.2.1 的 submodules/CMakeLists.txt 按分支克隆 highwayhash(master) 与 googletest(main)，
# 内容随时间漂移。本脚本从 MODULES 锁定的标签检出源码，只对 checkout_submodule_branch 增加一个可选的 commit 参数
# （克隆后 checkout 到该 commit 并核对 HEAD，不一致即 FATAL_ERROR），并给这两处调用传入固定 commit。
# patch 只由本脚本生成，禁止手改。
set -euo pipefail
HERE="$(cd "$(dirname "$0")" && pwd)"
PATCH="$HERE/patches/search/0001-pin-build-inputs.patch"

# 固定的提交（2026-11-13 核对：highwayhash master 最后提交 2024-04-18，googletest main 2026-11-12）
HIGHWAYHASH_COMMIT="f8381f3331d9c56a9792f9b4a35f61c41108c39e"
GOOGLETEST_COMMIT="845e54d32d86ee1bb872ebb5b1c755e6e3a94c34"

read -r _ _ repo tag _ commit < <(grep -E '^search[[:space:]]' "$HERE/MODULES")
W="$(mktemp -d)"; trap 'rm -rf "$W"' EXIT
git -c advice.detachedHead=false clone -q --depth 1 --branch "$tag" "$repo" "$W/src"
[[ "$(git -C "$W/src" rev-parse HEAD)" == "$commit" ]] || { echo "ERROR: 标签 $tag 与 MODULES 锁定的 commit 不一致" >&2; exit 1; }

python3 -I - "$W/src/submodules/CMakeLists.txt" "$HIGHWAYHASH_COMMIT" "$GOOGLETEST_COMMIT" <<'PY'
import sys
path, hh, gt = sys.argv[1:4]
s = open(path).read()
def rep(old, new):
    global s
    if s.count(old) != 1:
        sys.exit("ERROR: 上游 CMakeLists.txt 结构变化，找不到唯一的: " + old.splitlines()[0])
    s = s.replace(old, new)
rep('''# Helper method: checkout submodule for a given branch/tag and place it under
# the ".deps" folder
function(checkout_submodule_branch MODULE_NAME GIT_URL GIT_BRANCH HINT_PATH)
''', '''# Helper method: checkout submodule for a given branch/tag and place it under
# the ".deps" folder. CacheOS: an optional 5th argument pins an exact commit;
# the checkout must end up on it, otherwise configuration fails.
function(checkout_submodule_branch MODULE_NAME GIT_URL GIT_BRANCH HINT_PATH)
  set(GIT_COMMIT "")
  if(ARGC GREATER 4)
    set(GIT_COMMIT "${ARGV4}")
  endif()
''')
rep('''    execute_process(
      COMMAND
        ${_GIT_EXE} clone --branch=${GIT_BRANCH} ${GIT_URL}
        ${MODULES_DIR}/${MODULE_NAME} --recurse-submodules --shallow-submodules
        --depth=1 --single-branch
      OUTPUT_VARIABLE _GIT_CHECKOUT_OUTPUT
      OUTPUT_QUIET ERROR_QUIET
      RESULT_VARIABLE _CHECKOUT_RES)
''', '''    if(GIT_COMMIT)
      execute_process(
        COMMAND
          ${_GIT_EXE} clone --no-checkout ${GIT_URL}
          ${MODULES_DIR}/${MODULE_NAME}
        OUTPUT_QUIET ERROR_QUIET
        RESULT_VARIABLE _CHECKOUT_RES)
      if(_CHECKOUT_RES EQUAL 0)
        execute_process(
          COMMAND ${_GIT_EXE} -c advice.detachedHead=false checkout --detach
                  ${GIT_COMMIT}
          WORKING_DIRECTORY ${MODULES_DIR}/${MODULE_NAME}
          OUTPUT_QUIET ERROR_QUIET
          RESULT_VARIABLE _CHECKOUT_RES)
      endif()
      if(_CHECKOUT_RES EQUAL 0)
        execute_process(
          COMMAND ${_GIT_EXE} rev-parse HEAD
          WORKING_DIRECTORY ${MODULES_DIR}/${MODULE_NAME}
          OUTPUT_VARIABLE _HEAD_COMMIT
          OUTPUT_STRIP_TRAILING_WHITESPACE)
        if(NOT "${_HEAD_COMMIT}" STREQUAL "${GIT_COMMIT}")
          message(
            FATAL_ERROR
              "${MODULE_NAME}: HEAD ${_HEAD_COMMIT} is not the pinned commit ${GIT_COMMIT}"
          )
        endif()
      endif()
    else()
      execute_process(
        COMMAND
          ${_GIT_EXE} clone --branch=${GIT_BRANCH} ${GIT_URL}
          ${MODULES_DIR}/${MODULE_NAME} --recurse-submodules --shallow-submodules
          --depth=1 --single-branch
        OUTPUT_VARIABLE _GIT_CHECKOUT_OUTPUT
        OUTPUT_QUIET ERROR_QUIET
        RESULT_VARIABLE _CHECKOUT_RES)
    endif()
''')
rep('''    ${MODULE_HIGHWAYHASH} "https://github.com/google/highwayhash.git" "master"
    "${MODULES_DIR}/highwayhash")''', '''    ${MODULE_HIGHWAYHASH} "https://github.com/google/highwayhash.git" "master"
    "${MODULES_DIR}/highwayhash" "%s")''' % hh)
rep('''    ${MODULE_GOOGLE_TEST} "https://github.com/google/googletest.git" "main"
    "${MODULES_DIR}/googletest/build-release")''', '''    ${MODULE_GOOGLE_TEST} "https://github.com/google/googletest.git" "main"
    "${MODULES_DIR}/googletest/build-release" "%s")''' % gt)
open(path, "w").write(s)
PY
OUT="$W/new.patch"
( cd "$W/src" && git diff --no-color --no-ext-diff -- submodules/CMakeLists.txt ) > "$OUT"
[[ -s "$OUT" ]] || { echo "ERROR: 没有生成任何差异" >&2; exit 1; }
if [[ "${1:-}" == "--check" ]]; then
  cmp -s "$OUT" "$PATCH" || { echo "ERROR: 已入库的 patch 与生成结果不一致，请运行 $0 重新生成" >&2; diff "$PATCH" "$OUT" | head -30 >&2; exit 1; }
  echo "✓ search patch 与生成结果一致"
else
  mkdir -p "$(dirname "$PATCH")"; cp "$OUT" "$PATCH"; echo "✓ 已写入 $PATCH"
fi
