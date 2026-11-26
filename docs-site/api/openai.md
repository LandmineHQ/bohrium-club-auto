# OpenAI 兼容网关（多账号混合池 · Chat Completions & Responses API）

`bohrium-club-auto` 内置了一套严格遵循 **Bohrium 桌面客户端（SciX）原生协议** 的多账号混合路由 OpenAI 兼容网关，向外暴露标准的 `/v1/models`、`/v1/balance`、`/v1/chat/completions` 与 `/v1/responses` 接口。

## 多账号路由池与余额加权粘性绑定（SQLite）

- **严格手动入池（默认不入池）**：所有新登录的账号（包括从旧版 `data/state.json` 自动迁移的历史账号）均默认 `poolEnabled = false`，**不会**自动参与 OpenAI 路由。必须在 Web 控制台「03 OpenAI 网关」的路由池双列面板中手动将账号拖入右侧「已入池」，该账号才会承载 `/v1/*` 流量。若路由池为空，`/v1/*` 将返回 HTTP `503`（`error.code = "empty_pool"`）。
- **余额加权概率选路**：新会话到来时，路由器仅从已入池且健康（未过期、未处于 402/401 冷却期）的账号中按有效余额 $B_i = \max(0, \text{photon\_balance}_i + \text{org\_balance}_i)$ 进行线性加权随机采样（$P_i = B_i / \sum_j B_j$）——余额越高的账号被绑定到新会话的概率越大。
- **SQLite 7 天硬上限粘性绑定**：选中账号后，网关将 `session_hash → (account_id, scix_session_id, expires_at)` 写入 `data/bohrium.db` 的 `session_bindings` 表，同一对话后续轮次复用同一账号与独立 `storeId`/`sessionId`。每条绑定最长缓存 **7 天**（硬上限 `created_at + 7d`，不滑动续期），到期自动失效并由后台每小时物理清理；亦可在控制台实时查看与手动解绑。
- **首字节前无感故障转移与重绑**：若某会话已绑定的账号被移出池、发生 401 Token 失效或 402 光子不足，只要尚未向客户端写出首个响应字节，网关会自动将异常账号标记为过期/余额不足（30 分钟冷却），按剩余健康池账号余额权重重新采样、更新 SQLite 绑定关系并透明重试，响应头 `X-Bohrium-Routing-Mode` 会体现 `new_bind` / `bound` / `rebound`。

## 上游协议保真机制

网关向 Bohrium Sigma LLM 上游 (`https://www.bohrium.com/bohrapi/v1/sigma-search/api/v1/scix/llm`) 发起请求时，完整保留客户端原生交互与字段：

