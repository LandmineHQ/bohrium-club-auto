import { useCallback, useEffect, useState } from "react"
import { toast } from "sonner"
import {
  Area,
  AreaChart,
  Bar,
  BarChart,
  CartesianGrid,
  Cell,
  Legend,
  Pie,
  PieChart,
  ResponsiveContainer,
  Tooltip,
  XAxis,
  YAxis,
} from "recharts"
import { RefreshCw, RotateCcw, Save, Trash2 } from "lucide-react"
import { apiRequest } from "@/lib/api"
import { cn, formatNum, formatTime } from "@/lib/utils"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card"
import { Input, NativeSelect } from "@/components/ui/input"
import { Progress } from "@/components/ui/primitives"
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table"
import type { GatewayAnalyticsData, GatewayAnalyticsResponse } from "@/types/console"
import { UsageLogsTable } from "@/components/analytics/UsageLogsTable"

interface AnalyticsTabProps {
  active: boolean
  onRetentionUpdated?: () => Promise<void>
}

const RANGE_PRESETS: Array<{ label: string; days: number }> = [
  { label: "24 小时", days: 1 },
  { label: "7 天", days: 7 },
  { label: "30 天", days: 30 },
  { label: "90 天", days: 90 },
  { label: "180 天", days: 180 },
  { label: "全部", days: 0 },
]

const PALETTE = [
  "#2563eb",
  "#10b981",
  "#f59e0b",
  "#8b5cf6",
  "#ec4899",
  "#06b6d4",
  "#f97316",
  "#64748b",
]

function compactTokens(v: number): string {
  if (v >= 1_000_000) return `${(v / 1_000_000).toFixed(1)}M`
  if (v >= 1_000) return `${(v / 1_000).toFixed(1)}K`
  return String(v)
}

