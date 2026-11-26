import { useCallback, useEffect, useState } from "react"
import { toast } from "sonner"
import {
  ChevronLeft,
  ChevronRight,
  Copy,
  Eye,
  RefreshCw,
  Search,
} from "lucide-react"
import { apiRequest } from "@/lib/api"
import { cn, formatNum, formatTime } from "@/lib/utils"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card"
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
import { Input, NativeSelect } from "@/components/ui/input"
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table"
import type { GatewayUsageLogItem, UsageLogsPageResponse } from "@/types/console"

interface UsageLogsTableProps {
  active: boolean
  rangeDays: number
  apiKeyId: string
  model: string
  accountId: string
  refreshTick: number
}

function formatCompactDateTime(iso: string): { date: string; time: string } {
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return { date: iso, time: "" }
  const pad = (n: number) => String(n).padStart(2, "0")
  return {
    date: `${pad(d.getMonth() + 1)}-${pad(d.getDate())}`,
    time: `${pad(d.getHours())}:${pad(d.getMinutes())}:${pad(d.getSeconds())}`,
  }
}

function routingModeLabel(mode: string): { label: string; variant: "ok" | "info" | "warn" | "muted" } {
  switch (mode) {
    case "bound":
      return { label: "命中绑定", variant: "ok" }
    case "new_bind":
      return { label: "新建绑定", variant: "info" }
    case "rebound":
      return { label: "故障重绑", variant: "warn" }
    case "direct":
      return { label: "指定账号", variant: "muted" }
    default:
      return { label: mode || "未路由", variant: "muted" }
  }
}

function preflightModeLabel(mode?: string): { label: string; variant: "ok" | "info" | "muted" } {
  switch (mode) {
    case "preflight":
      return { label: "预检计费", variant: "ok" }
    case "tool_skip":
      return { label: "工具续传", variant: "info" }
    case "disabled":
      return { label: "免预检", variant: "muted" }
    default:
      return { label: mode || "-", variant: "muted" }
  }
}

function effortBadgeVariant(effort?: string): "info" | "warn" | "muted" {
  switch ((effort || "").toLowerCase()) {
    case "max":
    case "xhigh":
      return "warn"
    case "high":
    case "medium":
      return "info"
    default:
      return "muted"
  }
}

function endpointShortLabel(ep: string): string {
  if (ep === "/v1/chat/completions") return "/v1/chat"
  if (ep === "/v1/responses") return "/v1/responses"
  return ep || "-"
}

function formatMultiplier(m: number): string {
  const v = m > 0 ? m : 1
  return Number.isInteger(v) ? `${v}×` : `${Number(v.toFixed(2))}×`
}

function tokensPerSecond(completionTokens: number, durationMs: number): string | null {
  if (completionTokens <= 0 || durationMs <= 0) return null
  const tps = completionTokens / (durationMs / 1000)
  if (!Number.isFinite(tps) || tps <= 0) return null
  return `${tps >= 100 ? tps.toFixed(0) : tps.toFixed(1)} tok/s`
}

function copyText(text: string, label: string) {
  if (!text) return
  void navigator.clipboard?.writeText(text).then(
    () => toast.success(`已复制${label}`),
    () => toast.error("复制失败")
  )
}