- **动态模型目录与逐模型启用控制**：调用 `GET /models?tag=shrimp-brain-v2` 实时获取当前账号可用模型（如 `dspro-high`、`dsflash-high`、`kimi-k3`、`kimi-k3.1-pro`、`standard`、`advanced`、`vision`、`title`、`summary`），并缓存 60 秒。其中 `standard`/`advanced` 与官方客户端一致默认标记为 `hidden` 并禁用，`vision`/`title`/`summary` 等内部辅助模型保留在目录中并默认启用；可在 Web 控制台「Bohrium SciX 模型目录」通过复选框随时启用/禁用任意模型（禁用模型不会出现在 `/v1/models`，通过 `/v1/*` 调用将返回 `403 model_disabled`）。
- **独立桌面传输层（无浏览器头泄漏）**：对 `/scix/llm/*` 使用独立于 Web 签到的原生 HTTP 传输层——不注入 Chrome `User-Agent` / `Sec-CH-UA*` / `Accept-Language`，也不泄漏 Go 默认 UA，仅携带 `scix.exe` 白名单请求头及 `x-scix-runtime: scix_local`。
- **桌面执行上下文头与会话粘性**：自动生成或透传补全 unpadded base64url 编码的 `x-scix-execution` 请求头（包含 `version: 1`、`scenario: "local_agent"`、持久化 `storeId`、按会话保持稳定的 `rootSessionId`/`sessionId`/`sessionCreatedAt`、按用户轮次新建并在工具续跑时复用的 `rootRunId`/`runId` 与 `toolCallId`、`purpose`、`recoveryAttempt`、`question`），以及 W3C `traceparent`。
- **两层额度预检与兜底（与客户端一致）**：默认开启新轮次 `POST /billing/preflight` 准入预检（仅带 `Authorization`、`traceparent` 与 `x-scix-runtime`，与后续 LLM 请求共享同一 `traceparent`；Agent 多步工具续跑 `role: "tool"` / `function_call_output` 自动跳过重复预检）；若预检拒绝或运行中上游返回光子不足（`insufficient_balance` / `BILLING_PHOTON_NOT_ENOUGH`），统一返回 HTTP `402` 与 `insufficient_quota` / `insufficient_balance` 错误信封。可通过控制台或 `BOHRIUM_GATEWAY_PREFLIGHT=false` 关闭前置预检。
- **上游始终 SSE 流式拉取**：无论外部调用方传 `stream: true` 还是 `stream: false`，网关对 Bohrium 上游均以 `stream: true`（Chat 模式附带 `stream_options: { include_usage: true }`）发起请求；当调用方要求非流式时，由网关在本地实时聚合 SSE 帧为完整 JSON 响应返回。
- **SciMaster 系统提示词开关（默认开启）**：默认对主模型自动前置 Bohrium 桌面客户端原生 `SciMaster` 系统提示词（`title`/`summary` 辅助模型与调用方已自带 `You are SciMaster` 前缀的请求不重复注入），可在 Web 控制台「03 OpenAI 网关」或通过 `BOHRIUM_GATEWAY_SCIMASTER_PROMPT=false` 一键关闭以纯透传原始 Prompt。
- **原生兼容性规范化**：遵循客户端 `chat_completions` / `responses` 兼容规则（Chat 模式将 `developer` 规范化为 `system`、合并 `max_completion_tokens` 到 `max_tokens`、剥离 `store`；Responses 模式将系统提示词放进 `input[0]` 并附带 `prompt_cache_key` 与 `store: false`；函数工具默认补齐 `strict: false`；自动将推理强度 `reasoning_effort` 映射至目标模型支持的档位并过滤上游 `: heartbeat` 心跳注释）。

## 双向跨协议桥接

Bohrium 上游对不同模型限定了原生端点：
- 主力模型（`dspro-high`、`dsflash-high`、`kimi-k3`、`kimi-k3.1-pro`、`standard`、`advanced`）原生使用 `/chat/completions`。
- 内部辅助模型（`vision`、`title`、`summary`）原生使用无状态 `/responses`（`store: false`）。

网关会自动按模型原生协议路由并执行**无损双向桥接**：
1. 用主力模型调用 **`POST /v1/responses`** 时，网关底层以客户端原生格式请求 Bohrium `/chat/completions`，并实时输出符合 OpenAI 规范的 Responses SSE 事件流（`response.created` → `response.in_progress` → `response.reasoning_text.*` / `response.output_text.*` / `response.function_call_arguments.*` → `response.output_item.done` → `response.completed`）或非流式 `object: "response"` 对象。
2. 用 `responses` 原生模型调用 **`POST /v1/chat/completions`** 时，网关底层请求 Bohrium `/responses` 并实时转换为标准 `chat.completion.chunk` / `chat.completion`。
3. 通过本地 SQLite 持久化存储（`data/bohrium.db` `responses` 表，最多保留 200 条）完整支持 `previous_response_id` 多轮会话链、`store`、`GET /v1/responses/{id}`、`GET /v1/responses/{id}/input_items` 与 `DELETE /v1/responses/{id}`，同时向上游始终发送无状态 `store: false`。

## 鉴权方式

网关使用独立于控制台管理员密码的 `sk-brm-...` API Key（首次启动自动生成，可在控制台「OpenAI 网关」页管理，或通过 `BOHRIUM_API_KEYS` 环境变量配置）：

- `Authorization: Bearer sk-brm-...`
- `x-api-key: sk-brm-...` 或 `api-key: sk-brm-...`

