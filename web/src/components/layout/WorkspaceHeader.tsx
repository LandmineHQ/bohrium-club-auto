import { Badge } from "@/components/ui/badge"
import type { ConsoleTab, SchedulerStatus } from "@/types/console"

const TAB_TITLES: Record<ConsoleTab, string> = {
  overview: "多账号与签到",
  assets: "积分与容量",
  gateway: "OpenAI 路由池",
  apiKeys: "API Key 管理",
  analytics: "OpenAI 数据看板",
  logs: "执行日志",
  recon: "前端 Recon",
  settings: "定时与设置",
}

export function WorkspaceHeader({
  activeTab,
  taskDate,
  scheduler,
}: {
  activeTab: ConsoleTab
  taskDate?: string
  scheduler?: SchedulerStatus
}) {
  const schedPill = scheduler
    ? scheduler.running
      ? `RUN: ${scheduler.currentJob || "task"}`
      : scheduler.enabled
        ? `NEXT: ${scheduler.nextRunAt ? scheduler.nextRunAt.slice(5, 19) : scheduler.timeHHMM}`
        : "SCHED: OFF"
    : "调度器就绪"

  return (
    <header className="sticky top-0 z-20 flex h-13 items-center justify-between border-b bg-card/90 px-5 backdrop-blur-sm">
      <div className="flex items-center gap-2.5 text-xs">
        <span className="text-muted-foreground">Console</span>
        <span className="text-muted-foreground/50">/</span>
        <span className="font-semibold text-foreground">{TAB_TITLES[activeTab]}</span>
        <Badge variant="mono">{taskDate || "--"}</Badge>
      </div>
      <Badge variant="mono">{schedPill}</Badge>
    </header>
  )
}
