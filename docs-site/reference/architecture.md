# 架构与上游契约

## 模块分层

```text
cmd/bohrium-club-auto/main.go   CLI 入口（serve / recon pull / recon diff / version）
├── internal/chromehttp/        Chrome UA & Client Hints 统一出站客户端
├── internal/config/            环境变量 + data/config.json 原子持久化
├── internal/store/             data/state.json（登录态、调度日期、200 条执行日志）
├── internal/prompts/           go:embed 内嵌 870 条题库与 100 篇 arXiv 论文目录
├── internal/bohrium/           认证、每日任务、积分容量、PDF 下载与三项 Web 任务执行
├── internal/scheduler/         北京时间每日调度器 + Token 自动续期循环 + 执行互斥锁
├── internal/recon/             线上静态资源快照抓取与 SHA-256 / 契约标记 Diff 引擎
├── internal/server/            管理员鉴权门禁、限流、REST API 与静态 SPA 路由
└── web/                        go:embed 内置管理面板（零外部 CDN 依赖）
```

## 上游 4 域名核心契约

| 上游域名 | 用途 | 关键校验头 / 契约说明 |
| --- | --- | --- |
| `club.bohrium.com` | 每日任务列表 `/api/plaza/tasks`、领取 `/api/plaza/tasks/claim`、社区会话 `/api/platform/session` 与 `/api/enroll` | 必须携带 `Cookie: brmToken=...`、`Origin: https://club.bohrium.com`、`Referer: https://club.bohrium.com/` 与 `X-Plaza-Daily-Task-Gateway: production` |
| `platform.bohrium.com` | 微信登录二维码与扫码状态轮询、密码校验与登录 | `/api/account/wechat/login_qrcode` 与 `/api/account/wechat/qr_code/check_login_status` |
| `www.bohrium.com` | 光子余额、钱包、7日过期、流水、会员配额、存储容量、文献上传 `literature-sage`、`refresh_token` | `Authorization: Bearer <brmToken>` + `Cookie: brmToken=<brmToken>` |
| `sb-cloud.dp.tech` | SciX Reader 登记 `/api/v1/scix/reader/entries`、真实聊天 `/api/v1/chat?historyCapabilities=sessionForkHistoryV1`、任务完成上报 `/api/v1/query-events`、立即取消 `/api/v1/chat/<workflowId>/cancel` | `Authorization: Bearer <brmToken>`；按官方前端规范不发送 `x-shrimp-client-surface` 与 `Idempotency-Key`（仅在回退 `agent.bohrium.com` 时附加） |