## 端点一览

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| `GET` | `/v1/models` | 按 OpenAI 标准列表格式（`object` + `data`）返回当前已启用的模型，并附带 OpenRouter 与多客户端兼容扩展字段 |
| `GET` | `/v1/models/{id}` | 按模型 ID、`scix/<id>` 或显示名别名（如 `DeepSeek-V4-Pro`）查询单个模型的完整元数据 |
| `GET` | `/v1/balance` | 实时聚合所有已入池账号的光子余额、组织余额、钱包余额（CNY）、配额、知识库存储及各入池账号余额与路由权重明细（`pool_accounts`、`balance_infos`） |
| `POST` | `/v1/chat/completions` | 完整 Chat Completions（SSE/非流式、工具调用、`reasoning_effort`、多模态图像、`response_format`） |
| `POST` | `/v1/responses` | 完整 Responses API（SSE/非流式、`instructions`、`input` 项数组、工具调用、推理、视觉、`previous_response_id`） |
| `GET` | `/v1/responses/{id}` | 获取已存储的响应对象 |
| `GET` | `/v1/responses/{id}/input_items` | 获取已存储响应的输入项列表 |
| `DELETE` | `/v1/responses/{id}` | 删除本地存储的响应记录 |

### `/v1/models` 扩展字段（OpenRouter 与多客户端兼容超集）

`GET /v1/models` 与 `GET /v1/models/{id}` 在保留 OpenAI 标准字段（`id`、`object`、`created`、`owned_by`）的基础上，同时下发多生态兼容扩展字段，便于 Cherry Studio、Cline、Roo Code、LobeChat、OpenCode、`dsh`、vLLM / New API 兼容客户端自动识别上下文长度、多模态输入与思考等级：

- **展示信息**：`name`、`display_name`、`description`
- **容量限制**：`context_window`、`context_length`、`max_model_len`、`max_input_tokens`、`max_output_tokens`、`max_tokens`、`limit`（`context` / `output`）、`top_provider`（`context_length` / `max_completion_tokens` / `is_moderated`）
- **模态与端点能力**：`input`、`architecture`（`modality` / `input_modalities` / `output_modalities`）、`supported_parameters`、`supported_endpoint_types`（`["openai", "openai-response"]`）
- **推理 / 思考能力**（仅支持推理的模型下发；`title` / `summary` 等非推理模型自动省略）：`reasoning_effort`、`reasoning_efforts`、`thinking`（`supported` / `levels` / `default`）

## 调用示例

### 查询余额（`GET /v1/balance`）

OpenAI 官方规范本身不含余额接口，本网关按常见习惯提供 `GET /v1/balance`，实时聚合 Bohrium 光子、组织余额、钱包、会员配额与知识库存储，并兼容 `balance_infos` 结构：

```bash
curl http://127.0.0.1:8788/v1/balance \
  -H "Authorization: Bearer sk-brm-YOUR_KEY"
```

### Chat Completions（流式 + 推理 + 工具调用）

```bash
curl http://127.0.0.1:8788/v1/chat/completions \
  -H "Authorization: Bearer sk-brm-YOUR_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "dspro-high",
    "stream": true,
    "reasoning_effort": "high",
    "messages": [
      {"role": "system", "content": "你是一位严谨的科研助手。"},
      {"role": "user", "content": "简述超导量子比特的退相干机制。"}
    ]
  }'
```

### Responses API（流式 + `previous_response_id` 多轮链）

```bash
# 第一轮
curl http://127.0.0.1:8788/v1/responses \
  -H "Authorization: Bearer sk-brm-YOUR_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "dsflash-high",
    "stream": false,
    "instructions": "回答保持简洁。",
    "reasoning": {"effort": "low"},
    "input": "记住代号 ALPHA-42，回复收到即可。"
  }'

# 第二轮（传入上一轮返回的 resp_... ID）
curl http://127.0.0.1:8788/v1/responses \
  -H "Authorization: Bearer sk-brm-YOUR_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "dsflash-high",
    "stream": true,
    "previous_response_id": "resp_...",
    "input": "我刚刚让你记住的代号是什么？"
  }'
```

### OpenAI Python SDK

```python
from openai import OpenAI

client = OpenAI(
    base_url="http://127.0.0.1:8788/v1",
    api_key="sk-brm-YOUR_KEY",
)

# 1. Chat Completions
chat = client.chat.completions.create(
    model="dspro-high",
    messages=[{"role": "user", "content": "你好"}],
)
print(chat.choices[0].message.content)

# 2. Responses API
resp = client.responses.create(
    model="kimi-k3",
    input="用两句话解释密度泛函理论。",
    reasoning={"effort": "low"},
)
print(resp.output_text)
```
