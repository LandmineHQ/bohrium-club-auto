# 快速开始

支持通过 Docker Compose、预构建二进制或源码构建三种方式运行。

## 方式一：Docker Compose

```bash
git clone https://github.com/LandmineHQ/bohrium-club-auto.git
cd bohrium-club-auto

BOHRIUM_ADMIN_PASSWORD="YourStrongPassword" docker compose up -d --build
docker compose logs -f bohrium-club-auto
```

若未设置 `BOHRIUM_ADMIN_PASSWORD`，首次启动时会自动生成随机密码并写入 `/data/config.json`。

## 方式二：拉取预构建镜像

```bash
BOHRIUM_ADMIN_PASSWORD="YourStrongPassword" \
docker compose -f docker-compose.ghcr.yml up -d
```

## 方式三：直接运行二进制

解压对应平台的归档文件后直接执行：

```bash
tar -xzf bohrium-club-auto-linux-x64.tar.gz
chmod +x bohrium-club-auto
BOHRIUM_ADMIN_PASSWORD="YourStrongPassword" ./bohrium-club-auto
```

Windows 直接运行 `bohrium-club-auto.exe`。

## 初次配置步骤

1. 访问 `http://<host>:8788/`，使用管理员密码登录控制台；
2. 点击 **添加 / 登录账号**，通过微信扫码、OAuth、验证码或 `brmToken` 添加一个或多个账号；
3. 在「多账号与签到」页面检查账号余额、签到开关与每日任务状态；
4. 如需使用 `/v1/*` OpenAI 接口，前往「OpenAI 路由池」将账号拖入右侧路由池，并复制 API Base URL 与 API Key。
