import { clsx, type ClassValue } from "clsx"
import { twMerge } from "tailwind-merge"

export function cn(...inputs: ClassValue[]) {
  return twMerge(clsx(inputs))
}

export function formatNum(v: number | string | null | undefined): string {
  if (v === null || v === undefined || Number.isNaN(Number(v))) return "--"
  return Number(v).toLocaleString("zh-CN", { maximumFractionDigits: 2 })
}

export function formatQuota(q?: { remaining?: number | null; total?: number | null } | null): string {
  if (!q || (q.remaining === null && q.total === null)) return "--"
  if (q.total !== null && q.total !== undefined) {
    return `${q.remaining ?? 0}/${q.total}`
  }
  return `${q.remaining ?? "--"}`
}

export function formatTime(iso?: string | null): string {
  if (!iso) return "--"
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return String(iso)
  return d.toLocaleString("zh-CN", { hour12: false })
}

export function formatTTLRemaining(expiresISO?: string | null): string {
  if (!expiresISO) return "--"
  const ms = new Date(expiresISO).getTime() - Date.now()
  if (ms <= 0) return "已过期"
  const hours = Math.floor(ms / 3600000)
  const days = Math.floor(hours / 24)
  const remH = hours % 24
  if (days > 0) return `${days}天 ${remH}时`
  const mins = Math.floor((ms % 3600000) / 60000)
  return `${remH}时 ${mins}分`
}

export function maskApiKey(key?: string | null): string {
  if (!key || key.length < 16) return key || "--"
  return `${key.slice(0, 11)}••••••••${key.slice(-4)}`
}

export function gatewayBaseURL(): string {
  return `${window.location.origin}/v1`
}

export async function copyClipboard(text: string): Promise<boolean> {
  try {
    await navigator.clipboard.writeText(text)
    return true
  } catch {
    try {
      const ta = document.createElement("textarea")
      ta.value = text
      ta.style.position = "fixed"
      ta.style.opacity = "0"
      document.body.appendChild(ta)
      ta.select()
      const ok = document.execCommand("copy")
      ta.remove()
      return ok
    } catch {
      return false
    }
  }
}
