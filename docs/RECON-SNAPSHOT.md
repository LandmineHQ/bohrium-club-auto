# Bohrium 前端源码本地快照与更新比对（Recon Snapshot）

参考 [`LandmineHQ/duckai-cpa`](https://github.com/LandmineHQ/duckai-cpa) 的本地 Recon 快照规范设计：

- `recon/` 目录已加入 `.gitignore`，**绝不将上游前端源码提交进 Git 仓库**；
- 通过内置 Go CLI 或管理面板中的「前端 Recon 监控」标签页，随时拉取线上 `club.bohrium.com` 与 `platform.bohrium.com` 的公开 HTML/JS/CSS 快照，生成 `recon/bohrium-web/manifest.json`（含每个静态文件的 SHA-256 与关键 API 契约标记）；
- 对比命令仅请求公开静态资源并实时比对本地基线，**不会覆盖基线**。

## CLI 用法

### 1. 拉取 / 更新本地基线快照

```bash
./bohrium-club-auto recon pull
```

常用参数：

```bash
./bohrium-club-auto recon pull \
  --out recon/bohrium-web \
  --max-assets 150 \
  --delay-ms 80 \
  --proxy http://127.0.0.1:7890
```

### 2. 检测线上前端是否更新（Diff）

```bash
./bohrium-club-auto recon diff
# 输出 JSON（适合脚本/定时巡检）
./bohrium-club-auto recon diff --json
```

退出码约定（与 `duckai-cpa` 保持一致）：

- `0`：线上 HTML、静态资源 SHA-256 及关键 API 契约标记与本地基线完全一致；
- `1`：发现新增/变更/移除的资源、关键 API 契约标记变化，或本地基线尚不存在。

## 关键契约标记检测项

`manifest.json` 会自动扫描并记录以下核心标记，便于第一时间发现上游改版：

- `plazaTasksEndpoint`: `/api/plaza/tasks`
- `plazaClaimEndpoint`: `/api/plaza/tasks/claim`
- `gatewayHeader`: `X-Plaza-Daily-Task-Gateway`
- `wechatQrEndpoint`: `/api/account/wechat/login_qrcode`
- `dailyTaskBundleUrls`: 每日任务前端模块路径（例如 `daily-task-client-launch-...`）
- `sciSpaceTeamIdFound` / `queryEventsEndpoint`（当 `--targets` 包含 `https://www.bohrium.com/` 时）

## Dashboard 内置支持

也可以直接在 Web 控制台的 **「前端 Recon 监控」** 标签页点击：

- **拉取 / 更新基线 (Pull)**：在持久化卷 `/data/recon/bohrium-web/` 建立/刷新基线；
- **检测线上更新 (Diff)**：对比线上与基线并在表格中高亮展示新增、变更和移除的资源。
