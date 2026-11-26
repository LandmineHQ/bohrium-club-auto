import { formatNum } from "@/lib/utils"
import { Button } from "@/components/ui/button"
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card"
import { Progress } from "@/components/ui/primitives"
import { AlertBox } from "@/components/ui/table"
import { AccountsTable } from "./AccountsTable"
import { DailyTasksPanel } from "./DailyTasksPanel"
import type { OverviewResponse } from "@/types/console"

interface OverviewTabProps {
  data: OverviewResponse | null
  selectedAccountId: string
  onSelectAccount: (id: string) => void
  onOpenLogin: () => void
  onRefreshAll: () => Promise<void>
}

export function OverviewTab({
  data,
  selectedAccountId,
  onSelectAccount,
  onOpenLogin,
  onRefreshAll,
}: OverviewTabProps) {
  const accounts = data?.accounts || []
  const activeJobs = Array.isArray(data?.activeJobs) ? data.activeJobs : []
  const summary = data?.accountSummary
  const sched = data?.scheduler
  const storage = data?.assets?.storage
  const ps = data?.promptStats
  const pdfs = data?.pdfStats

  const totalAcc = summary?.totalAccounts ?? accounts.length
  const onlineAcc = summary?.onlineAccounts ?? 0
  const poolAcc = summary?.poolAccounts ?? 0

  return (
    <div className="space-y-5">
      {totalAcc === 0 && (
        <AlertBox variant="info" className="flex flex-wrap items-center justify-between gap-3">
          <div>
            <strong>尚未绑定任何玻尔账号</strong> — 点击「+ 添加 / 登录账号」，支持微信扫码、Google/第三方 OAuth、验证码或导入{" "}
            <code>brmToken</code>；新账号默认不进入 OpenAI 路由池，需在「03 OpenAI 路由池」手动拖入。
          </div>
          <Button size="xs" onClick={onOpenLogin}>
            添加首个账号
          </Button>
        </AlertBox>
      )}

      {/* 4-column KPI strip */}
      <div className="grid grid-cols-1 gap-3 sm:grid-cols-2 xl:grid-cols-4">
        <Card className="p-4">
          <div className="text-[11px] font-medium text-muted-foreground">托管账号 / 在线</div>
          <div className="mt-1.5 text-2xl font-semibold tracking-tight tabular-nums">
            {onlineAcc} / {totalAcc}
          </div>
          <div className="mt-1 text-[11px] text-muted-foreground tabular-nums">
            OpenAI 池 {summary?.healthyPoolAccounts || 0}/{poolAcc} · 活跃绑定 {summary?.activeBindings || 0} 会话
          </div>
        </Card>

        <Card className="p-4">
          <div className="text-[11px] font-medium text-muted-foreground">全账号总光子余额</div>
          <div className="mt-1.5 text-2xl font-semibold tracking-tight tabular-nums">
            {formatNum(summary?.totalPhotons ?? 0)}
          </div>
          <div className="mt-1 text-[11px] text-muted-foreground tabular-nums">
            池内可用 {formatNum(summary?.poolEffectivePhotons ?? 0)} · 组织{" "}
            {formatNum(summary?.totalOrgBalance ?? 0)}
          </div>
        </Card>

        <Card className="p-4">
          <div className="text-[11px] font-medium text-muted-foreground">今日多账号签到进度</div>
          <div className="mt-1.5 text-2xl font-semibold tracking-tight tabular-nums">
            {summary?.checkinDoneToday || 0} / {summary?.checkinEnabledCount || 0}
          </div>
          <div className="mt-1 text-[11px] text-muted-foreground tabular-nums">
            下次签到：{sched?.nextRunAt || "已暂停"}
          </div>
        </Card>

        <Card className="p-4">
          <div className="text-[11px] font-medium text-muted-foreground">聚焦账号存储容量</div>
          <div className="mt-1.5 text-2xl font-semibold tracking-tight tabular-nums">
            {storage?.remainingHuman || "--"}
          </div>
          <Progress value={storage?.usedPercent || 0} className="mt-2" />
          <div className="mt-1.5 text-[11px] text-muted-foreground tabular-nums">
            {storage
              ? `已用 ${storage.usedHuman || "--"} / ${storage.totalHuman || "--"}`
              : "选择账号后显示"}
          </div>
        </Card>
      </div>

      <AccountsTable
        accounts={accounts}
        selectedAccountId={selectedAccountId}
        poolCount={poolAcc}
        activeJobs={activeJobs}
        onSelectAccount={onSelectAccount}
        onOpenLogin={onOpenLogin}
        onRefreshAll={onRefreshAll}
      />

      <DailyTasksPanel
        accounts={accounts}
        selectedAccountId={selectedAccountId}
        onSelectAccount={onSelectAccount}
        tasks={data?.tasks}
        tasksError={data?.tasksError}
        activeJobs={activeJobs}
        onRefresh={onRefreshAll}
      />

      {/* Corpus & Runtime Spec Grid */}
      <div className="grid grid-cols-1 gap-4 lg:grid-cols-2">
        <Card>
          <CardHeader className="py-3">
            <CardTitle>内置随机语料与文献池</CardTitle>
          </CardHeader>
          <CardContent className="divide-y divide-border py-1">
            {[
              ["科学空间题库", `${ps?.scienceSpacePrompts || 0} 条`],
              ["学术写作题库", `${ps?.academicWritingPrompts || 0} 条`],
              ["文献阅读提问库", `${ps?.literatureReadingPrompts || 0} 条`],
              ["arXiv 开放文献目录", `${ps?.paperCatalogCount || 0} 篇`],
              ["本地缓存 / 自定义 PDF", `${pdfs?.cached || 0} 缓存 / ${pdfs?.custom || 0} 自定义`],
            ].map(([k, v]) => (
              <div key={k} className="flex items-center justify-between py-2.5 text-xs">
                <span className="text-muted-foreground">{k}</span>
                <strong className="tabular-nums font-medium">{v}</strong>
              </div>
            ))}
          </CardContent>
        </Card>

        <Card>
          <CardHeader className="py-3">
            <CardTitle>运行态与出站指纹</CardTitle>
          </CardHeader>
          <CardContent className="divide-y divide-border py-1">
            <div className="flex items-center justify-between py-2.5 text-xs">
              <span className="text-muted-foreground">调度器状态</span>
              <strong className="tabular-nums font-medium">
                {sched?.running
                  ? `执行中 (${sched.currentJob})`
                  : sched?.enabled
                    ? `已启用 · ${sched.timeHHMM} (+0~${sched.jitterMinutes}m 随机分散)`
                    : "已暂停"}
              </strong>
            </div>
            <div className="flex items-center justify-between py-2.5 text-xs">
              <span className="text-muted-foreground">最近签到完成</span>
              <strong className="tabular-nums font-medium">
                {sched?.lastScheduledDay
                  ? `${sched.lastScheduledDay} · ${sched.lastRunSummary || "完成"}`
                  : "尚无记录"}
              </strong>
            </div>
            <div className="flex items-center justify-between py-2.5 text-xs">
              <span className="text-muted-foreground">SQLite 持久化</span>
              <strong className="font-medium text-emerald-700 dark:text-emerald-400">
                WAL · 7天会话绑定
              </strong>
            </div>
            <div className="py-2.5 text-xs space-y-1">
              <div className="text-muted-foreground">Chrome User-Agent (Web 任务)</div>
              <code className="block break-all rounded bg-muted px-2 py-1.5 text-[11px]">
                {data?.userAgent || "--"}
              </code>
            </div>
          </CardContent>
        </Card>
      </div>
    </div>
  )
}
