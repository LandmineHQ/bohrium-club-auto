# 前端与二进制构建

## 1. 本地完整构建

前端源码位于 `web/`（React + Vite + TypeScript + Tailwind CSS + shadcn/ui），编译产物输出到 gitignored 的 `web/dist/`（仓库仅保留 `web/dist/.gitkeep` 占位文件）。

```bash
# 1. 构建前端静态资源
npm ci --prefix web
npm run build --prefix web

# 2. 编译静态单文件 Go 二进制（CGO_ENABLED=0）
CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X main.Version=dev" \
  -o dist/bohrium-club-auto ./cmd/bohrium-club-auto
```

开发调试时可开启后端 `serve` 后启动 Vite 开发服务器（默认代理 `/api`、`/v1`、`/healthz` 至 `http://127.0.0.1:8788`）：

```bash
npm run dev --prefix web
```

## 2. CI 与发布工作流

| Workflow | 触发方式 | 流程说明 |
| --- | --- | --- |
| `.github/workflows/ci.yml` | Push / PR | 构建 `web/`（TypeScript 校验 + Vite 构建）→ `go vet` → `go test -race` → 静态编译 |
| `.github/workflows/build-binaries.yml` | 手动 (`workflow_dispatch`) | 单次构建 `web/dist` Artifact → 5 平台矩阵编译静态二进制并执行 HTTP `/healthz` + SPA + 鉴权冒烟 |
| `.github/workflows/publish-binaries.yml` | 手动 (`workflow_dispatch`) | 构建 `web/dist` → 5 平台打包 `.zip`/`.tar.gz` + `SHA256SUMS.txt` 并发布 Release |
| `.github/workflows/build-docker-image.yml` | 手动 (`workflow_dispatch`) | 执行三阶段 `Dockerfile` 构建并运行容器级 HTTP 冒烟 |
| `.github/workflows/publish-docker-image.yml` | 手动 (`workflow_dispatch`) | Buildx 构建 `linux/amd64,linux/arm64` 多架构镜像并推送至 GHCR |
| `.github/workflows/deploy-docs.yml` | Push (`docs-site/**`) / 手动 | 构建 VitePress 文档并部署至 GitHub Pages |

## 3. 支持平台

| 平台 | Runner | 产物 |
| --- | --- | --- |
| Windows x64 | `windows-latest` | `bohrium-club-auto-windows-x64.zip` |
| Linux x64 (static) | `ubuntu-latest` | `bohrium-club-auto-linux-x64.tar.gz` |
| Linux arm64 (static) | `ubuntu-24.04-arm` | `bohrium-club-auto-linux-arm64.tar.gz` |
| macOS Intel | `macos-15-intel` | `bohrium-club-auto-darwin-x64.tar.gz` |
| macOS Apple Silicon | `macos-15` | `bohrium-club-auto-darwin-arm64.tar.gz` |
