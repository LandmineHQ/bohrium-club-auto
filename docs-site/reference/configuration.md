# 配置与环境变量

运行时配置默认保存在 `<dataDir>/config.json`，并通过环境变量覆盖（容器部署优先使用环境变量）。

## 环境变量列表

| 变量名 | 默认值 | 说明 |
| --- | --- | --- |
| `BOHRIUM_HOST` | `0.0.0.0` | HTTP 服务监听地址 |
| `BOHRIUM_PORT` | `8788` | HTTP 服务监听端口 |
| `BOHRIUM_DATA_DIR` | `data`（容器内 `/data`） | 持久化数据目录（`config.json`、`bohrium.db` SQLite 数据库、PDF 缓存；启动时自动无损迁移旧版 `state.json`/`responses.json`） |
| `BOHRIUM_ADMIN_PASSWORD` | 首次启动自动随机生成 | 管理后台登录密码（也兼容 `ADMIN_PASSWORD`） |
| `BOHRIUM_SESSION_SECRET` | 首次启动自动随机生成 | 管理员 Cookie HMAC-SHA256 签名密钥 |
| `BOHRIUM_SCHEDULE_ENABLED` | `true` | 是否开启每日定时自动执行 |
| `BOHRIUM_SCHEDULE_TIME` | `08:15` | 每日定时执行基准时间（24 小时制 `HH:MM`） |
| `BOHRIUM_SCHEDULE_TZ` | `Asia/Shanghai` | 定时任务时区（内置 IANA tzdata） |
| `BOHRIUM_USER_AGENT` | Chrome 136 Windows UA | 自定义出站 Chrome User-Agent |
| `BOHRIUM_PROXY` | 空 | 可选 HTTP/HTTPS 出站代理（自动回退读取 `HTTPS_PROXY`/`HTTP_PROXY`） |
| `BOHRIUM_RECON_DIR` | `recon/bohrium-web` | 本地 Recon 基线保存目录（容器内为 `/data/recon/bohrium-web`） |
| `BOHRIUM_GATEWAY_ENABLED` | `true` | 是否开启 OpenAI 兼容 `/v1/*` API 网关 |
| `BOHRIUM_API_KEYS` | 首次启动自动生成 `sk-brm-...` | 逗号分隔的 OpenAI 网关 API Key 列表 |
| `BOHRIUM_GATEWAY_DEFAULT_MODEL` | `dspro-high` | 请求未指定 `model` 时的默认模型 |
| `BOHRIUM_GATEWAY_ALLOW_NO_AUTH` | `false` | 是否允许免 Key 访问 `/v1/*`（仅限可信内网） |
| `BOHRIUM_GATEWAY_CORS_ORIGINS` | `*` | `/v1/*` 响应的 `Access-Control-Allow-Origin` |
| `BOHRIUM_GATEWAY_PREFLIGHT` | `true` | 是否在新用户轮次前调用 `/billing/preflight`（与客户端一致，工具续跑步骤自动跳过） |
| `BOHRIUM_GATEWAY_SCIMASTER_PROMPT` | `true` | 是否在主模型请求中自动前置 Bohrium 客户端原生 SciMaster 系统提示词（关闭后纯透传原始 Prompt） |
| `BOHRIUM_GATEWAY_RETENTION_DAYS` | `180` | OpenAI 用量看板 SQLite 记录保留天数（范围 1–3650，超过自动清理） |

## `data/config.json` 示例

```json
{
  "host": "0.0.0.0",
  "port": 8788,
  "dataDir": "/data",
  "adminPassword": "change-me-strong-password",
  "userAgent": "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/136.0.0.0 Safari/537.36",
  "proxyUrl": "",
  "schedule": {
    "enabled": true,
    "timeHHMM": "08:15",
    "timezone": "Asia/Shanghai",
    "jitterMinutes": 15,
    "autoClaim": true,
    "autoRefresh": true
  },
  "taskDelaySec": 3,
  "cancelAfterMs": 450,
  "maxPdfSizeBytes": 26214400
}
```
