import { useState } from "react"
import { toast } from "sonner"
import { apiRequest } from "@/lib/api"
import { formatTime } from "@/lib/utils"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card"
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table"
import type { ReconDiffData, ReconStatusResponse } from "@/types/console"

interface ReconTabProps {
  status: ReconStatusResponse | null
  onRefresh: () => Promise<void>
}

export function ReconTab({ status, onRefresh }: ReconTabProps) {
  const [pulling, setPulling] = useState(false)
  const [diffing, setDiffing] = useState(false)
  const [diff, setDiff] = useState<ReconDiffData | null>(null)

  async function handlePull() {
    setPulling(true)
    try {
      const res = await apiRequest<{ data?: { assets?: unknown[] } }>("/api/recon/pull", {
        method: "POST",
      })
      toast.success(`基线已更新（共 ${res.data?.assets?.length || 0} 个静态资源）`)
      await onRefresh()
    } catch (err) {
      toast.error("Recon Pull 失败: " + (err instanceof Error ? err.message : ""))
    } finally {
      setPulling(false)
    }
  }

  async function handleDiff() {
    setDiffing(true)
    try {
      const res = await apiRequest<{ data: ReconDiffData }>("/api/recon/diff", { method: "POST" })
      setDiff(res.data)
      if (res.data.same) toast.success(res.data.summary || "比对完成：无变化")
      else toast.warning(res.data.summary || "发现上游更新")
    } catch (err) {
      toast.error("Recon Diff 失败: " + (err instanceof Error ? err.message : ""))
    } finally {
      setDiffing(false)
    }
  }

  const markers = status?.markers || {}
  const diffRows: Array<{ kind: string; variant: "err" | "info" | "warn" | "muted"; text: string }> =
    diff
      ? [
          ...(diff.markerChanges || []).map((x) => ({
            kind: "契约变更",
            variant: "err" as const,
            text: x,
          })),
          ...(diff.added || []).map((x) => ({ kind: "新增", variant: "info" as const, text: x.url })),
          ...(diff.changed || []).map((x) => ({
            kind: "变更",
            variant: "warn" as const,
            text: x.url,
          })),
          ...(diff.removed || []).map((x) => ({
            kind: "移除",
            variant: "muted" as const,
            text: x.url,
          })),
        ]
      : []

  return (
    <Card>
      <CardHeader>
        <div>
          <CardTitle>前端 Recon 快照与漂移检测</CardTitle>
          <CardDescription>
            在本地 <code>recon/</code>（已 gitignore）建立基线，对比线上 Bohrium 静态资源 SHA-256 与关键 API 契约标记。
          </CardDescription>
        </div>
        <div className="flex items-center gap-2">
          <Button variant="outline" size="sm" disabled={pulling} onClick={handlePull}>
            {pulling ? "拉取中..." : "更新基线 (Pull)"}
          </Button>
          <Button size="sm" disabled={diffing} onClick={handleDiff}>
            {diffing ? "比对中..." : "检测变更 (Diff)"}
          </Button>
        </div>
      </CardHeader>

      <CardContent className="space-y-4">
        <div className="rounded-lg border bg-muted/25 p-4 text-xs space-y-2">
          {!status ? (
            <div className="text-muted-foreground">加载基线状态中...</div>
          ) : !status.baselineExists ? (
            <div>
              <strong>尚无本地基线快照</strong>（路径：<code>{status.manifestPath}</code>
              ）。请点击「更新基线 (Pull)」建立首次快照。
            </div>
          ) : (
            <>
              <div className="tabular-nums">
                <strong>本地基线时间：</strong>
                {formatTime(status.fetchedAt)} · 资源数：<strong>{status.assetCount}</strong> ·{" "}
                {(Number(status.totalBytes || 0) / 1024).toFixed(1)} KB
              </div>
              <div className="text-muted-foreground">
                基线路径：<code>{status.manifestPath}</code>
              </div>
              <div className="flex flex-wrap gap-1.5 pt-1">
                <Badge variant={markers.plazaTasksEndpoint ? "ok" : "warn"}>
                  /api/plaza/tasks
                </Badge>
                <Badge variant={markers.plazaClaimEndpoint ? "ok" : "warn"}>
                  /api/plaza/tasks/claim
                </Badge>
                <Badge variant={markers.gatewayHeader ? "ok" : "warn"}>
                  X-Plaza-Daily-Task-Gateway
                </Badge>
                <Badge variant={markers.wechatQrEndpoint ? "ok" : "warn"}>WeChat QR API</Badge>
              </div>
            </>
          )}
        </div>

        {diff && (
          <div className="rounded-lg border p-4 space-y-3">
            <div className="flex flex-wrap items-center gap-2 text-xs">
              <Badge variant={diff.same ? "ok" : "warn"}>
                {diff.same ? "无变化" : "发现上游更新"}
              </Badge>
              <strong>{diff.summary}</strong>
            </div>

            {diffRows.length > 0 && (
              <Table>
                <TableHeader>
                  <TableRow>
                    <TableHead>类型</TableHead>
                    <TableHead>路径 / 详情</TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {diffRows.map((row, idx) => (
                    <TableRow key={idx}>
                      <TableCell>
                        <Badge variant={row.variant}>{row.kind}</Badge>
                      </TableCell>
                      <TableCell className="break-all font-mono text-[11px]">{row.text}</TableCell>
                    </TableRow>
                  ))}
                </TableBody>
              </Table>
            )}
          </div>
        )}
      </CardContent>
    </Card>
  )
}
