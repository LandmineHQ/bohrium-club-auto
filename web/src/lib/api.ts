let unauthorizedHandler: (() => void) | null = null

export function setUnauthorizedHandler(fn: (() => void) | null) {
  unauthorizedHandler = fn
}

export class ApiError extends Error {
  status: number
  constructor(message: string, status: number) {
    super(message)
    this.status = status
  }
}

export async function apiRequest<T = Record<string, unknown>>(
  url: string,
  init: RequestInit = {}
): Promise<T> {
  const res = await fetch(url, {
    credentials: "same-origin",
    ...init,
    headers: {
      "Content-Type": "application/json",
      ...(init.headers || {}),
    },
  })

  const payload = (await res.json().catch(() => ({
    ok: false,
    error: `HTTP ${res.status}`,
  }))) as Record<string, unknown>

  if (res.status === 401 && !url.startsWith("/api/auth/")) {
    unauthorizedHandler?.()
    throw new ApiError(
      String(payload.error || "会话已过期，请重新登录控制台"),
      401
    )
  }

  if (!res.ok || payload.ok === false) {
    const nested = payload.data as Record<string, unknown> | undefined
    const msg = String(
      payload.error || payload.message || nested?.message || `HTTP ${res.status}`
    )
    throw new ApiError(msg, res.status)
  }

  return payload as T
}
