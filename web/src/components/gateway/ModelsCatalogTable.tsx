import { useState } from "react"
import { toast } from "sonner"
import { RefreshCw } from "lucide-react"
import { apiRequest } from "@/lib/api"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Card, CardDescription, CardHeader, CardTitle } from "@/components/ui/card"
import {
  AlertBox,
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table"
import type { SciXModelItem } from "@/types/console"

interface ModelsCatalogTableProps {
  models: SciXModelItem[]
  catalogError?: string
  loggedIn: boolean
  selectedAccountId: string
  modelEnabled?: Record<string, boolean>
  onRefreshCatalog: () => Promise<void>
  onToggleModel: (modelId: string, enabled: boolean) => Promise<void>
  onPickModel: (modelId: string) => void
}

export function isClientHiddenModel(m: SciXModelItem): boolean {
  return !m.hidden && m.presentation?.visible === false
}

export function isGatewayModelEnabled(
  m: SciXModelItem,
  modelEnabled?: Record<string, boolean>
): boolean {
  const key = m.id.toLowerCase()
  if (modelEnabled && Object.prototype.hasOwnProperty.call(modelEnabled, key)) {
    return !!modelEnabled[key]
  }
  return !isClientHiddenModel(m)
}

export function ModelsCatalogTable({
  models,
  catalogError,
  loggedIn,
  selectedAccountId,
  modelEnabled,
  onRefreshCatalog,
  onToggleModel,
  onPickModel,
}: ModelsCatalogTableProps) {
  const [refreshing, setRefreshing] = useState(false)
  const [preflightId, setPreflightId] = useState("")
  const [togglingId, setTogglingId] = useState("")

  async function handleToggle(modelId: string, enabled: boolean) {
    setTogglingId(modelId)
    try {
      await onToggleModel(modelId, enabled)
    } finally {
      setTogglingId("")
    }
  }

  async function refreshModels() {
    setRefreshing(true)
    try {
      await apiRequest("/api/gateway/models/refresh", { method: "POST" })
      toast.success("已刷新 Bohrium SciX 模型目录")
      await onRefreshCatalog()
    } catch (err) {
      toast.error("刷新失败: " + (err instanceof Error ? err.message : ""))
    } finally {
      setRefreshing(false)
    }
  }

  async function runPreflight(modelId: string) {
    setPreflightId(modelId)
    try {
      const res = await apiRequest<{
        allowed?: boolean
        reason?: string
        accountName?: string
      }>("/api/gateway/preflight", {
        method: "POST",
        body: JSON.stringify({ model: modelId, accountId: selectedAccountId }),
      })
      const who = res.accountName ? `[${res.accountName}] ` : ""
      if (res.allowed) {
        toast.success(`${who}模型 ${modelId} 额度预检通过 (allowed=true)`)
      } else {
        toast.warning(`${who}模型 ${modelId} 预检拒绝: ${res.reason || "额度不足"}`)
      }
    } catch (err) {
      toast.error("预检失败: " + (err instanceof Error ? err.message : ""))
    } finally {
      setPreflightId("")
    }
  }

  return (
    <Card>
      <CardHeader>
        <div>
          <div className="flex flex-wrap items-center gap-2">
            <CardTitle>Bohrium SciX 模型目录</CardTitle>
            <Badge variant="mono">shrimp-brain-v2</Badge>
          </div>
          <CardDescription>
            实时同步 Bohrium 客户端模型列表；勾选的模型将通过标准 OpenAI <code>/v1/models</code>{" "}
            暴露并允许调用（<code>standard</code>/<code>advanced</code> 与客户端一致默认{" "}
            <code>hidden</code> 禁用，<code>vision</code>/<code>title</code>/<code>summary</code>{" "}
            默认启用）。
          </CardDescription>
        </div>
        <Button variant="outline" size="sm" disabled={refreshing} onClick={refreshModels}>
          <RefreshCw className={`h-3.5 w-3.5 ${refreshing ? "animate-spin" : ""}`} />
          刷新模型目录
        </Button>
      </CardHeader>

      {catalogError && (
        <div className="px-5 pt-4">
          <AlertBox variant="err">{catalogError}</AlertBox>
        </div>
      )}

      <Table>
        <TableHeader>
          <TableRow>
            <TableHead className="w-16">启用</TableHead>
            <TableHead>模型 ID / 名称</TableHead>
            <TableHead>原生上游协议</TableHead>
            <TableHead>上下文 / 最大输出</TableHead>
            <TableHead>模态与推理</TableHead>
            <TableHead>倍率</TableHead>
            <TableHead className="text-right">操作</TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {models.length === 0 ? (
            <TableRow>
              <TableCell colSpan={7} className="py-6 text-center text-muted-foreground">
                {loggedIn
                  ? "暂无模型数据，请点击「刷新模型目录」"
                  : "请先添加玻尔账号，即可拉取 SciX 模型目录"}
              </TableCell>
            </TableRow>
          ) : (
            models.map((m) => {
              const nativeApi = m.api === "chat_completions" ? "chat_completions" : "responses"
              const displayName = m.presentation?.name || m.name || m.id
              const desc = m.presentation?.description || ""
              const ctxK = m.context_window ? `${Math.round(m.context_window / 1000)}K` : "--"
              const outK = m.max_output_tokens ? `${Math.round(m.max_output_tokens / 1000)}K` : "--"
              const mods = (m.input || ["text"]).join(", ")
              const effs = (m.reasoning_efforts || []).join("/")
              const defEff = m.reasoning_effort || ""
              const mult = m.presentation?.usageMultiplier
                ? `${m.presentation.usageMultiplier}x`
                : "--"
              const clientHidden = isClientHiddenModel(m)
              const enabled = isGatewayModelEnabled(m, modelEnabled)

              return (
                <TableRow key={m.id} className={!enabled ? "opacity-60" : undefined}>
                  <TableCell>
                    <input
                      type="checkbox"
                      className="h-4 w-4 cursor-pointer accent-foreground"
                      checked={enabled}
                      disabled={togglingId === m.id}
                      onChange={(e) => void handleToggle(m.id, e.target.checked)}
                      aria-label={`启用模型 ${m.id}`}
                    />
                  </TableCell>
                  <TableCell>
                    <div className="flex flex-wrap items-center gap-1.5">
                      <code className="rounded bg-muted px-1.5 py-0.5 text-[11px]">{m.id}</code>
                      <strong className="font-semibold">{displayName}</strong>
                      {m.presentation?.default && <Badge variant="mono">default</Badge>}
                      {m.hidden && <Badge variant="mono">internal</Badge>}
                      {clientHidden && <Badge variant="mono">hidden</Badge>}
                      {!enabled && <Badge variant="muted">已禁用</Badge>}
                    </div>
                    {desc && <div className="mt-0.5 text-[11px] text-muted-foreground">{desc}</div>}
                  </TableCell>
                  <TableCell>
                    <Badge variant="mono">{nativeApi}</Badge>
                  </TableCell>
                  <TableCell className="tabular-nums">
                    {ctxK} / {outK}
                  </TableCell>
                  <TableCell>
                    <div>{mods}</div>
                    {effs && (
                      <div className="mt-0.5 text-[11px] text-muted-foreground">
                        effort: {effs}
                        {defEff ? ` (默认 ${defEff})` : ""}
                      </div>
                    )}
                  </TableCell>
                  <TableCell className="tabular-nums">{mult}</TableCell>
                  <TableCell className="text-right">
                    <div className="flex justify-end gap-1.5">
                      <Button
                        variant="ghost"
                        size="xs"
                        disabled={preflightId === m.id}
                        onClick={() => runPreflight(m.id)}
                      >
                        预检
                      </Button>
                      <Button
                        variant="outline"
                        size="xs"
                        disabled={!enabled}
                        title={!enabled ? "启用该模型后可调试" : undefined}
                        onClick={() => onPickModel(m.id)}
                      >
                        调试
                      </Button>
                    </div>
                  </TableCell>
                </TableRow>
              )
            })
          )}
        </TableBody>
      </Table>
    </Card>
  )
}
