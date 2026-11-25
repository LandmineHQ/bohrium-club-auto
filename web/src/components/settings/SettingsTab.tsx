import { useEffect, useState, type FormEvent } from "react"
import { toast } from "sonner"
import { apiRequest } from "@/lib/api"
import { Button } from "@/components/ui/button"
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card"
import { Input } from "@/components/ui/input"
import { Label, Switch } from "@/components/ui/primitives"
import type { SettingsPayload } from "@/types/console"

interface SettingsTabProps {
  settings: SettingsPayload | null
  onSaved: () => Promise<void>
}

export function SettingsTab({ settings, onSaved }: SettingsTabProps) {
  const [schedEnabled, setSchedEnabled] = useState(true)
  const [autoClaim, setAutoClaim] = useState(true)
  const [autoRefresh, setAutoRefresh] = useState(true)
  const [timeHHMM, setTimeHHMM] = useState("08:15")
  const [timezone, setTimezone] = useState("Asia/Shanghai")
  const [jitterMinutes, setJitterMinutes] = useState(15)
  const [taskDelaySec, setTaskDelaySec] = useState(3)
  const [cancelAfterMs, setCancelAfterMs] = useState(450)
  const [userAgent, setUserAgent] = useState("")
  const [proxyUrl, setProxyUrl] = useState("")
  const [newAdminPassword, setNewAdminPassword] = useState("")
  const [saving, setSaving] = useState(false)

  useEffect(() => {
    if (!settings) return
    setSchedEnabled(!!settings.schedule?.enabled)
    setAutoClaim(!!settings.schedule?.autoClaim)
    setAutoRefresh(!!settings.schedule?.autoRefresh)
    setTimeHHMM(settings.schedule?.timeHHMM || "08:15")
    setTimezone(settings.schedule?.timezone || "Asia/Shanghai")
    setJitterMinutes(settings.schedule?.jitterMinutes ?? 15)
    setTaskDelaySec(settings.taskDelaySec ?? 3)
    setCancelAfterMs(settings.cancelAfterMs ?? 450)
    setUserAgent(settings.userAgent || "")
    setProxyUrl(settings.proxyUrl || "")
    setNewAdminPassword("")
  }, [settings])

  async function handleSubmit(e: FormEvent) {
    e.preventDefault()
    setSaving(true)
    try {
      const res = await apiRequest<{ message?: string }>("/api/settings", {
        method: "PUT",
        body: JSON.stringify({
          userAgent: userAgent.trim(),
          proxyUrl: proxyUrl.trim(),
          taskDelaySec: Number(taskDelaySec),
          cancelAfterMs: Number(cancelAfterMs),
          newAdminPassword,
          schedule: {
            enabled: schedEnabled,
            timeHHMM: timeHHMM.trim(),
            timezone: timezone.trim(),
            jitterMinutes: Number(jitterMinutes),
            autoClaim,
            autoRefresh,
          },
        }),
      })
      toast.success(res.message || "配置已保存")
      setNewAdminPassword("")
      await onSaved()
    } catch (err) {
      toast.error("保存配置失败: " + (err instanceof Error ? err.message : ""))
    } finally {
      setSaving(false)
    }
  }

  return (
    <Card>
      <CardHeader>
        <div>
          <CardTitle>多账号定时调度与系统配置</CardTitle>
          <CardDescription>
            修改立即原子写入 <code>data/config.json</code>；多账号自动按账号 ID 在基准时间 + 随机波动窗口内错峰签到。
          </CardDescription>
        </div>
      </CardHeader>

      <CardContent>
        <form onSubmit={handleSubmit} className="space-y-5 max-w-3xl">
          <div className="space-y-3 rounded-lg border p-4">
            <div className="flex items-start justify-between gap-3">
              <div className="text-xs">
                <div className="font-medium">启用每日多账号随机波动签到</div>
                <div className="text-[11px] text-muted-foreground">
                  所有已开启签到的账号将在设定窗口内各自随机错峰执行每日任务
                </div>
              </div>
              <Switch checked={schedEnabled} onCheckedChange={setSchedEnabled} />
            </div>
            <div className="flex items-start justify-between gap-3">
              <div className="text-xs">
                <div className="font-medium">自动一键领取任务</div>
                <div className="text-[11px] text-muted-foreground">
                  执行前自动领取未领取的每日任务
                </div>
              </div>
              <Switch checked={autoClaim} onCheckedChange={setAutoClaim} />
            </div>
            <div className="flex items-start justify-between gap-3">
              <div className="text-xs">
                <div className="font-medium">后台自动续期全部账号 Token</div>
                <div className="text-[11px] text-muted-foreground">
                  到期前 48h 自动调用 refresh_token 保持账号长期在线
                </div>
              </div>
              <Switch checked={autoRefresh} onCheckedChange={setAutoRefresh} />
            </div>
          </div>

          <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
            <div className="space-y-1.5">
              <Label htmlFor="set-time">每日起始基准时间 (HH:MM)</Label>
              <Input
                id="set-time"
                value={timeHHMM}
                onChange={(e) => setTimeHHMM(e.target.value)}
                placeholder="08:15"
              />
            </div>
            <div className="space-y-1.5">
              <Label htmlFor="set-tz">时区</Label>
              <Input
                id="set-tz"
                value={timezone}
                onChange={(e) => setTimezone(e.target.value)}
                placeholder="Asia/Shanghai"
              />
            </div>
            <div className="space-y-1.5">
              <Label htmlFor="set-jitter">随机波动窗口（分钟，0-720）</Label>
              <Input
                id="set-jitter"
                type="number"
                min={0}
                max={720}
                value={jitterMinutes}
                onChange={(e) => setJitterMinutes(Number(e.target.value))}
              />
            </div>
            <div className="space-y-1.5">
              <Label htmlFor="set-delay">单账号任务间隔（秒）</Label>
              <Input
                id="set-delay"
                type="number"
                min={0}
                max={60}
                value={taskDelaySec}
                onChange={(e) => setTaskDelaySec(Number(e.target.value))}
              />
            </div>
            <div className="space-y-1.5">
              <Label htmlFor="set-cancel">取消延迟（毫秒）</Label>
              <Input
                id="set-cancel"
                type="number"
                min={100}
                max={10000}
                value={cancelAfterMs}
                onChange={(e) => setCancelAfterMs(Number(e.target.value))}
              />
            </div>
            <div className="space-y-1.5 sm:col-span-2">
              <Label htmlFor="set-ua">出口 Chrome User-Agent（仅用于 Web 签到与登录）</Label>
              <Input id="set-ua" value={userAgent} onChange={(e) => setUserAgent(e.target.value)} />
            </div>
            <div className="space-y-1.5 sm:col-span-2">
              <Label htmlFor="set-proxy">出站代理 URL（留空直连）</Label>
              <Input
                id="set-proxy"
                value={proxyUrl}
                onChange={(e) => setProxyUrl(e.target.value)}
                placeholder="http://127.0.0.1:7890"
              />
            </div>
            <div className="space-y-1.5 sm:col-span-2">
              <Label htmlFor="set-new-pw">修改控制台管理员密码（留空不变）</Label>
              <Input
                id="set-new-pw"
                type="password"
                value={newAdminPassword}
                onChange={(e) => setNewAdminPassword(e.target.value)}
                placeholder="至少 6 位字符"
                autoComplete="new-password"
              />
            </div>
          </div>

          <Button type="submit" disabled={saving}>
            {saving ? "保存中..." : "保存配置"}
          </Button>
        </form>
      </CardContent>
    </Card>
  )
}
