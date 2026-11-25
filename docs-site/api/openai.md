# OpenAI 兼容网关（Chat Completions & Responses API）

`bohrium-club-auto` 内置了一套严格遵循 **Bohrium 桌面客户端（SciX）原生协议** 的 OpenAI 兼容网关，直接复用服务内已登录并自动续期的 `brmToken`，向外暴露标准的 `/v1/models`、`/v1/chat/completions` 与 `/v1/responses` 接口。

## 上游协议保真机制

网关向 Bohrium Sigma LLM 上游 (`https://www.bohrium.com/bohrapi/v1/sigma-search/api/v1/scix/llm`) 发起请求时，完整保留客户端原生交互与字段：

- **动态模型目录**：调用 `GET /models?tag=shrimp-brain-v2` 实时获取当前账号可用模型（如 `dspro-high`、`dsflash-high`、`kimi-k3`、`kimi-k3.1-pro`、`standard`、`advanced`、`vision`、`title`、`summary`），并缓存 60 秒。
- **桌面执行上下文头**：自动生成或透传补全 unpadded base64url 编码的 `x-scix-execution` 请求头（包含 `version: 1`、`scenario: "local_agent"`、持久化 `storeId`、标准 UUIDv4/v5 会话与调用 ID、`purpose`、`recoveryAttempt`、`sessionCreatedAt` 与 `question`），以及 W3C `traceparent`。
- **两层额度预检与兜底（与客户端一致）**：默认开启新轮次 `POST /billing/preflight` 准入预检（与后续 LLM 请求共享同一 `traceparent`；Agent 多步工具续跑 `role: "tool"` / `function_call_output` 自动跳过重复预检）；若预检拒绝或运行中上游返回光子不足（`insufficient_balance` / `BILLING_PHOTON_NOT_ENOUGH`），统一返回 HTTP `402` 与 `insufficient_quota` / `insufficient_balance` 错误信封。可通过控制台或 `BOHRIUM_GATEWAY_PREFLIGHT=false` 关闭前置预检。
- **原生兼容性规范化**：遵循客户端 `chat_completions` 兼容规则（将 `developer` 角色规范化为 `system`、合并 `max_completion_tokens` 到 `max_tokens`、剥离 `store`、将推理强度 `reasoning_effort` 映射至目标模型支持的档位、过滤上游 `: heartbeat` 心跳注释）。

## 双向跨协议桥接

Bohrium 上游对不同模型限定了原生端点：
- 主力模型（`dspro-high`、`dsflash-high`、`kimi-k3`、`kimi-k3.1-pro`、`standard`、`advanced`）原生使用 `/chat/completions`。
- 内部辅助模型（`vision`、`title`、`summary`）原生使用无状态 `/responses`（`store: false`）。

网关会自动按模型原生协议路由并执行**无损双向桥接**：
1. 用主力模型调用 **`POST /v1/responses`** 时，网关底层以客户端原生格式请求 Bohrium `/chat/completions`，并实时输出符合 OpenAI 规范的 Responses SSE 事件流（`response.created` → `response.in_progress` → `response.reasoning_text.*` / `response.output_text.*` / `response.function_call_arguments.*` → `response.output_item.done` → `response.completed`）或非流式 `object: "response"` 对象。
2. 用 `responses` 原生模型调用 **`POST /v1/chat/completions`** 时，网关底层请求 Bohrium `/responses` 并实时转换为标准 `chat.completion.chunk` / `chat.completion`。
3. 通过本地持久化存储（`data/responses.json`，最多保留 200 条）完整支持 `previous_response_id` 多轮会话链、`store`、`GET /v1/responses/{id}`、`GET /v1/responses/{id}/input_items` 与 `DELETE /v1/responses/{id}`，同时向上游始终发送无状态 `store: false`。

## 鉴权方式

网关使用独立于控制台管理员密码的 `sk-brm-...` API Key（首次启动自动生成，可在控制台「OpenAI 网关」页管理，或通过 `BOHRIUM_API_KEYS` 环境变量配置）：

- `Authorization: Bearer sk-brm-...`
- `x-api-key: sk-brm-...` 或 `api-key: sk-brm-...`

## 端点一览

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| `GET` | `/v1/models` | 返回实时 Bohrium SciX 模型列表（含 `scix_catalog` 元数据，兼容客户端与 OpenAI SDK） |
| `GET` | `/v1/models/{id}` | 按模型 ID、`scix/<id>` 或显示名别名（如 `DeepSeek-V4-Pro`）查询模型详情 |
| `GET` | `/v1/balance` | 查询当前账号光子余额、组织余额、钱包余额（CNY）、7 日将过期批次、会员配额与知识库存储容量（含 `balance_infos`） |
| `POST` | `/v1/chat/completions` | 完整 Chat Completions（SSE/非流式、工具调用、`reasoning_effort`、多模态图像、`response_format`） |
| `POST` | `/v1/responses` | 完整 Responses API（SSE/非流式、`instructions`、`input` 项数组、工具调用、推理、视觉、`previous_response_id`） |
| `GET` | `/v1/responses/{id}` | 获取已存储的响应对象 |
| `GET` | `/v1/responses/{id}/input_items` | 获取已存储响应的输入项列表 |
| `DELETE` | `/v1/responses/{id}` | 删除本地存储的响应记录 |

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
