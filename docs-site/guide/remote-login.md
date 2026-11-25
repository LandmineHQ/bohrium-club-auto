# 服务器端远程登录

玻尔生态全站（`club.bohrium.com`、`www.bohrium.com`、`platform.bohrium.com`）共享 `.bohrium.com` 域下的 JWT Cookie `brmToken`。本程序支持三种远程登录方式，**无需在本地电脑部署**。

## 方式一：微信官方扫码（推荐）

1. 在管理面板点击 **「远程登录玻尔」** → **「微信扫码（推荐）」**；
2. 点击 **「获取 / 刷新二维码」**，服务端向 `platform.bohrium.com/api/account/wechat/login_qrcode` 申请官方二维码；
3. 手机微信扫码并确认后，前端自动轮询 `/api/bohrium/wechat/poll`，登录完成后自动：
   - 解析 JWT 内的 `userId`、`orgId` 与过期时间；
   - 查询 `/bohrapi/v1/account/info` 获取昵称等基础信息；
   - 检查 `club.bohrium.com/api/platform/session`，若尚未绑定社区则自动调用 `POST /api/enroll` 完成绑定。

::: tip 无图形验证码
官方微信公众号扫码链路不触发腾讯图形验证码，非常适合远程无头服务器部署。
:::

## 方式二：导入 `brmToken`

如果浏览器已登录 `club.bohrium.com` 或 `www.bohrium.com`，可在 DevTools → Application → Cookies 中复制 `brmToken`（支持直接粘贴整段 Cookie 字符串），在弹窗的 **「导入 brmToken」** 标签页提交即可。

## 方式三：账号密码登录（仅无验证码时）

程序复现了 `platform.bohrium.com` 的 AES-128-CBC（PKCS7 + 双重 Base64）密码加密逻辑，在调用前先查询 `/api/captcha/check_user`：

- 若返回 `skip: true`，直接完成登录；
- 若触发图形验证码，会立即提示切换至「微信扫码」，绝不尝试绕过验证码。

## 自动续期机制

开启设置中的 **「后台自动续期 brmToken」**（默认开启）后，调度器每 30 分钟检查一次 JWT 有效期：当距离过期不足 48 小时或距上次续期超过 24 小时时，自动调用 `POST /platform-gateway/v1/account/refresh_token` 刷新并持久化新 Token。
