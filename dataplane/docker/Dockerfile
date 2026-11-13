# sycache 数据面运行时镜像：直接使用发布构建产出的 tar.gz（glibc 2.17 兼容构建，OpenSSL 静态链接）。
# 构建上下文 = 含 sycache-<版本>-<架构>.tar.gz 的目录（binary-packages 工作流的 dist/）。
#
#   docker build -f dataplane/docker/Dockerfile --build-arg TARBALL=sycache-0.1.0-x86_64.tar.gz dist/
FROM debian:12-slim
ARG TARBALL
LABEL org.opencontainers.image.source="https://github.com/xiaoguan521/cacheos" \
      org.opencontainers.image.description="SyCache data plane (Redis-compatible), CacheOS" \
      org.opencontainers.image.licenses="LicenseRef-Proprietary"
RUN test -n "$TARBALL" \
 && apt-get update \
 && apt-get install -y --no-install-recommends libsystemd0 ca-certificates \
 && rm -rf /var/lib/apt/lists/* \
 && groupadd -r -g 999 sycache \
 && useradd -r -u 999 -g sycache -d /data -s /usr/sbin/nologin sycache
COPY ${TARBALL} /tmp/sycache.tar.gz
RUN tar -C / -xzf /tmp/sycache.tar.gz && rm /tmp/sycache.tar.gz \
 && for b in sycache-server sycache-sentinel sycache-cli sycache-benchmark sycache-check-rdb sycache-check-aof; do \
      ln -s /home/atwasoft/sycache/bin/$b /usr/local/bin/$b; done \
 && install -d -o sycache -g sycache -m 0750 /data \
 && install -d -o sycache -g sycache -m 0750 /var/log/sycache \
 && install -d -m 0755 /etc/sycache
COPY sycache.conf /etc/sycache/sycache.conf
USER sycache
WORKDIR /data
VOLUME /data
EXPOSE 6379
HEALTHCHECK --interval=15s --timeout=3s --start-period=30s --retries=3 \
  CMD ["sh", "-c", "sycache-cli -p 6379 ping 2>/dev/null | grep -qE 'PONG|NOAUTH' || exit 1"]
# 前台运行，不要加 --daemonize yes。默认读取 /etc/sycache/sycache.conf（含默认口令，部署时务必替换 requirepass）；
# 也可用 docker run ... --requirepass 覆盖（此时不再读取该配置文件）。
ENTRYPOINT ["sycache-server"]
CMD ["/etc/sycache/sycache.conf"]
