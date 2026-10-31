# CacheOS —— 当前阶段只有数据面（P1）。控制台、agent、wrapper 在 P2 加入。
UPSTREAM_SRC ?=
DIST         ?= dist
SRC_OPT      := $(if $(UPSTREAM_SRC),-s $(UPSTREAM_SRC),)

.PHONY: all patch patch-check sycache golden compat brand test clean

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

# 改名黄金验收
golden:
	scripts/golden-rename.sh $(firstword $(wildcard $(DIST)/sycache-[0-9]*.tar.gz))

# 行为等价与互通测试；设置 VALKEY_BIN=<上游 valkey 的 src 目录> 时额外跑混合版本互通
compat:
	rm -rf $(DIST)/pkg && mkdir -p $(DIST)/pkg && tar -C $(DIST)/pkg -xzf $(firstword $(wildcard $(DIST)/sycache-[0-9]*.tar.gz))
	SYCACHE_BIN=$(abspath $(DIST))/pkg/opt/sycache/bin python3 test/compat/compat_test.py -v

brand:
	scripts/brand-check.sh

test: patch-check brand golden compat

clean:
	rm -rf $(DIST)
