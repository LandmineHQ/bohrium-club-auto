# 完整端点参考

## 健康检查与控制台登录

| 方法 | 路径 | 鉴权 | 说明 |
| --- | --- | --- | --- |
| <span class="api-method get">GET</span> | `/healthz` | 公开 | 进程存活状态、版本号、运行时长、当前 Chrome UA |
| <span class="api-method get">GET</span> | `/api/auth/status` | 公开 | 查询当前 Cookie/Header 是否已通过管理员认证 |
| <span class="api-method post">POST</span> | `/api/auth/login` | 公开 | 校验管理员密码并签发 `brm_auto_session` Cookie（带频次限流） |
| <span class="api-method post">POST</span> | `/api/auth/logout` | 公开 | 清除管理员登录 Cookie |

## 玻尔账号远程登录与会话

| 方法 | 路径 | 鉴权 | 说明 |
| --- | --- | --- | --- |
| <span class="api-method get">GET</span> | `/api/bohrium/session` | 管理员 | 实时检测当前 `brmToken` 登录与社区绑定状态 |
| <span class="api-method post">POST</span> | `/api/bohrium/wechat/qr` | 管理员 | 申请官方微信扫码二维码（返回 `sceneId`、`qrCodeUrl`） |
| <span class="api-method get">GET</span> | `/api/bohrium/wechat/poll?sceneId=...` | 管理员 | 轮询扫码结果，登录成功后自动保存并绑定社区 |
| <span class="api-method post">POST</span> | `/api/bohrium/oauth/url` | 管理员 | 获取官方第三方 OAuth 授权 URL（Body: `{"provider":"google|apple|carsi|feishu|pku"}`） |
| <span class="api-method post">POST</span> | `/api/bohrium/oauth/verify` | 管理员 | 提交回调 URL 或 `code` 完成第三方 OAuth 换票登录（Body: `{"provider":"...","callbackInput":"..."}`） |
| <span class="api-method post">POST</span> | `/api/bohrium/code/send` | 管理员 | 发送手机/邮箱验证码（Body: `{"kind":"phone|email","target":"...","countryCode":"86"}`） |
| <span class="api-method post">POST</span> | `/api/bohrium/login/code` | 管理员 | 使用手机/邮箱验证码登录（Body: `{"kind":"phone|email","target":"...","countryCode":"86","code":"..."}`） |
| <span class="api-method post">POST</span> | `/api/bohrium/login/token` | 管理员 | 手动导入 `brmToken`（Body: `{"token":"..."}`） |
| <span class="api-method post">POST</span> | `/api/bohrium/login/password` | 管理员 | 无图形验证码时的密码登录（Body: `{"account":"...","password":"..."}`） |
| <span class="api-method post">POST</span> | `/api/bohrium/refresh-token` | 管理员 | 手动触发 `/platform-gateway/v1/account/refresh_token` 续期 |
| <span class="api-method post">POST</span> | `/api/bohrium/logout` | 管理员 | 清除本地保存的玻尔登录凭据 |

## 每日任务与积分容量

| 方法 | 路径 | 鉴权 | 说明 |
| --- | --- | --- | --- |
| <span class="api-method get">GET</span> | `/api/overview` | 管理员 | 一次性聚合会话、积分容量、每日任务、调度状态、语料池统计与近期日志 |
| <span class="api-method get">GET</span> | `/api/tasks` | 管理员 | 拉取当日 4 项每日任务最新状态 |
| <span class="api-method post">POST</span> | `/api/tasks/claim-all` | 管理员 | **一键领取全部每日任务** |
| <span class="api-method post">POST</span> | `/api/tasks/claim` | 管理员 | 领取单项任务（Body: `{"taskKey":"..."}`） |
| <span class="api-method post">POST</span> | `/api/tasks/run` | 管理员 | 手动执行单项 Web 任务并立即取消工作流（Body: `{"taskKey":"..."}`） |
| <span class="api-method post">POST</span> | `/api/tasks/run-all` | 管理员 | 一键领取并顺序执行全部未完成的 Web 任务（自动跳过客户端任务） |
| <span class="api-method get">GET</span> | `/api/assets` | 管理员 | 刷新光子余额、过期批次、近期流水、会员配额与知识库存储容量 |
| <span class="api-method get">GET</span> | `/api/prompts/catalog` | 管理员 | 查看题库统计与 100 篇 arXiv 文献目录 |

## 日志、设置与 Recon 监控

| 方法 | 路径 | 鉴权 | 说明 |
| --- | --- | --- | --- |
| <span class="api-method get">GET</span> | `/api/logs?limit=100` | 管理员 | 获取最近执行与操作日志（最多 200 条） |
| <span class="api-method get">GET</span> | `/api/settings` | 管理员 | 读取当前调度、UA、代理及取消延迟配置 |
| <span class="api-method put">PUT</span> | `/api/settings` | 管理员 | 原子更新配置与管理员密码 |
| <span class="api-method get">GET</span> | `/api/recon/status` | 管理员 | 查看本地 Recon 基线状态与契约标记 |
| <span class="api-method post">POST</span> | `/api/recon/pull` | 管理员 | 拉取并更新本地 Bohrium 前端快照基线 |
| <span class="api-method post">POST</span> | `/api/recon/diff` | 管理员 | 对比线上前端与本地基线，返回变更资源与契约标记差异 |
