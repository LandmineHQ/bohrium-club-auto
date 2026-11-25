import { useState } from "react"
import { toast } from "sonner"
import { Plus, RefreshCw, Play } from "lucide-react"
import { apiRequest } from "@/lib/api"
import { cn, formatNum } from "@/lib/utils"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Card, CardDescription, CardHeader, CardTitle } from "@/components/ui/card"
import { Switch } from "@/components/ui/primitives"
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table"
import type { AccountSummaryView } from "@/types/console"

interface AccountsTableProps {
  accounts: AccountSummaryView[]
  selectedAccountId: string
  poolCount: number
  onSelectAccount: (id: string) => void
  onOpenLogin: () => void
  onRefreshAll: () => Promise<void>
}

export function AccountsTable({
  accounts,
  selectedAccountId,
  poolCount,
  onSelectAccount,
  onOpenLogin,
  onRefreshAll,
}: AccountsTableProps) {
  const [busyAction, setBusyAction] = useState<string>("")

  async function toggleCheckin(acc: AccountSummaryView, enabled: boolean) {
    try {
      await apiRequest(`/api/accounts/${encodeURIComponent(acc.id)}`, {
        method: "PATCH",
        body: JSON.stringify({ checkinEnabled: enabled }),
      })
      toast.success(enabled ? "已开启该账号每日随机签到" : "已暂停该账号每日自动签到")
      await onRefreshAll()
    } catch (err) {
      toast.error("更新失败: " + (err instanceof Error ? err.message : ""))
    }
  }

  async function renameAccount(acc: AccountSummaryView) {
    const next = window.prompt("输入新的账号备注名（留空恢复使用玻尔昵称）：", acc.label || "")
    if (next === null) return
    try {
      await apiRequest(`/api/accounts/${encodeURIComponent(acc.id)}`, {
        method: "PATCH",
        body: JSON.stringify({ label: next.trim() }),
      })
      toast.success("备注已更新")
      await onRefreshAll()
    } catch (err) {
      toast.error("更新备注失败: " + (err instanceof Error ? err.message : ""))
    }
  }

  async function runSingleCheckin(acc: AccountSummaryView) {
    setBusyAction(`run:${acc.id}`)
    try {
      const res = await apiRequest<{ data?: { summary?: string } }>(
        `/api/accounts/${encodeURIComponent(acc.id)}/tasks/run-all`,
        { method: "POST" }
      )
      toast.success(res.data?.summary || "签到执行完成")
      await onRefreshAll()
    } catch (err) {
      toast.error("执行失败: " + (err instanceof Error ? err.message : ""))
    } finally {
      setBusyAction("")
    }
  }

  async function refreshToken(acc: AccountSummaryView) {
    setBusyAction(`tok:${acc.id}`)
    try {
      await apiRequest(`/api/accounts/${encodeURIComponent(acc.id)}/refresh-token`, {
        method: "POST",
      })
      toast.success("Token 续期成功")
      await onRefreshAll()
    } catch (err) {
      toast.error("续期失败: " + (err instanceof Error ? err.message : ""))
    } finally {
      setBusyAction("")
    }
  }

  async function deleteAccount(acc: AccountSummaryView) {
    if (!window.confirm(`确定删除账号「${acc.displayName}」吗？其 OpenAI 会话绑定也将一并清理。`)) {
      return
    }
    try {
      await apiRequest(`/api/accounts/${encodeURIComponent(acc.id)}`, { method: "DELETE" })
      toast.warning(`已删除账号 ${acc.displayName}`)
      await onRefreshAll()
    } catch (err) {
      toast.error("删除失败: " + (err instanceof Error ? err.message : ""))
    }
  }

  async function refreshAllBalances() {
    setBusyAction("bal-all")
    try {
      const res = await apiRequest<{ refreshed?: number }>("/api/accounts/refresh-balances", {
        method: "POST",
      })
      toast.success(`已刷新 ${res.refreshed || 0} 个账号余额`)
      await onRefreshAll()
    } catch (err) {
      toast.error("批量刷新余额失败: " + (err instanceof Error ? err.message : ""))
    } finally {
      setBusyAction("")
    }
  }

  async function runAllAccounts() {
    setBusyAction("run-all")
    try {
      const res = await apiRequest<{ results?: Array<{ success: boolean }> }>(
        "/api/accounts/run-all",
        { method: "POST" }
      )
      const list = res.results || []
      const okCnt = list.filter((x) => x.success).length
      if (okCnt === list.length) {
        toast.success(`多账号批量签到完成：${okCnt}/${list.length} 成功`)
      } else {
        toast.warning(`多账号批量签到完成：${okCnt}/${list.length} 成功`)
      }
      await onRefreshAll()
    } catch (err) {
      toast.error("批量签到失败: " + (err instanceof Error ? err.message : ""))
    } finally {
      setBusyAction("")
    }
  }

  return (
    <Card>
      <CardHeader>
        <div>
          <div className="flex flex-wrap items-center gap-2">
            <CardTitle>多账号管理与随机波动签到</CardTitle>
            <Badge variant="mono">
              {accounts.length} 账号 · {poolCount} 入池
            </Badge>
          </div>
          <CardDescription>
            每个账号独立分配唯一 <code>storeId</code> 与每日随机签到时间点（同天稳定、跨天洗牌、账号间错峰执行）；新登录账号默认不进入 OpenAI 路由池。
          </CardDescription>
        </div>
        <div className="flex flex-wrap items-center gap-2">
          <Button variant="outline" size="sm" onClick={onOpenLogin}>
            <Plus className="h-3.5 w-3.5" />
            添加账号
          </Button>
          <Button
            variant="ghost"
            size="sm"
            disabled={busyAction === "bal-all"}
            onClick={refreshAllBalances}
          >
            <RefreshCw className={cn("h-3.5 w-3.5", busyAction === "bal-all" && "animate-spin")} />
            刷新全部余额
          </Button>
          <Button size="sm" disabled={busyAction === "run-all"} onClick={runAllAccounts}>
            <Play className="h-3.5 w-3.5" />
            {busyAction === "run-all" ? "全部签到中..." : "全部账号立即签到"}
          </Button>
        </div>
      </CardHeader>

      <Table>
        <TableHeader>
          <TableRow>
            <TableHead>账号 / 备注 / UID</TableHead>
            <TableHead>光子余额（个人 / 组织）</TableHead>
            <TableHead>登录状态 / 有效期</TableHead>
            <TableHead>今日随机签到计划</TableHead>
            <TableHead>OpenAI 路由池</TableHead>
            <TableHead className="text-right">操作</TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {accounts.length === 0 ? (
            <TableRow>
              <TableCell colSpan={6} className="py-8 text-center text-muted-foreground">
                暂无账号，请点击「+ 添加账号」登录玻尔账号
              </TableCell>
            </TableRow>
          ) : (
            accounts.map((a) => {
              const isSelected = a.id === selectedAccountId
              const daysLeft =
                a.expiresInSec > 0 ? `剩余 ${Math.floor(a.expiresInSec / 86400)} 天` : "已过期"
              const sc = a.schedule || {}
              const slotText = a.checkinEnabled
                ? sc.running
                  ? "执行中..."
                  : sc.doneToday
                    ? `今日已完成 (${sc.plannedTodayAt || "--"})`
                    : `计划 ${sc.plannedTodayAt || "--"}`
                : "自动签到关闭"
              const labelSub =
                a.label && a.nickname && a.label !== a.nickname ? `昵称 ${a.nickname} · ` : ""

              return (
                <TableRow key={a.id} className={cn(isSelected && "bg-accent/50")}>
                  <TableCell>
                    <div className="flex items-center gap-1.5">
                      <span className="font-semibold text-foreground">{a.displayName}</span>
                      {isSelected && <Badge variant="mono">聚焦</Badge>}
                    </div>
                    <div className="mt-0.5 text-[11px] text-muted-foreground">
                      {labelSub}UID {a.userId} · {a.loginMethod || "token"}
                    </div>
                  </TableCell>
                  <TableCell className="tabular-nums">
                    <div>
                      <strong className="font-semibold">{formatNum(a.photonBalance)}</strong> 光子
                    </div>
                    <div className="mt-0.5 text-[11px] text-muted-foreground">
                      组织 {formatNum(a.orgBalance)} · 有效 {formatNum(a.effectiveBalance)}
                    </div>
                  </TableCell>
                  <TableCell>
                    <div>
                      {a.healthStatus === "expired" || a.expiresInSec <= 0 ? (
                        <Badge variant="err">已过期</Badge>
                      ) : a.healthStatus === "insufficient" || a.healthStatus === "cooldown" ? (
                        <Badge variant="warn">冷却/余额不足</Badge>
                      ) : (
                        <Badge variant="ok">在线</Badge>
                      )}
                    </div>
                    <div className="mt-1 text-[11px] text-muted-foreground tabular-nums">
                      {daysLeft}
                    </div>
                  </TableCell>
                  <TableCell>
                    <div className="flex items-center gap-2">
                      <Switch
                        checked={a.checkinEnabled}
                        onCheckedChange={(checked) => toggleCheckin(a, checked)}
                        aria-label="启用每日随机签到"
                      />
                      {sc.doneToday ? (
                        <Badge variant="ok">已签到</Badge>
                      ) : a.checkinEnabled ? (
                        <Badge variant="info">待执行</Badge>
                      ) : (
                        <Badge variant="muted">已暂停</Badge>
                      )}
                    </div>
                    <div className="mt-1 text-[11px] text-muted-foreground tabular-nums">
                      {slotText}
                    </div>
                  </TableCell>
                  <TableCell>
                    <div>
                      {a.poolEnabled ? (
                        <Badge variant="ok">已入池 · {a.weightPercent}%</Badge>
                      ) : (
                        <Badge variant="muted">未入池</Badge>
                      )}
                    </div>
                    <div className="mt-1 text-[11px] text-muted-foreground tabular-nums">
                      {a.activeBindings || 0} 条活跃绑定
                    </div>
                  </TableCell>
                  <TableCell className="text-right">
                    <div className="flex flex-wrap justify-end gap-1">
                      <Button variant="ghost" size="xs" onClick={() => onSelectAccount(a.id)}>
                        任务/资产
                      </Button>
                      <Button
                        variant="outline"
                        size="xs"
                        disabled={busyAction === `run:${a.id}`}
                        onClick={() => runSingleCheckin(a)}
                      >
                        {busyAction === `run:${a.id}` ? "签到中..." : "签到"}
                      </Button>
                      <Button variant="ghost" size="xs" onClick={() => renameAccount(a)}>
                        备注
                      </Button>
                      <Button
                        variant="ghost"
                        size="xs"
                        disabled={busyAction === `tok:${a.id}`}
                        onClick={() => refreshToken(a)}
                      >
                        续期
                      </Button>
                      <Button
                        variant="ghost"
                        size="xs"
                        className="text-destructive hover:text-destructive"
                        onClick={() => deleteAccount(a)}
                      >
                        删除
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
