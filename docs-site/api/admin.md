# 完整端点参考

## 健康检查与控制台登录

| 方法 | 路径 | 鉴权 | 说明 |
| --- | --- | --- | --- |
| <span class="api-method get">GET</span> | `/healthz` | 公开 | 进程存活状态、版本号、运行时长、当前 Chrome UA |
| <span class="api-method get">GET</span> | `/api/auth/status` | 公开 | 查询当前 Cookie/Header 是否已通过管理员认证 |
| <span class="api-method post">POST</span> | `/api/auth/login` | 公开 | 校验管理员密码并签发 `brm_auto_session` Cookie（带频次限流） |
| <span class="api-method post">POST</span> | `/api/auth/logout` | 公开 | 清除管理员登录 Cookie |

## 玻尔多账号管理与远程登录

所有新登录账号默认 `poolEnabled = false`（不自动进入 OpenAI 路由池），并生成独立持久化 `storeId`。

| 方法 | 路径 | 鉴权 | 说明 |
| --- | --- | --- | --- |
| <span class="api-method get">GET</span> | `/api/accounts` | 管理员 | 列出全部已登录账号（脱敏 Token、独立余额快照、健康状态、入池状态、每日随机签到时间） |
| <span class="api-method get">GET</span> | `/api/accounts/{id}` | 管理员 | 查看单个账号详情、绑定会话数与调度信息 |
| <span class="api-method patch">PATCH</span> | `/api/accounts/{id}` | 管理员 | 修改账号备注标签（`label`）或独立签到开关（`checkinEnabled`） |
| <span class="api-method delete">DELETE</span> | `/api/accounts/{id}` | 管理员 | 删除账号并级联清理其 SQLite 会话绑定 |
| <span class="api-method post">POST</span> | `/api/accounts/{id}/refresh-token` | 管理员 | 为指定账号触发 `refresh_token` 续期 |
| <span class="api-method post">POST</span> | `/api/accounts/{id}/refresh-balance` | 管理员 | 为指定账号刷新实时光子/组织/钱包余额快照 |
| <span class="api-method post">POST</span> | `/api/accounts/{id}/tasks/run-all` | 管理员 | 为指定账号立即执行全部 Web 每日任务 |
| <span class="api-method post">POST</span> | `/api/accounts/{id}/tasks/run` | 管理员 | 为指定账号执行单项 Web 任务（Body: `{"taskKey":"..."}`） |
| <span class="api-method post">POST</span> | `/api/accounts/tasks/run-all` | 管理员 | 顺序错峰触发全部已启用签到的账号执行每日 Web 任务 |
| <span class="api-method get">GET</span> | `/api/bohrium/session?accountId=...` | 管理员 | 实时检测指定（或首个）账号的 `brmToken` 登录与社区绑定状态 |
| <span class="api-method post">POST</span> | `/api/bohrium/wechat/qr` | 管理员 | 申请官方微信扫码二维码（可传 `{"label":"..."}` 备注名） |
| <span class="api-method get">GET</span> | `/api/bohrium/wechat/poll?sceneId=...&label=...` | 管理员 | 轮询扫码结果，登录成功后入库（默认不入池）并绑定社区 |
| <span class="api-method post">POST</span> | `/api/bohrium/oauth/url` | 管理员 | 获取官方第三方 OAuth 授权 URL（Body: `{"provider":"google|apple|carsi|feishu|pku"}`） |
| <span class="api-method post">POST</span> | `/api/bohrium/oauth/verify` | 管理员 | 提交回调 URL 或 `code` 完成第三方 OAuth 换票登录（可选 `label`） |
| <span class="api-method post">POST</span> | `/api/bohrium/code/send` | 管理员 | 发送手机/邮箱验证码（Body: `{"kind":"phone|email","target":"...","countryCode":"86"}`） |
| <span class="api-method post">POST</span> | `/api/bohrium/login/code` | 管理员 | 使用手机/邮箱验证码登录（可选 `label`） |
| <span class="api-method post">POST</span> | `/api/bohrium/login/token` | 管理员 | 手动导入 `brmToken`（Body: `{"token":"...","label":"..."}`） |
| <span class="api-method post">POST</span> | `/api/bohrium/login/password` | 管理员 | 无图形验证码时的密码登录（Body: `{"account":"...","password":"...","label":"..."}`） |
| <span class="api-method post">POST</span> | `/api/bohrium/refresh-token` | 管理员 | 手动触发指定或全部账号的 JWT 续期 |
| <span class="api-method post">POST</span> | `/api/bohrium/logout` | 管理员 | 删除指定账号（`{"accountId":"..."}`）或首个账号凭据 |

