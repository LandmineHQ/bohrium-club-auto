# 前端、二进制与 Docker 构建说明

> 相关文件：`web/`、`Dockerfile`、`docker-compose.yml`、`docker-compose.ghcr.yml`、`.github/workflows/*.yml`。

## 1. 本地构建

项目由 React + Vite + TypeScript + shadcn/ui 前端（`web/`）与 Go 1.24 后端组成。构建二进制前需先生成 `web/dist/`（由 `web/embed.go` 打包进静态单文件）：

```bash
# 1. 构建前端
npm ci --prefix web
npm run build --prefix web

# 2. 编译当前平台二进制
CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X main.Version=v0.1.0" \
  -o dist/bohrium-club-auto ./cmd/bohrium-club-auto

# 交叉编译 Linux x64 / arm64
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w -X main.Version=v0.1.0" \
  -o dist/linux-x64/bohrium-club-auto ./cmd/bohrium-club-auto
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -ldflags="-s -w -X main.Version=v0.1.0" \
  -o dist/linux-arm64/bohrium-club-auto ./cmd/bohrium-club-auto
```

注：`web/dist/.gitkeep` 用于确保未执行前端构建时 `go test ./...` 也能直接通过编译。

## 2. Docker 多阶段构建

`Dockerfile` 包含三个阶段：`node:22-alpine` 构建 `web/dist` → `golang:1.24-alpine` 静态编译 Go 二进制 → `alpine:3.20` 极简运行镜像。

```bash
docker build -t bohrium-club-auto:local .
docker compose up -d --build
```

## 3. GitHub Actions 流水线

- `.github/workflows/ci.yml`：Push/PR 自动构建前端并运行 `go vet`、`go test -race` 与静态编译；
- `.github/workflows/build-binaries.yml`：手动触发，先构建一份 `web/dist` Artifact，再在 5 平台原生 Runner 上测试、编译并执行 HTTP 冒烟（校验 `/healthz`、SPA `/assets/` 与管理员 401/200）；
- `.github/workflows/publish-binaries.yml`：手动触发，打包 5 平台归档与 `SHA256SUMS.txt` 并发布 Release；
- `.github/workflows/build-docker-image.yml` / `publish-docker-image.yml`：容器构建冒烟与多架构 GHCR 推送。
