# syntax=docker/dockerfile:1
# ---------------------------------------------------------------------------
# Bohrium Club Auto — 极简运行时镜像（静态单文件 Go 二进制 + Alpine）
#
#   docker build -t bohrium-club-auto:latest .
#   docker run -d --name bohrium-club-auto -p 8788:8788 \
#     -e BOHRIUM_ADMIN_PASSWORD="change-me" \
#     -v bohrium-club-auto-data:/data bohrium-club-auto:latest
#
# 设计：
#   - build 阶段使用 golang:1.24-alpine，CGO_ENABLED=0 静态编译单文件二进制
#     （所有 870+ 提问语料、100 篇文献目录与管理面板均通过 go:embed 内置）；
#   - runtime 阶段仅包含 alpine:3.20 + ca-certificates + tzdata + 单文件二进制。
# ---------------------------------------------------------------------------

FROM --platform=$BUILDPLATFORM golang:1.24-alpine AS build

ARG TARGETOS=linux
ARG TARGETARCH=amd64
ARG APP_VERSION="dev"

WORKDIR /src

COPY go.mod ./
COPY cmd ./cmd
COPY internal ./internal
COPY web ./web

RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath -ldflags="-s -w -X main.Version=${APP_VERSION}" \
    -o /out/bohrium-club-auto ./cmd/bohrium-club-auto

# ---------- 运行阶段：最小 Alpine + 静态二进制 ----------
FROM alpine:3.20

ARG APP_VERSION="dev"

ENV BOHRIUM_VERSION=${APP_VERSION} \
    BOHRIUM_HOST=0.0.0.0 \
    BOHRIUM_PORT=8788 \
    BOHRIUM_DATA_DIR=/data \
    BOHRIUM_RECON_DIR=/data/recon/bohrium-web \
    TZ=Asia/Shanghai

LABEL org.opencontainers.image.title="bohrium-club-auto" \
      org.opencontainers.image.description="Self-hosted Bohrium User Community daily task automation and points/capacity dashboard (single Go binary)" \
      org.opencontainers.image.version="${APP_VERSION}" \
      org.opencontainers.image.licenses="MIT"

RUN apk add --no-cache ca-certificates tzdata \
 && addgroup -S -g 10001 bohrium \
 && adduser -S -D -H -u 10001 -G bohrium bohrium \
 && mkdir -p /data \
 && chown -R bohrium:bohrium /data

COPY --from=build /out/bohrium-club-auto /usr/local/bin/bohrium-club-auto

USER bohrium

# 持久化数据：config.json / state.json / pdfs/ / custom_pdfs/ / recon/
VOLUME ["/data"]

EXPOSE 8788

HEALTHCHECK --interval=30s --timeout=5s --start-period=15s --retries=3 \
  CMD wget -q -O /dev/null "http://127.0.0.1:${BOHRIUM_PORT}/healthz" || exit 1

STOPSIGNAL SIGTERM
ENTRYPOINT ["/usr/local/bin/bohrium-club-auto"]
