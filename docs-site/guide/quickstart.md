# 快速开始

推荐在服务器上使用 **Docker Compose** 部署，或直接在 GitHub Releases 下载对应平台的静态单文件二进制运行。

## 方式一：Docker Compose（推荐）

```bash
git clone <your-repo-url> bohrium-club-auto
cd bohrium-club-auto

# 指定管理员密码并启动
BOHRIUM_ADMIN_PASSWORD="YourStrongPassword" docker compose up -d --build

# 查看启动日志
docker compose logs -f bohrium-club-auto
```

若未设置 `BOHRIUM_ADMIN_PASSWORD`，首次启动会自动生成强随机密码并打印到日志及写入 `/data/config.json`。

## 方式二：GHCR 预构建极简镜像

通过仓库内置的 `publish-docker-image.yml` 发布镜像后，可直接拉取运行：

```bash
GHCR_IMAGE="ghcr.io/<owner>/<repo>:latest" \
BOHRIUM_ADMIN_PASSWORD="YourStrongPassword" \
docker compose -f docker-compose.ghcr.yml up -d
```

## 方式三：直接运行单文件二进制

从仓库 **Releases** 下载对应平台的归档文件（无需安装 Go、Node.js 或任何依赖）：

```bash
# Linux / macOS
tar -xzf bohrium-club-auto-linux-x64.tar.gz
chmod +x bohrium-club-auto
BOHRIUM_ADMIN_PASSWORD="YourStrongPassword" ./bohrium-club-auto
```

Windows 用户解压 `bohrium-club-auto-windows-x64.zip` 后，双击或在终端运行 `bohrium-club-auto.exe`。

## 首次使用步骤

1. 浏览器访问 `http://<server-ip>:8788/`；
2. 输入管理员密码登录控制台；
3. 点击右上角 **「远程登录玻尔」** → **「获取 / 刷新二维码」**，用微信扫码完成远程登录；
4. 登录后首页自动展示光子余额、存储容量、配额与 4 项每日任务，可点击 **「一键领取每日任务」** 或 **「一键执行全部 Web 任务」** / 单项手动按钮测试。
