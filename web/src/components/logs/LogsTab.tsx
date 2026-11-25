import { useCallback, useEffect, useState } from "react"
import { toast } from "sonner"
import { RefreshCw } from "lucide-react"
import { apiRequest } from "@/lib/api"
import { formatTime } from "@/lib/utils"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Card, CardDescription, CardHeader, CardTitle } from "@/components/ui/card"
import { NativeSelect } from "@/components/ui/input"
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table"
import type { AccountSummaryView, RunLogItem } from "@/types/console"

interface LogsTabProps {
  accounts: AccountSummaryView[]
  initialLogs: RunLogItem[]
  active: boolean
}

export function LogsTab({ accounts, initialLogs, active }: LogsTabProps) {
  const [accountFilter, setAccountFilter] = useState("")
  const [logs, setLogs] = useState<RunLogItem[]>(initialLogs)
  const [loading, setLoading] = useState(false)

  useEffect(() => {
    if (!accountFilter && initialLogs.length > 0) {
      setLogs(initialLogs)
    }
  }, [initialLogs, accountFilter])

  const loadLogs = useCallback(async (accId: string, showToast = false) => {
    setLoading(true)
    try {
      const qs = accId ? `?limit=120&accountId=${encodeURIComponent(accId)}` : "?limit=120"
      const res = await apiRequest<{ data?: RunLogItem[] }>(`/api/logs${qs}`)
      setLogs(res.data || [])
      if (showToast) toast.success("日志已刷新")
    } catch (err) {
      toast.error("加载日志失败: " + (err instanceof Error ? err.message : ""))
    } finally {
      setLoading(false)
    }
  }, [])

  useEffect(() => {
    if (active) {
      void loadLogs(accountFilter)
    }
  }, [active, accountFilter, loadLogs])

  return (
    <Card>
      <CardHeader>
        <div>
          <div className="flex flex-wrap items-center gap-2">
            <CardTitle>执行与操作日志</CardTitle>
            <NativeSelect
              aria-label="按账号筛选日志"
              value={accountFilter}
              onChange={(e) => setAccountFilter(e.target.value)}
            >
              <option value="">全部账号</option>
              {accounts.map((a) => (
                <option key={a.id} value={a.id}>
                  {a.displayName} (UID {a.userId})
                </option>
              ))}
            </NativeSelect>
          </div>
          <CardDescription>
            持久化保存在 SQLite（最近 1000 条），记录各账号签到、续期、登录与 Recon 操作。
          </CardDescription>
        </div>
        <Button
          variant="outline"
          size="sm"
          disabled={loading}
          onClick={() => loadLogs(accountFilter, true)}
        >
          <RefreshCw className={`h-3.5 w-3.5 ${loading ? "animate-spin" : ""}`} />
          刷新日志
        </Button>
      </CardHeader>

      <Table>
        <TableHeader>
          <TableRow>
            <TableHead>时间</TableHead>
            <TableHead>账号</TableHead>
            <TableHead>来源</TableHead>
            <TableHead>任务 / 动作</TableHead>
            <TableHead>状态</TableHead>
            <TableHead>耗时</TableHead>
            <TableHead>详情</TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {logs.length === 0 ? (
            <TableRow>
              <TableCell colSpan={7} className="py-8 text-center text-muted-foreground">
                暂无执行日志
              </TableCell>
            </TableRow>
          ) : (
            logs.map((l) => {
              const accLabel =
                l.nickname || (l.accountUid ? `UID ${l.accountUid}` : l.accountId || "全局")
              return (
                <TableRow key={l.id}>
                  <TableCell className="tabular-nums whitespace-nowrap">
                    {formatTime(l.timestamp)}
                  </TableCell>
                  <TableCell>
                    <Badge variant="mono">{accLabel}</Badge>
                  </TableCell>
                  <TableCell>
                    <code className="text-[11px]">{l.trigger || "manual"}</code>
                  </TableCell>
                  <TableCell>{l.taskTitle || l.action || "--"}</TableCell>
                  <TableCell>
                    {l.skipped ? (
                      <Badge variant="muted">跳过</Badge>
                    ) : l.success ? (
                      <Badge variant="ok">成功</Badge>
                    ) : (
                      <Badge variant="err">失败</Badge>
                    )}
                  </TableCell>
                  <TableCell className="tabular-nums whitespace-nowrap">
                    {l.durationMs ? `${l.durationMs} ms` : "--"}
                  </TableCell>
                  <TableCell className="max-w-md">
                    <div className="break-words">{l.message}</div>
                    {l.meta?.promptPreview && (
                      <div className="mt-0.5 text-[11px] text-muted-foreground">
                        提问：{l.meta.promptPreview}
                      </div>
                    )}
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