export function UsageLogsTable({
  active,
  rangeDays,
  apiKeyId,
  model,
  accountId,
  refreshTick,
}: UsageLogsTableProps) {
  const [statusFilter, setStatusFilter] = useState<string>("")
  const [streamFilter, setStreamFilter] = useState<string>("")
  const [searchInput, setSearchInput] = useState<string>("")
  const [debouncedQuery, setDebouncedQuery] = useState<string>("")
  const [page, setPage] = useState<number>(1)
  const [pageSize, setPageSize] = useState<number>(20)

  const [items, setItems] = useState<GatewayUsageLogItem[]>([])
  const [total, setTotal] = useState<number>(0)
  const [totalPages, setTotalPages] = useState<number>(1)
  const [loading, setLoading] = useState<boolean>(false)
  const [selected, setSelected] = useState<GatewayUsageLogItem | null>(null)

  useEffect(() => {
    const timer = window.setTimeout(() => {
      setDebouncedQuery(searchInput.trim())
      setPage(1)
    }, 300)
    return () => window.clearTimeout(timer)
  }, [searchInput])

  // Reset to page 1 when upstream filters change.
  useEffect(() => {
    setPage(1)
  }, [rangeDays, apiKeyId, model, accountId, statusFilter, streamFilter, pageSize])

  const loadLogs = useCallback(async () => {
    setLoading(true)
    try {
      const qs = new URLSearchParams()
      qs.set("days", String(rangeDays))
      qs.set("page", String(page))
      qs.set("pageSize", String(pageSize))
      if (apiKeyId) qs.set("apiKeyId", apiKeyId)
      if (model) qs.set("model", model)
      if (accountId) qs.set("accountId", accountId)
      if (statusFilter) qs.set("status", statusFilter)
      if (streamFilter) qs.set("stream", streamFilter)
      if (debouncedQuery) qs.set("q", debouncedQuery)

      const res = await apiRequest<UsageLogsPageResponse>(
        `/api/gateway/analytics/logs?${qs.toString()}`
      )
      setItems(res.items || [])
      setTotal(res.total || 0)
      setTotalPages(Math.max(1, res.totalPages || 1))
    } catch (err) {
      toast.error("加载请求明细失败: " + (err instanceof Error ? err.message : ""))
    } finally {
      setLoading(false)
    }
  }, [rangeDays, apiKeyId, model, accountId, statusFilter, streamFilter, debouncedQuery, page, pageSize])

  useEffect(() => {
    if (active) {
      void loadLogs()
    }
  }, [active, loadLogs, refreshTick])

  const selectedOk =
    selected != null &&
    selected.status >= 200 &&
    selected.status < 300 &&
    !selected.error

  return (
    <>
      <Card>
        <CardHeader className="py-3.5">
          <div>
            <div className="flex flex-wrap items-center gap-2">
              <CardTitle>详细使用记录</CardTitle>
              <Badge variant="mono">共 {formatNum(total)} 条</Badge>
            </div>
            <CardDescription>
              记录每次 OpenAI 网关调用的密钥、模型与推理强度、端点/桥接类型、路由账号、计费策略与倍率、Token 明细、首字/总延迟、IP 与 User-Agent；点击任意行查看完整诊断详情。
            </CardDescription>
          </div>

          <div className="flex flex-wrap items-center gap-2">
            <div className="relative">
              <Search className="pointer-events-none absolute left-2.5 top-1/2 h-3.5 w-3.5 -translate-y-1/2 text-muted-foreground" />
              <Input
                value={searchInput}
                onChange={(e) => setSearchInput(e.target.value)}
                placeholder="搜索 IP / UA / 模型 / Request ID / 错误..."
                className="h-8 w-56 pl-8 text-xs"
                aria-label="搜索使用记录"
              />
            </div>

            <NativeSelect
              aria-label="按请求状态筛选"
              value={statusFilter}
              onChange={(e) => setStatusFilter(e.target.value)}
            >
              <option value="">全部状态</option>
              <option value="success">仅成功 (2xx)</option>
              <option value="error">仅失败</option>
            </NativeSelect>

            <NativeSelect
              aria-label="按流式类型筛选"
              value={streamFilter}
              onChange={(e) => setStreamFilter(e.target.value)}
            >
              <option value="">全部类型</option>
              <option value="true">流式 (SSE)</option>
              <option value="false">非流式 (JSON)</option>
            </NativeSelect>

            <NativeSelect
              aria-label="每页条数"
              value={String(pageSize)}
              onChange={(e) => setPageSize(Number(e.target.value))}
            >
              <option value="20">20 条/页</option>
              <option value="50">50 条/页</option>
              <option value="100">100 条/页</option>
            </NativeSelect>

            <Button
              size="xs"
              variant="outline"
              disabled={loading}
              onClick={() => void loadLogs()}
              title="刷新明细列表"
            >
              <RefreshCw className={cn("h-3 w-3", loading && "animate-spin")} />
            </Button>
          </div>
        </CardHeader>

        <CardContent className="p-0">
          <div className="overflow-x-auto">
            <Table>
              <TableHeader>
                <TableRow className="whitespace-nowrap">
                  <TableHead>时间 / 状态</TableHead>
                  <TableHead>API 密钥</TableHead>
                  <TableHead>模型 / 推理强度</TableHead>
                  <TableHead>端点 / 类型</TableHead>
                  <TableHead>路由账号 / 分组</TableHead>
                  <TableHead>计费策略 / 倍率</TableHead>
                  <TableHead className="text-right">Token 明细</TableHead>
                  <TableHead className="text-right">延迟 / 速率</TableHead>
                  <TableHead>IP / User-Agent</TableHead>
                  <TableHead className="w-9 text-center">详情</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {items.length === 0 ? (
                  <TableRow>
                    <TableCell colSpan={10} className="py-10 text-center text-xs text-muted-foreground">
                      {loading ? "正在加载使用记录..." : "当前筛选条件下暂无使用记录"}
                    </TableCell>
                  </TableRow>
                ) : (
                  items.map((item) => {
                    const isOk = item.status >= 200 && item.status < 300 && !item.error
                    const dt = formatCompactDateTime(item.createdAt)
                    const routeInfo = routingModeLabel(item.routingMode)
                    const pfInfo = preflightModeLabel(item.preflightMode)
                    const tps = tokensPerSecond(item.completionTokens, item.durationMs)
                    return (
                      <TableRow
                        key={item.id}
                        className="cursor-pointer hover:bg-muted/40 transition-colors"
                        onClick={() => setSelected(item)}
                      >
                        {/* 时间 / 状态 */}
                        <TableCell className="whitespace-nowrap align-top">
                          <div className="flex items-center gap-1.5">
                            <Badge variant={isOk ? "ok" : "err"} className="font-mono text-[10px] px-1.5">
                              {item.status || "ERR"}
                            </Badge>
                            <span className="font-mono text-[11px] font-medium tabular-nums">
                              {dt.time}
                            </span>
                          </div>
                          <div className="mt-0.5 font-mono text-[10px] text-muted-foreground tabular-nums">
                            {dt.date}
                          </div>
                        </TableCell>

                        {/* API 密钥 */}
                        <TableCell className="align-top whitespace-nowrap">
                          <div className="max-w-[130px] truncate font-medium text-xs" title={item.apiKeyName}>
                            {item.apiKeyName || item.apiKeyId}
                          </div>
                          <div className="font-mono text-[10px] text-muted-foreground">
                            {item.apiKeyId}
                          </div>
                        </TableCell>

                        {/* 模型 / 推理强度 */}
                        <TableCell className="align-top whitespace-nowrap">
                          <div className="flex items-center gap-1.5">
                            <code className="rounded bg-muted px-1.5 py-0.5 text-[11px] font-medium">
                              {item.model || "-"}
                            </code>
                            {item.reasoningEffort && (
                              <Badge
                                variant={effortBadgeVariant(item.reasoningEffort)}
                                className="text-[10px] px-1.5"
                              >
                                {item.reasoningEffort}
                              </Badge>
                            )}
                          </div>
                          {item.requestedModel && item.requestedModel !== item.model && (
                            <div
                              className="mt-0.5 max-w-[170px] truncate text-[10px] text-muted-foreground"
                              title={`请求别名: ${item.requestedModel}`}
                            >
                              别名: {item.requestedModel}
                            </div>
                          )}
                        </TableCell>

                        {/* 端点 / 类型 */}
                        <TableCell className="align-top whitespace-nowrap">
                          <div className="font-mono text-[11px]">{endpointShortLabel(item.endpoint)}</div>
                          <div className="mt-1 flex flex-wrap items-center gap-1">
                            <Badge
                              variant={item.stream ? "info" : "muted"}
                              className="text-[10px] px-1.5"
                            >
                              {item.stream ? "SSE" : "JSON"}
                            </Badge>
                            {item.bridged && (
                              <Badge
                                variant="warn"
                                className="text-[10px] px-1.5"
                                title={`跨协议桥接至上游 ${item.upstreamApi}`}
                              >
                                桥接
                              </Badge>
                            )}
                            {item.hasTools && (
                              <Badge variant="default" className="text-[10px] px-1.5">
                                Tools
                              </Badge>
                            )}
                            {item.hasVision && (
                              <Badge variant="info" className="text-[10px] px-1.5">
                                Vision
                              </Badge>
                            )}
                          </div>
                        </TableCell>

                        {/* 路由账号 / 分组 */}
                        <TableCell className="align-top whitespace-nowrap">
                          <div className="max-w-[140px] truncate text-xs font-medium" title={item.accountLabel}>
                            {item.accountLabel || "未路由"}
                          </div>
                          <div className="mt-1 flex items-center gap-1">
                            <Badge variant={routeInfo.variant} className="text-[10px] px-1.5">
                              {routeInfo.label}
                            </Badge>
                            {item.sessionHash && (
                              <span
                                className="font-mono text-[10px] text-muted-foreground"
                                title={`Session Hash: ${item.sessionHash}`}
                              >
                                #{item.sessionHash.slice(0, 8)}
                              </span>
                            )}
                          </div>
                        </TableCell>

                        {/* 计费策略 / 倍率 */}
                        <TableCell className="align-top whitespace-nowrap">
                          <div className="flex items-center gap-1">
                            <Badge variant="mono" className="text-[10px] px-1.5">
                              {formatMultiplier(item.usageMultiplier)}
                            </Badge>
                            <span className="text-[11px] tabular-nums text-muted-foreground">
                              折算 {formatNum(Math.round(item.weightedTokens || 0))}
                            </span>
                          </div>
                          <div className="mt-1 flex items-center gap-1">
                            <Badge variant={pfInfo.variant} className="text-[10px] px-1.5">
                              {pfInfo.label}
                            </Badge>
                            {item.sciMasterPrompt && (
                              <Badge variant="muted" className="text-[10px] px-1.5">
                                SciMaster
                              </Badge>
                            )}
                          </div>
                        </TableCell>

                        {/* Token 明细 */}
                        <TableCell className="align-top text-right whitespace-nowrap tabular-nums">
                          <div className="font-semibold text-xs">
                            {formatNum(item.totalTokens)}
                          </div>
                          <div className="text-[10px] text-muted-foreground">
                            入 {formatNum(item.promptTokens)} · 出 {formatNum(item.completionTokens)}
                          </div>
                          {(item.reasoningTokens > 0 || item.cachedTokens > 0) && (
                            <div className="text-[10px] space-x-1.5">
                              {item.reasoningTokens > 0 && (
                                <span className="text-violet-600 dark:text-violet-400">
                                  思 {formatNum(item.reasoningTokens)}
                                </span>
                              )}
                              {item.cachedTokens > 0 && (
                                <span className="text-emerald-600 dark:text-emerald-400">
                                  缓 {formatNum(item.cachedTokens)}
                                </span>
                              )}
                            </div>
                          )}
                        </TableCell>

                        {/* 延迟 / 速率 */}
                        <TableCell className="align-top text-right whitespace-nowrap tabular-nums">
                          <div className="text-xs font-medium">
                            {formatNum(item.durationMs)} ms
                          </div>
                          <div className="text-[10px] text-muted-foreground">
                            首字 {item.firstTokenMs > 0 ? `${formatNum(item.firstTokenMs)}ms` : "-"}
                          </div>
                          {tps && (
                            <div className="text-[10px] text-muted-foreground">{tps}</div>
                          )}
                        </TableCell>

                        {/* IP / User-Agent */}
                        <TableCell className="align-top max-w-[200px]">
                          <div className="font-mono text-[11px] truncate">
                            {item.clientIp || "-"}
                          </div>
                          <div
                            className="mt-0.5 truncate text-[10px] text-muted-foreground"
                            title={item.userAgent || ""}
                          >
                            {item.userAgent || "-"}
                          </div>
                        </TableCell>

                        {/* 详情按钮 */}
                        <TableCell className="align-top text-center">
                          <Button
                            size="xs"
                            variant="ghost"
                            className="h-7 w-7 p-0"
                            onClick={(e) => {
                              e.stopPropagation()
                              setSelected(item)
                            }}
                            title="查看完整请求详情"
                          >
                            <Eye className="h-3.5 w-3.5" />
                          </Button>
                        </TableCell>
                      </TableRow>
                    )
                  })
                )}
              </TableBody>
            </Table>
          </div>

          {/* Pagination Footer */}
          <div className="flex flex-wrap items-center justify-between gap-2 border-t px-4 py-3 text-xs text-muted-foreground">
            <div className="tabular-nums">
              第 {page} / {totalPages} 页 · 共 {formatNum(total)} 条记录
            </div>
            <div className="flex items-center gap-1.5">
              <Button
                size="xs"
                variant="outline"
                disabled={loading || page <= 1}
                onClick={() => setPage((p) => Math.max(1, p - 1))}
              >
                <ChevronLeft className="h-3.5 w-3.5" />
                上一页
              </Button>
              <Button
                size="xs"
                variant="outline"
                disabled={loading || page >= totalPages}
                onClick={() => setPage((p) => Math.min(totalPages, p + 1))}
              >
                下一页
                <ChevronRight className="h-3.5 w-3.5" />
              </Button>
            </div>
          </div>
        </CardContent>
      </Card>

      {/* Request Inspection Dialog */}
      <Dialog open={selected !== null} onOpenChange={(open) => !open && setSelected(null)}>
        <DialogContent className="w-[min(95vw,700px)]">
          {selected && (
            <>
              <DialogHeader>
                <div className="flex flex-wrap items-center gap-2">
                  <DialogTitle>使用记录详情</DialogTitle>
                  <Badge variant={selectedOk ? "ok" : "err"} className="font-mono">
                    HTTP {selected.status || "ERR"}
                  </Badge>
                  <Badge variant={selected.stream ? "info" : "muted"}>
                    {selected.stream ? "流式 SSE" : "非流式 JSON"}
                  </Badge>
                  {selected.bridged && <Badge variant="warn">协议桥接</Badge>}
                </div>
                <DialogDescription className="font-mono">
                  {formatTime(selected.createdAt)} · 记录 ID: {selected.id}
                </DialogDescription>
              </DialogHeader>

              <div className="space-y-4 text-xs">
                {selected.error && (
                  <div className="rounded-md border border-red-500/30 bg-red-500/10 p-3 text-red-700 dark:text-red-300">
                    <div className="font-semibold mb-1">错误信息</div>
                    <div className="font-mono whitespace-pre-wrap break-all">{selected.error}</div>
                  </div>
                )}

                {/* Token & Latency highlight strips */}
                <div className="grid grid-cols-2 gap-2.5 sm:grid-cols-4">
                  <div className="rounded-md border bg-muted/30 p-2.5">
                    <div className="text-[10px] text-muted-foreground">总 Token / 折算</div>
                    <div className="mt-0.5 text-sm font-semibold tabular-nums">
                      {formatNum(selected.totalTokens)}
                    </div>
                    <div className="text-[10px] text-muted-foreground tabular-nums">
                      {formatMultiplier(selected.usageMultiplier)} →{" "}
                      {formatNum(Math.round(selected.weightedTokens || 0))}
                    </div>
                  </div>

                  <div className="rounded-md border bg-muted/30 p-2.5">
                    <div className="text-[10px] text-muted-foreground">输入 / 输出</div>
                    <div className="mt-0.5 text-sm font-semibold tabular-nums">
                      {formatNum(selected.promptTokens)} / {formatNum(selected.completionTokens)}
                    </div>
                    <div className="text-[10px] text-muted-foreground tabular-nums">
                      缓存 {formatNum(selected.cachedTokens)}
                      {selected.promptTokens > 0
                        ? ` (${((selected.cachedTokens / selected.promptTokens) * 100).toFixed(1)}%)`
                        : ""}
                    </div>
                  </div>

                  <div className="rounded-md border bg-muted/30 p-2.5">
                    <div className="text-[10px] text-muted-foreground">推理 Token</div>
                    <div className="mt-0.5 text-sm font-semibold tabular-nums text-violet-600 dark:text-violet-400">
                      {formatNum(selected.reasoningTokens)}
                    </div>
                    <div className="text-[10px] text-muted-foreground">
                      强度: {selected.reasoningEffort || "默认/无"}
                    </div>
                  </div>

                  <div className="rounded-md border bg-muted/30 p-2.5">
                    <div className="text-[10px] text-muted-foreground">首字 TTFT / 总耗时</div>
                    <div className="mt-0.5 text-sm font-semibold tabular-nums">
                      {selected.firstTokenMs > 0 ? `${formatNum(selected.firstTokenMs)}ms` : "-"} /{" "}
                      {formatNum(selected.durationMs)}ms
                    </div>
                    <div className="text-[10px] text-muted-foreground tabular-nums">
                      {tokensPerSecond(selected.completionTokens, selected.durationMs) || "-"}
                    </div>
                  </div>
                </div>

                {/* Metadata key-value grid */}
                <div className="grid grid-cols-1 gap-x-4 gap-y-2.5 rounded-md border p-3.5 sm:grid-cols-2">
                  <div>
                    <div className="text-[10px] text-muted-foreground">API 密钥</div>
                    <div className="mt-0.5 font-medium">
                      {selected.apiKeyName}{" "}
                      <span className="font-mono text-[11px] text-muted-foreground">
                        ({selected.apiKeyId})
                      </span>
                    </div>
                  </div>

                  <div>
                    <div className="text-[10px] text-muted-foreground">模型 / 请求别名</div>
                    <div className="mt-0.5 font-mono">
                      {selected.model || "-"}
                      {selected.requestedModel && selected.requestedModel !== selected.model && (
                        <span className="text-muted-foreground"> ← {selected.requestedModel}</span>
                      )}
                    </div>
                  </div>

                  <div>
                    <div className="text-[10px] text-muted-foreground">入口端点 → 上游协议</div>
                    <div className="mt-0.5 font-mono">
                      {selected.endpoint} → {selected.upstreamApi || "-"}
                      {selected.bridged ? " (桥接)" : " (直连)"}
                    </div>
                  </div>

                  <div>
                    <div className="text-[10px] text-muted-foreground">请求能力标签</div>
                    <div className="mt-1 flex flex-wrap gap-1">
                      <Badge variant={selected.stream ? "info" : "muted"}>
                        {selected.stream ? "SSE 流式" : "JSON 非流式"}
                      </Badge>
                      {selected.hasTools && <Badge variant="default">Tools 调用</Badge>}
                      {selected.hasVision && <Badge variant="info">Vision 图像</Badge>}
                      {selected.sciMasterPrompt && <Badge variant="muted">SciMaster 注入</Badge>}
                    </div>
                  </div>

                  <div>
                    <div className="text-[10px] text-muted-foreground">命中路由账号 / 策略</div>
                    <div className="mt-0.5 flex flex-wrap items-center gap-1.5">
                      <span className="font-medium">{selected.accountLabel || "未路由"}</span>
                      <Badge variant={routingModeLabel(selected.routingMode).variant}>
                        {routingModeLabel(selected.routingMode).label}
                      </Badge>
                    </div>
                  </div>

                  <div>
                    <div className="text-[10px] text-muted-foreground">计费准入与倍率</div>
                    <div className="mt-0.5 flex flex-wrap items-center gap-1.5">
                      <Badge variant={preflightModeLabel(selected.preflightMode).variant}>
                        {preflightModeLabel(selected.preflightMode).label}
                      </Badge>
                      <Badge variant="mono">{formatMultiplier(selected.usageMultiplier)}</Badge>
                    </div>
                  </div>

                  <div>
                    <div className="text-[10px] text-muted-foreground">客户端 IP</div>
                    <div className="mt-0.5 font-mono">{selected.clientIp || "-"}</div>
                  </div>

                  <div>
                    <div className="text-[10px] text-muted-foreground">上游 Request ID</div>
                    <div className="mt-0.5 flex items-center gap-1.5 font-mono">
                      <span className="truncate">{selected.requestId || "-"}</span>
                      {selected.requestId && (
                        <Button
                          size="xs"
                          variant="ghost"
                          className="h-5 w-5 p-0 shrink-0"
                          onClick={() => copyText(selected.requestId!, "Request ID")}
                        >
                          <Copy className="h-3 w-3" />
                        </Button>
                      )}
                    </div>
                  </div>

                  <div className="sm:col-span-2">
                    <div className="text-[10px] text-muted-foreground">Session Hash（7 天绑定键）</div>
                    <div className="mt-0.5 flex items-center gap-1.5 font-mono break-all">
                      <span>{selected.sessionHash || "-"}</span>
                      {selected.sessionHash && (
                        <Button
                          size="xs"
                          variant="ghost"
                          className="h-5 w-5 p-0 shrink-0"
                          onClick={() => copyText(selected.sessionHash!, "Session Hash")}
                        >
                          <Copy className="h-3 w-3" />
                        </Button>
                      )}
                    </div>
                  </div>

                  <div className="sm:col-span-2">
                    <div className="text-[10px] text-muted-foreground">User-Agent</div>
                    <div className="mt-0.5 font-mono break-all text-muted-foreground">
                      {selected.userAgent || "-"}
                    </div>
                  </div>
                </div>
              </div>
            </>
          )}
        </DialogContent>
      </Dialog>
    </>
  )
}
