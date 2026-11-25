import { useEffect, useRef, useState } from "react"
import { toast } from "sonner"
import { Send, Shuffle } from "lucide-react"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card"
import { NativeSelect, Textarea } from "@/components/ui/input"
import { Label } from "@/components/ui/primitives"
import type { AccountSummaryView, SciXModelItem } from "@/types/console"

interface PlaygroundPanelProps {
  accounts: AccountSummaryView[]
  models: SciXModelItem[]
  prompts: string[]
  selectedAccountId: string
  onSelectAccountId: (id: string) => void
  selectedModel: string
  onSelectModel: (model: string) => void
  onCompleted: () => void
}

export function PlaygroundPanel({
  accounts,
  models,
  prompts,
  selectedAccountId,
  onSelectAccountId,
  selectedModel,
  onSelectModel,
  onCompleted,
}: PlaygroundPanelProps) {
  const [endpoint, setEndpoint] = useState<"chat" | "responses">("chat")
  const [effort, setEffort] = useState("low")
  const [stream, setStream] = useState(true)
  const [autoRandom, setAutoRandom] = useState(true)
  const [prompt, setPrompt] = useState("")
  const [customized, setCustomized] = useState(false)
  const [poolBadge, setPoolBadge] = useState("随机池 100 条")
  const lastIdxRef = useRef(-1)

  const [running, setRunning] = useState(false)
  const [hasOutput, setHasOutput] = useState(false)
  const [statusBadge, setStatusBadge] = useState("等待响应")
  const [usageMeta, setUsageMeta] = useState("")
  const [sentPrompt, setSentPrompt] = useState("")
  const [reasoning, setReasoning] = useState("")
  const [answer, setAnswer] = useState("")

  function rollRandom(silent = false): string {
    if (!prompts.length) return ""
    let idx = Math.floor(Math.random() * prompts.length)
    if (prompts.length > 1 && idx === lastIdxRef.current) {
      idx = (idx + 1 + Math.floor(Math.random() * (prompts.length - 1))) % prompts.length
    }
    lastIdxRef.current = idx
    const picked = prompts[idx]
    setPrompt(picked)
    setCustomized(false)
    setPoolBadge(`随机 #${idx + 1} / ${prompts.length}`)
    if (!silent) toast.info("已随机切换测试提示词")
    return picked
  }

  useEffect(() => {
    if (prompts.length > 0 && !customized && !prompt.trim()) {
      rollRandom(true)
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [prompts.length])

  async function handleRun() {
    let activePrompt = prompt.trim()
    if (!activePrompt && prompts.length > 0) {
      activePrompt = rollRandom(true)
    }
    if (!activePrompt) {
      toast.warning("请输入测试提示词")
      return
    }

    const shouldRollNext = autoRandom && !customized && prompts.length > 1
    setRunning(true)
    setHasOutput(true)
    setSentPrompt(`本次提问：${activePrompt}`)
    setReasoning("")
    setAnswer("")
    setUsageMeta("")
    setStatusBadge(`请求中 (${endpoint} / ${selectedModel})...`)
    if (shouldRollNext) {
      rollRandom(true)
    }

    const t0 = performance.now()
    try {
      const res = await fetch("/api/gateway/playground", {
        method: "POST",
        credentials: "same-origin",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          accountId: selectedAccountId,
          endpoint,
          model: selectedModel,
          prompt: activePrompt,
          reasoningEffort: effort,
          stream,
          maxTokens: 1024,
        }),
      })

      if (!res.ok) {
        const errData = (await res.json().catch(() => ({}))) as {
          error?: string | { message?: string }
        }
        const msg =
          (typeof errData.error === "object" ? errData.error?.message : errData.error) ||
          `HTTP ${res.status}`
        setStatusBadge(`失败 (HTTP ${res.status})`)
        setAnswer(msg)
        toast.error("调试请求失败: " + msg)
        return
      }

      const routeMode = res.headers.get("X-Bohrium-Routing-Mode") || ""
      if (!stream) {
        const data = (await res.json()) as Record<string, unknown>
        const ms = Math.round(performance.now() - t0)
        setStatusBadge(`完成 (${ms} ms${routeMode ? " · " + routeMode : ""})`)
        if (endpoint === "responses") {
          const outputs = (data.output as Array<Record<string, unknown>>) || []
          const rTexts = outputs
            .filter((o) => o.type === "reasoning")
            .flatMap((o) =>
              ((o.content as Array<{ text?: string }>) || []).map((p) => p.text || "")
            )
          const aTexts = outputs
            .filter((o) => o.type === "message")
            .flatMap((o) =>
              ((o.content as Array<{ text?: string }>) || []).map((p) => p.text || "")
            )
          setReasoning(rTexts.join("\n"))
          setAnswer(aTexts.join("") || JSON.stringify(data, null, 2))
          const u = data.usage as
            | {
                input_tokens?: number
                output_tokens?: number
                output_tokens_details?: { reasoning_tokens?: number }
              }
            | undefined
          if (u) {
            setUsageMeta(
              `in=${u.input_tokens || 0} out=${u.output_tokens || 0} reasoning=${u.output_tokens_details?.reasoning_tokens || 0}`
            )
          }
        } else {
          const choices = (data.choices as Array<{ message?: Record<string, unknown> }>) || []
          const msg = choices[0]?.message || {}
          if (typeof msg.reasoning_content === "string") setReasoning(msg.reasoning_content)
          setAnswer(
            typeof msg.content === "string"
              ? msg.content
              : JSON.stringify(msg.tool_calls || data, null, 2)
          )
          const u = data.usage as
            | {
                prompt_tokens?: number
                completion_tokens?: number
                completion_tokens_details?: { reasoning_tokens?: number }
              }
            | undefined
          if (u) {
            setUsageMeta(
              `in=${u.prompt_tokens || 0} out=${u.completion_tokens || 0} reasoning=${u.completion_tokens_details?.reasoning_tokens || 0}`
            )
          }
        }
        onCompleted()
        return
      }

      const reader = res.body?.getReader()
      if (!reader) throw new Error("浏览器不支持响应流读取")
      const decoder = new TextDecoder()
      let buf = ""
      let rAcc = ""
      let aAcc = ""

      while (true) {
        const { done, value } = await reader.read()
        if (done) break
        buf += decoder.decode(value, { stream: true })
        const lines = buf.split("\n")
        buf = lines.pop() || ""
        for (const rawLine of lines) {
          const line = rawLine.trim()
          if (!line.startsWith("data:")) continue
          const payload = line.slice(5).trim()
          if (!payload || payload === "[DONE]") continue
          let obj: Record<string, unknown>
          try {
            obj = JSON.parse(payload) as Record<string, unknown>
          } catch {
            continue
          }

          if (endpoint === "responses") {
            const typ = obj.type
            if (
              typ === "response.reasoning_text.delta" ||
              typ === "response.reasoning_summary_text.delta"
            ) {
              rAcc += String(obj.delta || "")
              setReasoning(rAcc)
            } else if (typ === "response.output_text.delta") {
              aAcc += String(obj.delta || "")
              setAnswer(aAcc)
            } else if (typ === "response.completed" || typ === "response.incomplete") {
              const resp = obj.response as {
                usage?: {
                  input_tokens?: number
                  output_tokens?: number
                  output_tokens_details?: { reasoning_tokens?: number }
                }
              }
              if (resp?.usage) {
                const u = resp.usage
                setUsageMeta(
                  `in=${u.input_tokens || 0} out=${u.output_tokens || 0} reasoning=${u.output_tokens_details?.reasoning_tokens || 0}`
                )
              }
            }
          } else {
            const choices = (obj.choices as Array<{ delta?: Record<string, unknown> }>) || []
            const delta = choices[0]?.delta
            if (delta?.reasoning_content) {
              rAcc += String(delta.reasoning_content)
              setReasoning(rAcc)
            }
            if (delta?.content) {
              aAcc += String(delta.content)
              setAnswer(aAcc)
            }
            const u = obj.usage as
              | {
                  prompt_tokens?: number
                  completion_tokens?: number
                  completion_tokens_details?: { reasoning_tokens?: number }
                }
              | undefined
            if (u) {
              setUsageMeta(
                `in=${u.prompt_tokens || 0} out=${u.completion_tokens || 0} reasoning=${u.completion_tokens_details?.reasoning_tokens || 0}`
              )
            }
          }
        }
      }

      const ms = Math.round(performance.now() - t0)
      setStatusBadge(`流式完成 (${ms} ms${routeMode ? " · " + routeMode : ""})`)
      onCompleted()
    } catch (err) {
      const msg = err instanceof Error ? err.message : "未知异常"
      setStatusBadge("异常")
      setAnswer(msg)
      toast.error("调试异常: " + msg)
    } finally {
      setRunning(false)
    }
  }

  return (
    <Card>
      <CardHeader>
        <div>
          <div className="flex flex-wrap items-center gap-2">
            <CardTitle>在线调试（Playground）</CardTitle>
            <Badge variant="mono">{poolBadge}</Badge>
          </div>
          <CardDescription>
            内置 100 条随机测试提示词；默认按 OpenAI 路由池余额权重自动选号，也可指定单个账号定向测试。
          </CardDescription>
        </div>

        <div className="flex flex-wrap items-center gap-2.5 text-xs">
          <label className="flex items-center gap-1.5 cursor-pointer">
            <input
              type="checkbox"
              checked={autoRandom}
              onChange={(e) => setAutoRandom(e.target.checked)}
            />
            每次自动随机
          </label>
          <label className="flex items-center gap-1.5 cursor-pointer">
            <input
              type="checkbox"
              checked={stream}
              onChange={(e) => setStream(e.target.checked)}
            />
            SSE 流式
          </label>
          <Button variant="outline" size="sm" onClick={() => rollRandom(false)}>
            <Shuffle className="h-3.5 w-3.5" />
            随机换一题
          </Button>
          <Button size="sm" disabled={running} onClick={handleRun}>
            <Send className="h-3.5 w-3.5" />
            {running ? "请求中..." : "发送测试"}
          </Button>
        </div>
      </CardHeader>

      <CardContent className="space-y-4">
        <div className="grid grid-cols-1 gap-3 sm:grid-cols-2 lg:grid-cols-4">
          <div className="space-y-1.5">
            <Label htmlFor="pg-account">路由账号</Label>
            <NativeSelect
              id="pg-account"
              className="w-full h-9"
              value={selectedAccountId}
              onChange={(e) => onSelectAccountId(e.target.value)}
            >
              <option value="">自动混合路由（按池余额权重）</option>
              {accounts.map((a) => (
                <option key={a.id} value={a.id}>
                  定向: {a.displayName} ({a.poolEnabled ? `池内 ${a.weightPercent}%` : "未入池"})
                </option>
              ))}
            </NativeSelect>
          </div>

          <div className="space-y-1.5">
            <Label htmlFor="pg-endpoint">接口协议</Label>
            <NativeSelect
              id="pg-endpoint"
              className="w-full h-9"
              value={endpoint}
              onChange={(e) => setEndpoint(e.target.value as "chat" | "responses")}
            >
              <option value="chat">Chat Completions (/v1/chat/completions)</option>
              <option value="responses">Responses API (/v1/responses)</option>
            </NativeSelect>
          </div>

          <div className="space-y-1.5">
            <Label htmlFor="pg-model">模型</Label>
            <NativeSelect
              id="pg-model"
              className="w-full h-9"
              value={selectedModel}
              onChange={(e) => onSelectModel(e.target.value)}
            >
              {models.length === 0 ? (
                <>
                  <option value="dsflash-high">dsflash-high (DeepSeek-V4.1-Flash)</option>
                  <option value="dspro-high">dspro-high (DeepSeek-V4-Pro)</option>
                </>
              ) : (
                models.map((m) => (
                  <option key={m.id} value={m.id}>
                    {m.id} ({m.presentation?.name || m.name || m.id})
                    {m.hidden ? " [hidden]" : ""}
                  </option>
                ))
              )}
            </NativeSelect>
          </div>

          <div className="space-y-1.5">
            <Label htmlFor="pg-effort">推理强度</Label>
            <NativeSelect
              id="pg-effort"
              className="w-full h-9"
              value={effort}
              onChange={(e) => setEffort(e.target.value)}
            >
              <option value="low">low</option>
              <option value="medium">medium</option>
              <option value="high">high</option>
              <option value="max">max</option>
              <option value="">默认</option>
            </NativeSelect>
          </div>
        </div>

        <div className="space-y-1.5">
          <Label htmlFor="pg-prompt">测试提示词（默认从 100 条题库随机选取，也可自行编辑）</Label>
          <Textarea
            id="pg-prompt"
            rows={2}
            value={prompt}
            onChange={(e) => {
              setPrompt(e.target.value)
              const hasCustom = e.target.value.trim().length > 0
              setCustomized(hasCustom)
              if (hasCustom) setPoolBadge("自定义输入")
            }}
            placeholder="正在从 100 条内置测试题库随机抽取，也可直接输入自定义提示词..."
          />
        </div>

        {hasOutput && (
          <div className="rounded-lg border bg-muted/25 p-4 space-y-2.5">
            <div className="flex flex-wrap items-center justify-between gap-2">
              <Badge variant="mono">{statusBadge}</Badge>
              {usageMeta && (
                <Badge variant="mono" className="tabular-nums">
                  {usageMeta}
                </Badge>
              )}
            </div>
            {sentPrompt && <div className="text-[11px] text-muted-foreground">{sentPrompt}</div>}
            {reasoning && (
              <div className="rounded-md border bg-background/80 p-3 text-xs text-muted-foreground whitespace-pre-wrap">
                {reasoning}
              </div>
            )}
            <pre className="max-h-96 overflow-auto whitespace-pre-wrap break-words rounded-md bg-background p-3 text-xs leading-relaxed">
              {answer}
            </pre>
          </div>
        )}
      </CardContent>
    </Card>
  )
}
