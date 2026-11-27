import { useMemo, useState } from "react"
import { toast } from "sonner"
import {
  Check,
  Copy,
  Eye,
  EyeOff,
  KeyRound,
  Pencil,
  Plus,
  RefreshCw,
  RotateCw,
  Search,
  Trash2,
  X,
} from "lucide-react"
import { apiRequest } from "@/lib/api"
import {
  copyClipboard,
  formatNum,
  formatTime,
  formatTTLRemaining,
  gatewayBaseURL,
  maskApiKey,
} from "@/lib/utils"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Card, CardDescription, CardHeader, CardTitle } from "@/components/ui/card"
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
import { Input } from "@/components/ui/input"
import { Label, Progress, Switch } from "@/components/ui/primitives"
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table"
import { isGatewayModelEnabled } from "./ModelsCatalogTable"
import type { GatewayApiKey, GatewayStatusResponse, SciXModelItem } from "@/types/console"

interface KeyManagementTabProps {
  data: GatewayStatusResponse | null
  onRefresh: () => Promise<void>
}

interface KeyFormState {
  name: string
  enabled: boolean
  defaultModel: string
  restrictModels: boolean
  allowedModels: string[]
  rateLimit: string
  concurrencyLimit: string
  expiresAt: string
  tokenQuota: string
  dailyTokenQuota: string
}

const EMPTY_FORM: KeyFormState = {
  name: "",
  enabled: true,
  defaultModel: "",
  restrictModels: false,
  allowedModels: [],
  rateLimit: "0",
  concurrencyLimit: "0",
  expiresAt: "",
  tokenQuota: "0",
  dailyTokenQuota: "0",
}

const selectClass =
  "h-9 w-full rounded-md border border-input bg-card px-3 text-xs text-foreground outline-none transition-colors focus:ring-2 focus:ring-ring disabled:cursor-not-allowed disabled:opacity-50"

function toLocalDateTimeInput(iso?: string): string {
  if (!iso) return ""
  const date = new Date(iso)
  if (Number.isNaN(date.getTime())) return ""
  const local = new Date(date.getTime() - date.getTimezoneOffset() * 60_000)
  return local.toISOString().slice(0, 16)
}

function formFromKey(key?: GatewayApiKey): KeyFormState {
  if (!key) return { ...EMPTY_FORM, allowedModels: [] }
  return {
    name: key.name || "",
    enabled: key.enabled,
    defaultModel: key.defaultModel || "",
    restrictModels: !!key.restrictModels,
    allowedModels: [...(key.allowedModels || [])],
    rateLimit: String(key.rateLimit || 0),
    concurrencyLimit: String(key.concurrencyLimit || 0),
    expiresAt: toLocalDateTimeInput(key.expiresAt),
    tokenQuota: String(key.tokenQuota || 0),
    dailyTokenQuota: String(key.dailyTokenQuota || 0),
  }
}

function keyStatus(key: GatewayApiKey): { label: string; variant: "ok" | "muted" | "warn" | "err" } {
  const status = key.status || (key.enabled ? "active" : "disabled")
  switch (status) {
    case "active":
      return { label: "可用", variant: "ok" }
    case "expired":
      return { label: "已过期", variant: "err" }
    case "quota_exhausted":
      return { label: "额度耗尽", variant: "warn" }
    default:
      return { label: "已停用", variant: "muted" }
  }
}

function modelName(model: SciXModelItem): string {
  return model.presentation?.name || model.name || model.id
}

