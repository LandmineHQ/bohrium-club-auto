# 安装与部署

## 1. Docker 多阶段构建说明

根目录 `Dockerfile` 采用两阶段构建：

- **Build 阶段**：`golang:1.24-alpine` 执行 `CGO_ENABLED=0 go build -trimpath -ldflags="-s -w"`；
- **Runtime 阶段**：仅包含 `alpine:3.20` + `ca-certificates` + `tzdata` + `/usr/local/bin/bohrium-club-auto`，以非 root 用户 `bohrium`（UID `10001`）运行。

持久化数据统一挂载在 `/data`：

```text
/data/
├── config.json          # 管理员密码、定时计划、Chrome UA、代理配置
├── state.json           # 玻尔登录态、上次定时日期与最近 200 条执行日志
├── pdfs/                # 自动缓存的 arXiv 科学文献 PDF
├── custom_pdfs/         # 可选：放入自定义 PDF 将优先参与任务 3 随机抽取
└── recon/bohrium-web/   # 可选：通过面板触发的本地前端 Recon 基线
```

## 2. 反向代理建议（Nginx / Caddy）

如果通过域名对外暴露管理后台，请启用 HTTPS，并保持 Cookie 与超时配置正常（文献上传+提问可能需要 10~30 秒）：

```nginx
location / {
    proxy_pass http://127.0.0.1:8788;
    proxy_set_header Host $host;
    proxy_set_header X-Real-IP $remote_addr;
    proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
    proxy_set_header X-Forwarded-Proto $scheme;
    proxy_read_timeout 180s;
}
```

## 3. systemd 托管（裸机二进制部署）

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
