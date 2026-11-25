# 安装与部署

## 1. Docker 多阶段构建

根目录 `Dockerfile` 分为三个阶段：

1. **web-build (`node:22-alpine`)**：在 `web/` 中执行 `npm ci && npm run build`，构建 React + Vite + shadcn/ui 静态资源至 `/web/dist`；
2. **go-build (`golang:1.24-alpine`)**：拷贝 `/web/dist` 并执行 `CGO_ENABLED=0 go build` 产出静态二进制；
3. **runtime (`alpine:3.20`)**：仅包含 `ca-certificates`、`tzdata` 与可执行文件，以非 root 用户（UID `10001`）运行。

持久化数据目录 `/data` 结构：

```text
/data/
├── config.json          # 管理员密码、定时参数、OpenAI 网关配置与 API Key
├── bohrium.db           # SQLite（多账号凭据、7 天会话绑定、Responses 记录、执行日志）
├── pdfs/                # 自动下载缓存的文献 PDF
├── custom_pdfs/         # 可选：自定义 PDF 目录（任务 3 优先随机抽取）
└── recon/bohrium-web/   # 可选：前端 Recon 基线目录
```

## 2. 反向代理（Nginx）

若通过域名访问控制台或 `/v1/*` 流式接口，建议关闭代理缓冲并适当放宽读超时：

```nginx
location / {
    proxy_pass http://127.0.0.1:8788;
    proxy_set_header Host $host;
    proxy_set_header X-Real-IP $remote_addr;
    proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
    proxy_set_header X-Forwarded-Proto $scheme;
    proxy_buffering off;
    proxy_read_timeout 300s;
}
```

## 3. systemd 托管

```ini
[Unit]
Description=Bohrium Club Auto
After=network-online.target

[Service]
Type=simple
WorkingDirectory=/opt/bohrium-club-auto
Environment=BOHRIUM_HOST=127.0.0.1
Environment=BOHRIUM_PORT=8788
Environment=BOHRIUM_DATA_DIR=/opt/bohrium-club-auto/data
Environment=BOHRIUM_ADMIN_PASSWORD=change-me
ExecStart=/opt/bohrium-club-auto/bohrium-club-auto serve
Restart=always
RestartSec=5

[Install]
WantedBy=multi-user.target
```
