import { useEffect, useRef, useState } from "react"
import { toast } from "sonner"
import { ExternalLink, QrCode } from "lucide-react"
import { apiRequest } from "@/lib/api"
import {
  runTencentCaptcha,
  TencentCaptchaCancelledError,
  type CaptchaProofResult,
} from "@/lib/tencentCaptcha"
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs"
import { Button } from "@/components/ui/button"
import { Input, Textarea } from "@/components/ui/input"
import { Label } from "@/components/ui/primitives"

interface AccountLoginResult {
  isNew?: boolean
  account?: { id?: string }
  data?: { loggedIn?: boolean; status?: string }
}

interface AccountLoginDialogProps {
  open: boolean
  onOpenChange: (open: boolean) => void
  onSuccess: (accountId?: string) => Promise<void>
}

const OFFICIAL_LOGIN_URL = "https://platform.bohrium.com/login?business=Bohrium&lang=zh-cn"

const OAUTH_PROVIDERS = [
  { id: "google", label: "Google 登录" },
  { id: "apple", label: "Apple 登录" },
  { id: "carsi", label: "CARSI 高校" },
  { id: "feishu", label: "飞书 Feishu" },
  { id: "pku", label: "北大 IAAA" },
]

export function AccountLoginDialog({
  open,
  onOpenChange,
  onSuccess,
}: AccountLoginDialogProps) {
  const [label, setLabel] = useState("")
  const [activeTab, setActiveTab] = useState("wechat")

  // WeChat QR state
  const [qrUrl, setQrUrl] = useState("")
  const [qrStatus, setQrStatus] = useState("等待发起扫码")
  const [qrBusy, setQrBusy] = useState(false)
  const pollTimerRef = useRef<number | null>(null)
  const labelRef = useRef(label)
  labelRef.current = label

  // OAuth state
  const [oauthProvider, setOauthProvider] = useState("google")
  const [oauthAuthUrl, setOauthAuthUrl] = useState("")
  const [oauthCallback, setOauthCallback] = useState("")
  const [oauthBusy, setOauthBusy] = useState(false)

  // OTP state
  const [otpKind, setOtpKind] = useState<"phone" | "email">("phone")
  const [otpCountryCode, setOtpCountryCode] = useState("86")
  const [otpOversea, setOtpOversea] = useState(false)
  const [otpTarget, setOtpTarget] = useState("")
  const [otpCode, setOtpCode] = useState("")
  const [otpSending, setOtpSending] = useState(false)
  const [otpCooldown, setOtpCooldown] = useState(0)
  const [otpSubmitting, setOtpSubmitting] = useState(false)

  // Password state
  const [pwAccount, setPwAccount] = useState("")
  const [pwSecret, setPwSecret] = useState("")
  const [pwBusy, setPwBusy] = useState(false)

  // Interactive Tencent Captcha modal guard (prevents Radix Dialog from blocking pointer events or dismissing)
  const [captchaOpen, setCaptchaOpen] = useState(false)

  // Manual token state
  const [rawToken, setRawToken] = useState("")
  const [tokenBusy, setTokenBusy] = useState(false)

  useEffect(() => {
    if (otpCooldown <= 0) return
    const timer = window.setInterval(() => {
      setOtpCooldown((prev) => (prev <= 1 ? 0 : prev - 1))
    }, 1000)
    return () => window.clearInterval(timer)
  }, [otpCooldown])

  async function acquireCaptchaProof(
    scene: "otp" | "password",
    account?: string
  ): Promise<CaptchaProofResult | null> {
    const prep = await apiRequest<{
      data?: { skip?: boolean; captchaAppId?: string; aidEncrypted?: string }
    }>("/api/bohrium/captcha/prepare", {
      method: "POST",
      body: JSON.stringify({ scene, account: account || "" }),
    })
    const info = prep.data
    if (!info || info.skip) {
      return null
    }
    if (!info.captchaAppId || !info.aidEncrypted) {
      throw new Error("未获取到有效的图形验证码初始化参数")
    }
    setCaptchaOpen(true)
    try {
      return await runTencentCaptcha({
        captchaAppId: info.captchaAppId,
        aidEncrypted: info.aidEncrypted,
      })
    } finally {
      setCaptchaOpen(false)
    }
  }

  function stopPolling() {
    if (pollTimerRef.current !== null) {
      window.clearInterval(pollTimerRef.current)
      pollTimerRef.current = null
    }
  }

  useEffect(() => {
    if (!open) {
      stopPolling()
    } else {
      setLabel("")
    }
    return () => stopPolling()
  }, [open])

  function openOfficialLoginWindow() {
    window.open(
      OFFICIAL_LOGIN_URL,
      "bohrium_official_login",
      "noopener,noreferrer,width=520,height=720"
    )
    setActiveTab("token")
    toast.info("已打开玻尔官方登录窗口，登录完成后请复制浏览器 Cookie 中的 brmToken 粘贴到下方")
  }

  async function finishLogin(res: AccountLoginResult, methodLabel: string) {
    const msg = res.isNew
      ? `${methodLabel}成功：已添加新账号（默认未入池，如需路由请前往「03 OpenAI 路由池」拖入）`
      : `${methodLabel}成功：已刷新账号凭据`
    toast.success(msg)
    stopPolling()
    onOpenChange(false)
    await onSuccess(res.account?.id)
  }

  async function startWeChatQR() {
    stopPolling()
    setQrBusy(true)
    setQrStatus("正在申请微信登录二维码...")
    try {
      const res = await apiRequest<{ data: { sceneId: string; qrCodeUrl: string } }>(
        "/api/bohrium/wechat/qr",
        { method: "POST" }
      )
      const { sceneId, qrCodeUrl } = res.data
      setQrUrl(qrCodeUrl)
      setQrStatus("请使用微信扫一扫并确认登录（轮询中...）")

      pollTimerRef.current = window.setInterval(async () => {
        try {
          const qs = new URLSearchParams({
            sceneId,
            label: labelRef.current.trim(),
          })
          const poll = await apiRequest<AccountLoginResult>(
            `/api/bohrium/wechat/poll?${qs.toString()}`
          )
          const p = poll.data
          if (p?.loggedIn) {
            stopPolling()
            setQrStatus("扫码登录成功")
            await finishLogin(poll, "微信远程登录")
          } else if (p?.status === "expired") {
            stopPolling()
            setQrStatus("二维码已过期，请重新获取")
          } else if (p?.status === "scanned") {
            setQrStatus("已扫码，请在手机微信上点击确认...")
          }
        } catch {
          // Keep polling across transient network errors
        }
      }, 2200)
    } catch (err) {
      const msg = err instanceof Error ? err.message : "未知错误"
      setQrStatus("获取二维码失败: " + msg)
      toast.error("获取二维码失败: " + msg)
    } finally {
      setQrBusy(false)
    }
  }

  async function startOAuth(provider: string) {
    setOauthProvider(provider)
    setOauthBusy(true)
    try {
      const res = await apiRequest<{ data?: { authUrl?: string } }>("/api/bohrium/oauth/url", {
        method: "POST",
        body: JSON.stringify({ provider }),
      })
      const authUrl = res.data?.authUrl
      if (!authUrl) throw new Error("未获取到授权链接")
      setOauthAuthUrl(authUrl)
      window.open(authUrl, "_blank", "noopener,noreferrer")
      toast.success(`已获取 ${provider.toUpperCase()} 官方授权链接，完成后请粘贴回调 URL`)
    } catch (err) {
      toast.error("获取 OAuth 链接失败: " + (err instanceof Error ? err.message : ""))
    } finally {
      setOauthBusy(false)
    }
  }

  async function verifyOAuth() {
    if (!oauthCallback.trim()) {
      toast.warning("请先粘贴授权跳转后的完整 URL 或 code")
      return
    }
    setOauthBusy(true)
    try {
      const res = await apiRequest<AccountLoginResult>("/api/bohrium/oauth/verify", {
        method: "POST",
        body: JSON.stringify({
          provider: oauthProvider,
          callbackInput: oauthCallback.trim(),
          label: label.trim(),
        }),
      })
      setOauthCallback("")
      await finishLogin(res, `${oauthProvider.toUpperCase()} OAuth 登录`)
    } catch (err) {
      toast.error(err instanceof Error ? err.message : "OAuth 登录失败")
    } finally {
      setOauthBusy(false)
    }
  }

  async function sendOTP() {
    if (!otpTarget.trim()) {
      toast.warning(otpKind === "phone" ? "请输入手机号" : "请输入邮箱")
      return
    }
    if (otpCooldown > 0) return
    setOtpSending(true)
    try {
      const proof = await acquireCaptchaProof("otp")
      const res = await apiRequest<{ message?: string }>("/api/bohrium/code/send", {
        method: "POST",
        body: JSON.stringify({
          kind: otpKind,
          target: otpTarget.trim(),
          countryCode: otpCountryCode.trim() || "86",
          isOversea: otpOversea,
          ticket: proof?.ticket || "",
          randStr: proof?.randStr || "",
          captchaAppId: proof?.captchaAppId || 0,
        }),
      })
      setOtpCooldown(60)
      toast.success(
        otpKind === "email"
          ? "验证码已请求发送，请查收收件箱及垃圾邮件箱（未收到可切换海外通道重试）"
          : res.message || "验证码已发送"
      )
    } catch (err) {
      if (err instanceof TencentCaptchaCancelledError) {
        toast.info(err.message)
      } else {
        toast.warning(err instanceof Error ? err.message : "发送验证码失败")
      }
    } finally {
      setOtpSending(false)
    }
  }

  async function submitOTP() {
    if (!otpTarget.trim() || !otpCode.trim()) {
      toast.warning("请填写账号和验证码")
      return
    }
    setOtpSubmitting(true)
    try {
      const res = await apiRequest<AccountLoginResult>("/api/bohrium/login/code", {
        method: "POST",
        body: JSON.stringify({
          kind: otpKind,
          target: otpTarget.trim(),
          countryCode: otpCountryCode.trim() || "86",
          isOversea: otpOversea,
          code: otpCode.trim(),
          label: label.trim(),
        }),
      })
      setOtpCode("")
      await finishLogin(res, "验证码登录")
    } catch (err) {
      toast.error(err instanceof Error ? err.message : "验证码登录失败")
    } finally {
      setOtpSubmitting(false)
    }
  }

  async function submitPassword() {
    if (!pwAccount.trim() || !pwSecret) {
      toast.warning("请输入账号与密码")
      return
    }
    setPwBusy(true)
    try {
      const proof = await acquireCaptchaProof("password", pwAccount.trim())
      const res = await apiRequest<AccountLoginResult>("/api/bohrium/login/password", {
        method: "POST",
        body: JSON.stringify({
          account: pwAccount.trim(),
          password: pwSecret,
          label: label.trim(),
          ticket: proof?.ticket || "",
          randStr: proof?.randStr || "",
          captchaAppId: proof?.captchaAppId || 0,
        }),
      })
      setPwSecret("")
      await finishLogin(res, "密码登录")
    } catch (err) {
      if (err instanceof TencentCaptchaCancelledError) {
        toast.info(err.message)
      } else {
        toast.warning(err instanceof Error ? err.message : "密码登录失败")
      }
    } finally {
      setPwBusy(false)
    }
  }

  async function submitToken() {
    if (!rawToken.trim()) {
      toast.warning("请先粘贴 brmToken")
      return
    }
    setTokenBusy(true)
    try {
      const res = await apiRequest<AccountLoginResult>("/api/bohrium/login/token", {
        method: "POST",
        body: JSON.stringify({ token: rawToken.trim(), label: label.trim() }),
      })
      setRawToken("")
      await finishLogin(res, "Token 导入")
    } catch (err) {
      toast.error("导入失败: " + (err instanceof Error ? err.message : ""))
    } finally {
      setTokenBusy(false)
    }
  }

  return (
    <Dialog
      open={open}
      modal={!captchaOpen}
      onOpenChange={(next) => {
        if (!captchaOpen) onOpenChange(next)
      }}
    >
      <DialogContent
        onInteractOutside={(e) => {
          if (captchaOpen) e.preventDefault()
        }}
        onPointerDownOutside={(e) => {
          if (captchaOpen) e.preventDefault()
        }}
        onEscapeKeyDown={(e) => {
          if (captchaOpen) e.preventDefault()
        }}
      >
        <DialogHeader>
          <DialogTitle>添加 / 登录玻尔账号</DialogTitle>
          <DialogDescription>
            凭据持久化于 SQLite <code className="rounded bg-muted px-1">data/bohrium.db</code>；新账号默认
            <strong>不入池</strong>，需手动拖入 OpenAI 路由池。
          </DialogDescription>
        </DialogHeader>

        <div className="mb-4 space-y-1.5">
          <Label htmlFor="login-account-label">账号备注名（可选，留空默认使用玻尔昵称）</Label>
          <Input
            id="login-account-label"
            value={label}
            onChange={(e) => setLabel(e.target.value)}
            placeholder="例如：主力账号 / 实验室账号 A"
          />
        </div>

        <Tabs value={activeTab} onValueChange={setActiveTab}>
          <TabsList>
            <TabsTrigger value="wechat">微信扫码</TabsTrigger>
            <TabsTrigger value="oauth">Google / 第三方</TabsTrigger>
            <TabsTrigger value="otp">验证码</TabsTrigger>
            <TabsTrigger value="password">账号密码</TabsTrigger>
            <TabsTrigger value="token">导入 Token</TabsTrigger>
          </TabsList>

          <TabsContent value="wechat" className="space-y-3">
            <p className="text-xs text-muted-foreground">
              由服务端向 <code>platform.bohrium.com</code> 申请官方微信登录二维码，无图形验证码，适合远程部署。
            </p>
            <div className="flex flex-col items-center justify-center rounded-lg border border-dashed bg-muted/30 p-5">
              {qrUrl ? (
                <img
                  src={qrUrl}
                  alt="微信扫码登录二维码"
                  className="h-44 w-44 rounded-md border bg-white p-1.5"
                />
              ) : (
                <div className="flex h-44 w-44 flex-col items-center justify-center gap-2 text-xs text-muted-foreground">
                  <QrCode className="h-8 w-8 opacity-40" />
                  <span>点击下方按钮生成二维码</span>
                </div>
              )}
              <div className="mt-3 text-xs text-muted-foreground">{qrStatus}</div>
            </div>
            <div className="flex justify-end">
              <Button onClick={startWeChatQR} disabled={qrBusy}>
                {qrBusy ? "申请中..." : "获取 / 刷新二维码"}
              </Button>
            </div>
          </TabsContent>

          <TabsContent value="oauth" className="space-y-3">
            <p className="text-xs text-muted-foreground">
              选择第三方渠道打开玻尔官方 OAuth 授权页；授权后将浏览器跳转后的完整网址（含 <code>code=</code> 或{" "}
              <code>token=</code>）粘贴到下方完成添加。
            </p>
            <div className="flex flex-wrap gap-2">
              {OAUTH_PROVIDERS.map((p) => (
                <Button
                  key={p.id}
                  size="sm"
                  variant={oauthProvider === p.id ? "default" : "outline"}
                  disabled={oauthBusy}
                  onClick={() => startOAuth(p.id)}
                >
                  {p.label}
                </Button>
              ))}
            </div>
            {oauthAuthUrl && (
              <div className="rounded-md border bg-muted/40 p-3 text-xs space-y-1">
                <div className="font-mono text-[11px] text-muted-foreground">
                  {oauthProvider.toUpperCase()} 授权链接（已尝试在新标签打开）
                </div>
                <a
                  href={oauthAuthUrl}
                  target="_blank"
                  rel="noopener noreferrer"
                  className="inline-flex items-center gap-1 break-all text-blue-600 underline dark:text-blue-400"
                >
                  {oauthAuthUrl}
                  <ExternalLink className="h-3 w-3 shrink-0" />
                </a>
              </div>
            )}
            <div className="space-y-1.5">
              <Label htmlFor="oauth-callback-input">粘贴授权回调 URL（或直接粘贴 code）</Label>
              <Textarea
                id="oauth-callback-input"
                rows={3}
                value={oauthCallback}
                onChange={(e) => setOauthCallback(e.target.value)}
                placeholder="https://login.bohrium.com/?code=4%2F0A...&state=..."
              />
            </div>
            <div className="flex justify-end">
              <Button onClick={verifyOAuth} disabled={oauthBusy}>
                完成 OAuth 验证登录
              </Button>
            </div>
          </TabsContent>

          <TabsContent value="otp" className="space-y-3">
            <p className="text-xs text-muted-foreground">
              支持手机号短信验证码或邮箱验证码登录；点击「获取验证码」后将弹出官方腾讯云人机图形验证，请手动完成滑块/点选验证。
            </p>
            <div className="flex flex-wrap items-center justify-between gap-3 text-xs">
              <div className="flex items-center gap-4">
                <label className="flex items-center gap-1.5 cursor-pointer">
                  <input
                    type="radio"
                    name="otp-kind"
                    checked={otpKind === "phone"}
                    onChange={() => setOtpKind("phone")}
                  />
                  手机号验证码
                </label>
                <label className="flex items-center gap-1.5 cursor-pointer">
                  <input
                    type="radio"
                    name="otp-kind"
                    checked={otpKind === "email"}
                    onChange={() => setOtpKind("email")}
                  />
                  邮箱验证码
                </label>
              </div>
              <label
                className="flex items-center gap-1.5 cursor-pointer text-muted-foreground"
                title="对应官网境内/境外发信通道（部分海外邮箱域名需切换海外通道）"
              >
                <input
                  type="checkbox"
                  checked={otpOversea}
                  onChange={(e) => setOtpOversea(e.target.checked)}
                />
                海外通道 (en-us)
              </label>
            </div>
            {otpKind === "phone" && (
              <div className="space-y-1.5">
                <Label htmlFor="otp-cc">国家区号</Label>
                <Input
                  id="otp-cc"
                  value={otpCountryCode}
                  onChange={(e) => setOtpCountryCode(e.target.value)}
                  placeholder="86"
                />
              </div>
            )}
            <div className="space-y-1.5">
              <Label htmlFor="otp-target">{otpKind === "phone" ? "手机号" : "邮箱地址"}</Label>
              <div className="flex gap-2">
                <Input
                  id="otp-target"
                  value={otpTarget}
                  onChange={(e) => setOtpTarget(e.target.value)}
                  placeholder={otpKind === "phone" ? "13800138000" : "name@example.com"}
                />
                <Button
                  variant="outline"
                  size="sm"
                  onClick={sendOTP}
                  disabled={otpSending || otpCooldown > 0}
                  className="shrink-0 tabular-nums"
                >
                  {otpSending
                    ? "验证中..."
                    : otpCooldown > 0
                      ? `${otpCooldown}s 后重发`
                      : "获取验证码"}
                </Button>
              </div>
            </div>
            <div className="space-y-1.5">
              <Label htmlFor="otp-code">验证码</Label>
              <Input
                id="otp-code"
                value={otpCode}
                onChange={(e) => setOtpCode(e.target.value)}
                placeholder="输入收到的 6 位验证码"
              />
            </div>
            <div className="flex flex-wrap items-center justify-between gap-2 pt-1">
              <Button
                type="button"
                variant="outline"
                size="sm"
                onClick={openOfficialLoginWindow}
                title="若后端发码触发上游阿里云 WAF，可直接在新窗口登录官网后导入 brmToken"
              >
                <ExternalLink className="h-3.5 w-3.5" />
                打开官方登录窗口
              </Button>
              <Button onClick={submitOTP} disabled={otpSubmitting}>
                验证码登录
              </Button>
            </div>
          </TabsContent>

          <TabsContent value="password" className="space-y-3">
            <p className="text-xs text-muted-foreground">
              支持手机号 / 邮箱 / 学术码（Account ID）+ 密码登录；若触发安全风控，将自动弹出官方腾讯云人机图形验证供您手动验证。
            </p>
            <div className="space-y-1.5">
              <Label htmlFor="pw-account">手机号 / 邮箱 / 学术码</Label>
              <Input
                id="pw-account"
                value={pwAccount}
                onChange={(e) => setPwAccount(e.target.value)}
                placeholder="手机号、邮箱或学术码"
              />
            </div>
            <div className="space-y-1.5">
              <Label htmlFor="pw-secret">密码</Label>
              <Input
                id="pw-secret"
                type="password"
                value={pwSecret}
                onChange={(e) => setPwSecret(e.target.value)}
                placeholder="玻尔账号密码"
              />
            </div>
            <div className="flex flex-wrap items-center justify-between gap-2 pt-1">
              <Button
                type="button"
                variant="outline"
                size="sm"
                onClick={openOfficialLoginWindow}
                title="若后端登录触发上游阿里云 WAF，可直接在新窗口登录官网后导入 brmToken"
              >
                <ExternalLink className="h-3.5 w-3.5" />
                打开官方登录窗口
              </Button>
              <Button onClick={submitPassword} disabled={pwBusy}>
                {pwBusy ? "验证登录中..." : "密码登录"}
              </Button>
            </div>
          </TabsContent>

          <TabsContent value="token" className="space-y-3">
            <div className="rounded-md border bg-muted/40 p-3 text-xs space-y-2">
              <div className="text-muted-foreground leading-relaxed">
                推荐用于邮箱/密码账号：点击下方按钮在官方独立窗口完成登录（自然通过阿里云 WAF 与腾讯验证），随后在浏览器开发者工具（<kbd className="rounded border bg-background px-1">F12</kbd> → Application → Cookies）复制{" "}
                <code>brmToken</code> 粘贴到下方即可，导入后后台会自动续期。
              </div>
              <div>
                <Button type="button" variant="outline" size="xs" onClick={openOfficialLoginWindow}>
                  <ExternalLink className="h-3 w-3" />
                  打开玻尔官方登录窗口 (platform.bohrium.com)
                </Button>
              </div>
            </div>
            <div className="space-y-1.5">
              <Label htmlFor="import-token-input">brmToken JWT（支持直接粘贴整段 Cookie）</Label>
              <Textarea
                id="import-token-input"
                rows={4}
                value={rawToken}
                onChange={(e) => setRawToken(e.target.value)}
                placeholder="eyJhbGciOi..."
              />
            </div>
            <div className="flex justify-end">
              <Button onClick={submitToken} disabled={tokenBusy}>
                验证并添加账号
              </Button>
            </div>
          </TabsContent>
        </Tabs>
      </DialogContent>
    </Dialog>
  )
}
