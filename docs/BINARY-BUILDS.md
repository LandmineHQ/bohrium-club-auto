# 多平台单文件二进制与镜像构建说明

> 相关文件：`Dockerfile`、`docker-compose.yml`、`docker-compose.ghcr.yml`、`.github/workflows/build-binaries.yml`、`.github/workflows/publish-binaries.yml`、`.github/workflows/build-docker-image.yml`、`.github/workflows/publish-docker-image.yml`。

## 1. 交付产物矩阵

本项目采用 Go 1.24 零第三方依赖编写，所有 Web 控制台静态资源（`web/static/*`）、870 条科研/写作提问语料、100 篇 arXiv 论文目录与 IANA 时区数据均通过 `go:embed` 与 `time/tzdata` 编译进单文件二进制。

通过 `CGO_ENABLED=0` 静态编译后，目标机器**无需安装 Go、Node.js、glibc/musl 额外运行库**，开箱即用：

| 目标平台 | GOOS / GOARCH | 产物名称 | 参考体积 |
| --- | --- | --- | --- |
| Windows x64 | `windows/amd64` | `bohrium-club-auto.exe` | ~7.8 MiB |
| Linux x64（glibc / Alpine musl 通用） | `linux/amd64` | `bohrium-club-auto` | ~7.5 MiB |
| Linux arm64（glibc / Alpine musl 通用） | `linux/arm64` | `bohrium-club-auto` | ~7.2 MiB |
| macOS Intel | `darwin/amd64` | `bohrium-club-auto` | ~7.6 MiB |
| macOS Apple Silicon | `darwin/arm64` | `bohrium-club-auto` | ~7.3 MiB |

## 2. 本地编译命令

```bash
# 当前平台
CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X main.Version=v0.1.0" \
  -o dist/bohrium-club-auto ./cmd/bohrium-club-auto

# 交叉编译 Linux x64 静态二进制
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w -X main.Version=v0.1.0" \
  -o dist/linux-x64/bohrium-club-auto ./cmd/bohrium-club-auto

# 交叉编译 Linux arm64 静态二进制
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -ldflags="-s -w -X main.Version=v0.1.0" \
  -o dist/linux-arm64/bohrium-club-auto ./cmd/bohrium-club-auto
```

## 3. GitHub Actions 构建与发布

全部构建与发布流水线均位于 `.github/workflows/`：

1. **`.github/workflows/build-binaries.yml`**（手动 `workflow_dispatch`）：
   - 在 `windows-latest`、`ubuntu-latest`、`ubuntu-24.04-arm`、`macos-15-intel`、`macos-15` 5 个原生 Runner 上分别运行 `go test`、静态编译并执行真实 HTTP 冒烟（验证 `/healthz` Chrome UA、未鉴权 `/api/settings` 返回 `401`、携带 `X-Admin-Password` 返回 `200`）；
   - 上传 5 个平台构建 Artifact。
2. **`.github/workflows/publish-binaries.yml`**（手动 `workflow_dispatch`，入参 `version` / `prerelease` / `draft`）：
   - 原生矩阵编译并打包 5 平台 `.zip`/`.tar.gz`，自动生成 `SHA256SUMS.txt`；
   - 创建或更新对应 Tag 的 GitHub Release，并自动写入下载链接表格。
3. **`.github/workflows/build-docker-image.yml`**（手动 `workflow_dispatch`）：
   - 构建 `Dockerfile`（`alpine:3.20` + 单文件二进制），输出镜像体积并运行容器级 HTTP 冒烟验证。
4. **`.github/workflows/publish-docker-image.yml`**（手动 `workflow_dispatch`，入参 `version` / `also_latest`）：
   - 通过 Buildx + QEMU 构建 `linux/amd64,linux/arm64` 多架构镜像并推送至 `ghcr.io/landminehq/bohrium-club-auto`。

> Runner 说明：保留 `macos-15-intel` 与 `macos-15` 标签（已避开 GitHub Actions 退役的 `macos-13`/`macos-14`）。

## 4. 直接运行二进制

```bash
# Linux / macOS
tar -xzf bohrium-club-auto-linux-x64.tar.gz
chmod +x bohrium-club-auto
BOHRIUM_ADMIN_PASSWORD="YourStrongPassword" ./bohrium-club-auto
```

- 默认监听 `http://0.0.0.0:8788/`，数据目录位于当前工作目录的 `data/`（可用 `BOHRIUM_DATA_DIR` 指定绝对路径）；
- 首次启动若未设置 `BOHRIUM_ADMIN_PASSWORD`，会自动生成随机密码打印至控制台并保存于 `data/config.json`；
- 二进制未做代码签名：Windows SmartScreen / macOS Gatekeeper 提示时放行即可（macOS 可执行 `xattr -d com.apple.quarantine ./bohrium-club-auto`）。
