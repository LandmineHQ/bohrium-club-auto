# 管理 API 概览与鉴权

所有内部控制端接口均托管在同一 HTTP 服务下（默认 `http://127.0.0.1:8788`），返回 `application/json; charset=utf-8` 与 `Cache-Control: no-store`。

## 认证方式

除 `/healthz` 与 `/api/auth/*` 外，所有 `/api/*` 接口均强制校验管理员密码，支持两种凭据方式：

1. **浏览器会话 Cookie（管理面板默认）**：先调用 `POST /api/auth/login` 传入 `{"password":"..."}`，成功后服务端签发 7 天有效期的 HMAC-SHA256 签名 `HttpOnly` Cookie (`brm_auto_session`)。
2. **脚本 / CI 请求头**：直接在请求中附带 `X-Admin-Password: <your-admin-password>`，适合自动化脚本或监控探针调用。

```bash
# 健康检查（免鉴权）
curl -fsS http://127.0.0.1:8788/healthz

# 使用请求头查询总览
curl -fsS -H "X-Admin-Password: YourStrongPassword" \
  http://127.0.0.1:8788/api/overview
```

## 通用响应格式

```json
{
  "ok": true,
  "data": {}
}
```

错误时返回对应 HTTP 状态码（`400` / `401` / `409` / `429` / `502`）及：

```json
{
  "ok": false,
  "error": "错误说明"
}
```
