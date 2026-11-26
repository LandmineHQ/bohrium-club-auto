const TENCENT_CAPTCHA_SDK_URL = "https://turing.captcha.qcloud.com/TJCaptcha.js"

interface TencentCaptchaCallbackResult {
  ret: number
  ticket?: string
  randstr?: string
  appid?: string
  errorCode?: number
  errorMessage?: string
}

interface TencentCaptchaOptions {
  userLanguage?: string
  aidEncrypted?: string
}

interface TencentCaptchaInstance {
  show: () => void
  destroy?: () => void
}

type TencentCaptchaConstructor = new (
  appId: string,
  callback: (res: TencentCaptchaCallbackResult) => void,
  options?: TencentCaptchaOptions
) => TencentCaptchaInstance

declare global {
  interface Window {
    TencentCaptcha?: TencentCaptchaConstructor
  }
}

export interface CaptchaProofResult {
  ticket: string
  randStr: string
  captchaAppId: number
}

export class TencentCaptchaCancelledError extends Error {
  constructor(message = "已取消人机图形验证") {
    super(message)
    this.name = "TencentCaptchaCancelledError"
  }
}

let sdkLoadPromise: Promise<TencentCaptchaConstructor> | null = null

function loadTencentCaptchaSDK(): Promise<TencentCaptchaConstructor> {
  if (typeof window !== "undefined" && typeof window.TencentCaptcha === "function") {
    return Promise.resolve(window.TencentCaptcha)
  }
  if (sdkLoadPromise) {
    return sdkLoadPromise
  }

  sdkLoadPromise = new Promise<TencentCaptchaConstructor>((resolve, reject) => {
    const existing = document.querySelector<HTMLScriptElement>(
      `script[src="${TENCENT_CAPTCHA_SDK_URL}"]`
    )
    let settled = false
    const timer = window.setTimeout(() => {
      if (!settled) {
        settled = true
        sdkLoadPromise = null
        reject(new Error("加载腾讯云验证码组件超时，请检查浏览器网络是否可访问 turing.captcha.qcloud.com"))
      }
    }, 15000)

    const onReady = () => {
      if (settled) return
      if (typeof window.TencentCaptcha === "function") {
        settled = true
        window.clearTimeout(timer)
        resolve(window.TencentCaptcha)
      } else {
        settled = true
        window.clearTimeout(timer)
        sdkLoadPromise = null
        reject(new Error("腾讯云验证码脚本已加载但未暴露 window.TencentCaptcha"))
      }
    }

    const onError = () => {
      if (settled) return
      settled = true
      window.clearTimeout(timer)
      sdkLoadPromise = null
      reject(new Error("加载腾讯云验证码组件失败，请检查网络或浏览器拦截插件"))
    }

    if (existing) {
      existing.addEventListener("load", onReady, { once: true })
      existing.addEventListener("error", onError, { once: true })
      return
    }

    const script = document.createElement("script")
    script.src = TENCENT_CAPTCHA_SDK_URL
    script.async = true
    script.onload = onReady
    script.onerror = onError
    document.head.appendChild(script)
  })

  return sdkLoadPromise
}

/**
 * Displays the official Tencent Cloud Captcha interactive modal in the user's browser
 * so the user manually solves the puzzle and obtains (ticket, randStr) for Bohrium login.
 */
export async function runTencentCaptcha(params: {
  captchaAppId: string
  aidEncrypted: string
  userLanguage?: string
}): Promise<CaptchaProofResult> {
  const TencentCaptcha = await loadTencentCaptchaSDK()
  return new Promise<CaptchaProofResult>((resolve, reject) => {
    try {
      const instance = new TencentCaptcha(
        params.captchaAppId,
        (res) => {
          if (res.ret === 0) {
            if (res.errorCode) {
              reject(
                new Error(
                  `腾讯云人机验证返回异常 (code=${res.errorCode}): ${res.errorMessage || "请重试或改用微信扫码登录"}`
                )
              )
              return
            }
            if (!res.ticket || !res.randstr) {
              reject(new Error("未获取到有效的人机验证票据，请重试"))
              return
            }
            resolve({
              ticket: res.ticket,
              randStr: res.randstr,
              captchaAppId: Number(res.appid || params.captchaAppId),
            })
            return
          }
          if (res.ret === 2) {
            reject(new TencentCaptchaCancelledError())
            return
          }
          reject(
            new Error(
              `人机验证未完成 (ret=${res.ret}${res.errorCode ? `, code=${res.errorCode}` : ""})${
                res.errorMessage ? `: ${res.errorMessage}` : ""
              }`
            )
          )
        },
        {
          userLanguage: params.userLanguage || "zh-cn",
          aidEncrypted: params.aidEncrypted,
        }
      )
      instance.show()
    } catch (err) {
      reject(err instanceof Error ? err : new Error("唤起腾讯云图形验证码失败"))
    }
  })
}
