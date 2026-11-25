# 每日任务与立即终止机制

## 一键领取每日任务

点击控制台的 **「一键领取每日任务」**（或调用 `POST /api/tasks/claim-all`），服务端使用 Chrome UA 与必要校验头（`Origin`、`Referer`、`X-Plaza-Daily-Task-Gateway: production`）向 `POST https://club.bohrium.com/api/plaza/tasks/claim` 发送 `{"publisher":"bohrium-user-community"}`，一次领取当日所有任务。

## 任务分类与执行链路

### 1. 体验玻尔科学空间「Web端」（`bohrium_science_space_web`）

1. 从内嵌的 330 条跨学科科研题库（材料、物理化学、人工智能、生命科学、量子信息、数学能源）随机抽取一条问题；
2. 调用 `POST https://sb-cloud.dp.tech/api/v1/chat?historyCapabilities=sessionForkHistoryV1`（目标 `{"kind":"team","teamId":"AgK4D5B8Fdju"}`）；
3. 获取 `workflowId` / `queryId` 后，按官方前端一致的格式向 `POST https://sb-cloud.dp.tech/api/v1/query-events` 上报 `task_key: "bohrium_science_space_web"`；
4. 等待极短间隔（默认 `450ms`，可配置）后**立即调用** `POST /api/v1/chat/<workflowId>/cancel`（`{"source":"api_request"}`）终止生成。

### 2. 体验玻尔科学空间「客户端」（`bohrium_science_space_client`）

1. 从内嵌的 330 条科学空间题库随机抽取一条科研问题；
2. 严格使用 Bohrium 桌面客户端（`scix` v0.10.25）原生非浏览器传输层（`x-scix-runtime: scix_local`，无 Chrome UA / `Sec-CH-UA`）：先请求 `/models?tag=shrimp-brain-v2` 与 `POST /billing/preflight`（共享同一 W3C `traceparent`）；
3. 构造与桌面客户端完全一致的 `POST /scix/llm/chat/completions` 请求体（含完整 `SciMaster` 系统提示词、16 项内置桌面工具 `tools` 且 `strict: false`、`<environment_context>`、`stream: true` 及每账号独立 `storeId` 的 `x-scix-execution`）；
4. 收到上游受理响应后等待极短间隔（`cancelAfterMs`）即**立即取消 HTTP SSE 流**（与桌面端 `AbortController.abort()` 一致，最小化光子消耗），随后向 `POST https://sb-cloud.dp.tech/api/v1/query-events` 上报 `task_key: "bohrium_science_space_client"`（与 `scix.exe` 内置逻辑一致）。

### 3. 体验玻尔「文献阅读」（`bohrium_literature_reading`）

1. 从内嵌的 **100 篇真实公开 arXiv 科学文献目录**随机挑选一篇（首次下载后自动缓存至 `data/pdfs/`；若你在 `data/custom_pdfs/` 放入了自定义 `.pdf`，会优先随机使用）；
2. 调用 `literature-sage` 接口创建/定位「文献阅读」文件夹 → `file/multipart` 预检 → 上传二进制 PDF（带 `X-Storage-Param`）→ `file/submit` 获取 `userResourceId`；
3. 调用 `POST https://sb-cloud.dp.tech/api/v1/scix/reader/entries` 登记文献并获取 `readerEntryId`；
4. 从 220 条文献精读问题中随机挑选一条，携带 `readerEntryId` 发起真实提问 → 上报 `task_key: "bohrium_literature_reading"` → **立即调用 `cancelWorkflow` 终止请求**。

### 4. 体验玻尔「学术写作」（`bohrium_academic_writing`）

1. 从内嵌的 320 条学术写作问题中随机挑选一条；
2. 携带 `Referer: https://www.bohrium.com/academic-writing` 发起真实提问 → 上报 `task_key: "bohrium_academic_writing"` → **立即调用 `cancelWorkflow` 终止请求**。

::: warning 关于旧版 200 光子每日签到
旧版 `/bohrapi/v1/account/user/integral_receive` 已官方废弃，本程序代码中彻底不包含且永不调用该接口。
:::
