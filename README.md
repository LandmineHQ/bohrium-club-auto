# Bohrium Club Auto

面向 **玻尔用户社区 (`https://club.bohrium.com/`)** 的自托管 **Go** 每日任务自动化程序与 Web 管理面板，零第三方依赖、静态单文件编译、内置 Chrome 浏览器出站指纹与前端 Recon 变更查询。

## 核心特性

1. **服务器端远程登录（无需本地电脑挂机）**
   - **微信官方扫码登录（推荐）**：服务端直接向 `platform.bohrium.com` 申请微信登录二维码，在管理面板中手机扫码即可完成服务器侧登录，无图形验证码。
   - **手动导入 `brmToken`**：支持直接粘贴浏览器 Cookie 中的 `brmToken`。
   - **自动续期**：后台定时调用 `/platform-gateway/v1/account/refresh_token` 延长 JWT 有效期。
2. **管理员密码保护的 Web 管理面板**
   - 展示**剩余积分与容量**：个人/组织光子余额、钱包余额、7 日即将过期光子及批次明细、近期光子收支流水、会员等级与功能配额（文献检索 / 知识库对话 / 深度研究）、知识库存储容量（剩余 / 已用 / 总量）。
   - 展示**每日 4 项任务状态**（未领取 / 待完成 / 已完成 / 已跳过）：
     - 提供 **「一键领取每日任务」** 按钮；
     - 提供 **「一键执行全部 Web 任务」** 及 **单项任务手动触发按钮**；
     - 自动跳过第 2 项桌面客户端任务（`体验玻尔科学空间「客户端」`）；
     - **彻底禁用旧版已废弃的 200 光子每日签到接口**（绝不调用 `/bohrapi/v1/account/user/integral_receive`）。
3. **真实产品 API 执行 + 立即终止请求**
   - 内置 **870 条科研与学术写作随机提问语料**（330 条科学空间 Web 问题 + 320 条学术写作问题 + 220 条文献阅读问题）及 **100 篇真实公开 arXiv 科学文献 PDF 目录**（通过 `go:embed` 直接打包进二进制）。
   - **任务 1（科学空间 Web）** / **任务 4（学术写作）**：随机抽取问题调用真实聊天 API，上报对应 `task_key` 至 `/api/v1/query-events` 后**立即调用 `cancelWorkflow` 终止请求**。
   - **任务 3（文献阅读）**：从 100 篇文献目录中随机挑选 PDF（自动下载并缓存到 `data/pdfs/`，也支持往 `data/custom_pdfs/` 放入自定义 PDF），经由 `literature-sage` 上传并登记至 `scix/reader`，随机抽取文献问题提交提问后**立即终止请求**。
4. **Chrome 浏览器出站指纹**
   - 所有出站 HTTP 请求统一注入桌面端 Chrome `User-Agent`、`Sec-CH-UA`、`Sec-CH-UA-Mobile`、`Sec-CH-UA-Platform` 与 `Accept-Language`，绝不暴露 `Go-http-client`。
5. **Recon 快照与上游变更查询 + GitHub Actions CI/CD**
   - 参考 [`LandmineHQ/duckai-cpa`](https://github.com/LandmineHQ/duckai-cpa) 的仓库结构、CI 流水线及 `recon/` 本地快照比对模式（详见 [docs/RECON-SNAPSHOT.md](docs/RECON-SNAPSHOT.md)）。

## Docker Compose 快速部署（推荐）

```bash
git clone <your-repo-url> bohrium-club-auto
cd bohrium-club-auto

# 设置管理员密码并启动
BOHRIUM_ADMIN_PASSWORD="YourStrongPassword" docker compose up -d --build

# 查看日志（若未设置 BOHRIUM_ADMIN_PASSWORD，首次启动会在日志中打印随机生成的密码）
docker compose logs -f bohrium-club-auto
```

打开浏览器访问 `http://<server-ip>:8788/`，输入管理员密码进入控制台，点击 **「远程登录玻尔」** 扫码绑定账号即可。

使用预构建 GHCR 镜像：

```bash
GHCR_IMAGE="ghcr.io/<owner>/bohrium-club-auto:latest" \
BOHRIUM_ADMIN_PASSWORD="YourStrongPassword" \
docker compose -f docker-compose.ghcr.yml up -d
```

## 环境变量

| 变量名 | 默认值 | 说明 |
| --- | --- | --- |
| `BOHRIUM_HOST` | `0.0.0.0` | 监听地址 |
| `BOHRIUM_PORT` | `8788` | 监听端口 |
| `BOHRIUM_DATA_DIR` | `data`（容器内 `/data`） | 配置、登录态、日志与 PDF 缓存目录 |
| `BOHRIUM_ADMIN_PASSWORD` | 首次启动自动生成 | 控制台管理员密码（也支持 `ADMIN_PASSWORD`） |
| `BOHRIUM_SCHEDULE_ENABLED` | `true` | 是否开启每日定时自动执行 |
| `BOHRIUM_SCHEDULE_TIME` | `08:15` | 每日定时执行时间（24 小时制 `HH:MM`） |
| `BOHRIUM_SCHEDULE_TZ` | `Asia/Shanghai` | 定时任务时区 |
| `BOHRIUM_USER_AGENT` | Chrome 136 Windows UA | 自定义出站 Chrome User-Agent |
| `BOHRIUM_PROXY` | 空 | 可选 HTTP/HTTPS 出站代理 |
| `BOHRIUM_RECON_DIR` | `recon/bohrium-web` | 本地 Recon 基线目录（已 gitignore） |

## 本地开发与 Recon 命令

```bash
# 运行全部单元测试（含竞态检测）
go test -race -v ./...

# 编译单文件二进制
CGO_ENABLED=0 go build -o bohrium-club-auto ./cmd/bohrium-club-auto

# 启动服务
./bohrium-club-auto serve

# 拉取前端基线快照 & 检测线上更新
./bohrium-club-auto recon pull
./bohrium-club-auto recon diff
```

## GitHub Actions 流水线

- `.github/workflows/ci.yml`：Push/PR 自动执行 `go vet`、`go test -race` 与静态编译校验。
- `.github/workflows/build-binaries.yml`：手动触发构建 5 平台静态单文件二进制（Windows x64 / Linux x64 & arm64 / macOS Intel & Apple Silicon）并执行原生 HTTP `/healthz` + 管理员鉴权冒烟测试。
- `.github/workflows/publish-binaries.yml`：手动触发构建并发布多平台归档至 GitHub Release。
- `.github/workflows/build-docker-image.yml`：手动构建极简 Alpine 镜像、统计体积并运行容器级 HTTP 冒烟测试。
- `.github/workflows/publish-docker-image.yml`：手动构建多架构（`linux/amd64,linux/arm64`）镜像并推送至 GHCR。
- `.github/workflows/recon-check.yml`：手动触发线上 Bohrium 前端静态源码 Recon 巡检与契约标记验证。
