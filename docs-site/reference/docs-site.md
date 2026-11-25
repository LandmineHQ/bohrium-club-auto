# 文档站本地预览与部署

文档站源码位于 `docs-site/`，基于 **VitePress 1.6** 构建，与 Go 主工程完全解耦（`.dockerignore` 已排除 `/docs-site`，不会增大容器体积）。

## 本地开发与构建

```bash
cd docs-site
npm ci
npm run dev      # 本地热重载预览（默认 http://localhost:5173）
npm run build    # 生成静态站点至 docs-site/.vitepress/dist
npm run preview  # 预览构建产物
```

## GitHub Pages 自动部署

文档站在线地址：<https://landminehq.github.io/bohrium-club-auto/>

工作流文件为 `.github/workflows/deploy-docs.yml`：

1. 在 GitHub 仓库进入 **Settings → Pages**，将 **Source** 设置为 **GitHub Actions**；
2. 每次修改 `docs-site/**` 并推送到 `main`，或手动触发 **Actions → Deploy docs** 时，工作流会自动注入 `DOCS_BASE="/bohrium-club-auto/"`（随仓库名自动适配）构建并发布；
3. 若绑定了自定义域名，可将构建环境变量覆盖为 `DOCS_BASE=/`。

::: tip 私有仓库说明
GitHub 私有仓库启用 GitHub Pages 需要对应套餐权限；即使未开启 Pages，也可以随时在本地运行 `npm run build` 或 `npm run dev` 查阅离线文档。
:::
