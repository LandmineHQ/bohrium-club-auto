# syntax=docker/dockerfile:1
# Multi-stage build:
#   1) web-build: compiles React + Vite + shadcn/ui dashboard into /web/dist
#   2) go-build:  compiles static Go binary (CGO_ENABLED=0) embedding /web/dist
#   3) runtime:   minimal Alpine image with ca-certificates + tzdata

FROM --platform=$BUILDPLATFORM node:22-alpine AS web-build

WORKDIR /web
COPY web/package.json web/package-lock.json ./
RUN npm ci
COPY web/index.html web/tsconfig.json web/tsconfig.app.json web/tsconfig.node.json web/vite.config.ts web/components.json ./
COPY web/src ./src
RUN npm run build

FROM --platform=$BUILDPLATFORM golang:1.24-alpine AS go-build

ARG TARGETOS=linux
ARG TARGETARCH=amd64
ARG APP_VERSION="dev"

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
COPY web/embed.go ./web/embed.go
COPY --from=web-build /web/dist ./web/dist

RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath -ldflags="-s -w -X main.Version=${APP_VERSION}" \
    -o /out/bohrium-club-auto ./cmd/bohrium-club-auto

FROM alpine:3.20

ARG APP_VERSION="dev"

ENV BOHRIUM_VERSION=${APP_VERSION} \
    BOHRIUM_HOST=0.0.0.0 \
    BOHRIUM_PORT=8788 \
    BOHRIUM_DATA_DIR=/data \
    BOHRIUM_RECON_DIR=/data/recon/bohrium-web \
    TZ=Asia/Shanghai

LABEL org.opencontainers.image.title="bohrium-club-auto" \
      org.opencontainers.image.version="${APP_VERSION}" \
      org.opencontainers.image.licenses="MIT"

RUN apk add --no-cache ca-certificates tzdata \
 && addgroup -S -g 10001 bohrium \
 && adduser -S -D -H -u 10001 -G bohrium bohrium \
 && mkdir -p /data \
 && chown -R bohrium:bohrium /data

COPY --from=go-build /out/bohrium-club-auto /usr/local/bin/bohrium-club-auto

USER bohrium

VOLUME ["/data"]

EXPOSE 8788

HEALTHCHECK --interval=30s --timeout=5s --start-period=15s --retries=3 \
  CMD wget -q -O /dev/null "http://127.0.0.1:${BOHRIUM_PORT}/healthz" || exit 1

STOPSIGNAL SIGTERM
ENTRYPOINT ["/usr/local/bin/bohrium-club-auto"]
