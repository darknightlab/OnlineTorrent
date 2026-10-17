import { useEffect, useRef, useState } from "react"
import { api, isApiError } from "@/lib/api"
import type { PlayRequest, PlayResponse, Torrent } from "@/lib/types"
import { buildMagnet } from "@/lib/utils"

export interface WatchParams {
  uri: string | null
  t: string | null
  ih: string | null
}

export type ResolveState =
  | { status: "idle" }
  | { status: "loading" }
  | { status: "ready"; magnet: string; play: PlayResponse | null; direct: boolean }
  | { status: "login"; message: string }
  | { status: "vip"; message: string }
  | { status: "blocked"; message: string }
  | { status: "fallback"; magnet: string; seconds: number; reason: string }
  | { status: "error"; message: string }

/** 后端不可用时回退为直连磁力的等待时间（与旧版一致） */
const FALLBACK_MS = 5000
const REQUEST_TIMEOUT_MS = 15000

function requestOf(p: WatchParams): PlayRequest | null {
  if (p.uri) return { magnet: p.uri }
  if (p.t) return { torrent_id: p.t }
  if (p.ih) return { infohash: p.ih.toLowerCase() }
  return null
}

/** 不经过后端时可以直接使用的磁力链接 */
function directMagnet(p: WatchParams, hint: Torrent | undefined, trackers: string[]): string | null {
  if (p.uri) return p.uri
  if (p.ih) return buildMagnet(p.ih.toLowerCase(), undefined, trackers)
  if (p.t?.startsWith("ih:")) return buildMagnet(p.t.slice(3), undefined, trackers)
  if (hint?.infohash) return buildMagnet(hint.infohash, hint.title, trackers)
  return null
}

/**
 * POST /play → 策略结果。401/402/451 转为对应提示；网络错误/5xx 在 5 秒后回退为直连磁力。
 * `attempt` 变化（重试、登录状态变化）时重新请求。
 */
export function usePlayResolve(params: WatchParams, hint: Torrent | undefined, trackers: string[], attempt: string) {
  const [state, setState] = useState<ResolveState>({ status: "idle" })
  const [skip, setSkip] = useState(0)
  const skipRef = useRef<() => void>(() => {})
  const key = `${params.uri}|${params.t}|${params.ih}`

  useEffect(() => {
    const req = requestOf(params)
    if (!req) {
      setState({ status: "idle" })
      return
    }
    const started = Date.now()
    const ctrl = new AbortController()
    let done = false
    let timer = 0
    let tick = 0
    setState({ status: "loading" })

    const goDirect = (magnet: string) => {
      done = true
      window.clearInterval(tick)
      setState({ status: "ready", magnet, play: null, direct: true })
    }

    const fallback = (reason: string) => {
      const magnet = directMagnet(params, hint, trackers)
      if (!magnet) {
        setState({ status: "error", message: `${reason}，且缺少可直连的磁力信息，请稍后重试。` })
        return
      }
      skipRef.current = () => goDirect(magnet)
      const update = () => {
        if (done) return
        const left = Math.max(0, FALLBACK_MS - (Date.now() - started))
        if (left <= 0) return goDirect(magnet)
        setState({ status: "fallback", magnet, seconds: Math.ceil(left / 1000), reason })
      }
      update()
      tick = window.setInterval(update, 250)
    }

    timer = window.setTimeout(() => ctrl.abort(), REQUEST_TIMEOUT_MS)
    fetchPlay(req, ctrl.signal)
      .then((play) => {
        if (done) return
        done = true
        if (play.access.action !== "allow") return setState(gateOf(play.access.action, play.access.reason))
        setState({ status: "ready", magnet: play.magnet, play, direct: false })
      })
      .catch((e) => {
        if (done) return
        if (isApiError(e) && !e.unavailable) {
          done = true
          if (e.status === 401) return setState({ status: "login", message: e.message })
          if (e.status === 402) return setState({ status: "vip", message: e.message })
          if (e.status === 451) return setState({ status: "blocked", message: e.message })
          if (e.status === 404) return setState({ status: "error", message: "未找到该种子，链接可能已失效。" })
          return setState({ status: "error", message: e.message })
        }
        fallback(isApiError(e) ? (e.status ? `服务器错误（${e.status}）` : "无法连接服务器") : "服务器响应超时")
      })
      .finally(() => window.clearTimeout(timer))

    return () => {
      done = true
      ctrl.abort()
      window.clearTimeout(timer)
      window.clearInterval(tick)
    }
  }, [key, attempt, skip])

  return { state, skipWait: () => skipRef.current(), retry: () => setSkip((n) => n + 1) }
}

function gateOf(action: string, reason: string): ResolveState {
  if (action === "login") return { status: "login", message: reason }
  if (action === "vip") return { status: "vip", message: reason }
  return { status: "blocked", message: reason }
}

async function fetchPlay(req: PlayRequest, signal: AbortSignal) {
  // api.play 不支持 signal，这里用 Promise.race 实现超时/取消
  return await Promise.race([
    api.play(req),
    new Promise<never>((_, reject) => signal.addEventListener("abort", () => reject(new Error("timeout")), { once: true })),
  ])
}
