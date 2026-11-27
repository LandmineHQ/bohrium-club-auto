import { useState } from "react"
import { toast } from "sonner"
import { RefreshCw } from "lucide-react"
import { apiRequest } from "@/lib/api"
import { pollTaskJob, type TaskJobStartResponse } from "@/lib/taskJobs"
import { formatNum } from "@/lib/utils"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Card, CardDescription, CardHeader, CardTitle } from "@/components/ui/card"
import { NativeSelect } from "@/components/ui/input"
import { AlertBox } from "@/components/ui/table"
import type {
  AccountSummaryView,
  ClientLoginBenefitClaimResult,
  ClientLoginBenefitStatus,
  DailyTasksResponse,
  TaskJob,
} from "@/types/console"

interface DailyTasksPanelProps {
  accounts: AccountSummaryView[]
  selectedAccountId: string
  onSelectAccount: (id: string) => void
  tasks?: DailyTasksResponse | null
  tasksError?: string
  clientBenefit?: ClientLoginBenefitStatus | null
  clientBenefitError?: string
  activeJobs?: TaskJob[] | null
  onRefresh: () => Promise<void>
}

export function DailyTasksPanel({
  accounts,
  selectedAccountId,
  onSelectAccount,
  tasks,
  tasksError,
  clientBenefit,
  clientBenefitError,
  activeJobs = [],
  onRefresh,
}: DailyTasksPanelProps) {
  const [busyKey, setBusyKey] = useState("")

  const summaryText = tasks
    ? `${tasks.completedCount}/${Math.max(0, tasks.totalCount - tasks.skippedCount)} 已完成 · +${formatNum(tasks.earnedPhotons)} 光子`
    : accounts.length > 0
      ? "未加载"
      : "暂无账号"

  const safeActiveJobs = Array.isArray(activeJobs) ? activeJobs : []
  const accountJobs = safeActiveJobs.filter(
    (j) =>
      j.status === "running" &&
      (j.kind === "all_accounts" || !j.accountId || j.accountId === selectedAccountId)
  )
  const accountAllRunning =
    busyKey === "run-all" ||
    accountJobs.some((j) => j.kind === "account_all" || j.kind === "all_accounts")

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
      const res = await apiRequest<TaskJobStartResponse>(
        `/api/accounts/${encodeURIComponent(selectedAccountId)}/tasks/run-all`,
        { method: "POST" }
      )
      if (res.async && res.job?.id) {
        const finalJob = await pollTaskJob(res.job.id)
        if (finalJob.status === "failed") {
          toast.error("执行失败: " + (finalJob.error || finalJob.message || "任务失败"))
        } else if (finalJob.accountResult && !finalJob.accountResult.allWebDone) {
          toast.warning(finalJob.message || finalJob.accountResult.summary || "部分任务未完成")
        } else {
          toast.success(finalJob.message || finalJob.accountResult?.summary || "全部任务已执行")
        }
      } else {
        toast.success(res.data?.summary || "全部任务已执行")
      }
      await onRefresh()
    } catch (err) {
      toast.error("执行失败: " + (err instanceof Error ? err.message : ""))
      await onRefresh()
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
      const res = await apiRequest<TaskJobStartResponse>(
        `/api/accounts/${encodeURIComponent(selectedAccountId)}/tasks/run`,
        { method: "POST", body: JSON.stringify({ taskKey }) }
      )
      if (res.async && res.job?.id) {
        const finalJob = await pollTaskJob(res.job.id)
        const sr = finalJob.singleResult
        if (finalJob.status === "completed" && sr?.success) {
          toast.success(sr.message || finalJob.message || "任务已执行")
        } else if (finalJob.status === "completed") {
          toast.warning(sr?.message || finalJob.message || "任务已执行")
        } else {
          toast.error("执行失败: " + (finalJob.error || sr?.message || finalJob.message || "未知错误"))
        }
      } else if (res.data?.success) {
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

  async function claimClientBenefit() {
    if (!selectedAccountId) return toast.warning("请先添加账号")
    setBusyKey("claim:client-benefit")
    try {
      const res = await apiRequest<{ data?: ClientLoginBenefitClaimResult }>(
        `/api/accounts/${encodeURIComponent(selectedAccountId)}/benefits/client-login/claim`,
        { method: "POST" }
      )
      const data = res.data
      if (data?.claimed) {
        toast.success(data.message || "已领取 10,000 光子客户端首登礼包")
      } else {
        toast.info(data?.message || "当前账号暂不可领取客户端首登礼包")
      }
      await onRefresh()
    } catch (err) {
      toast.error("领取客户端礼包失败: " + (err instanceof Error ? err.message : ""))
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
          <Button size="sm" disabled={accountAllRunning} onClick={runAll}>
            {accountAllRunning ? "后台执行中..." : "执行该账号任务"}
          </Button>
        </div>
      </CardHeader>

      {tasksError && (
        <div className="px-5 pt-4">
          <AlertBox variant="err">{tasksError}</AlertBox>
        </div>
      )}

      {selectedAccountId && (clientBenefit || clientBenefitError) && (
        <div className="border-b border-border bg-muted/20 px-5 py-3">
          {clientBenefitError ? (
            <AlertBox variant="warn">客户端首登礼包状态查询失败: {clientBenefitError}</AlertBox>
          ) : clientBenefit ? (
            <div className="grid grid-cols-1 gap-3 sm:grid-cols-[1fr_110px_90px_160px] sm:items-center">
              <div className="flex items-start gap-3">
                <span className="mt-0.5 rounded bg-amber-500/15 px-1.5 py-0.5 font-mono text-[10px] font-semibold text-amber-700 dark:text-amber-300">
                  一次性
                </span>
                <div>
                  <div className="flex flex-wrap items-center gap-2">
                    <span className="text-xs font-semibold text-foreground">
                      首次登录 Bohrium 客户端领光子（{clientBenefit.validDays || 90} 天有效）
                    </span>
                    <code className="rounded bg-muted px-1.5 py-0.5 text-[10px] text-muted-foreground">
                      bohrium_client_first_login
                    </code>
                  </div>
                  <p className="mt-0.5 text-[11px] text-muted-foreground">
                    {clientBenefit.reason ||
                      "已完成 scix/membership/client-activity 前置校验，可触发客户端首登礼包领取"}
                  </p>
                </div>
              </div>

              <div className="text-xs font-semibold tabular-nums text-emerald-700 dark:text-emerald-400">
                +{formatNum(clientBenefit.rewardPhotons || 10000)} 光子
              </div>

              <div>
                {clientBenefit.eligible ? (
                  <Badge variant="warn">可领取</Badge>
                ) : clientBenefit.claimed ? (
                  <Badge variant="ok">已领取</Badge>
                ) : !clientBenefit.personalAccount ? (
                  <Badge variant="muted">仅个人版</Badge>
                ) : (
                  <Badge variant="muted">不可领取</Badge>
                )}
              </div>

              <div className="flex flex-wrap justify-start gap-1.5 sm:justify-end">
                <Button
                  variant={clientBenefit.eligible ? "default" : "outline"}
                  size="xs"
                  disabled={!clientBenefit.eligible || busyKey === "claim:client-benefit"}
                  onClick={claimClientBenefit}
                >
                  {busyKey === "claim:client-benefit"
                    ? "校验领取中..."
                    : clientBenefit.eligible
                      ? "领取首登礼包"
                      : clientBenefit.claimed
                        ? "已领取"
                        : "不满足条件"}
                </Button>
              </div>
            </div>
          ) : null}
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
            const running =
              busyKey === `run:${t.taskKey}` ||
              accountJobs.some(
                (j) =>
                  j.kind === "account_all" ||
                  j.kind === "all_accounts" ||
                  (j.kind === "single_task" &&
                    (j.taskKey === t.taskKey || j.taskKey === t.actionKey))
              )
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
                        disabled={running || t.status === 1}
                        onClick={() => runOne(t.taskKey)}
                      >
                        {running ? "后台执行中..." : t.status === 1 ? "今日已完成" : "手动执行"}
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
