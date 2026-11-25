# 二进制与 Docker 发布流水线

本仓库提供了完整的多平台静态单文件二进制构建、GitHub Release 发布与多架构 GHCR Docker 镜像发布流水线，**完全兼容 GitHub Private 仓库**。

## 工作流总览

| Workflow 文件 | 触发方式 | 作用 |
| --- | --- | --- |
| `.github/workflows/ci.yml` | Push / PR | 执行 `go vet ./...`、`go test -race -v ./...` 与 `CGO_ENABLED=0` 静态编译验证 |
| `.github/workflows/build-binaries.yml` | 手动 (`workflow_dispatch`) | 在 5 平台原生 Runner 编译静态二进制，执行真实 HTTP `/healthz` + 管理员 401/200 冒烟测试并上传 Artifact |
| `.github/workflows/publish-binaries.yml` | 手动 (`workflow_dispatch`) | 构建 5 平台静态归档 + `SHA256SUMS.txt` 并创建/更新 GitHub Release（自动生成下载表格） |
| `.github/workflows/build-docker-image.yml` | 手动 (`workflow_dispatch`) | 构建极简 Alpine 镜像，汇报未压缩 MiB 体积并运行容器级 HTTP 冒烟测试 |
| `.github/workflows/publish-docker-image.yml` | 手动 (`workflow_dispatch`) | 使用 Buildx 构建 `linux/amd64,linux/arm64` 多架构镜像并推送至 `ghcr.io/landminehq/bohrium-club-auto` |
| `.github/workflows/recon-check.yml` | 手动 (`workflow_dispatch`) | 抓取线上 Bohrium 前端源码并校验关键 API 契约标记 |
| `.github/workflows/deploy-docs.yml` | Push (`docs-site/**`) / 手动 | 构建 VitePress 文档站并部署至 GitHub Pages |

## 支持的二进制平台矩阵

所有产物均以 `CGO_ENABLED=0` 纯静态编译，Linux 产物同时兼容 glibc（Debian/Ubuntu/CentOS）与 musl（Alpine），单文件约 **7.2 ~ 7.8 MB**：

| 平台 | GitHub Runner | 压缩包 |
| --- | --- | --- |
| Windows x64 | `windows-latest` | `bohrium-club-auto-windows-x64.zip` |
| Linux x64 (static) | `ubuntu-latest` | `bohrium-club-auto-linux-x64.tar.gz` |
| Linux arm64 (static) | `ubuntu-24.04-arm` | `bohrium-club-auto-linux-arm64.tar.gz` |
| macOS Intel | `macos-15-intel` | `bohrium-club-auto-darwin-x64.tar.gz` |
| macOS Apple Silicon | `macos-15` | `bohrium-club-auto-darwin-arm64.tar.gz` |

## 在 GitHub Actions 中触发发布

1. **发布单文件二进制到 Release**：在仓库 **Actions → `publish-binaries` → Run workflow**，输入版本号（如 `v0.1.0`），完成后即可在 [Releases](https://github.com/LandmineHQ/bohrium-club-auto/releases) 下载各平台压缩包与 `SHA256SUMS.txt`。
2. **发布多架构 Docker 镜像到 GHCR**：在 **Actions → `publish-docker-image` → Run workflow**，输入 `version`（如 `v0.1.0`）并勾选 `also_latest`，即可推送至 `ghcr.io/landminehq/bohrium-club-auto`。
