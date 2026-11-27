import { useEffect, useMemo, useState } from "react"
import { toast } from "sonner"
import { Copy, RefreshCw } from "lucide-react"
import { apiRequest } from "@/lib/api"
import { copyClipboard, formatNum, formatTime, gatewayBaseURL } from "@/lib/utils"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card"
import { Input } from "@/components/ui/input"
import { Label, Switch } from "@/components/ui/primitives"
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table"
import { PoolBoard } from "./PoolBoard"
import { SessionBindingsTable } from "./SessionBindingsTable"
import { ModelsCatalogTable, isClientHiddenModel, isGatewayModelEnabled } from "./ModelsCatalogTable"
import { PlaygroundPanel } from "./PlaygroundPanel"
import type { GatewayStatusResponse } from "@/types/console"

interface GatewayTabProps {
  data: GatewayStatusResponse | null
  onRefresh: () => Promise<void>
  onRefreshAll: () => Promise<void>
}

const ROUTE_MODE_LABELS: Record<string, string> = {
  new_bind: "新绑定",
  bound: "缓存命中",
  rebound: "容错重绑",
  direct: "定向调试",
}

export function GatewayTab({ data, onRefresh, onRefreshAll }: GatewayTabProps) {
  const cfg = data?.config
  const stats = data?.stats
  const pool = data?.pool
  const pooled = pool?.pooled || []
  const candidates = pool?.candidates || []
  const bindings = data?.bindings || []
  const accounts = data?.accounts || []

  const [enabled, setEnabled] = useState(true)
  const [allowNoAuth, setAllowNoAuth] = useState(false)
  const [preflightMode, setPreflightMode] = useState(true)
  const [sciMasterPrompt, setSciMasterPrompt] = useState(true)
  const [defaultModel, setDefaultModel] = useState("dspro-high")
  const [corsOrigins, setCorsOrigins] = useState("*")
  const [retentionDays, setRetentionDays] = useState(180)
  const [savingSettings, setSavingSettings] = useState(false)

  const [pgAccountId, setPgAccountId] = useState("")
  const [pgModel, setPgModel] = useState("dsflash-high")

  useEffect(() => {
    if (!cfg) return
    setEnabled(!!cfg.enabled)
    setAllowNoAuth(!!cfg.allowNoAuth)
    setPreflightMode(!!cfg.preflightMode)
    setSciMasterPrompt(cfg.sciMasterPrompt !== false)
    setDefaultModel(cfg.defaultModel || "dspro-high")
    setCorsOrigins(cfg.corsOrigins || "*")
    setRetentionDays(cfg.analyticsRetentionDays || 180)
  }, [cfg])

  const sortedModels = useMemo(() => {
    const raw = data?.catalog?.data || []
    const tier = (m: (typeof raw)[number]) =>
      m.presentation?.visible ? 0 : m.hidden ? 1 : isClientHiddenModel(m) ? 2 : 0
    return [...raw].sort((a, b) => {
      const ta = tier(a)
      const tb = tier(b)
      if (ta !== tb) return ta - tb
      return (a.presentation?.sortOrder || 999) - (b.presentation?.sortOrder || 999)
    })
  }, [data?.catalog?.data])

  useEffect(() => {
    if (!sortedModels.length) return
    const current = sortedModels.find((m) => m.id === pgModel)
    if (!current || !isGatewayModelEnabled(current, cfg?.modelEnabled)) {
      const def =
        sortedModels.find(
          (m) => m.id === "dsflash-high" && isGatewayModelEnabled(m, cfg?.modelEnabled)
        ) ||
        sortedModels.find(
          (m) => m.presentation?.default && isGatewayModelEnabled(m, cfg?.modelEnabled)
        ) ||
        sortedModels.find((m) => isGatewayModelEnabled(m, cfg?.modelEnabled)) ||
        sortedModels[0]
      if (def) setPgModel(def.id)
    }
  }, [sortedModels, pgModel, cfg?.modelEnabled])

  const healthyPool = pooled.filter((x) => x.healthy).length
  const totalTok = (stats?.promptTokens || 0) + (stats?.completionTokens || 0)
  const enabledModels = sortedModels.filter((m) =>
    isGatewayModelEnabled(m, cfg?.modelEnabled)
  ).length
  const apiKeys = cfg?.apiKeys || []

  async function handleToggleModel(modelId: string, nextEnabled: boolean) {
    try {
      await apiRequest(`/api/gateway/models/${encodeURIComponent(modelId)}`, {
        method: "PATCH",
        body: JSON.stringify({ enabled: nextEnabled }),
      })
      toast.success(
        nextEnabled ? `已启用模型 ${modelId}（可通过 OpenAI 端点调用）` : `已隐藏并禁用模型 ${modelId}`
      )
      await onRefresh()
    } catch (err) {
      toast.error("更新模型状态失败: " + (err instanceof Error ? err.message : ""))
    }
  }

  function firstEnabledKey(): string {
    const found = apiKeys.find((k) => k.enabled && k.key && (!k.status || k.status === "active"))
    return found?.key || "sk-brm-YOUR_KEY"
  }

  async function doCopy(text: string, label: string) {
    const ok = await copyClipboard(text)
    if (ok) toast.success(`已复制 ${label}`)
    else toast.error("复制失败")
  }

  async function saveGatewaySettings() {
    setSavingSettings(true)
    try {
      await apiRequest("/api/gateway/settings", {
        method: "PUT",
        body: JSON.stringify({
          enabled,
          allowNoAuth,
          preflightMode,
          sciMasterPrompt,
          defaultModel: defaultModel.trim() || "dspro-high",
          corsOrigins: corsOrigins.trim() || "*",
          analyticsRetentionDays: Number(retentionDays) || 180,
        }),
      })
      toast.success("OpenAI 网关配置已保存")
      await onRefresh()
    } catch (err) {
      toast.error("保存失败: " + (err instanceof Error ? err.message : ""))
    } finally {
      setSavingSettings(false)
    }
  }

  const baseUrl = gatewayBaseURL()

  return (
    <div className="space-y-5">
      {/* 4 KPI cards */}
      <div className="grid grid-cols-1 gap-3 sm:grid-cols-2 xl:grid-cols-4">
        <Card className="p-4">
          <div className="text-[11px] font-medium text-muted-foreground">路由池与网关</div>
          <div className="mt-1.5 text-2xl font-semibold tracking-tight">
            {cfg?.enabled ? (healthyPool > 0 ? "运行中" : "池为空") : "已停用"}
          </div>
          <div className="mt-1 text-[11px] text-muted-foreground tabular-nums">
            池内健康 {healthyPool}/{pooled.length} · 可用光子{" "}
            {formatNum(pool?.totalEffectiveBalance || 0)}
          </div>
        </Card>

        <Card className="p-4">
          <div className="text-[11px] font-medium text-muted-foreground">累计 API 调用</div>
          <div className="mt-1.5 text-2xl font-semibold tracking-tight tabular-nums">
            {formatNum(stats?.totalRequests || 0)}
          </div>
          <div className="mt-1 text-[11px] text-muted-foreground tabular-nums">
            Chat {formatNum(stats?.chatRequests || 0)} · Resp{" "}
            {formatNum(stats?.responseRequests || 0)} · 流式 {formatNum(stats?.streamRequests || 0)}
          </div>
        </Card>

        <Card className="p-4">
          <div className="text-[11px] font-medium text-muted-foreground">Token 统计</div>
          <div className="mt-1.5 text-2xl font-semibold tracking-tight tabular-nums">
            {formatNum(totalTok)}
          </div>
          <div className="mt-1 text-[11px] text-muted-foreground tabular-nums">
            入 {formatNum(stats?.promptTokens || 0)} · 出 {formatNum(stats?.completionTokens || 0)} · 思{" "}
            {formatNum(stats?.reasoningTokens || 0)}
          </div>
        </Card>

        <Card className="p-4">
          <div className="text-[11px] font-medium text-muted-foreground">SQLite 7天会话绑定</div>
          <div className="mt-1.5 text-2xl font-semibold tracking-tight tabular-nums">
            {formatNum(bindings.length)}
          </div>
          <div className="mt-1 text-[11px] text-muted-foreground tabular-nums">
            已启用模型 {sortedModels.length ? `${enabledModels}/${sortedModels.length}` : "--"} · 链{" "}
            {formatNum(stats?.storedResponses || 0)}
          </div>
        </Card>
      </div>

      <PoolBoard
        candidates={candidates}
        pooled={pooled}
        totalEffectiveBalance={pool?.totalEffectiveBalance || 0}
        onChanged={onRefreshAll}
      />

      <SessionBindingsTable bindings={bindings} onRefresh={onRefresh} />

      {/* Gateway settings */}
      <div className="grid grid-cols-1">
        <Card>
          <CardHeader className="py-3.5">
            <div>
              <CardTitle>OpenAI 兼容接入配置</CardTitle>
              <CardDescription>
                保持 Bohrium 桌面客户端（SciX）原生传输层、每账号独立 <code>storeId</code> 与{" "}
                <code>x-scix-execution</code>，支持 Chat Completions 与 Responses API。
              </CardDescription>
            </div>
          </CardHeader>
          <CardContent className="space-y-4">
            <div className="space-y-1.5">
              <Label>API Base URL</Label>
              <div className="flex gap-2">
                <Input value={baseUrl} readOnly className="font-mono text-xs" />
                <Button variant="outline" size="sm" onClick={() => doCopy(baseUrl, "Base URL")}>
                  <Copy className="h-3.5 w-3.5" />
                  复制
                </Button>
              </div>
            </div>

            <div className="flex flex-wrap gap-1.5">
              {[
                "GET /v1/models",
                "GET /v1/user/balance",
                "POST /v1/chat/completions",
                "POST /v1/responses",
                "GET/DELETE /v1/responses/{id}",
              ].map((ep) => (
                <Badge key={ep} variant="mono">
                  {ep}
                </Badge>
              ))}
            </div>

            <div className="space-y-3 rounded-md border p-3">
              {[
                {
                  checked: enabled,
                  set: setEnabled,
                  title: "启用 OpenAI /v1/* 网关",
                  desc: "关闭后所有 /v1/* 请求返回 HTTP 503",
                },
                {
                  checked: allowNoAuth,
                  set: setAllowNoAuth,
                  title: "允许无 Key 访问（仅限可信内网）",
                  desc: "默认关闭，建议保持 Bearer API Key 鉴权",
                },
                {
                  checked: preflightMode,
                  set: setPreflightMode,
                  title: "新轮次前执行 Billing Preflight",
                  desc: "新对话前调用 /billing/preflight，402 时自动切换池内其他健康账号",
                },
                {
                  checked: sciMasterPrompt,
                  set: setSciMasterPrompt,
                  title: "注入 SciMaster 系统提示词",
                  desc: "主模型自动前置 Bohrium 原生 SciMaster 提示词（title/summary 不注入）",
                },
              ].map((item) => (
                <div key={item.title} className="flex items-start justify-between gap-3">
                  <div className="text-xs">
                    <div className="font-medium">{item.title}</div>
                    <div className="text-[11px] text-muted-foreground">{item.desc}</div>
                  </div>
                  <Switch checked={item.checked} onCheckedChange={item.set} />
                </div>
              ))}
            </div>

            <div className="grid grid-cols-1 gap-3 sm:grid-cols-3">
              <div className="space-y-1.5">
                <Label htmlFor="gw-default-model">默认模型</Label>
                <Input
                  id="gw-default-model"
                  value={defaultModel}
                  onChange={(e) => setDefaultModel(e.target.value)}
                  placeholder="dspro-high"
                />
              </div>
              <div className="space-y-1.5">
                <Label htmlFor="gw-cors">CORS Allow-Origin</Label>
                <Input
                  id="gw-cors"
                  value={corsOrigins}
                  onChange={(e) => setCorsOrigins(e.target.value)}
                  placeholder="*"
                />
              </div>
              <div className="space-y-1.5">
                <Label htmlFor="gw-retention">看板保留天数</Label>
                <Input
                  id="gw-retention"
                  type="number"
                  min={1}
                  max={3650}
                  value={retentionDays}
                  onChange={(e) => setRetentionDays(Number(e.target.value))}
                  placeholder="180"
                />
              </div>
            </div>

            <div className="flex flex-wrap gap-2">
              <Button size="sm" disabled={savingSettings} onClick={saveGatewaySettings}>
                保存网关设置
              </Button>
              <Button
                variant="outline"
                size="sm"
                onClick={() => {
                  const m = defaultModel.trim() || "dspro-high"
                  doCopy(
                    `curl ${baseUrl}/chat/completions \\\n  -H "Authorization: Bearer ${firstEnabledKey()}" \\\n  -H "Content-Type: application/json" \\\n  -d '{"model":"${m}","stream":true,"messages":[{"role":"user","content":"Hello!"}]}'`,
                    "Chat cURL"
                  )
                }}
              >
                Chat cURL
              </Button>
              <Button
                variant="outline"
                size="sm"
                onClick={() => {
                  const m = defaultModel.trim() || "dspro-high"
                  doCopy(
                    `curl ${baseUrl}/responses \\\n  -H "Authorization: Bearer ${firstEnabledKey()}" \\\n  -H "Content-Type: application/json" \\\n  -d '{"model":"${m}","stream":true,"input":"Hello!"}'`,
                    "Responses cURL"
                  )
                }}
              >
                Responses cURL
              </Button>
              <Button
                variant="outline"
                size="sm"
                onClick={() =>
                  doCopy(
                    `curl ${baseUrl}/user/balance \\\n  -H "Authorization: Bearer ${firstEnabledKey()}"`,
                    "Balance cURL"
                  )
                }
              >
                Balance cURL
              </Button>
            </div>
          </CardContent>
        </Card>

      </div>

      <ModelsCatalogTable
        models={sortedModels}
        catalogError={data?.catalogError}
        loggedIn={!!data?.bohriumLoggedIn}
        selectedAccountId={pgAccountId}
        modelEnabled={cfg?.modelEnabled}
        onRefreshCatalog={onRefresh}
        onToggleModel={handleToggleModel}
        onPickModel={(mid) => {
          setPgModel(mid)
          toast.info(`已在调试区选中模型 ${mid}`)
        }}
      />

      <PlaygroundPanel
        accounts={accounts}
        models={sortedModels}
        prompts={data?.playgroundPrompts || []}
        modelEnabled={cfg?.modelEnabled}
        selectedAccountId={pgAccountId}
        onSelectAccountId={setPgAccountId}
        selectedModel={pgModel}
        onSelectModel={setPgModel}
        onCompleted={() => void onRefresh()}
      />

      {/* Recent gateway calls */}
      <Card>
        <CardHeader className="py-3.5">
          <CardTitle>近期网关调用记录</CardTitle>
          <Button
            variant="ghost"
            size="xs"
            onClick={async () => {
              await onRefresh()
              toast.success("已刷新网关调用统计")
            }}
          >
            <RefreshCw className="h-3.5 w-3.5" />
            刷新
          </Button>
        </CardHeader>
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>时间</TableHead>
              <TableHead>API Key</TableHead>
              <TableHead>接口</TableHead>
              <TableHead>模型</TableHead>
              <TableHead>命中账号 / 路由</TableHead>
              <TableHead>状态</TableHead>
              <TableHead>耗时</TableHead>
              <TableHead>Token (入/出/思)</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {!stats?.recentRequests?.length ? (
              <TableRow>
                <TableCell colSpan={8} className="py-6 text-center text-muted-foreground">
                  暂无网关调用记录
                </TableCell>
              </TableRow>
            ) : (
              stats.recentRequests.map((r, idx) => {
                const ok = r.status >= 200 && r.status < 300 && !r.error
                const modeLabel = r.routingMode
                  ? ROUTE_MODE_LABELS[r.routingMode] || r.routingMode
                  : ""
                return (
                  <TableRow key={idx}>
                    <TableCell className="tabular-nums whitespace-nowrap">
                      {formatTime(r.timestamp)}
                    </TableCell>
                    <TableCell>
                      <Badge variant="mono">{r.apiKeyName || r.apiKeyId || "--"}</Badge>
                    </TableCell>
                    <TableCell>
                      <code className="rounded bg-muted px-1.5 py-0.5 text-[11px]">
                        {r.endpoint}
                      </code>
                      {r.stream && (
                        <Badge variant="mono" className="ml-1">
                          SSE
                        </Badge>
                      )}
                    </TableCell>
                    <TableCell>
                      <code className="text-[11px]">{r.model || "--"}</code>
                    </TableCell>
                    <TableCell>
                      <span>{r.accountLabel || r.accountId || "--"}</span>
                      {modeLabel && (
                        <Badge variant="mono" className="ml-1.5">
                          {modeLabel}
                        </Badge>
                      )}
                    </TableCell>
                    <TableCell>
                      {ok ? (
                        <Badge variant="ok">{r.status}</Badge>
                      ) : (
                        <Badge variant="err">{r.status || "ERR"}</Badge>
                      )}
                    </TableCell>
                    <TableCell className="tabular-nums">{formatNum(r.durationMs)} ms</TableCell>
                    <TableCell className="tabular-nums">
                      {formatNum(r.promptTokens)} / {formatNum(r.completionTokens)} /{" "}
                      {formatNum(r.reasoningTokens)}
                    </TableCell>
                  </TableRow>
                )
              })
            )}
          </TableBody>
        </Table>
      </Card>
    </div>
  )
}
