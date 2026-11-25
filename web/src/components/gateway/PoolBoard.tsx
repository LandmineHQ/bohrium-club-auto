import { useState, type DragEvent } from "react"
import { toast } from "sonner"
import { ArrowLeft, ArrowRight, GripVertical, RefreshCw } from "lucide-react"
import { apiRequest } from "@/lib/api"
import { cn, formatNum } from "@/lib/utils"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Card, CardDescription, CardHeader, CardTitle } from "@/components/ui/card"
import { Progress } from "@/components/ui/primitives"
import type { AccountSummaryView, PoolMemberView } from "@/types/console"

interface PoolBoardProps {
  candidates: AccountSummaryView[]
  pooled: PoolMemberView[]
  totalEffectiveBalance: number
  onChanged: () => Promise<void>
}

export function PoolBoard({
  candidates,
  pooled,
  totalEffectiveBalance,
  onChanged,
}: PoolBoardProps) {
  const [draggingId, setDraggingId] = useState("")
  const [overZone, setOverZone] = useState<"" | "candidates" | "pooled">("")
  const [refreshing, setRefreshing] = useState(false)

  const currentPooledIDs = pooled.map((p) => p.accountId)

  async function savePoolOrder(nextIds: string[]) {
    try {
      await apiRequest("/api/gateway/pool", {
        method: "PUT",
        body: JSON.stringify({ accountIds: nextIds }),
      })
      toast.success("OpenAI 路由池已更新")
      await onChanged()
    } catch (err) {
      toast.error("更新路由池失败: " + (err instanceof Error ? err.message : ""))
      await onChanged()
    }
  }

  function handleDragStart(e: DragEvent, accountId: string) {
    setDraggingId(accountId)
    e.dataTransfer.effectAllowed = "move"
    try {
      e.dataTransfer.setData("text/plain", accountId)
    } catch {
      // ignore
    }
  }

  function handleDragEnd() {
    setDraggingId("")
    setOverZone("")
  }

  async function handleDrop(
    e: DragEvent,
    targetZone: "candidates" | "pooled",
    insertBeforeId?: string
  ) {
    e.preventDefault()
    e.stopPropagation()
    const accId = draggingId || e.dataTransfer.getData("text/plain")
    setDraggingId("")
    setOverZone("")
    if (!accId) return

    const next = currentPooledIDs.filter((id) => id !== accId)
    if (targetZone === "pooled") {
      if (insertBeforeId) {
        const idx = next.indexOf(insertBeforeId)
        if (idx >= 0) next.splice(idx, 0, accId)
        else next.push(accId)
      } else {
        next.push(accId)
      }
    }
    await savePoolOrder(next)
  }

  async function refreshPoolBalances() {
    setRefreshing(true)
    try {
      await apiRequest("/api/accounts/refresh-balances", { method: "POST" })
      await onChanged()
      toast.success("已刷新路由池余额与抽签权重")
    } catch (err) {
      toast.error("刷新失败: " + (err instanceof Error ? err.message : ""))
    } finally {
      setRefreshing(false)
    }
  }

  return (
    <Card>
      <CardHeader>
        <div>
          <div className="flex flex-wrap items-center gap-2">
            <CardTitle>OpenAI 混合路由池（拖拽入池 · 余额加权）</CardTitle>
            <Badge variant="mono">池总有效余额 {formatNum(totalEffectiveBalance)} 光子</Badge>
          </div>
          <CardDescription>
            默认账号<strong>不会</strong>自动进入路由池，请将左侧候补账号<strong>拖入右侧路由池</strong>
            （或点击移入按钮）。新会话按可用余额线性正比概率（<code>P ∝ 个人光子+组织余额</code>）抽签绑定至 SQLite（最长缓存 7 天，到期自动删除）；首包前遇 401/402 自动无感切换重绑。
          </CardDescription>
        </div>
        <Button variant="outline" size="sm" disabled={refreshing} onClick={refreshPoolBalances}>
          <RefreshCw className={cn("h-3.5 w-3.5", refreshing && "animate-spin")} />
          刷新池余额与权重
        </Button>
      </CardHeader>

      <div className="grid grid-cols-1 gap-4 p-5 lg:grid-cols-2">
        {/* Left: Candidates */}
        <div
          className={cn(
            "flex flex-col rounded-lg border bg-muted/20 p-3.5 transition-colors",
            overZone === "candidates" && "border-primary bg-accent/50"
          )}
          onDragOver={(e) => {
            e.preventDefault()
            setOverZone("candidates")
          }}
          onDragLeave={() => setOverZone("")}
          onDrop={(e) => handleDrop(e, "candidates")}
        >
          <div className="mb-3 flex items-center justify-between">
            <div className="flex items-center gap-2 text-xs font-semibold">
              <span>候补账号（未入池 · 仅签到）</span>
              <Badge variant="mono">{candidates.length}</Badge>
            </div>
            <span className="text-[11px] text-muted-foreground">不接收 /v1/* 流量</span>
          </div>

          <div className="flex-1 space-y-2 min-h-32">
            {candidates.length === 0 ? (
              <div className="flex h-32 items-center justify-center rounded-md border border-dashed text-center text-xs text-muted-foreground px-4">
                无未入池候补账号（所有已登录账号均已入池，或尚未添加账号）
              </div>
            ) : (
              candidates.map((a) => (
                <div
                  key={a.id}
                  draggable
                  onDragStart={(e) => handleDragStart(e, a.id)}
                  onDragEnd={handleDragEnd}
                  className={cn(
                    "flex items-center gap-2.5 rounded-md border bg-card p-3 cursor-grab active:cursor-grabbing",
                    draggingId === a.id && "opacity-50"
                  )}
                >
                  <GripVertical className="h-4 w-4 shrink-0 text-muted-foreground/60" />
                  <div className="min-w-0 flex-1">
                    <div className="flex items-center justify-between gap-2">
                      <span className="truncate text-xs font-semibold">{a.displayName}</span>
                      <Badge variant="mono" className="tabular-nums">
                        {formatNum(a.effectiveBalance)} 光子
                      </Badge>
                    </div>
                    <div className="mt-1 flex flex-wrap gap-x-3 text-[11px] text-muted-foreground tabular-nums">
                      <span>UID {a.userId}</span>
                      <span>
                        个人 {formatNum(a.photonBalance)} + 组织 {formatNum(a.orgBalance)}
                      </span>
                    </div>
                  </div>
                  <Button
                    variant="outline"
                    size="xs"
                    onClick={() =>
                      savePoolOrder([...currentPooledIDs.filter((id) => id !== a.id), a.id])
                    }
                  >
                    移入池
                    <ArrowRight className="h-3 w-3" />
                  </Button>
                </div>
              ))
            )}
          </div>
        </div>

        {/* Right: Active Pool */}
        <div
          className={cn(
            "flex flex-col rounded-lg border border-primary/30 bg-primary/[0.02] p-3.5 transition-colors",
            overZone === "pooled" && "border-primary bg-accent/50"
          )}
          onDragOver={(e) => {
            e.preventDefault()
            setOverZone("pooled")
          }}
          onDragLeave={() => setOverZone("")}
          onDrop={(e) => handleDrop(e, "pooled")}
        >
          <div className="mb-3 flex items-center justify-between">
            <div className="flex items-center gap-2 text-xs font-semibold">
              <span>OpenAI 路由池（已启用混合路由）</span>
              <Badge variant="ok">{pooled.length}</Badge>
            </div>
            <span className="text-[11px] text-muted-foreground">拖拽卡片至此即可入池 / 排序</span>
          </div>

          <div className="flex-1 space-y-2 min-h-32">
            {pooled.length === 0 ? (
              <div className="flex h-32 items-center justify-center rounded-md border border-dashed text-center text-xs text-muted-foreground px-4">
                当前路由池为空：请将左侧账号拖入此处（或点击「移入池」）后即可开始 OpenAI 加权路由
              </div>
            ) : (
              pooled.map((p) => {
                const pct = Math.min(100, Math.max(0, Number(p.weightPercent || 0)))
                return (
                  <div
                    key={p.accountId}
                    draggable
                    onDragStart={(e) => handleDragStart(e, p.accountId)}
                    onDragEnd={handleDragEnd}
                    onDragOver={(e) => e.preventDefault()}
                    onDrop={(e) => handleDrop(e, "pooled", p.accountId)}
                    className={cn(
                      "flex items-center gap-2.5 rounded-md border bg-card p-3 cursor-grab active:cursor-grabbing",
                      draggingId === p.accountId && "opacity-50"
                    )}
                  >
                    <GripVertical className="h-4 w-4 shrink-0 text-muted-foreground/60" />
                    <div className="min-w-0 flex-1">
                      <div className="flex items-center justify-between gap-2">
                        <span className="truncate text-xs font-semibold">{p.displayName}</span>
                        {p.healthy ? (
                          <Badge variant="ok">就绪</Badge>
                        ) : (
                          <Badge variant="warn">{p.healthStatus || "冷却"}</Badge>
                        )}
                      </div>
                      <div className="mt-1 flex flex-wrap gap-x-3 text-[11px] text-muted-foreground tabular-nums">
                        <span>UID {p.userId}</span>
                        <span>
                          有效余额 <strong className="text-foreground">{formatNum(p.effectiveBalance)}</strong>
                        </span>
                        <span>绑定 {p.activeBindings || 0} 会话</span>
                      </div>
                      <div className="mt-2 flex items-center gap-2" title="新会话按余额加权命中概率">
                        <Progress value={pct} className="flex-1" />
                        <span className="w-11 text-right font-mono text-[11px] text-muted-foreground tabular-nums">
                          {pct.toFixed(1)}%
                        </span>
                      </div>
                    </div>
                    <Button
                      variant="ghost"
                      size="xs"
                      onClick={() =>
                        savePoolOrder(currentPooledIDs.filter((id) => id !== p.accountId))
                      }
                    >
                      <ArrowLeft className="h-3 w-3" />
                      移出
                    </Button>
                  </div>
                )
              })
            )}
          </div>
        </div>
      </div>
    </Card>
  )
}
