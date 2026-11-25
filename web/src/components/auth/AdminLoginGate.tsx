import { useState, type FormEvent } from "react"
import { Lock } from "lucide-react"
import { apiRequest } from "@/lib/api"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/primitives"
import { AlertBox } from "@/components/ui/table"

export function AdminLoginGate({ onAuthenticated }: { onAuthenticated: () => Promise<void> }) {
  const [password, setPassword] = useState("")
  const [error, setError] = useState("")
  const [submitting, setSubmitting] = useState(false)

  async function handleSubmit(e: FormEvent) {
    e.preventDefault()
    if (!password) return
    setError("")
    setSubmitting(true)
    try {
      await apiRequest("/api/auth/login", {
        method: "POST",
        body: JSON.stringify({ password }),
      })
      setPassword("")
      await onAuthenticated()
    } catch (err) {
      setError(err instanceof Error ? err.message : "登录失败")
    } finally {
      setSubmitting(false)
    }
  }

  return (
    <div className="flex min-h-screen items-center justify-center bg-background p-4">
      <div className="w-full max-w-sm rounded-xl border bg-card p-6 shadow-sm">
        <div className="mb-4 flex items-center gap-2 text-xs font-mono text-muted-foreground">
          <span className="h-2.5 w-2.5 rounded-xs bg-foreground" />
          <span>BOHRIUM CLUB AUTO</span>
        </div>

        <h1 className="text-lg font-semibold tracking-tight">控制台认证</h1>
        <p className="mt-1.5 text-xs text-muted-foreground leading-relaxed">
          输入管理员密码以管理多账号登录、随机波动签到、资产与 OpenAI 混合路由池。
        </p>

        <form onSubmit={handleSubmit} className="mt-5 space-y-4">
          <div className="space-y-1.5">
            <Label htmlFor="admin-password-input">管理员密码</Label>
            <Input
              id="admin-password-input"
              type="password"
              value={password}
              onChange={(e) => setPassword(e.target.value)}
              placeholder="BOHRIUM_ADMIN_PASSWORD"
              autoComplete="current-password"
              autoFocus
              required
            />
          </div>

          {error && <AlertBox variant="err">{error}</AlertBox>}

          <Button type="submit" className="w-full" disabled={submitting}>
            <Lock className="h-3.5 w-3.5" />
            {submitting ? "验证中..." : "进入控制台"}
          </Button>
        </form>

        <p className="mt-4 text-[11px] text-muted-foreground">
          首次启动密码可在启动日志或 <code className="rounded bg-muted px-1 py-0.5">data/config.json</code> 中查看。
        </p>
      </div>
    </div>
  )
}
