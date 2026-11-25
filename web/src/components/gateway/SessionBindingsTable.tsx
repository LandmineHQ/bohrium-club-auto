import { toast } from "sonner"
import { RefreshCw, Trash2 } from "lucide-react"
import { apiRequest } from "@/lib/api"
import { formatNum, formatTime, formatTTLRemaining } from "@/lib/utils"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Card, CardDescription, CardHeader, CardTitle } from "@/components/ui/card"
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table"
import type { SessionBindingItem } from "@/types/console"

interface SessionBindingsTableProps {
  bindings: SessionBindingItem[]
  onRefresh: () => Promise<void>
}

export function SessionBindingsTable({ bindings, onRefresh }: SessionBindingsTableProps) {
  async function deleteOne(hash: string) {
    try {
      await apiRequest(`/api/gateway/bindings/${encodeURIComponent(hash)}`, {
        method: "DELETE",
      })
      toast.success("已解除该会话路由绑定")
      await onRefresh()
    } catch (err) {
      toast.error("解绑失败: " + (err instanceof Error ? err.message : ""))
    }
  }

  async function clearAll() {
    if (
      !window.confirm(
        "确定清空所有 SQLite 会话路由绑定吗？后续请求将按当前余额权重重新抽签绑定。"
      )
    ) {
      return
    }
    try {
      const res = await apiRequest<{ cleared?: number }>("/api/gateway/bindings", {
        method: "DELETE",
      })
      toast.warning(`已清空 ${res.cleared || 0} 条会话绑定`)
      await onRefresh()
    } catch (err) {
      toast.error("清空失败: " + (err instanceof Error ? err.message : ""))
    }
  }

  return (
    <Card>
      <CardHeader className="py-3.5">
        <div>
          <div className="flex flex-wrap items-center gap-2">
            <CardTitle>活跃会话路由绑定（SQLite · 最长缓存 7 天）</CardTitle>
            <Badge variant="mono">{bindings.length} 条</Badge>
          </div>
          <CardDescription>
            同会话多轮对话与工具续跑自动粘性命中同一账号，到期满 7 天后自动失效并由后台清理协程物理删除。
          </CardDescription>
        </div>
        <div className="flex items-center gap-2">
          <Button
            variant="ghost"
            size="xs"
            onClick={async () => {
              await onRefresh()
              toast.success("已刷新会话绑定列表")
            }}
          >
            <RefreshCw className="h-3.5 w-3.5" />
            刷新
          </Button>
          <Button variant="destructiveOutline" size="xs" onClick={clearAll}>
            <Trash2 className="h-3.5 w-3.5" />
            清空全部绑定
          </Button>
        </div>
      </CardHeader>

      <Table>
        <TableHeader>
          <TableRow>
            <TableHead>Session Hash</TableHead>
            <TableHead>绑定账号</TableHead>
            <TableHead>命中次数</TableHead>
            <TableHead>最近模型</TableHead>
            <TableHead>绑定时间</TableHead>
            <TableHead>剩余有效期</TableHead>
            <TableHead className="text-right">操作</TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {bindings.length === 0 ? (
            <TableRow>
              <TableCell colSpan={7} className="py-6 text-center text-muted-foreground">
                暂无活跃会话绑定（有新请求通过 OpenAI 路由池后自动生成）
              </TableCell>
            </TableRow>
          ) : (
            bindings.map((b) => {
              const accName =
                b.accountLabel ||
                b.accountNickname ||
                (b.accountUid ? `UID ${b.accountUid}` : b.accountId)
              const shortHash = b.sessionHash ? `${b.sessionHash.slice(0, 14)}…` : "--"
              return (
                <TableRow key={b.sessionHash}>
                  <TableCell>
                    <code className="rounded bg-muted px-1.5 py-0.5 text-[11px]" title={b.sessionHash}>
                      {shortHash}
                    </code>
                  </TableCell>
                  <TableCell>
                    <strong className="font-semibold">{accName}</strong>{" "}
                    <span className="text-[11px] text-muted-foreground">
                      UID {b.accountUid || "--"}
                    </span>
                  </TableCell>
                  <TableCell className="tabular-nums">{formatNum(b.hitCount)}</TableCell>
                  <TableCell>
                    <Badge variant="mono">{b.modelHint || "--"}</Badge>
                  </TableCell>
                  <TableCell className="tabular-nums whitespace-nowrap">
                    {formatTime(b.createdAt)}
                  </TableCell>
                  <TableCell className="tabular-nums">
                    <Badge variant="mono">{formatTTLRemaining(b.expiresAt)}</Badge>
                  </TableCell>
                  <TableCell className="text-right">
                    <Button
                      variant="ghost"
                      size="xs"
                      className="text-destructive hover:text-destructive"
                      onClick={() => deleteOne(b.sessionHash)}
                    >
                      解绑
                    </Button>
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
