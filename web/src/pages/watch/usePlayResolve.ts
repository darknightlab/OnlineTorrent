import { useEffect, useRef, useState } from "react"
import { api, isApiError } from "@/lib/api"
import type { Access, PlayResponse } from "@/lib/types"
import { buildMagnet } from "@/lib/utils"
import { useUser } from "@/lib/queries"

/** 当前身份标识：变化时重新请求 play；/me 未加载时为 "pending" */
export function usePlayAttempt() {
  const { user, loading } = useUser()
  return loading ? "pending" : `${user?.id ?? "guest"}:${user?.role ?? ""}:${user?.vip_until ?? ""}`
}

/** 要打开的种子：按 infohash，或按 bangumi.moe ID（仅旧链接 /watch?t= 使用） */
export type PlaySource = { kind: "hash"; ih: string } | { kind: "moe"; id: string }

/** 播放前已知的展示信息（来自列表页 / 单集文件列表），只用于占位，不参与判定 */
export interface PlayHint {
  infohash?: string
  title?: string
  team?: string | null
}

/** 上一个页面已拿到的 play 结果（/watch?t= 解析后跳转到 /watch/hash/{ih} 时复用，避免重复请求） */
export interface PlayPreload {
  play: PlayResponse
  attempt: string
  at: number
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
/** preload 只在跳转后短时间内有效（history state 刷新页面后仍在，过期后重新判定） */
const PRELOAD_TTL_MS = 30_000

/** 不经过后端时可以直接使用的磁力链接 */
function directMagnet(src: PlaySource, hint: PlayHint | undefined, trackers: string[]): string | null {
  if (src.kind === "hash") return buildMagnet(src.ih, undefined, trackers)
  if (hint?.infohash) return buildMagnet(hint.infohash, hint.title, trackers)
  return null
}

function sourceKey(src: PlaySource | null) {
  if (!src) return ""
  return src.kind === "hash" ? `hash:${src.ih}` : `moe:${src.id}`
}

/**
 * POST torrents/{hash|moe}/{id}/play → 策略结果。401/402/451 转为对应提示；网络错误/5xx 在 5 秒后回退为直连磁力。
 * `attempt` 变化（重试、登录状态变化）时重新请求；为 "pending"（/me 未加载）时暂不请求，避免身份确定后重复请求。
 * `known` 为列表里已知的 access：非 allow 时直接显示对应提示，不发请求。
 */
export function usePlayResolve(
  src: PlaySource | null,
  opts: { hint?: PlayHint; trackers: string[]; attempt: string; preload?: PlayPreload | null; known?: Access | null },
) {
  const { hint, trackers, attempt, preload, known } = opts
  const [state, setState] = useState<ResolveState>({ status: src ? "loading" : "idle" })
  const [skip, setSkip] = useState(0)
  const skipRef = useRef<() => void>(() => {})
  const key = sourceKey(src)
  // 只有非 allow 的已知结果会影响判定；allow 与未知等价（播放页 ↔ 单集页切换时不重新请求）
  const knownAction = known && known.action !== "allow" ? known.action : ""

  useEffect(() => {
    if (!src) {
      setState({ status: "idle" })
      return
    }
    if (known && knownAction && skip === 0) {
      setState(gateOf(known.action, known.reason))
      return
    }
    setState({ status: "loading" })
    if (attempt === "pending") return
    if (
      preload &&
      skip === 0 &&
      src.kind === "hash" &&
      preload.play.infohash === src.ih &&
      preload.attempt === attempt &&
      Date.now() - preload.at < PRELOAD_TTL_MS &&
      preload.play.access.action === "allow"
    ) {
      setState({ status: "ready", magnet: preload.play.magnet, play: preload.play, direct: false })
      return
    }

    const started = Date.now()
    const ctrl = new AbortController()
    let done = false
    let tick = 0

    const goDirect = (magnet: string) => {
      done = true
      window.clearInterval(tick)
      setState({ status: "ready", magnet, play: null, direct: true })
    }

    const fallback = (reason: string) => {
      const magnet = directMagnet(src, hint, trackers)
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

    let timedOut = false
    const timer = window.setTimeout(() => {
      timedOut = true
      ctrl.abort()
    }, REQUEST_TIMEOUT_MS)
    const req = src.kind === "hash" ? api.playHash(src.ih, ctrl.signal) : api.playMoe(src.id, ctrl.signal)
    req
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
        fallback(isApiError(e) ? (e.status ? `服务器错误（${e.status}）` : "无法连接服务器") : timedOut ? "服务器响应超时" : "请求失败")
      })
      .finally(() => window.clearTimeout(timer))

    return () => {
      done = true
      ctrl.abort()
      window.clearTimeout(timer)
      window.clearInterval(tick)
    }
    // hint/trackers/preload 只在请求发起时读取
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [key, attempt, skip, knownAction])

  return { state, skipWait: () => skipRef.current(), retry: () => setSkip((n) => n + 1) }
}

function gateOf(action: string, reason: string): ResolveState {
  if (action === "login") return { status: "login", message: reason }
  if (action === "vip") return { status: "vip", message: reason }
  return { status: "blocked", message: reason }
}