export function KeyManagementTab({ data, onRefresh }: KeyManagementTabProps) {
  const keys = data?.config?.apiKeys || []
  const models = useMemo(() => data?.catalog?.data || [], [data?.catalog?.data])
  const sortedModels = useMemo(
    () =>
      [...models].sort(
        (a, b) =>
          (a.presentation?.sortOrder || 999) - (b.presentation?.sortOrder || 999) ||
          a.id.localeCompare(b.id)
      ),
    [models]
  )

  const [refreshing, setRefreshing] = useState(false)
  const [query, setQuery] = useState("")
  const [statusFilter, setStatusFilter] = useState("all")
  const [revealedIDs, setRevealedIDs] = useState<Set<string>>(() => new Set())
  const [noticeKey, setNoticeKey] = useState<GatewayApiKey | null>(null)
  const [editorOpen, setEditorOpen] = useState(false)
  const [editingKey, setEditingKey] = useState<GatewayApiKey | null>(null)
  const [form, setForm] = useState<KeyFormState>(EMPTY_FORM)
  const [modelQuery, setModelQuery] = useState("")
  const [saving, setSaving] = useState(false)
  const [busyID, setBusyID] = useState("")

  const totals = useMemo(
    () => ({
      active: keys.filter((key) => (key.status || (key.enabled ? "active" : "disabled")) === "active").length,
      requests: keys.reduce((sum, key) => sum + (key.requestCount || 0), 0),
      tokens: keys.reduce((sum, key) => sum + (key.totalTokens || 0), 0),
    }),
    [keys]
  )

  const filteredKeys = useMemo(() => {
    const normalized = query.trim().toLowerCase()
    return keys.filter((key) => {
      const status = key.status || (key.enabled ? "active" : "disabled")
      if (statusFilter !== "all" && status !== statusFilter) return false
      if (!normalized) return true
      return [key.name, key.id, key.key, key.defaultModel, ...(key.allowedModels || [])]
        .filter(Boolean)
        .some((value) => String(value).toLowerCase().includes(normalized))
    })
  }, [keys, query, statusFilter])

  const filteredModels = useMemo(() => {
    const normalized = modelQuery.trim().toLowerCase()
    if (!normalized) return sortedModels
    return sortedModels.filter((model) =>
      `${model.id} ${modelName(model)}`.toLowerCase().includes(normalized)
    )
  }, [sortedModels, modelQuery])

  const globallyEnabledModels = sortedModels.filter((model) =>
    isGatewayModelEnabled(model, data?.config?.modelEnabled)
  )

  function patchForm<K extends keyof KeyFormState>(field: K, value: KeyFormState[K]) {
    setForm((current) => ({ ...current, [field]: value }))
  }

  function openEditor(key?: GatewayApiKey) {
    setEditingKey(key || null)
    setForm(formFromKey(key))
    setModelQuery("")
    setEditorOpen(true)
  }

  async function doCopy(text: string, label: string) {
    if (await copyClipboard(text)) toast.success(`已复制${label}`)
    else toast.error("复制失败，请手动复制")
  }

  async function refresh() {
    setRefreshing(true)
    try {
      await onRefresh()
      toast.success("Key 与用量数据已刷新")
    } catch (err) {
      toast.error("刷新失败: " + (err instanceof Error ? err.message : ""))
    } finally {
      setRefreshing(false)
    }
  }

  function toggleRevealed(id: string) {
    setRevealedIDs((current) => {
      const next = new Set(current)
      if (next.has(id)) next.delete(id)
      else next.add(id)
      return next
    })
  }

  function parseNonNegative(value: string, label: string): number {
    const parsed = Number(value)
    if (!Number.isSafeInteger(parsed) || parsed < 0) {
      throw new Error(`${label}必须是非负整数，0 表示不限`)
    }
    return parsed
  }

  async function saveKey() {
    try {
      const name = form.name.trim()
      if (!name) throw new Error("请输入 Key 备注名")
      if (form.restrictModels && form.allowedModels.length === 0) {
        throw new Error("自定义模型范围至少选择一个模型")
      }
      if (
        form.restrictModels &&
        form.defaultModel &&
        !form.allowedModels.includes(form.defaultModel)
      ) {
        throw new Error("默认模型必须包含在当前模型白名单中")
      }
      let expiresAt = ""
      if (form.expiresAt) {
        const date = new Date(form.expiresAt)
        if (Number.isNaN(date.getTime())) throw new Error("请选择有效的过期时间")
        expiresAt = date.toISOString()
      }
      const payload = {
        name,
        enabled: form.enabled,
        defaultModel: form.defaultModel,
        restrictModels: form.restrictModels,
        allowedModels: form.allowedModels,
        rateLimit: parseNonNegative(form.rateLimit, "每分钟请求数"),
        concurrencyLimit: parseNonNegative(form.concurrencyLimit, "并发上限"),
        expiresAt,
        tokenQuota: parseNonNegative(form.tokenQuota, "总 Token 额度"),
        dailyTokenQuota: parseNonNegative(form.dailyTokenQuota, "每日 Token 额度"),
      }
      setSaving(true)
      const response = await apiRequest<{ key: GatewayApiKey }>(
        editingKey ? `/api/gateway/keys/${encodeURIComponent(editingKey.id)}` : "/api/gateway/keys",
        { method: editingKey ? "PATCH" : "POST", body: JSON.stringify(payload) }
      )
      await onRefresh()
      setEditorOpen(false)
      if (!editingKey && response.key) {
        setNoticeKey(response.key)
        setRevealedIDs((current) => new Set(current).add(response.key.id))
      }
      toast.success(editingKey ? "Key 策略已保存并立即生效" : "API Key 已创建，请妥善保存")
    } catch (err) {
      toast.error(err instanceof Error ? err.message : "保存失败")
    } finally {
      setSaving(false)
    }
  }

  async function toggleKey(key: GatewayApiKey) {
    setBusyID(key.id)
    try {
      await apiRequest(`/api/gateway/keys/${encodeURIComponent(key.id)}`, {
        method: "PATCH",
        body: JSON.stringify({ enabled: !key.enabled }),
      })
      await onRefresh()
      toast.success(key.enabled ? "Key 已停用" : "Key 已启用")
    } catch (err) {
      toast.error(err instanceof Error ? err.message : "状态更新失败")
    } finally {
      setBusyID("")
    }
  }

  async function regenerateKey(key: GatewayApiKey) {
    if (!window.confirm(`确定轮换「${key.name || key.id}」吗？旧 Key 将立即失效。`)) return
    setBusyID(key.id)
    try {
      const response = await apiRequest<{ key: GatewayApiKey }>(
        `/api/gateway/keys/${encodeURIComponent(key.id)}/regenerate`,
        { method: "POST" }
      )
      await onRefresh()
      if (response.key) {
        setNoticeKey(response.key)
        setRevealedIDs((current) => new Set(current).add(response.key.id))
      }
      toast.success("Key 已轮换，请更新客户端配置")
    } catch (err) {
      toast.error(err instanceof Error ? err.message : "轮换失败")
    } finally {
      setBusyID("")
    }
  }

  async function deleteKey(key: GatewayApiKey) {
    if (!window.confirm(`确定删除「${key.name || key.id}」吗？使用该 Key 的客户端将立即失去访问权限。`)) return
    setBusyID(key.id)
    try {
      await apiRequest(`/api/gateway/keys/${encodeURIComponent(key.id)}`, { method: "DELETE" })
      await onRefresh()
      if (noticeKey?.id === key.id) setNoticeKey(null)
      toast.success("Key 已删除；历史用量记录保留用于审计")
    } catch (err) {
      toast.error(err instanceof Error ? err.message : "删除失败")
    } finally {
      setBusyID("")
    }
  }

  function setExpirationDays(days: number) {
    const date = new Date(Date.now() + days * 24 * 60 * 60 * 1000)
    patchForm("expiresAt", toLocalDateTimeInput(date.toISOString()))
  }

  return (
    <div className="space-y-5">
      <div className="flex flex-wrap items-end justify-between gap-3">
        <div>
          <div className="flex items-center gap-2">
            <KeyRound className="h-4 w-4" />
            <h1 className="text-base font-semibold tracking-tight">API Key 管理</h1>
          </div>
          <p className="mt-1 text-xs leading-relaxed text-muted-foreground">
            为每个客户端独立配置模型范围、滑动 60 秒速率、在途并发、有效期与 Token 额度。
          </p>
        </div>
        <div className="flex gap-2">
          <Button variant="outline" size="sm" disabled={refreshing} onClick={refresh}>
            <RefreshCw className={`h-3.5 w-3.5 ${refreshing ? "animate-spin" : ""}`} />
            刷新
          </Button>
          <Button size="sm" onClick={() => openEditor()}>
            <Plus className="h-3.5 w-3.5" />
            新建 Key
          </Button>
        </div>
      </div>

      <div className="grid grid-cols-2 gap-3 xl:grid-cols-4">
        {[
          { label: "Key 总数", value: formatNum(keys.length), note: `${keys.filter((k) => k.enabled).length} 个已启用` },
          { label: "当前可用", value: formatNum(totals.active), note: "排除停用、过期与额度耗尽" },
          { label: "累计 API 请求", value: formatNum(totals.requests), note: "按 Key 独立汇总" },
          { label: "累计 Token", value: formatNum(totals.tokens), note: "输入 + 输出，永久计数" },
        ].map((item) => (
          <Card key={item.label} className="p-4">
            <div className="text-[11px] font-medium text-muted-foreground">{item.label}</div>
            <div className="mt-1.5 text-2xl font-semibold tracking-tight tabular-nums">{item.value}</div>
            <div className="mt-1 text-[11px] text-muted-foreground">{item.note}</div>
          </Card>
        ))}
      </div>

      <Card className="p-3.5">
        <div className="flex flex-col gap-2 sm:flex-row sm:items-center">
          <div className="shrink-0 text-[11px] font-medium text-muted-foreground">OPENAI BASE URL</div>
          <code className="min-w-0 flex-1 truncate rounded bg-muted px-2.5 py-2 text-[11px]">{gatewayBaseURL()}</code>
          <Button variant="outline" size="xs" onClick={() => doCopy(gatewayBaseURL(), " Base URL")}>
            <Copy className="h-3 w-3" />
            复制接入地址
          </Button>
        </div>
      </Card>

      {noticeKey && (
        <Card className="border-foreground/30 bg-muted/20 p-4">
          <div className="flex flex-col gap-3 sm:flex-row sm:items-center">
            <div className="min-w-0 flex-1">
              <div className="text-xs font-semibold">请保存新的 API Key</div>
              <div className="mt-1 text-[11px] text-muted-foreground">{noticeKey.name} · 旧客户端需要使用此密钥重新鉴权</div>
              <code className="mt-2 block break-all rounded border bg-card px-2.5 py-2 text-[11px]">{noticeKey.key}</code>
            </div>
            <div className="flex shrink-0 gap-2">
              <Button size="xs" onClick={() => doCopy(noticeKey.key, " API Key")}>
                <Copy className="h-3 w-3" />复制 Key
              </Button>
              <Button variant="ghost" size="icon" aria-label="关闭密钥提示" onClick={() => setNoticeKey(null)}>
                <X className="h-4 w-4" />
              </Button>
            </div>
          </div>
        </Card>
      )}

      <Card>
        <CardHeader className="gap-3 py-3.5">
          <div>
            <CardTitle>密钥与策略</CardTitle>
            <CardDescription>最终可用模型 = 全局启用模型 ∩ Key 白名单；所有策略在保存后立即生效。</CardDescription>
          </div>
          <div className="flex w-full flex-col gap-2 sm:w-auto sm:flex-row">
            <div className="relative">
              <Search className="pointer-events-none absolute left-2.5 top-2.5 h-3.5 w-3.5 text-muted-foreground" />
              <Input
                value={query}
                onChange={(event) => setQuery(event.target.value)}
                placeholder="搜索备注、Key 或模型"
                className="w-full pl-8 sm:w-60"
              />
            </div>
            <select aria-label="筛选 Key 状态" className={selectClass} value={statusFilter} onChange={(event) => setStatusFilter(event.target.value)}>
              <option value="all">全部状态</option>
              <option value="active">可用</option>
              <option value="disabled">已停用</option>
              <option value="expired">已过期</option>
              <option value="quota_exhausted">额度耗尽</option>
            </select>
          </div>
        </CardHeader>

        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>备注 / API Key</TableHead>
              <TableHead>模型范围</TableHead>
              <TableHead>速率 / 并发</TableHead>
              <TableHead>额度使用</TableHead>
              <TableHead>有效期</TableHead>
              <TableHead>状态</TableHead>
              <TableHead>最近使用</TableHead>
              <TableHead className="text-right">操作</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {filteredKeys.length === 0 ? (
              <TableRow>
                <TableCell colSpan={8} className="py-12 text-center">
                  <KeyRound className="mx-auto mb-3 h-5 w-5 text-muted-foreground" />
                  <div className="font-medium">{!data ? "正在加载 Key 与策略…" : keys.length === 0 ? "尚未创建 API Key" : "没有匹配的 Key"}</div>
                  <div className="mt-1 text-[11px] text-muted-foreground">
                    {!data ? "正在读取配置与 SQLite 用量计数。" : keys.length === 0 ? "创建一个独立密钥，并为其配置访问策略。" : "请调整搜索词或状态筛选。"}
                  </div>
                  {data && keys.length === 0 && <Button size="sm" className="mt-4" onClick={() => openEditor()}><Plus className="h-3.5 w-3.5" />新建 Key</Button>}
                </TableCell>
              </TableRow>
            ) : (
              filteredKeys.map((key) => {
                const revealed = revealedIDs.has(key.id)
                const status = keyStatus(key)
                const totalPercent = key.tokenQuota > 0 ? Math.min(100, (key.totalTokens / key.tokenQuota) * 100) : 0
                const dailyPercent = key.dailyTokenQuota > 0 ? Math.min(100, (key.todayTokens / key.dailyTokenQuota) * 100) : 0
                return (
                  <TableRow key={key.id} className={!key.enabled ? "opacity-65" : undefined}>
                    <TableCell className="min-w-64">
                      <div className="font-medium">{key.name || key.id}</div>
                      <div className="mt-1 flex items-center gap-1">
                        <code className={`text-[11px] ${revealed ? "break-all" : ""}`}>{revealed ? key.key : maskApiKey(key.key)}</code>
                        <Button variant="ghost" size="icon" className="h-6 w-6" title={revealed ? "隐藏 Key" : "显示 Key"} onClick={() => toggleRevealed(key.id)}>
                          {revealed ? <EyeOff className="h-3 w-3" /> : <Eye className="h-3 w-3" />}
                        </Button>
                        <Button variant="ghost" size="icon" className="h-6 w-6" title="复制 Key" onClick={() => doCopy(key.key, " API Key")}>
                          <Copy className="h-3 w-3" />
                        </Button>
                      </div>
                      <div className="mt-1 font-mono text-[10px] text-muted-foreground">{key.id}</div>
                    </TableCell>
                    <TableCell className="min-w-36">
                      {key.restrictModels ? (
                        <>
                          <Badge variant="default">{key.allowedModels?.length || 0} 个指定模型</Badge>
                          <div className="mt-1 max-w-52 truncate font-mono text-[10px] text-muted-foreground" title={(key.allowedModels || []).join(", ")}>
                            {(key.allowedModels || []).join(" · ") || "未选择"}
                          </div>
                        </>
                      ) : (
                        <Badge variant="muted">全部全局启用模型</Badge>
                      )}
                      <div className="mt-1 text-[10px] text-muted-foreground">默认：{key.defaultModel || "跟随网关"}</div>
                    </TableCell>
                    <TableCell className="whitespace-nowrap tabular-nums">
                      <div>{key.rateLimit > 0 ? `${formatNum(key.rateLimit)} 次/分` : "不限速"}</div>
                      <div className="mt-1 text-[11px] text-muted-foreground">{key.concurrencyLimit > 0 ? `并发 ${formatNum(key.concurrencyLimit)}` : "并发不限"}</div>
                    </TableCell>
                    <TableCell className="min-w-48 tabular-nums">
                      <div className="flex items-center justify-between gap-2 text-[11px]">
                        <span className="text-muted-foreground">累计</span>
                        <span>{formatNum(key.totalTokens)} / {key.tokenQuota > 0 ? formatNum(key.tokenQuota) : "不限"}</span>
                      </div>
                      {key.tokenQuota > 0 && <Progress className="mt-1.5" value={totalPercent} />}
                      <div className="mt-2 flex items-center justify-between gap-2 text-[11px]">
                        <span className="text-muted-foreground">今日</span>
                        <span>{formatNum(key.todayTokens)} / {key.dailyTokenQuota > 0 ? formatNum(key.dailyTokenQuota) : "不限"}</span>
                      </div>
                      {key.dailyTokenQuota > 0 && <Progress className="mt-1.5" value={dailyPercent} />}
                    </TableCell>
                    <TableCell className="whitespace-nowrap">
                      {key.expiresAt ? (
                        <>
                          <div className="text-[11px]">{formatTime(key.expiresAt)}</div>
                          <div className={`mt-1 text-[10px] ${status.label === "已过期" ? "text-destructive" : "text-muted-foreground"}`}>{formatTTLRemaining(key.expiresAt)}</div>
                        </>
                      ) : <span className="text-muted-foreground">永不过期</span>}
                    </TableCell>
                    <TableCell><Badge variant={status.variant}>{status.label}</Badge></TableCell>
                    <TableCell className="whitespace-nowrap tabular-nums">
                      <div className="text-[11px]">{formatTime(key.lastUsedAt)}</div>
                      <div className="mt-1 text-[10px] text-muted-foreground">{formatNum(key.requestCount)} 次请求</div>
                    </TableCell>
                    <TableCell className="text-right">
                      <div className="flex min-w-max justify-end gap-0.5">
                        <Button variant="ghost" size="xs" title="编辑策略" onClick={() => openEditor(key)}><Pencil className="h-3 w-3" />编辑</Button>
                        <Button variant="ghost" size="xs" disabled={busyID === key.id} onClick={() => toggleKey(key)}>{key.enabled ? "停用" : "启用"}</Button>
                        <Button variant="ghost" size="icon" className="h-7 w-7" title="轮换 Key" disabled={busyID === key.id} onClick={() => regenerateKey(key)}><RotateCw className="h-3 w-3" /></Button>
                        <Button variant="ghost" size="icon" className="h-7 w-7 text-destructive hover:text-destructive" title="删除 Key" disabled={busyID === key.id} onClick={() => deleteKey(key)}><Trash2 className="h-3 w-3" /></Button>
                      </div>
                    </TableCell>
                  </TableRow>
                )
              })
            )}
          </TableBody>
        </Table>
      </Card>

      <Dialog open={editorOpen} onOpenChange={setEditorOpen}>
        <DialogContent className="w-[min(96vw,820px)] p-0">
          <DialogHeader className="border-b px-5 py-4">
            <DialogTitle>{editingKey ? "编辑 Key 策略" : "创建 API Key"}</DialogTitle>
            <DialogDescription>
              配置独立于其他 Key 保存。数值为 0 表示不限制；有效期留空表示永不过期。
            </DialogDescription>
          </DialogHeader>

          <div className="space-y-6 px-5 py-5">
            <section className="space-y-3">
              <div className="flex items-center gap-2">
                <span className="font-mono text-[10px] text-muted-foreground">01</span>
                <h3 className="text-xs font-semibold">基本信息</h3>
              </div>
              <div className="grid gap-3 sm:grid-cols-2">
                <div className="space-y-1.5">
                  <Label htmlFor="key-name">备注名</Label>
                  <Input id="key-name" autoFocus value={form.name} onChange={(event) => patchForm("name", event.target.value)} placeholder="例如 Codex / Cursor / 测试环境" maxLength={80} />
                </div>
                <div className="space-y-1.5">
                  <Label htmlFor="key-default-model">默认模型（请求未指定时使用）</Label>
                  <select id="key-default-model" className={selectClass} value={form.defaultModel} onChange={(event) => patchForm("defaultModel", event.target.value)}>
                    <option value="">跟随网关默认模型</option>
                    {globallyEnabledModels
                      .filter((model) => !form.restrictModels || form.allowedModels.includes(model.id))
                      .map((model) => <option key={model.id} value={model.id}>{modelName(model)} · {model.id}</option>)}
                    {form.defaultModel && !globallyEnabledModels.some((model) => model.id === form.defaultModel && (!form.restrictModels || form.allowedModels.includes(model.id))) && <option value={form.defaultModel}>{form.defaultModel}（当前不可用）</option>}
                  </select>
                </div>
              </div>
              <div className="flex items-center justify-between rounded-md border px-3 py-2.5">
                <div>
                  <div className="text-xs font-medium">启用此 Key</div>
                  <div className="mt-0.5 text-[11px] text-muted-foreground">停用后立即拒绝新的 API 请求，策略与用量保留。</div>
                </div>
                <Switch checked={form.enabled} onCheckedChange={(checked) => patchForm("enabled", checked)} />
              </div>
            </section>

            <section className="space-y-3 border-t pt-5">
              <div className="flex flex-wrap items-center justify-between gap-2">
                <div className="flex items-center gap-2">
                  <span className="font-mono text-[10px] text-muted-foreground">02</span>
                  <h3 className="text-xs font-semibold">模型访问</h3>
                </div>
                <select aria-label="模型访问模式" className={`${selectClass} w-auto`} value={form.restrictModels ? "custom" : "all"} onChange={(event) => patchForm("restrictModels", event.target.value === "custom")}>
                  <option value="all">全部全局启用模型</option>
                  <option value="custom">自定义模型白名单</option>
                </select>
              </div>
              <p className="text-[11px] leading-relaxed text-muted-foreground">白名单只能收紧全局模型开关，不能启用已在路由池中禁用的模型。</p>
              {form.restrictModels && (
                <div className="rounded-md border">
                  <div className="flex flex-col gap-2 border-b bg-muted/30 p-3 sm:flex-row sm:items-center">
                    <div className="relative flex-1">
                      <Search className="pointer-events-none absolute left-2.5 top-2.5 h-3.5 w-3.5 text-muted-foreground" />
                      <Input value={modelQuery} onChange={(event) => setModelQuery(event.target.value)} placeholder="搜索模型名称或 ID" className="pl-8" />
                    </div>
                    <div className="flex gap-1.5">
                      <Button type="button" variant="outline" size="xs" onClick={() => patchForm("allowedModels", [...new Set([...form.allowedModels, ...globallyEnabledModels.map((model) => model.id)])])}><Check className="h-3 w-3" />全选可用</Button>
                      <Button type="button" variant="ghost" size="xs" onClick={() => patchForm("allowedModels", [])}>清空</Button>
                    </div>
                  </div>
                  <div className="grid max-h-60 gap-1 overflow-y-auto p-2 sm:grid-cols-2">
                    {filteredModels.length === 0 ? (
                      <div className="col-span-full py-6 text-center text-[11px] text-muted-foreground">{models.length === 0 ? "暂无模型目录，请先在路由池页同步模型。" : "没有匹配的模型"}</div>
                    ) : filteredModels.map((model) => {
                      const globalEnabled = isGatewayModelEnabled(model, data?.config?.modelEnabled)
                      const checked = form.allowedModels.includes(model.id)
                      return (
                        <label key={model.id} className={`flex items-start gap-2.5 rounded px-2.5 py-2 text-xs ${globalEnabled || checked ? "cursor-pointer hover:bg-accent" : "opacity-50"}`}>
                          <input
                            type="checkbox"
                            className="mt-0.5 h-3.5 w-3.5 accent-foreground"
                            checked={checked}
                            disabled={!globalEnabled && !checked}
                            onChange={(event) => patchForm("allowedModels", event.target.checked ? [...form.allowedModels, model.id] : form.allowedModels.filter((id) => id !== model.id))}
                          />
                          <span className="min-w-0">
                            <span className="block truncate font-medium">{modelName(model)}</span>
                            <code className="mt-0.5 block truncate text-[10px] text-muted-foreground">{model.id}{!globalEnabled ? " · 全局已禁用" : ""}</code>
                          </span>
                        </label>
                      )
                    })}
                  </div>
                  <div className="border-t px-3 py-2 text-[11px] text-muted-foreground">已选择 <span className="font-medium text-foreground tabular-nums">{form.allowedModels.length}</span> 个模型</div>
                </div>
              )}
            </section>

            <section className="space-y-4 border-t pt-5">
              <div className="flex items-center gap-2">
                <span className="font-mono text-[10px] text-muted-foreground">03</span>
                <h3 className="text-xs font-semibold">使用策略</h3>
              </div>
              <div className="grid gap-3 sm:grid-cols-2">
                <div className="space-y-1.5">
                  <Label htmlFor="key-rate">滑动 60 秒请求上限</Label>
                  <Input id="key-rate" type="number" min={0} step={1} value={form.rateLimit} onChange={(event) => patchForm("rateLimit", event.target.value)} />
                  <p className="text-[10px] text-muted-foreground">单位：次/分钟；0 = 不限。</p>
                </div>
                <div className="space-y-1.5">
                  <Label htmlFor="key-concurrency">最大在途并发</Label>
                  <Input id="key-concurrency" type="number" min={0} step={1} value={form.concurrencyLimit} onChange={(event) => patchForm("concurrencyLimit", event.target.value)} />
                  <p className="text-[10px] text-muted-foreground">覆盖整个流式请求生命周期；0 = 不限。</p>
                </div>
                <div className="space-y-1.5">
                  <Label htmlFor="key-total-quota">累计 Token 额度</Label>
                  <Input id="key-total-quota" type="number" min={0} step={1} value={form.tokenQuota} onChange={(event) => patchForm("tokenQuota", event.target.value)} />
                  <p className="text-[10px] text-muted-foreground">输入 + 输出 Token；不受日志保留清理影响。</p>
                </div>
                <div className="space-y-1.5">
                  <Label htmlFor="key-daily-quota">每日 Token 额度</Label>
                  <Input id="key-daily-quota" type="number" min={0} step={1} value={form.dailyTokenQuota} onChange={(event) => patchForm("dailyTokenQuota", event.target.value)} />
                  <p className="text-[10px] text-muted-foreground">按调度器时区的自然日重置；0 = 不限。</p>
                </div>
              </div>
              <div className="space-y-1.5">
                <Label htmlFor="key-expires">过期时间</Label>
                <div className="flex flex-col gap-2 sm:flex-row">
                  <Input id="key-expires" type="datetime-local" value={form.expiresAt} onChange={(event) => patchForm("expiresAt", event.target.value)} />
                  <div className="flex shrink-0 gap-1.5">
                    {[7, 30, 90].map((days) => <Button key={days} type="button" variant="outline" size="xs" className="h-9" onClick={() => setExpirationDays(days)}>{days} 天</Button>)}
                    <Button type="button" variant="ghost" size="xs" className="h-9" onClick={() => patchForm("expiresAt", "")}>永不</Button>
                  </div>
                </div>
              </div>
              <div className="rounded-md border bg-muted/30 px-3 py-2.5 text-[11px] leading-relaxed text-muted-foreground">
                额度按已完成请求落账；多个在途请求同时完成时，最后一次结算可能使实际用量略高于上限。
              </div>
            </section>
          </div>

          <div className="flex items-center justify-between gap-3 border-t bg-muted/20 px-5 py-4">
            <div className="text-[11px] text-muted-foreground">{editingKey ? `Key ID: ${editingKey.id}` : "创建后自动生成高强度 sk-brm- 密钥"}</div>
            <div className="flex shrink-0 gap-2">
              <Button variant="outline" size="sm" disabled={saving} onClick={() => setEditorOpen(false)}>取消</Button>
              <Button size="sm" disabled={saving} onClick={saveKey}>{saving ? "保存中…" : editingKey ? "保存策略" : "创建 Key"}</Button>
            </div>
          </div>
        </DialogContent>
      </Dialog>
    </div>
  )
}
