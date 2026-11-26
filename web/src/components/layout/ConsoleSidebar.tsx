import { Moon, Plus, LogOut, Sun } from "lucide-react"
import { Button } from "@/components/ui/button"
import { Badge } from "@/components/ui/badge"
import { cn } from "@/lib/utils"
import type { AccountOverviewSummary, ConsoleTab } from "@/types/console"

interface ConsoleSidebarProps {
  version: string
  activeTab: ConsoleTab
  onSelectTab: (tab: ConsoleTab) => void
  summary?: AccountOverviewSummary
  accountCount: number
  theme: "light" | "dark"
  onToggleTheme: () => void
  onOpenLogin: () => void
  onAdminLogout: () => void
}

const NAV_GROUPS: Array<{
  label: string
  items: Array<{ id: ConsoleTab; index: string; title: string }>
}> = [
  {
    label: "工作台",
    items: [
      { id: "overview", index: "01", title: "多账号与签到" },
      { id: "assets", index: "02", title: "积分与容量" },
      { id: "gateway", index: "03", title: "OpenAI 路由池" },
      { id: "analytics", index: "04", title: "OpenAI 数据看板" },
    ],
  },
  {
    label: "可观测性",
    items: [
      { id: "logs", index: "05", title: "执行日志" },
      { id: "recon", index: "06", title: "前端 Recon" },
    ],
  },
  {
    label: "系统",
    items: [{ id: "settings", index: "07", title: "定时与设置" }],
  },
]

export function ConsoleSidebar({
  version,
  activeTab,
  onSelectTab,
  summary,
  accountCount,
  theme,
  onToggleTheme,
  onOpenLogin,
  onAdminLogout,
}: ConsoleSidebarProps) {
  const totalAcc = summary?.totalAccounts ?? accountCount
  const onlineAcc = summary?.onlineAccounts ?? 0
  const poolAcc = summary?.poolAccounts ?? 0
  const isOnline = totalAcc > 0 && onlineAcc > 0

  return (
    <aside className="flex w-full flex-col border-b bg-card md:w-60 md:shrink-0 md:border-b-0 md:border-r md:sticky md:top-0 md:h-screen">
      <div className="flex items-center justify-between border-b px-4 py-3.5">
        <div className="flex items-center gap-2.5">
          <span className="h-2.5 w-2.5 rounded-xs bg-foreground" aria-hidden="true" />
          <span className="text-sm font-semibold tracking-tight">Bohrium Auto</span>
        </div>
        <Badge variant="mono">{version || "dev"}</Badge>
      </div>

      <nav className="flex-1 space-y-5 overflow-y-auto px-3 py-4" aria-label="主导航">
        {NAV_GROUPS.map((group) => (
          <div key={group.label}>
            <div className="mb-1.5 px-2.5 text-[11px] font-medium text-muted-foreground">
              {group.label}
            </div>
            <div className="space-y-0.5">
              {group.items.map((item) => {
                const active = activeTab === item.id
                return (
                  <button
                    key={item.id}
                    type="button"
                    onClick={() => onSelectTab(item.id)}
                    className={cn(
                      "flex w-full items-center gap-2.5 rounded-md px-2.5 py-2 text-left text-xs font-medium transition-colors cursor-pointer",
                      active
                        ? "bg-primary text-primary-foreground"
                        : "text-muted-foreground hover:bg-accent hover:text-foreground"
                    )}
                  >
                    <span
                      className={cn(
                        "font-mono text-[10px]",
                        active ? "opacity-80" : "opacity-60"
                      )}
                    >
                      {item.index}
                    </span>
                    <span>{item.title}</span>
                  </button>
                )
              })}
            </div>
          </div>
        ))}
      </nav>

      <div className="border-t p-3 space-y-2.5">
        <div
          className={cn(
            "rounded-md border px-3 py-2.5 text-xs",
            isOnline
              ? "border-emerald-500/30 bg-emerald-500/8"
              : "border-border bg-muted/40"
          )}
        >
          <div className="flex items-center gap-2 font-medium">
            <span
              className={cn(
                "h-2 w-2 rounded-full",
                isOnline ? "bg-emerald-500" : "bg-muted-foreground/50"
              )}
            />
            <span>
              {totalAcc > 0
                ? isOnline
                  ? `已托管 ${totalAcc} 个账号`
                  : `${totalAcc} 个账号需续登`
                : "尚未绑定账号"}
            </span>
          </div>
          <div className="mt-1 text-[11px] text-muted-foreground">
            {totalAcc > 0
              ? isOnline
                ? `在线 ${onlineAcc} · OpenAI 池 ${poolAcc} 个`
                : "请刷新 Token 或重新登录"
              : "支持多账号管理与混合路由"}
          </div>
        </div>

        <Button size="xs" className="w-full" onClick={onOpenLogin}>
          <Plus className="h-3.5 w-3.5" />
          添加 / 登录账号
        </Button>

        <div className="grid grid-cols-2 gap-1.5">
          <Button variant="ghost" size="xs" onClick={onToggleTheme} title="切换明暗主题">
            {theme === "dark" ? (
              <>
                <Sun className="h-3.5 w-3.5" />
                浅色
              </>
            ) : (
              <>
                <Moon className="h-3.5 w-3.5" />
                暗色
              </>
            )}
          </Button>
          <Button variant="ghost" size="xs" onClick={onAdminLogout}>
            <LogOut className="h-3.5 w-3.5" />
            退出
          </Button>
        </div>
      </div>
    </aside>
  )
}
