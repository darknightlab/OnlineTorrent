import { useEffect, useRef } from "react"
import { useTheme } from "@/lib/theme"

declare global {
  interface Window {
    turnstile?: {
      render: (el: HTMLElement, opts: Record<string, unknown>) => string
      remove: (id: string) => void
    }
  }
}

let scriptPromise: Promise<void> | null = null
function loadScript() {
  if (window.turnstile) return Promise.resolve()
  if (scriptPromise) return scriptPromise
  scriptPromise = new Promise((resolve, reject) => {
    const s = document.createElement("script")
    s.src = "https://challenges.cloudflare.com/turnstile/v0/api.js?render=explicit"
    s.async = true
    s.defer = true
    s.onload = () => resolve()
    s.onerror = () => {
      scriptPromise = null
      reject(new Error("Turnstile 加载失败"))
    }
    document.head.appendChild(s)
  })
  return scriptPromise
}

/** Cloudflare Turnstile 人机验证（site key 为空时不渲染） */
export function Turnstile({ siteKey, action, resetKey = 0, onToken }: { siteKey: string; action: string; resetKey?: number; onToken: (t: string) => void }) {
  const host = useRef<HTMLDivElement>(null)
  const { resolved } = useTheme()
  const cb = useRef(onToken)
  cb.current = onToken
  useEffect(() => {
    if (!siteKey || !host.current) return
    let id = ""
    let disposed = false
    loadScript()
      .then(() => {
        if (disposed || !host.current || !window.turnstile) return
        id = window.turnstile.render(host.current, {
          sitekey: siteKey,
          action,
          theme: resolved,
          language: "zh-cn",
          size: host.current.clientWidth < 300 ? "compact" : "flexible",
          callback: (t: string) => cb.current(t),
          "expired-callback": () => cb.current(""),
          "error-callback": () => cb.current(""),
        })
      })
      .catch(() => cb.current(""))
    return () => {
      disposed = true
      if (id && window.turnstile) window.turnstile.remove(id)
    }
  }, [siteKey, action, resetKey, resolved])
  if (!siteKey) return null
  return <div ref={host} className="flex min-h-[65px] w-full justify-center" />
}
