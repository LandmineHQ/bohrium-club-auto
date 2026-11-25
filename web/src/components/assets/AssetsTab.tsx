import { toast } from "sonner"
import { RefreshCw } from "lucide-react"
import { formatNum, formatQuota, formatTime } from "@/lib/utils"
import { Button } from "@/components/ui/button"
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card"
import { NativeSelect } from "@/components/ui/input"
import {
  AlertBox,
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table"
import type { AccountSummaryView, PointsAndCapacity } from "@/types/console"

interface AssetsTabProps {
  accounts: AccountSummaryView[]
  selectedAccountId: string
  onSelectAccount: (id: string) => void
  assets?: PointsAndCapacity | null
  onRefresh: () => Promise<void>
}

export function AssetsTab({
  accounts,
  selectedAccountId,
  onSelectAccount,
  assets,
  onRefresh,
}: AssetsTabProps) {
  const st = assets?.storage
  const metrics = assets
    ? [
        ["个人可用光子", formatNum(assets.photonBalance)],
        [
          "组织余额 / 上限",
          `${formatNum(assets.orgBalance)} / ${formatNum(assets.orgBalanceLimit)}`,
        ],
        ["钱包现金余额", `¥ ${formatNum(assets.walletBalance)}`],
        ["7 日内过期光子", formatNum(assets.expiring7DayPhotons)],
        ["知识库剩余存储", `${st?.remainingHuman || "--"} / ${st?.totalHuman || "--"}`],
        ["文献检索配额", formatQuota(assets.paperQuota)],
        ["知识库对话配额", formatQuota(assets.knowledgeChat)],
        ["深度研究配额", formatQuota(assets.deepResearch)],
      ]
    : []

  const lots = assets?.expiringLots || []
  const hist = assets?.recentIntegralHistory || []

  return (
    <div className="space-y-5">
      <Card>
        <CardHeader>
          <div>
            <div className="flex flex-wrap items-center gap-2">
              <CardTitle>积分、配额与容量明细</CardTitle>
              <NativeSelect
                aria-label="选择查看资产的账号"
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
            </div>
            <CardDescription>
              聚合所选账号的光子余额、钱包、7 日即将过期批次、近期流水、会员配额与知识库存储空间。
            </CardDescription>
          </div>
          <Button
            variant="outline"
            size="sm"
            onClick={async () => {
              await onRefresh()
              toast.success("资产数据已刷新")
            }}
          >
            <RefreshCw className="h-3.5 w-3.5" />
            刷新资产
          </Button>
        </CardHeader>

        <CardContent className="space-y-4">
          {assets?.warnings && assets.warnings.length > 0 && (
            <AlertBox variant="warn">
              部分子接口提示：{assets.warnings.join("；")}
            </AlertBox>
          )}

          {!assets ? (
            <div className="py-6 text-center text-xs text-muted-foreground">
              请先添加并选择玻尔账号以查看实时积分与容量明细。
            </div>
          ) : (
            <div className="grid grid-cols-2 gap-3 lg:grid-cols-4">
              {metrics.map(([label, val]) => (
                <div key={label} className="rounded-md border bg-muted/25 p-3">
                  <div className="text-[11px] text-muted-foreground">{label}</div>
                  <div className="mt-1 text-base font-semibold tabular-nums">{val}</div>
                </div>
              ))}
            </div>
          )}
        </CardContent>
      </Card>

      <div className="grid grid-cols-1 gap-4 lg:grid-cols-2">
        <Card>
          <CardHeader className="py-3">
            <CardTitle>即将过期光子批次</CardTitle>
          </CardHeader>
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>剩余光子</TableHead>
                <TableHead>来源</TableHead>
                <TableHead>获取时间</TableHead>
                <TableHead>过期时间</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {lots.length === 0 ? (
                <TableRow>
                  <TableCell colSpan={4} className="py-6 text-center text-muted-foreground">
                    暂无即将过期光子批次
                  </TableCell>
                </TableRow>
              ) : (
                lots.map((l, idx) => (
                  <TableRow key={idx}>
                    <TableCell className="font-semibold tabular-nums">
                      {formatNum(l.remaining)}
                    </TableCell>
                    <TableCell>{l.description || l.sourceType || "--"}</TableCell>
                    <TableCell className="tabular-nums">{l.createdTime || "--"}</TableCell>
                    <TableCell className="tabular-nums">{l.expireTime || "--"}</TableCell>
                  </TableRow>
                ))
              )}
            </TableBody>
          </Table>
        </Card>

        <Card>
          <CardHeader className="py-3">
            <CardTitle>近期光子流水</CardTitle>
          </CardHeader>
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>变动</TableHead>
                <TableHead>说明</TableHead>
                <TableHead>时间</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {hist.length === 0 ? (
                <TableRow>
                  <TableCell colSpan={3} className="py-6 text-center text-muted-foreground">
                    暂无近期光子流水
                  </TableCell>
                </TableRow>
              ) : (
                hist.map((r, idx) => (
                  <TableRow key={idx}>
                    <TableCell
                      className={`font-semibold tabular-nums ${
                        r.amount >= 0
                          ? "text-emerald-700 dark:text-emerald-400"
                          : "text-destructive"
                      }`}
                    >
                      {r.amount > 0 ? `+${formatNum(r.amount)}` : formatNum(r.amount)}
                    </TableCell>
                    <TableCell>
                      <div>{r.description || r.type || "--"}</div>
                      {r.expireAt && (
                        <div className="text-[11px] text-muted-foreground">
                          有效期至 {r.expireAt}
                        </div>
                      )}
                    </TableCell>
                    <TableCell className="whitespace-nowrap tabular-nums">
                      {formatTime(r.createdAt)}
                    </TableCell>
                  </TableRow>
                ))
              )}
            </TableBody>
          </Table>
        </Card>
      </div>
    </div>
  )
}
