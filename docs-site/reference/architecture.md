# 系统架构

## 模块分层

```text
cmd/bohrium-club-auto/main.go   CLI 入口（serve / recon pull / recon diff / version）
├── internal/chromehttp/        Chrome UA / Client Hints 与流式传输客户端
├── internal/config/            环境变量 + data/config.json 原子配置管理
├── internal/store/             纯 Go SQLite（data/bohrium.db）：多账号、7 天会话绑定、Responses 记录、日志
├── internal/prompts/           go:embed 内嵌题库、100 篇文献目录、SciMaster 提示词与 16 项桌面 tools
├── internal/bohrium/           多账号 Client Manager、认证、每日任务（含桌面端任务）、资产与 SciX LLM
├── internal/openai/            OpenAI /v1/* 网关、余额加权路由池与 Chat/Responses 双向桥接
├── internal/scheduler/         多账号错峰随机签到、自动续期、余额刷新与过期绑定清理
├── internal/recon/             线上前端静态资源抓取与 SHA-256 / 契约标记比对
├── internal/server/            管理员鉴权门禁、限流、REST API 与 SPA 托管
└── web/                        React + Vite + TypeScript + Tailwind CSS + shadcn/ui 前端工程（产物 web/dist 由 go:embed 嵌入）
```

## 数据流与路由机制

1. **多账号隔离**：每个账号在 SQLite `accounts` 表中持有独立 `brmToken` 与持久化 UUIDv4 `store_id`，调度与网关均按账号粒度隔离客户端实例。
2. **加权会话路由**：
   - 仅 `pool_enabled = 1` 且处于健康状态的账号参与 `/v1/*` 流量分配；
   - 新会话根据 `max(0, photon_balance + org_balance)` 线性比例加权抽签，并写入 `session_bindings`（硬上限 7 天，过期自动删除）；
   - 首包返回前遇到 401/402/账号移出池时自动重选其他健康账号并更新绑定。
3. **前后端构建**：前端开发期在 `web/` 内使用 Vite 热更新并代理后端接口；构建期先生成 `web/dist`，再由 `web/embed.go` 打包为单文件 Go 二进制。

## 上游接口契约

| 上游域名 | 用途 | 关键校验说明 |
| --- | --- | --- |
| `club.bohrium.com` | 每日任务列表 `/api/plaza/tasks`、领取 `/api/plaza/tasks/claim`、社区会话 `/api/platform/session` 与 `/api/enroll` | 携带 `Cookie: brmToken=...`、`Origin`、`Referer` 与 `X-Plaza-Daily-Task-Gateway: production` |
| `platform.bohrium.com` | 微信扫码、OAuth 授权、验证码与密码登录 | `/api/account/wechat/*`、`/api/account/oauth_url`、`/api/oauth/verify` 等 |
| `www.bohrium.com` | 账号资料、光子与配额查询、文献上传、`refresh_token` 及 SciX LLM 接口 | Web 接口使用浏览器 UA；`/scix/llm/*` 使用桌面端原生头 `x-scix-runtime: scix_local` 与 `x-scix-execution` |
| `sb-cloud.dp.tech` | Reader 登记、Web 端聊天、任务事件上报 `query-events` 与工作流取消 `/cancel` | `Authorization: Bearer <brmToken>` |
