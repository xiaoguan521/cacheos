#!/usr/bin/env bash
# 由上游 valkey.conf / sentinel.conf 生成 sycache 默认配置模板（规格书 5.1、5.3）。
# 只替换注释中的品牌字样与默认路径，不改任何参数默认值；
# 控制台为每个实例渲染的正式配置见 internal/render（P2），本模板仅供手工部署参考。
#
# 用法：make-conf.sh <上游配置文件>  > 输出
set -euo pipefail
IN="${1:?usage: make-conf.sh <valkey.conf|sentinel.conf>}"
sed -E \
  -e 's#https?://valkey\.io/[a-z/-]*#the CacheOS documentation#g' \
  -e 's#https?://valkey\.io#the CacheOS documentation#g' \
  -e 's#/var/run/valkey_#/run/sycache/sycache-#g' \
  -e 's#/var/run/valkey-sentinel\.pid#/run/sycache/sycache-sentinel.pid#g' \
  -e 's#/var/run/valkey\.pid#/run/sycache/sycache.pid#g' \
  -e 's#/run/valkey\.(sock|pid)#/run/sycache/sycache.\1#g' \
  -e 's#/etc/valkey/#/opt/sycache/conf/#g' \
  -e 's#/var/valkey/#/opt/sycache/scripts/#g' \
  -e 's#valkey\.conf#sycache.conf#g' \
  -e 's#Valkey#SyCache#g; s#VALKEY#SYCACHE#g; s#valkey#sycache#g' \
  "$IN"