## OpenAI 路由池与 SQLite 7 天会话绑定

| 方法 | 路径 | 鉴权 | 说明 |
| --- | --- | --- | --- |
| <span class="api-method get">GET</span> | `/api/gateway/status` | 管理员 | 返回网关状态、模型目录、路由池权重明细（`pool`）、活跃绑定统计与账号列表 |
| <span class="api-method get">GET</span> | `/api/gateway/analytics` | 管理员 | 按时间（`days`、`granularity`）、`apiKeyId`、`model`、`accountId` 聚合 Token 消耗、倍率折算、平均首字 TTFT 与调用统计（默认保留 180 天） |
| <span class="api-method get">GET</span> | `/api/gateway/analytics/logs` | 管理员 | 分页检索详细使用记录（支持 `days`、`apiKeyId`、`model`、`accountId`、`status`、`stream`、`q`、`page`、`pageSize`），返回密钥、模型/别名、推理强度、端点/协议桥接、路由账号/Session Hash、计费预检策略/倍率、Token 明细、首字 TTFT/总耗时、IP 与 User-Agent |
| <span class="api-method post">POST</span> | `/api/gateway/analytics/purge` | 管理员 | 立即按保留天数清理过期记录（或传 `{"clearAll":true}` 清空全部用量历史） |
| <span class="api-method put">PUT</span> | `/api/gateway/settings` | 管理员 | 更新网关开关、默认模型、Preflight 开关、SciMaster 提示词开关与数据保留天数（`analyticsRetentionDays`） |
| <span class="api-method put">PUT</span> | `/api/gateway/pool` | 管理员 | 手动编排 OpenAI 路由池成员与顺序（Body: `{"accountIds":["acc_1","acc_2"]}`） |
| <span class="api-method get">GET</span> | `/api/gateway/bindings` | 管理员 | 查看 SQLite 中所有未过期的 7 天会话绑定（`sessionHash`、归属账号、命中次数、到期时间） |
| <span class="api-method delete">DELETE</span> | `/api/gateway/bindings/{hash}` | 管理员 | 手动解绑单个会话哈希 |
| <span class="api-method delete">DELETE</span> | `/api/gateway/bindings` | 管理员 | 清空全部会话绑定缓存 |
| <span class="api-method post">POST</span> | `/api/gateway/keys` | 管理员 | 创建新的 `sk-brm-...` API Key |
| <span class="api-method patch">PATCH</span> | `/api/gateway/keys/{id}` | 管理员 | 启用或禁用指定 API Key |
| <span class="api-method delete">DELETE</span> | `/api/gateway/keys/{id}` | 管理员 | 吊销并删除指定 API Key |
| <span class="api-method post">POST</span> | `/api/gateway/preflight` | 管理员 | 测试 `/billing/preflight`（支持指定 `accountId` 或使用路由池） |
| <span class="api-method post">POST</span> | `/api/gateway/playground` | 管理员 | 控制台在线测试 `/v1/chat/completions` 与 `/v1/responses`（支持自动加权路由或指定 `accountId`） |

## 每日任务与积分容量

| 方法 | 路径 | 鉴权 | 说明 |
| --- | --- | --- | --- |
| <span class="api-method get">GET</span> | `/api/overview` | 管理员 | 一次性聚合会话、积分容量、每日任务、调度状态、语料池统计与近期日志 |
| <span class="api-method get">GET</span> | `/api/tasks` | 管理员 | 拉取当日 4 项每日任务最新状态 |
| <span class="api-method post">POST</span> | `/api/tasks/claim-all` | 管理员 | **一键领取全部每日任务** |
| <span class="api-method post">POST</span> | `/api/tasks/claim` | 管理员 | 领取单项任务（Body: `{"taskKey":"..."}`） |
| <span class="api-method post">POST</span> | `/api/tasks/run` | 管理员 | 手动执行单项每日任务（Web 任务或客户端原生 tools 任务）并立即终止（Body: `{"taskKey":"...","accountId":"..."}`） |
| <span class="api-method post">POST</span> | `/api/tasks/run-all` | 管理员 | 一键领取并顺序执行全部 4 项未完成的每日任务（含桌面客户端任务） |
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
