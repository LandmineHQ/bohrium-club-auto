# 服务器端远程登录

玻尔生态全站（`club.bohrium.com`、`www.bohrium.com`、`platform.bohrium.com`）共享 `.bohrium.com` 域下的 JWT Cookie `brmToken`。本程序支持五类远程登录方式，**无需在本地电脑长期运行**。

## 方式一：微信官方扫码（推荐）

1. 在管理面板左下角点击 **「远程登录 / 切换账号」** → **「微信扫码」**；
2. 点击 **「获取 / 刷新二维码」**，服务端向 `platform.bohrium.com/api/account/wechat/login_qrcode` 申请官方二维码；
3. 手机微信扫码并确认后，前端自动轮询 `/api/bohrium/wechat/poll`，登录完成后自动：
   - 解析 JWT 内的 `userId`、`orgId` 与过期时间；
   - 查询 `/bohrapi/v1/account/info` 获取账号信息；
   - 检查 `club.bohrium.com/api/platform/session`，若尚未绑定社区则自动调用 `POST /api/enroll` 完成绑定。

::: tip 无图形验证码
官方微信公众号扫码链路不触发腾讯图形验证码，非常适合远程无头服务器部署。
:::

## 方式二：Google / Apple / CARSI / 飞书 / 北大 IAAA（OAuth 第三方登录）

1. 切换到 **「Google / 第三方」** 标签页，选择对应登录方式（支持 `Google`、`Apple`、`CARSI 高校`、`飞书 Feishu`、`北大 IAAA`）；
2. 服务端调用 `platform.bohrium.com/api/account/oauth_url` 获取最新官方授权链接并在新标签页打开；
3. 在授权页完成登录后，浏览器会跳转到 `login.bohrium.com` 或 `platform.bohrium.com/auth-code`（地址栏含 `code=` 或 `token=`），复制该完整回调网址粘贴回控制台输入框，点击 **「完成 OAuth 验证登录」**，服务端通过 `POST /api/oauth/verify` 完成换票并持久化会话。

## 方式三：手机 / 邮箱验证码登录

在 **「验证码」** 标签页选择手机号或邮箱，点击 **「获取验证码」** 后，后端会调用 `/api/bohrium/captcha/prepare` 获取初始化参数，并在当前浏览器中弹出官方腾讯云人机图形验证（`TJCaptcha.js`）；您手动完成滑块/点选验证后，前端自动将验证票据（`ticket`、`randStr`、`captchaAppId`）随发送请求提交至 `send_by_phone_v3` / `send_by_email_v3`，输入收到的验证码即可登录。

## 方式四：账号密码登录（支持学术码）

支持手机号、邮箱或学术码（Account ID）+ 密码登录，复现了 `platform.bohrium.com` 的 AES-128-CBC（PKCS7 + 双重 Base64）加密逻辑；提交前先通过 `/api/captcha/check_user` 判断是否免验，如需人机验证则自动在页面弹出官方腾讯云图形验证组件供您手动完成验证，再携带验证票据提交 `/api/account/v3/login`。

## 方式五：导入 `brmToken`

如果浏览器已登录 `club.bohrium.com` 或 `www.bohrium.com`，可在 DevTools → Application → Cookies 中复制 `brmToken`（支持直接粘贴整段 Cookie 字符串），在 **「导入 Token」** 标签页提交即可。

## 自动续期机制

开启设置中的 **「后台自动续期 brmToken」**（默认开启）后，调度器每 30 分钟检查一次 JWT 有效期：当距离过期不足 48 小时或距上次续期超过 24 小时时，自动调用 `POST /platform-gateway/v1/account/refresh_token` 刷新并持久化新 Token。
