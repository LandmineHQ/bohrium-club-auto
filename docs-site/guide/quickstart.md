# 快速开始

支持通过 **Docker Compose / 一行 Docker 命令**（直接拉取官方预构建镜像，无需克隆仓库）或 **预构建二进制** 快速运行。

## 方式一：Docker Compose（推荐，无需拉取仓库）

创建 `compose.yaml`（或 `docker-compose.yml`）：

```yaml
services:
  bohrium-club-auto:
    image: ghcr.io/landminehq/bohrium-club-auto:latest
    container_name: bohrium-club-auto
    restart: unless-stopped
    ports:
      - "8788:8788"
    environment:
      BOHRIUM_HOST: "0.0.0.0"
      BOHRIUM_PORT: "8788"
      BOHRIUM_DATA_DIR: "/data"
      TZ: "Asia/Shanghai"
      BOHRIUM_ADMIN_PASSWORD: "replace-with-a-strong-password"
    volumes:
      - bohrium-club-auto-data:/data

volumes:
  bohrium-club-auto-data:
```

启动服务并查看日志：

```bash
docker compose up -d
docker compose logs -f bohrium-club-auto
```

### 或者直接使用一行 `docker run` 启动

如果不想新建 Compose 文件，也可以直接执行：

```bash
docker run -d \
  --name bohrium-club-auto \
  --restart unless-stopped \
  -p 8788:8788 \
  -e BOHRIUM_HOST="0.0.0.0" \
  -e BOHRIUM_PORT="8788" \
  -e BOHRIUM_DATA_DIR="/data" \
  -e TZ="Asia/Shanghai" \
  -e BOHRIUM_ADMIN_PASSWORD="replace-with-a-strong-password" \
  -v bohrium-club-auto-data:/data \
  ghcr.io/landminehq/bohrium-club-auto:latest
```

若未设置 `BOHRIUM_ADMIN_PASSWORD`，首次启动时会自动生成随机密码并写入启动日志与 `/data/config.json`。

## 方式二：直接运行二进制

从 Release 下载对应平台归档文件解压后直接执行：

```bash
tar -xzf bohrium-club-auto-linux-x64.tar.gz
chmod +x bohrium-club-auto
BOHRIUM_ADMIN_PASSWORD="replace-with-a-strong-password" ./bohrium-club-auto
```

Windows 直接运行 `bohrium-club-auto.exe`。默认监听 `http://127.0.0.1:8788/`，持久化数据位于 `./data`（可通过 `BOHRIUM_DATA_DIR` 修改）。

## 方式三：本地源码构建（可选）

仅当需要自行修改源码并构建镜像时再克隆仓库：

```bash
git clone https://github.com/LandmineHQ/bohrium-club-auto.git
cd bohrium-club-auto
BOHRIUM_ADMIN_PASSWORD="replace-with-a-strong-password" docker compose up -d --build
```

## 初次配置步骤

1. 访问 `http://<host>:8788/`，使用管理员密码登录控制台；
2. 点击 **添加 / 登录账号**，通过微信扫码、OAuth、验证码或 `brmToken` 添加一个或多个账号；
3. 在「多账号与签到」页面检查账号余额、签到开关与每日任务状态；
4. 如需使用 `/v1/*` OpenAI 接口，前往「OpenAI 路由池」将账号拖入右侧路由池，并复制 API Base URL 与 API Key。
