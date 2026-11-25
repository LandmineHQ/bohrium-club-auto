# 前端 Recon 快照与更新比对

`/recon/` 目录已写入 `.gitignore`，**绝不将上游前端源码提交进 Git 仓库**，所有快照仅保存在本地用于基线比对。

## 命令行用法

```bash
# 1. 拉取并生成本地基线快照（写入 recon/bohrium-web/manifest.json）
./bohrium-club-auto recon pull

# 2. 对比线上最新静态资源与本地基线（不修改基线）
./bohrium-club-auto recon diff

# 3. JSON 输出模式（便于脚本解析）
./bohrium-club-auto recon diff --json
```

退出码约定：

- `0`：线上 HTML（规范化掉阿里云单次请求 `clientSignal` 后）、静态资源 SHA-256 及关键 API 契约标记与基线完全一致；
- `1`：存在新增/变更/移除的静态文件、关键 API 契约标记发生变化，或本地基线不存在。

## 关键契约标记

每次拉取与比对都会自动扫描以下标记：

- `plazaTasksEndpoint`: `/api/plaza/tasks`
- `plazaClaimEndpoint`: `/api/plaza/tasks/claim`
- `gatewayHeader`: `X-Plaza-Daily-Task-Gateway`
- `wechatQrEndpoint`: `/api/account/wechat/login_qrcode`（自动纳入一方 CDN `cdn.bohrium.com/platform/web/*`）
- `dailyTaskBundleUrls`: 每日任务前端模块路径清单

## GitHub Actions 巡检

仓库内置 `.github/workflows/recon-check.yml`，可在 GitHub Actions 页面手动触发 `workflow_dispatch`，自动生成契约标记报告并把 `manifest.json` 作为临时 Artifact 上传（不污染代码仓库）。
