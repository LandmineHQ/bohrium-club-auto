import { useCallback, useEffect, useState } from "react"
import { Toaster, toast } from "sonner"
import { apiRequest, setUnauthorizedHandler } from "@/lib/api"
import { ConsoleSidebar } from "@/components/layout/ConsoleSidebar"
import { WorkspaceHeader } from "@/components/layout/WorkspaceHeader"
import { AdminLoginGate } from "@/components/auth/AdminLoginGate"
import { AccountLoginDialog } from "@/components/auth/AccountLoginDialog"
import { OverviewTab } from "@/components/overview/OverviewTab"
import { AssetsTab } from "@/components/assets/AssetsTab"
import { GatewayTab } from "@/components/gateway/GatewayTab"
import { AnalyticsTab } from "@/components/analytics/AnalyticsTab"
import { LogsTab } from "@/components/logs/LogsTab"
import { ReconTab } from "@/components/recon/ReconTab"
import { SettingsTab } from "@/components/settings/SettingsTab"
import type {
  ConsoleTab,
  GatewayStatusResponse,
  OverviewResponse,
  ReconStatusResponse,
  SettingsPayload,
} from "@/types/console"

export function App() {
  const [authState, setAuthState] = useState<"loading" | "authed" | "unauthed">("loading")
  const [version, setVersion] = useState("dev")
  const [theme, setTheme] = useState<"light" | "dark">(() => {
    try {
      return localStorage.getItem("brm_console_theme") === "dark" ? "dark" : "light"
    } catch {
      return "light"
    }
  })

  const [activeTab, setActiveTab] = useState<ConsoleTab>("overview")
  const [selectedAccountId, setSelectedAccountId] = useState("")
  const [loginDialogOpen, setLoginDialogOpen] = useState(false)

  const [overview, setOverview] = useState<OverviewResponse | null>(null)
  const [gateway, setGateway] = useState<GatewayStatusResponse | null>(null)
  const [settings, setSettings] = useState<SettingsPayload | null>(null)
  const [reconStatus, setReconStatus] = useState<ReconStatusResponse | null>(null)

  useEffect(() => {
    const root = document.documentElement
    root.classList.toggle("dark", theme === "dark")
    try {
      localStorage.setItem("brm_console_theme", theme)
    } catch {
      // ignore
    }
  }, [theme])

  useEffect(() => {
    setUnauthorizedHandler(() => setAuthState("unauthed"))
    return () => setUnauthorizedHandler(null)
  }, [])

  const loadOverview = useCallback(
    async (accountIdOverride?: string) => {
      try {
        const target = accountIdOverride !== undefined ? accountIdOverride : selectedAccountId
        const qs = target ? `?accountId=${encodeURIComponent(target)}` : ""
        const data = await apiRequest<OverviewResponse>(`/api/overview${qs}`)
        setOverview(data)
        if (data.version) setVersion(data.version)
        if (data.selectedAccountId) setSelectedAccountId(data.selectedAccountId)
      } catch (err) {
        toast.error("加载总览失败: " + (err instanceof Error ? err.message : ""))
      }
    },
    [selectedAccountId]
  )

  const loadGateway = useCallback(async () => {
    try {
      const data = await apiRequest<GatewayStatusResponse>("/api/gateway/status")
      setGateway(data)
    } catch (err) {
      toast.error("加载 OpenAI 网关状态失败: " + (err instanceof Error ? err.message : ""))
    }
  }, [])

  const loadSettings = useCallback(async () => {
    try {
      const res = await apiRequest<{ data: SettingsPayload }>("/api/settings")
      setSettings(res.data)
    } catch (err) {
      toast.error("读取配置失败: " + (err instanceof Error ? err.message : ""))
    }
  }, [])

  const loadRecon = useCallback(async () => {
    try {
      const data = await apiRequest<ReconStatusResponse>("/api/recon/status")
      setReconStatus(data)
    } catch {
      // ignore
    }
  }, [])

  const bootstrapAll = useCallback(
    async (focusId?: string) => {
      await Promise.all([loadOverview(focusId), loadGateway(), loadSettings(), loadRecon()])
    },
    [loadOverview, loadGateway, loadSettings, loadRecon]
  )

  useEffect(() => {
    const hasRunningJob =
      Boolean(overview?.scheduler?.running) ||
      (overview?.activeJobs?.some((j) => j.status === "running") ?? false)
    if (authState !== "authed" || activeTab !== "overview" || !hasRunningJob) {
      return
    }
    const timer = window.setInterval(() => {
      void loadOverview()
    }, 3000)
    return () => window.clearInterval(timer)
  }, [authState, activeTab, overview?.scheduler?.running, overview?.activeJobs, loadOverview])

  useEffect(() => {
    ;(async () => {
      try {
        const st = await apiRequest<{ authenticated?: boolean; version?: string }>(
          "/api/auth/status"
        )
        if (st.version) setVersion(st.version)
        if (st.authenticated) {
          setAuthState("authed")
          await bootstrapAll("")
        } else {
          setAuthState("unauthed")
        }
      } catch {
        setAuthState("unauthed")
      }
    })()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  async function handleSelectAccount(id: string) {
    setSelectedAccountId(id)
    await loadOverview(id)
  }

  async function handleAdminLogout() {
    await apiRequest("/api/auth/logout", { method: "POST" }).catch(() => {})
    setAuthState("unauthed")
  }

  return (
    <>
      <Toaster position="top-right" richColors theme={theme} />

      {authState === "loading" ? (
        <div className="flex min-h-screen items-center justify-center bg-background text-xs text-muted-foreground">
          正在加载控制台...
        </div>
      ) : authState === "unauthed" ? (
        <AdminLoginGate
          onAuthenticated={async () => {
            setAuthState("authed")
            await bootstrapAll("")
          }}
        />
      ) : (
        <div className="flex min-h-screen flex-col md:flex-row bg-background text-foreground">
          <ConsoleSidebar
            version={version}
            activeTab={activeTab}
            onSelectTab={(tab) => {
              setActiveTab(tab)
              if (tab === "gateway") void loadGateway()
            }}
            summary={overview?.accountSummary}
            accountCount={overview?.accounts?.length || 0}
            theme={theme}
            onToggleTheme={() => setTheme((t) => (t === "dark" ? "light" : "dark"))}
            onOpenLogin={() => setLoginDialogOpen(true)}
            onAdminLogout={handleAdminLogout}
          />

          <div className="flex min-w-0 flex-1 flex-col">
            <WorkspaceHeader
              activeTab={activeTab}
              taskDate={overview?.tasks?.taskDate}
              scheduler={overview?.scheduler}
            />

            <main className="mx-auto w-full max-w-7xl flex-1 p-5">
              {activeTab === "overview" && (
                <OverviewTab
                  data={overview}
                  selectedAccountId={selectedAccountId}
                  onSelectAccount={handleSelectAccount}
                  onOpenLogin={() => setLoginDialogOpen(true)}
                  onRefreshAll={async () => {
                    await Promise.all([loadOverview(), loadGateway()])
                  }}
                />
              )}

              {activeTab === "assets" && (
                <AssetsTab
                  accounts={overview?.accounts || []}
                  selectedAccountId={selectedAccountId}
                  onSelectAccount={handleSelectAccount}
                  assets={overview?.assets}
                  onRefresh={() => loadOverview()}
                />
              )}

              {activeTab === "gateway" && (
                <GatewayTab
                  data={gateway}
                  onRefresh={loadGateway}
                  onRefreshAll={async () => {
                    await Promise.all([loadOverview(), loadGateway()])
                  }}
                />
              )}

              {activeTab === "analytics" && (
                <AnalyticsTab active={activeTab === "analytics"} onRetentionUpdated={loadGateway} />
              )}

              {activeTab === "logs" && (
                <LogsTab
                  accounts={overview?.accounts || []}
                  initialLogs={overview?.logs || []}
                  active={activeTab === "logs"}
                />
              )}

              {activeTab === "recon" && <ReconTab status={reconStatus} onRefresh={loadRecon} />}

              {activeTab === "settings" && (
                <SettingsTab
                  settings={settings}
                  onSaved={async () => {
                    await Promise.all([loadSettings(), loadOverview()])
                  }}
                />
              )}
            </main>
          </div>

          <AccountLoginDialog
            open={loginDialogOpen}
            onOpenChange={setLoginDialogOpen}
            onSuccess={async (accId) => {
              if (accId) setSelectedAccountId(accId)
              await Promise.all([loadOverview(accId), loadGateway()])
            }}
          />
        </div>
      )}
    </>
  )
}
