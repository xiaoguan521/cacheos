# sycache-console 运行时镜像：前端与 Go 二进制都在镜像内构建（依赖已 vendor，无需联网拉 Go 模块）。
# 构建上下文 = 仓库根目录：
#   docker build -f deploy/docker/console.Dockerfile -t sycache-console:dev .
FROM node:22-bookworm-slim AS web
WORKDIR /src/web
RUN corepack enable
COPY web/package.json web/pnpm-lock.yaml ./
RUN corepack pnpm install --frozen-lockfile
COPY web/ ./
RUN corepack pnpm run build

FROM golang:1.26-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
COPY vendor ./vendor
COPY cmd ./cmd
COPY internal ./internal
COPY api ./api
COPY web/embed.go ./web/embed.go
COPY --from=web /src/web/dist ./web/dist
RUN CGO_ENABLED=0 go build -mod=vendor -trimpath -o /out/sycache-console ./cmd/sycache-console

FROM debian:12-slim
LABEL org.opencontainers.image.source="https://github.com/xiaoguan521/cacheos" \
      org.opencontainers.image.description="CacheOS console" \
      org.opencontainers.image.licenses="LicenseRef-Proprietary"
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates curl \
 && rm -rf /var/lib/apt/lists/* \
 && groupadd -r -g 10001 sycache-console \
 && useradd -r -u 10001 -g sycache-console -d /var/lib/sycache-console -s /usr/sbin/nologin sycache-console \
 && install -d -o sycache-console -g sycache-console -m 0700 /var/lib/sycache-console
COPY --from=build /out/sycache-console /usr/bin/sycache-console
USER sycache-console
ENV CACHEOS_DATA=/var/lib/sycache-console \
    CACHEOS_LISTEN=0.0.0.0:8443 \
    CACHEOS_GRPC_LISTEN=0.0.0.0:9443
VOLUME /var/lib/sycache-console
EXPOSE 8443 9443
# 未启用 HTTPS 时用 http 探测；CACHEOS_TLS_AUTO / TLS 证书启用后改用 https（-k）。
HEALTHCHECK --interval=15s --timeout=3s --start-period=20s --retries=3 \
  CMD ["sh", "-c", "curl -fsS http://127.0.0.1:8443/healthz >/dev/null || curl -fsSk https://127.0.0.1:8443/healthz >/dev/null"]
ENTRYPOINT ["/usr/bin/sycache-console"]
