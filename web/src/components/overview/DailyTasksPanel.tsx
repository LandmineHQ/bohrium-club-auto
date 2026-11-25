import { useState } from "react"
import { toast } from "sonner"
import { RefreshCw } from "lucide-react"
import { apiRequest } from "@/lib/api"
import { formatNum } from "@/lib/utils"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Card, CardDescription, CardHeader, CardTitle } from "@/components/ui/card"
import { NativeSelect } from "@/components/ui/input"
import { AlertBox } from "@/components/ui/table"
import type { AccountSummaryView, DailyTasksResponse } from "@/types/console"

interface DailyTasksPanelProps {
  accounts: AccountSummaryView[]
  selectedAccountId: string
  onSelectAccount: (id: string) => void
  tasks?: DailyTasksResponse | null
  tasksError?: string
  onRefresh: () => Promise<void>
}

export function DailyTasksPanel({
  accounts,
  selectedAccountId,
  onSelectAccount,
  tasks,
  tasksError,
  onRefresh,
}: DailyTasksPanelProps) {
  const [busyKey, setBusyKey] = useState("")

  const summaryText = tasks
    ? `${tasks.completedCount}/${Math.max(0, tasks.totalCount - tasks.skippedCount)} 已完成 · +${formatNum(tasks.earnedPhotons)} 光子`
    : accounts.length > 0
      ? "未加载"
      : "暂无账号"

  async function claimAll() {
    if (!selectedAccountId) return toast.warning("请先添加账号")
    setBusyKey("claim-all")
    try {
      const res = await apiRequest<{ data?: { message?: string } }>(
        `/api/accounts/${encodeURIComponent(selectedAccountId)}/tasks/claim-all`,
        { method: "POST" }
      )
      toast.success(res.data?.message || "已一键领取全部每日任务")
      await onRefresh()
    } catch (err) {
      toast.error("一键领取失败: " + (err instanceof Error ? err.message : ""))
    } finally {
      setBusyKey("")
    }
  }

  async function runAll() {
    if (!selectedAccountId) return toast.warning("请先添加账号")
    setBusyKey("run-all")
    try {
      const res = await apiRequest<{ data?: { summary?: string } }>(
        `/api/accounts/${encodeURIComponent(selectedAccountId)}/tasks/run-all`,
        { method: "POST" }
      )
      toast.success(res.data?.summary || "全部任务已执行")
      await onRefresh()
    } catch (err) {
      toast.error("执行失败: " + (err instanceof Error ? err.message : ""))
    } finally {
      setBusyKey("")
    }
  }

  async function claimOne(taskKey: string) {
    if (!selectedAccountId) return toast.warning("请先添加账号")
    setBusyKey(`claim:${taskKey}`)
    try {
      const res = await apiRequest<{ data?: { message?: string } }>(
        `/api/accounts/${encodeURIComponent(selectedAccountId)}/tasks/claim`,
        { method: "POST", body: JSON.stringify({ taskKey }) }
      )
      toast.success(res.data?.message || "领取成功")
      await onRefresh()
    } catch (err) {
      toast.error("领取失败: " + (err instanceof Error ? err.message : ""))
    } finally {
      setBusyKey("")
    }
  }

  async function runOne(taskKey: string) {
    if (!selectedAccountId) return toast.warning("请先添加账号")
    setBusyKey(`run:${taskKey}`)
    try {
      const res = await apiRequest<{ data?: { message?: string; success?: boolean } }>(
        `/api/accounts/${encodeURIComponent(selectedAccountId)}/tasks/run`,
        { method: "POST", body: JSON.stringify({ taskKey }) }
      )
      if (res.data?.success) {
        toast.success(res.data?.message || "任务已执行")
      } else {
        toast.warning(res.data?.message || "任务已执行")
      }
      await onRefresh()
    } catch (err) {
      toast.error("执行失败: " + (err instanceof Error ? err.message : ""))
      await onRefresh()
    } finally {
      setBusyKey("")
    }
  }

  return (
    <Card>
      <CardHeader>
        <div>
          <div className="flex flex-wrap items-center gap-2">
            <CardTitle>单账号每日任务详情</CardTitle>
            <NativeSelect
              aria-label="选择聚焦账号"
              value={selectedAccountId}
              onChange={(e) => onSelectAccount(e.target.value)}
            >
              {accounts.length === 0 ? (
                <option value="">暂无账号</option>
              ) : (
                accounts.map((a) => (
                  <option key={a.id} value={a.id}>
                    {a.displayName} (UID {a.userId}){a.poolEnabled ? " [入池]" : ""}
                  </option>
                ))
              )}
            </NativeSelect>
            <Badge variant="mono">{summaryText}</Badge>
          </div>
          <CardDescription>
            支持全部 4 项每日任务：Web 端 / 文献阅读 / 学术写作走真实产品 API，客户端任务按 Bohrium 桌面端原生协议构造完整 tools 请求并在发起后立即取消。
          </CardDescription>
        </div>

        <div className="flex flex-wrap items-center gap-2">
          <Button
            variant="ghost"
            size="sm"
            onClick={async () => {
              await onRefresh()
              toast.success("已刷新最新数据")
            }}
          >
            <RefreshCw className="h-3.5 w-3.5" />
            刷新
          </Button>
          <Button variant="outline" size="sm" disabled={busyKey === "claim-all"} onClick={claimAll}>
            {busyKey === "claim-all" ? "领取中..." : "一键领取任务"}
          </Button>
          <Button size="sm" disabled={busyKey === "run-all"} onClick={runAll}>
            {busyKey === "run-all" ? "执行中..." : "执行该账号任务"}
          </Button>
        </div>
      </CardHeader>

      {tasksError && (
        <div className="px-5 pt-4">
          <AlertBox variant="err">{tasksError}</AlertBox>
        </div>
      )}

      <div className="divide-y divide-border">
        {!tasks || !tasks.tasks || tasks.tasks.length === 0 ? (
          <div className="px-5 py-8 text-center text-xs text-muted-foreground">
            请先添加并选择一个玻尔账号，此处将显示该账号的每日任务并支持一键或单项触发。
          </div>
        ) : (
          tasks.tasks.map((t, idx) => {
            const num = String(idx + 1).padStart(2, "0")
            const running = busyKey === `run:${t.taskKey}`
            const claiming = busyKey === `claim:${t.taskKey}`
            return (
              <div
                key={t.taskKey}
                className="grid grid-cols-1 gap-3 px-5 py-3.5 sm:grid-cols-[1fr_110px_90px_160px] sm:items-center"
              >
                <div className="flex items-start gap-3">
                  <span className="mt-0.5 font-mono text-xs text-muted-foreground">{num}</span>
                  <div>
                    <div className="flex flex-wrap items-center gap-2">
                      <span className="text-xs font-semibold text-foreground">
                        {t.titleZh || t.taskKey}
                      </span>
                      <code className="rounded bg-muted px-1.5 py-0.5 text-[10px] text-muted-foreground">
                        {t.actionKey || t.taskKey}
                      </code>
                    </div>
                    <p className="mt-0.5 text-[11px] text-muted-foreground">
                      {t.descriptionZh || (t.skipped ? t.skipReason : "")}
                    </p>
                  </div>
                </div>

                <div className="text-xs font-semibold tabular-nums text-emerald-700 dark:text-emerald-400">
                  +{formatNum(t.rewardAmount)} 光子
                </div>

                <div>
                  {t.skipped ? (
                    <Badge variant="muted">已跳过</Badge>
                  ) : t.status === 1 ? (
                    <Badge variant="ok">已完成</Badge>
                  ) : t.status === 0 ? (
                    <Badge variant="info">待完成</Badge>
                  ) : t.status === -1 ? (
                    <Badge variant="warn">未领取</Badge>
                  ) : (
                    <Badge variant="muted">{t.statusLabel}</Badge>
                  )}
                </div>

                <div className="flex flex-wrap justify-start gap-1.5 sm:justify-end">
                  {t.skipped ? (
                    <span className="text-[11px] text-muted-foreground">
                      {t.skipReason || "已跳过"}
                    </span>
                  ) : (
                    <>
                      {t.status === -1 && (
                        <Button
                          variant="outline"
                          size="xs"
                          disabled={claiming}
                          onClick={() => claimOne(t.taskKey)}
                        >
                          领取
                        </Button>
                      )}
                      <Button
                        variant={t.status === 1 ? "outline" : "default"}
                        size="xs"
                        disabled={running}
                        onClick={() => runOne(t.taskKey)}
                      >
                        {running ? "执行中..." : t.status === 1 ? "重新执行" : "手动执行"}
                      </Button>
                    </>
                  )}
                </div>
              </div>
            )
          })
        )}
      </div>
    </Card>
  )
}
