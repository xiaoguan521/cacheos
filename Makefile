# CacheOS —— 数据面门禁与控制面/agent 的 Go、Vue 测试分开执行。
UPSTREAM_SRC ?=
DIST         ?= dist
SRC_OPT      := $(if $(UPSTREAM_SRC),-s $(UPSTREAM_SRC),)
JOBS         ?= $(shell if command -v nproc >/dev/null 2>&1; then nproc; elif command -v sysctl >/dev/null 2>&1; then sysctl -n hw.ncpu; else echo 2; fi)
# 发布构建必须 USE_SYSTEMD=yes（sycache@.service 使用 Type=notify）
export USE_SYSTEMD

.PHONY: all patch patch-check sycache golden compat compat-interop compat-deps brand test ci versions vex compat-build deb clients upstream-suite demo-local modules modules-check clean

# 模块清单与版本一致性检查（轻量，可加入 ci）
modules-check:
	dataplane/modules/check.sh

# 本地完整校验（与 CI 的 build-and-verify 相同），提交前执行，避免反复占用 GitHub Actions
ci: patch-check brand modules-check sycache versions vex golden compat-interop clients

# versions.yaml、UPSTREAM、patches、BUILDINFO 与 SBOM 一致
versions:
	scripts/check-versions.sh $(firstword $(wildcard $(DIST)/sycache-[0-9]*.tar.gz))

vex:
	scripts/check-vex.sh

# 兼容构建（glibc 2.17 容器 + 静态 OpenSSL）并打 RPM / DEB，需要 docker
compat-build:
	dataplane/build-compat.sh $(SRC_OPT)

# 由当前构建产物打 DEB（宿主机构建，依赖随宿主 glibc；正式发布用 compat-build）
deb:
	dataplane/packaging/build-deb.sh $(firstword $(wildcard $(DIST)/sycache-[0-9]*.tar.gz)) $(DIST)

# 上游 Tcl 测试集（需要 tclsh），结果与 test/upstream/known-diff.txt 比对
upstream-suite:
	test/upstream/run-suite.sh $(SRC_OPT)

# 构建一份未改名的上游 Valkey，与 sycache 做混合复制与 RDB 互读测试
compat-interop: compat-deps
	$(MAKE) compat VALKEY_BIN=$(abspath $(DIST))/valkey-upstream/src

compat-deps:
	rm -rf $(DIST)/valkey-upstream
	@if [ -n "$(UPSTREAM_SRC)" ]; then cp -a $(UPSTREAM_SRC) $(DIST)/valkey-upstream; else \
	  . dataplane/UPSTREAM && git -c advice.detachedHead=false clone -q --depth 1 --branch $$UPSTREAM_TAG $$UPSTREAM_GIT $(DIST)/valkey-upstream; fi
	$(MAKE) -C $(DIST)/valkey-upstream -j$(JOBS) BUILD_TLS=no >/dev/null

all: patch-check sycache golden

# 由 dataplane/rename/rename.py 重新生成改名 patch（修改改名规则后执行并提交结果）
patch:
	scripts/gen-patch.sh $(SRC_OPT)

# CI：仓库中的 patch 必须与规则重新生成的结果逐字节一致
patch-check:
	scripts/gen-patch.sh $(SRC_OPT) -c

# 构建安装包：$(DIST)/sycache-<版本>-<架构>.tar.gz
sycache:
	dataplane/build-sycache.sh $(SRC_OPT) -o $(DIST)

# 编译扩展数据类型模块（json、search、bloom）；耗时，非 make ci 必需项
modules:
	dataplane/build-modules.sh -o $(DIST)

# 改名黄金验收
golden:
	scripts/golden-rename.sh $(firstword $(wildcard $(DIST)/sycache-[0-9]*.tar.gz))

# 行为等价与互通测试；设置 VALKEY_BIN=<上游 valkey 的 src 目录> 时额外跑混合版本互通
compat:
	rm -rf $(DIST)/pkg && mkdir -p $(DIST)/pkg && tar -C $(DIST)/pkg -xzf $(firstword $(wildcard $(DIST)/sycache-[0-9]*.tar.gz))
	SYCACHE_BIN=$(abspath $(DIST))/pkg/home/atwasoft/sycache/bin python3 test/compat/compat_test.py -v

# 客户端兼容矩阵（Python、Go；可选 Java、Node、.NET），源码按 test/clients/clients.lock 拉取
clients:
	SYCACHE_BIN=$(abspath $(DIST))/pkg/home/atwasoft/sycache/bin python3 test/clients/client_matrix_test.py -v

# P4 单机演示：需要先准备 sycache 二进制，默认从 /home/atwasoft/sycache/bin 读取
demo-local:
	scripts/demo-local.sh

brand:
	scripts/brand-check.sh

test: patch-check brand golden compat

clean:
	rm -rf $(DIST)