export function AnalyticsTab({ active, onRetentionUpdated }: AnalyticsTabProps) {
  const [rangeDays, setRangeDays] = useState<number>(30)
  const [granularity, setGranularity] = useState<"auto" | "hour" | "day">("auto")
  const [apiKeyId, setApiKeyId] = useState("")
  const [model, setModel] = useState("")
  const [accountId, setAccountId] = useState("")
  const [trendMode, setTrendMode] = useState<"tokens" | "requests">("tokens")

  const [data, setData] = useState<GatewayAnalyticsData | null>(null)
  const [retentionDays, setRetentionDays] = useState<number>(180)
  const [retentionInput, setRetentionInput] = useState<string>("180")
  const [loading, setLoading] = useState(false)
  const [savingRetention, setSavingRetention] = useState(false)
  const [refreshTick, setRefreshTick] = useState(0)

  const loadAnalytics = useCallback(
    async (showToast = false) => {
      setLoading(true)
      try {
        const qs = new URLSearchParams()
        qs.set("days", String(rangeDays))
        if (granularity !== "auto") qs.set("granularity", granularity)
        if (apiKeyId) qs.set("apiKeyId", apiKeyId)
        if (model) qs.set("model", model)
        if (accountId) qs.set("accountId", accountId)
        const res = await apiRequest<GatewayAnalyticsResponse>(
          `/api/gateway/analytics?${qs.toString()}`
        )
        setData(res.analytics)
        if (res.analyticsRetentionDays > 0) {
          setRetentionDays(res.analyticsRetentionDays)
          setRetentionInput(String(res.analyticsRetentionDays))
        }
        setRefreshTick((t) => t + 1)
        if (showToast) toast.success("数据看板已刷新")
      } catch (err) {
        toast.error("加载用量看板失败: " + (err instanceof Error ? err.message : ""))
      } finally {
        setLoading(false)
      }
    },
    [rangeDays, granularity, apiKeyId, model, accountId]
  )

  useEffect(() => {
    if (active) {
      void loadAnalytics(false)
    }
  }, [active, loadAnalytics])

  async function saveRetention() {
    const parsed = Number(retentionInput)
    if (!Number.isInteger(parsed) || parsed < 1 || parsed > 3650) {
      toast.warning("数据保留天数需为 1 ~ 3650 之间的整数")
      return
    }
    setSavingRetention(true)
    try {
      const res = await apiRequest<{ purged?: number }>("/api/gateway/settings", {
        method: "PUT",
        body: JSON.stringify({ analyticsRetentionDays: parsed }),
      })
      setRetentionDays(parsed)
      const extra = res.purged ? `，已清理 ${res.purged} 条超期记录` : ""
      toast.success(`数据保留期已更新为 ${parsed} 天${extra}`)
      await loadAnalytics(false)
      if (onRetentionUpdated) await onRetentionUpdated()
    } catch (err) {
      toast.error("保存保留策略失败: " + (err instanceof Error ? err.message : ""))
    } finally {
      setSavingRetention(false)
    }
  }

  async function clearAllUsage() {
    if (!window.confirm("确定清空所有已持久化的 OpenAI 用量历史记录吗？此操作不可撤销。")) {
      return
    }
    try {
      const res = await apiRequest<{ purged?: number }>("/api/gateway/analytics/purge", {
        method: "POST",
        body: JSON.stringify({ clearAll: true }),
      })
      toast.warning(`已清空 ${res.purged || 0} 条用量记录`)
      await loadAnalytics(false)
    } catch (err) {
      toast.error("清空失败: " + (err instanceof Error ? err.message : ""))
    }
  }

  const summary = data?.summary
  const timeSeries = data?.timeSeries || []
  const byKey = data?.byKey || []
  const byModel = data?.byModel || []
  const byAccount = data?.byAccount || []
  const byKeyModel = data?.byKeyModel || []
  const hasFilters = Boolean(apiKeyId || model || accountId)

  const cacheHitRatio =
    summary && summary.promptTokens > 0
      ? ((summary.cachedTokens / summary.promptTokens) * 100).toFixed(1)
      : "0.0"

  return (
    <div className="space-y-5">
      {/* Control Bar: Time range + Dimension filters + Retention config */}
      <Card>
        <CardContent className="p-4 space-y-3.5">
          <div className="flex flex-wrap items-center justify-between gap-3">
            <div className="flex flex-wrap items-center gap-1.5">
              {RANGE_PRESETS.map((p) => (
                <Button
                  key={p.days}
                  size="xs"
                  variant={rangeDays === p.days ? "default" : "outline"}
                  onClick={() => {
                    setRangeDays(p.days)
                    if (p.days === 1) setGranularity("hour")
                    else if (p.days > 14 && granularity === "hour") setGranularity("day")
                  }}
                >
                  {p.label}
                </Button>
              ))}

              <div className="ml-1 flex items-center rounded-md border p-0.5">
                <button
                  type="button"
                  onClick={() => setGranularity("hour")}
                  disabled={rangeDays > 14}
                  title={rangeDays > 14 ? "超过 14 天窗口请按天聚合" : "按小时聚合"}
                  className={cn(
                    "rounded px-2 py-1 text-[11px] font-medium cursor-pointer disabled:opacity-40 disabled:cursor-not-allowed",
                    (data?.granularity || granularity) === "hour"
                      ? "bg-primary text-primary-foreground"
                      : "text-muted-foreground hover:text-foreground"
                  )}
                >
                  按小时
                </button>
                <button
                  type="button"
                  onClick={() => setGranularity("day")}
                  className={cn(
                    "rounded px-2 py-1 text-[11px] font-medium cursor-pointer",
                    (data?.granularity || granularity) === "day"
                      ? "bg-primary text-primary-foreground"
                      : "text-muted-foreground hover:text-foreground"
                  )}
                >
                  按天
                </button>
              </div>
            </div>

            <div className="flex flex-wrap items-center gap-2">
              <NativeSelect
                aria-label="按 API Key 筛选"
                value={apiKeyId}
                onChange={(e) => setApiKeyId(e.target.value)}
              >
                <option value="">全部 API Key</option>
                {(data?.availableKeys || []).map((k) => (
                  <option key={k.id} value={k.id}>
                    Key: {k.name}
                  </option>
                ))}
              </NativeSelect>

              <NativeSelect
                aria-label="按模型筛选"
                value={model}
                onChange={(e) => setModel(e.target.value)}
              >
                <option value="">全部模型</option>
                {(data?.availableModels || []).map((m) => (
                  <option key={m.id} value={m.id}>
                    模型: {m.name}
                  </option>
                ))}
              </NativeSelect>

              <NativeSelect
                aria-label="按账号筛选"
                value={accountId}
                onChange={(e) => setAccountId(e.target.value)}
              >
                <option value="">全部路由账号</option>
                {(data?.availableAccounts || []).map((a) => (
                  <option key={a.id} value={a.id}>
                    账号: {a.name}
                  </option>
                ))}
              </NativeSelect>

              {hasFilters && (
                <Button
                  variant="ghost"
                  size="xs"
                  onClick={() => {
                    setApiKeyId("")
                    setModel("")
                    setAccountId("")
                  }}
                >
                  <RotateCcw className="h-3 w-3" />
                  重置筛选
                </Button>
              )}

              <Button
                variant="outline"
                size="xs"
                disabled={loading}
                onClick={() => loadAnalytics(true)}
              >
                <RefreshCw className={cn("h-3 w-3", loading && "animate-spin")} />
                刷新
              </Button>
            </div>
          </div>

          <div className="flex flex-wrap items-center justify-between gap-3 border-t pt-3 text-xs">
            <div className="flex flex-wrap items-center gap-2 text-muted-foreground tabular-nums">
              <span>SQLite 持久化记录：</span>
              <Badge variant="mono">{formatNum(data?.totalStoredRecords || 0)} 条</Badge>
              {data?.oldestRecordAt && (
                <span>最早记录：{formatTime(data.oldestRecordAt)}</span>
              )}
              <span>· 当前保留策略：{retentionDays} 天</span>
            </div>

            <div className="flex flex-wrap items-center gap-2">
              <span className="text-muted-foreground">数据保留天数</span>
              <Input
                type="number"
                min={1}
                max={3650}
                value={retentionInput}
                onChange={(e) => setRetentionInput(e.target.value)}
                className="h-7 w-22 text-xs tabular-nums"
                aria-label="数据保留天数"
              />
              <Button
                size="xs"
                variant="outline"
                disabled={savingRetention}
                onClick={saveRetention}
              >
                <Save className="h-3 w-3" />
                {savingRetention ? "保存中..." : "保存"}
              </Button>
              <Button
                size="xs"
                variant="destructiveOutline"
                onClick={clearAllUsage}
                title="清空全部用量记录"
              >
                <Trash2 className="h-3 w-3" />
                清空
              </Button>
            </div>
          </div>
        </CardContent>
      </Card>

      {/* 4 KPI Summary Cards */}
      <div className="grid grid-cols-1 gap-3 sm:grid-cols-2 xl:grid-cols-4">
        <Card className="p-4">
          <div className="text-[11px] font-medium text-muted-foreground">总 Token 消耗</div>
          <div className="mt-1.5 text-2xl font-semibold tracking-tight tabular-nums">
            {formatNum(summary?.totalTokens || 0)}
          </div>
          <div className="mt-1 text-[11px] text-muted-foreground tabular-nums">
            输入 {formatNum(summary?.promptTokens || 0)} · 输出{" "}
            {formatNum(summary?.completionTokens || 0)}
            {(summary?.weightedTokens || 0) > 0 && (
              <> · 折算 {formatNum(Math.round(summary?.weightedTokens || 0))}</>
            )}
          </div>
        </Card>

        <Card className="p-4">
          <div className="text-[11px] font-medium text-muted-foreground">API 调用与成功率</div>
          <div className="mt-1.5 text-2xl font-semibold tracking-tight tabular-nums">
            {formatNum(summary?.totalRequests || 0)}
          </div>
          <div className="mt-1 text-[11px] text-muted-foreground tabular-nums">
            成功 {formatNum(summary?.successRequests || 0)} · 失败{" "}
            {formatNum(summary?.failedRequests || 0)} · 成功率 {summary?.successRate ?? 0}%
          </div>
        </Card>

        <Card className="p-4">
          <div className="text-[11px] font-medium text-muted-foreground">推理与缓存 Token</div>
          <div className="mt-1.5 text-2xl font-semibold tracking-tight tabular-nums">
            {formatNum(summary?.reasoningTokens || 0)}
          </div>
          <div className="mt-1 text-[11px] text-muted-foreground tabular-nums">
            缓存命中 {formatNum(summary?.cachedTokens || 0)}（输入缓存率 {cacheHitRatio}%）
          </div>
        </Card>

        <Card className="p-4">
          <div className="text-[11px] font-medium text-muted-foreground">平均延迟与活跃维度</div>
          <div className="mt-1.5 text-2xl font-semibold tracking-tight tabular-nums">
            {formatNum(summary?.avgDurationMs || 0)} ms
          </div>
          <div className="mt-1 text-[11px] text-muted-foreground tabular-nums">
            首字 TTFT {(summary?.avgFirstTokenMs || 0) > 0 ? `${formatNum(summary?.avgFirstTokenMs || 0)}ms` : "-"} · Key {summary?.activeKeys || 0} · 模型 {summary?.activeModels || 0} · 账号{" "}
            {summary?.activeAccounts || 0}
          </div>
        </Card>
      </div>

      {/* Time-Series Trend Chart */}
      <Card>
        <CardHeader>
          <div>
            <div className="flex flex-wrap items-center gap-2">
              <CardTitle>Token 消耗与调用时间趋势</CardTitle>
              <Badge variant="mono">
                {data?.granularity === "hour" ? "按小时" : "按天"} · {data?.timezone || "Asia/Shanghai"}
              </Badge>
            </div>
            <CardDescription>
              展示选定时间窗口内的输入、输出、推理与缓存 Token 变化趋势，可切换查看请求成功/失败分布。
            </CardDescription>
          </div>
          <div className="flex items-center gap-1.5">
            <Button
              size="xs"
              variant={trendMode === "tokens" ? "default" : "outline"}
              onClick={() => setTrendMode("tokens")}
            >
              Token 消耗
            </Button>
            <Button
              size="xs"
              variant={trendMode === "requests" ? "default" : "outline"}
              onClick={() => setTrendMode("requests")}
            >
              调用次数
            </Button>
          </div>
        </CardHeader>
        <CardContent>
          <div className="h-72 w-full">
            {timeSeries.length === 0 ? (
              <div className="flex h-full items-center justify-center text-xs text-muted-foreground">
                暂无时间序列数据
              </div>
            ) : trendMode === "tokens" ? (
              <ResponsiveContainer width="100%" height="100%">
                <AreaChart data={timeSeries} margin={{ top: 8, right: 16, left: 0, bottom: 0 }}>
                  <defs>
                    <linearGradient id="gradPrompt" x1="0" y1="0" x2="0" y2="1">
                      <stop offset="5%" stopColor="#2563eb" stopOpacity={0.35} />
                      <stop offset="95%" stopColor="#2563eb" stopOpacity={0.02} />
                    </linearGradient>
                    <linearGradient id="gradComp" x1="0" y1="0" x2="0" y2="1">
                      <stop offset="5%" stopColor="#10b981" stopOpacity={0.35} />
                      <stop offset="95%" stopColor="#10b981" stopOpacity={0.02} />
                    </linearGradient>
                    <linearGradient id="gradReason" x1="0" y1="0" x2="0" y2="1">
                      <stop offset="5%" stopColor="#8b5cf6" stopOpacity={0.35} />
                      <stop offset="95%" stopColor="#8b5cf6" stopOpacity={0.02} />
                    </linearGradient>
                  </defs>
                  <CartesianGrid strokeDasharray="3 3" stroke="currentColor" opacity={0.12} />
                  <XAxis
                    dataKey="label"
                    tick={{ fontSize: 11 }}
                    stroke="currentColor"
                    opacity={0.6}
                    minTickGap={16}
                  />
                  <YAxis
                    tickFormatter={compactTokens}
                    tick={{ fontSize: 11 }}
                    stroke="currentColor"
                    opacity={0.6}
                    width={54}
                  />
                  <Tooltip
                    contentStyle={{
                      backgroundColor: "var(--card)",
                      borderColor: "var(--border)",
                      borderRadius: "8px",
                      fontSize: "12px",
                    }}
                    formatter={(value, name) => [formatNum(Number(value ?? 0)), String(name ?? "")]}
                    labelFormatter={(_, payload) =>
                      payload?.[0]?.payload?.bucket || ""
                    }
                  />
                  <Legend wrapperStyle={{ fontSize: "12px" }} />
                  <Area
                    type="monotone"
                    dataKey="promptTokens"
                    name="输入 Token"
                    stackId="1"
                    stroke="#2563eb"
                    fill="url(#gradPrompt)"
                    strokeWidth={1.8}
                  />
                  <Area
                    type="monotone"
                    dataKey="completionTokens"
                    name="输出 Token"
                    stackId="1"
                    stroke="#10b981"
                    fill="url(#gradComp)"
                    strokeWidth={1.8}
                  />
                  <Area
                    type="monotone"
                    dataKey="reasoningTokens"
                    name="推理 Token"
                    stroke="#8b5cf6"
                    fill="url(#gradReason)"
                    strokeWidth={1.6}
                  />
                </AreaChart>
              </ResponsiveContainer>
            ) : (
              <ResponsiveContainer width="100%" height="100%">
                <BarChart data={timeSeries} margin={{ top: 8, right: 16, left: 0, bottom: 0 }}>
                  <CartesianGrid strokeDasharray="3 3" stroke="currentColor" opacity={0.12} />
                  <XAxis
                    dataKey="label"
                    tick={{ fontSize: 11 }}
                    stroke="currentColor"
                    opacity={0.6}
                    minTickGap={16}
                  />
                  <YAxis
                    allowDecimals={false}
                    tick={{ fontSize: 11 }}
                    stroke="currentColor"
                    opacity={0.6}
                    width={46}
                  />
                  <Tooltip
                    contentStyle={{
                      backgroundColor: "var(--card)",
                      borderColor: "var(--border)",
                      borderRadius: "8px",
                      fontSize: "12px",
                    }}
                    formatter={(value, name) => [formatNum(Number(value ?? 0)), String(name ?? "")]}
                    labelFormatter={(_, payload) =>
                      payload?.[0]?.payload?.bucket || ""
                    }
                  />
                  <Legend wrapperStyle={{ fontSize: "12px" }} />
                  <Bar
                    dataKey="successRequests"
                    name="成功请求"
                    stackId="req"
                    fill="#10b981"
                    radius={[3, 3, 0, 0]}
                  />
                  <Bar
                    dataKey="failedRequests"
                    name="失败请求"
                    stackId="req"
                    fill="#ef4444"
                    radius={[3, 3, 0, 0]}
                  />
                </BarChart>
              </ResponsiveContainer>
            )}
          </div>
        </CardContent>
      </Card>

      {/* By API Key & By Model Charts */}
      <div className="grid grid-cols-1 gap-4 lg:grid-cols-2">
        {/* By API Key */}
        <Card>
          <CardHeader className="py-3.5">
            <div>
              <CardTitle>按 API Key 消耗分布</CardTitle>
              <CardDescription>点击下方 Key 行可联动筛选对应的时间曲线与模型分布。</CardDescription>
            </div>
            <Badge variant="mono">{byKey.length} 个 Key</Badge>
          </CardHeader>
          <CardContent className="space-y-4">
            {byKey.length === 0 ? (
              <div className="py-10 text-center text-xs text-muted-foreground">
                选定窗口内暂无 API Key 调用记录
              </div>
            ) : (
              <>
                <div className="h-56 w-full">
                  <ResponsiveContainer width="100%" height="100%">
                    <BarChart
                      data={byKey.slice(0, 8)}
                      layout="vertical"
                      margin={{ top: 4, right: 16, left: 4, bottom: 4 }}
                    >
                      <CartesianGrid strokeDasharray="3 3" stroke="currentColor" opacity={0.12} />
                      <XAxis
                        type="number"
                        tickFormatter={compactTokens}
                        tick={{ fontSize: 11 }}
                        stroke="currentColor"
                        opacity={0.6}
                      />
                      <YAxis
                        type="category"
                        dataKey="name"
                        width={96}
                        tick={{ fontSize: 11 }}
                        stroke="currentColor"
                        opacity={0.75}
                      />
                      <Tooltip
                        contentStyle={{
                          backgroundColor: "var(--card)",
                          borderColor: "var(--border)",
                          borderRadius: "8px",
                          fontSize: "12px",
                        }}
                        formatter={(value, name) => [formatNum(Number(value ?? 0)), String(name ?? "")]}
                      />
                      <Legend wrapperStyle={{ fontSize: "11px" }} />
                      <Bar dataKey="promptTokens" name="输入" stackId="k" fill="#2563eb" />
                      <Bar
                        dataKey="completionTokens"
                        name="输出"
                        stackId="k"
                        fill="#10b981"
                        radius={[0, 3, 3, 0]}
                      />
                    </BarChart>
                  </ResponsiveContainer>
                </div>

                <Table>
                  <TableHeader>
                    <TableRow>
                      <TableHead>API Key</TableHead>
                      <TableHead>请求数</TableHead>
                      <TableHead>输入 / 输出</TableHead>
                      <TableHead className="text-right">总 Token / 占比</TableHead>
                    </TableRow>
                  </TableHeader>
                  <TableBody>
                    {byKey.map((item) => (
                      <TableRow
                        key={item.id}
                        className={cn(
                          "cursor-pointer",
                          apiKeyId === item.id && "bg-accent/60"
                        )}
                        onClick={() => setApiKeyId(apiKeyId === item.id ? "" : item.id)}
                      >
                        <TableCell>
                          <div className="font-medium">{item.name}</div>
                          <div className="font-mono text-[10px] text-muted-foreground">
                            {item.id}
                          </div>
                        </TableCell>
                        <TableCell className="tabular-nums">{formatNum(item.requests)}</TableCell>
                        <TableCell className="tabular-nums text-muted-foreground">
                          {formatNum(item.promptTokens)} / {formatNum(item.completionTokens)}
                        </TableCell>
                        <TableCell className="text-right tabular-nums">
                          <div className="font-semibold">{formatNum(item.totalTokens)}</div>
                          <div className="text-[11px] text-muted-foreground">
                            {item.sharePercent.toFixed(1)}%
                          </div>
                        </TableCell>
                      </TableRow>
                    ))}
                  </TableBody>
                </Table>
              </>
            )}
          </CardContent>
        </Card>

        {/* By Model */}
        <Card>
          <CardHeader className="py-3.5">
            <div>
              <CardTitle>按模型消耗分布</CardTitle>
              <CardDescription>点击模型行可快速筛选该模型在各 Key 与时间段的表现。</CardDescription>
            </div>
            <Badge variant="mono">{byModel.length} 个模型</Badge>
          </CardHeader>
          <CardContent className="space-y-4">
            {byModel.length === 0 ? (
              <div className="py-10 text-center text-xs text-muted-foreground">
                选定窗口内暂无模型调用记录
              </div>
            ) : (
              <>
                <div className="h-56 w-full">
                  <ResponsiveContainer width="100%" height="100%">
                    <PieChart>
                      <Pie
                        data={byModel.slice(0, 8)}
                        dataKey="totalTokens"
                        nameKey="name"
                        cx="50%"
                        cy="50%"
                        innerRadius={48}
                        outerRadius={84}
                        paddingAngle={2}
                      >
                        {byModel.slice(0, 8).map((entry, index) => (
                          <Cell
                            key={entry.id}
                            fill={PALETTE[index % PALETTE.length]}
                            className="cursor-pointer"
                            onClick={() => setModel(model === entry.id ? "" : entry.id)}
                          />
                        ))}
                      </Pie>
                      <Tooltip
                        contentStyle={{
                          backgroundColor: "var(--card)",
                          borderColor: "var(--border)",
                          borderRadius: "8px",
                          fontSize: "12px",
                        }}
                        formatter={(value: unknown) => [formatNum(Number(value)) + " tokens", "总消耗"]}
                      />
                      <Legend wrapperStyle={{ fontSize: "11px" }} />
                    </PieChart>
                  </ResponsiveContainer>
                </div>

                <Table>
                  <TableHeader>
                    <TableRow>
                      <TableHead>模型</TableHead>
                      <TableHead>请求 / 均耗</TableHead>
                      <TableHead>推理 Token</TableHead>
                      <TableHead className="text-right">总 Token / 占比</TableHead>
                    </TableRow>
                  </TableHeader>
                  <TableBody>
                    {byModel.map((item, idx) => (
                      <TableRow
                        key={item.id}
                        className={cn(
                          "cursor-pointer",
                          model === item.id && "bg-accent/60"
                        )}
                        onClick={() => setModel(model === item.id ? "" : item.id)}
                      >
                        <TableCell>
                          <div className="flex items-center gap-2">
                            <span
                              className="h-2.5 w-2.5 shrink-0 rounded-full"
                              style={{ backgroundColor: PALETTE[idx % PALETTE.length] }}
                            />
                            <code className="font-medium">{item.name}</code>
                          </div>
                        </TableCell>
                        <TableCell className="tabular-nums">
                          {formatNum(item.requests)} · {formatNum(item.avgDurationMs)}ms
                        </TableCell>
                        <TableCell className="tabular-nums text-muted-foreground">
                          {formatNum(item.reasoningTokens)}
                        </TableCell>
                        <TableCell className="text-right tabular-nums">
                          <div className="font-semibold">{formatNum(item.totalTokens)}</div>
                          <div className="text-[11px] text-muted-foreground">
                            {item.sharePercent.toFixed(1)}%
                          </div>
                        </TableCell>
                      </TableRow>
                    ))}
                  </TableBody>
                </Table>
              </>
            )}
          </CardContent>
        </Card>
      </div>

      {/* Bottom Row: Key × Model Breakdown & Per-Account Pool Consumption */}
      <div className="grid grid-cols-1 gap-4 lg:grid-cols-2">
        <Card>
          <CardHeader className="py-3.5">
            <CardTitle>Key × 模型明细</CardTitle>
            <Badge variant="mono">{byKeyModel.length} 组合</Badge>
          </CardHeader>
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>API Key</TableHead>
                <TableHead>模型</TableHead>
                <TableHead>调用</TableHead>
                <TableHead className="text-right">总 Token (入 / 出 / 思)</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {byKeyModel.length === 0 ? (
                <TableRow>
                  <TableCell colSpan={4} className="py-6 text-center text-muted-foreground">
                    暂无交叉统计数据
                  </TableCell>
                </TableRow>
              ) : (
                byKeyModel.slice(0, 20).map((km, idx) => (
                  <TableRow key={`${km.apiKeyId}-${km.model}-${idx}`}>
                    <TableCell className="font-medium">{km.apiKeyName}</TableCell>
                    <TableCell>
                      <code className="rounded bg-muted px-1.5 py-0.5 text-[11px]">{km.model}</code>
                    </TableCell>
                    <TableCell className="tabular-nums">{formatNum(km.requests)}</TableCell>
                    <TableCell className="text-right tabular-nums">
                      <div className="font-semibold">{formatNum(km.totalTokens)}</div>
                      <div className="text-[11px] text-muted-foreground">
                        {formatNum(km.promptTokens)} / {formatNum(km.completionTokens)} /{" "}
                        {formatNum(km.reasoningTokens)}
                      </div>
                    </TableCell>
                  </TableRow>
                ))
              )}
            </TableBody>
          </Table>
        </Card>

        <Card>
          <CardHeader className="py-3.5">
            <CardTitle>按路由池账号消耗分布</CardTitle>
            <Badge variant="mono">{byAccount.length} 账号</Badge>
          </CardHeader>
          <CardContent className="space-y-3">
            {byAccount.length === 0 ? (
              <div className="py-6 text-center text-xs text-muted-foreground">
                暂无路由账号消耗记录
              </div>
            ) : (
              byAccount.map((acc) => (
                <div
                  key={acc.id}
                  onClick={() =>
                    acc.id !== "_unrouted" && setAccountId(accountId === acc.id ? "" : acc.id)
                  }
                  className={cn(
                    "rounded-md border p-3 text-xs space-y-2 transition-colors",
                    acc.id !== "_unrouted" && "cursor-pointer hover:bg-muted/30",
                    accountId === acc.id && "border-primary bg-accent/50"
                  )}
                >
                  <div className="flex items-center justify-between gap-2">
                    <span className="font-semibold">{acc.name}</span>
                    <span className="tabular-nums font-medium">
                      {formatNum(acc.totalTokens)} tokens ({acc.sharePercent.toFixed(1)}%)
                    </span>
                  </div>
                  <Progress value={acc.sharePercent} />
                  <div className="flex flex-wrap justify-between gap-2 text-[11px] text-muted-foreground tabular-nums">
                    <span>
                      请求 {formatNum(acc.requests)}（成功 {formatNum(acc.successRequests)}）
                    </span>
                    <span>
                      输入 {formatNum(acc.promptTokens)} · 输出 {formatNum(acc.completionTokens)} ·
                      均耗 {formatNum(acc.avgDurationMs)}ms
                    </span>
                  </div>
                </div>
              ))
            )}
          </CardContent>
        </Card>
      </div>

      {/* Detailed sub2api-style Usage Records Table */}
      <UsageLogsTable
        active={active}
        rangeDays={rangeDays}
        apiKeyId={apiKeyId}
        model={model}
        accountId={accountId}
        refreshTick={refreshTick}
      />
    </div>
  )
}
