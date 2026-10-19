import { useEffect, useRef, useState } from "react"
import { api, isApiError } from "@/lib/api"
import type { Access, PlayResponse } from "@/lib/types"
import { useUser } from "@/lib/queries"
import { stepOfEvent, stepOfPlay, type ResolveStep } from "@/lib/playresolve"

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
  /** 新种子：服务器正在获取种子信息（只取元数据），等待判定 */
  | { status: "pending" }
  /** 已映射到某番某集：调用方跳转到 path（随后重新请求 play，在新页面就地播放） */
  | { status: "redirect"; path: string }
  | { status: "ready"; magnet: string; play: PlayResponse | null; direct: boolean }
  | { status: "login"; message: string }
  | { status: "vip"; message: string }
  | { status: "blocked"; message: string }
  /** 暂时无法获取种子信息（服务器仍在队列里继续尝试） */
  | { status: "timeout" }
  | { status: "error"; message: string }

const REQUEST_TIMEOUT_MS = 15000
/** preload 只在跳转后短时间内有效（history state 刷新页面后仍在，过期后重新判定） */
const PRELOAD_TTL_MS = 30_000
/** SSE 连续出错（期间未收到事件）多少次后放弃 */
const MAX_SSE_ERRORS = 4

function sourceKey(src: PlaySource | null) {
  if (!src) return ""
  return src.kind === "hash" ? `hash:${src.ih}` : `moe:${src.id}`
}

/**
 * POST torrents/{hash|moe}/{id}/play → 策略结果。401/402/451 转为对应提示。
 * 新种子返回 pending（不带磁力：判定前不启动 P2P），随后订阅 resolve/events 等待服务器获取元数据并判定：
 * redirect（已映射）/ ready（重新请求 play）/ denied / timeout（可重试）。服务器不可用时显示错误与重试，
 * 不再回退为直连磁力。
 * `attempt` 变化（重试、登录状态变化）时重新请求；为 "pending"（/me 未加载）时暂不请求。
 * `known` 为列表里已知的 access：非 allow 时直接显示对应提示，不发请求。
 */
export function usePlayResolve(
  src: PlaySource | null,
  opts: { hint?: PlayHint; trackers: string[]; attempt: string; preload?: PlayPreload | null; known?: Access | null; file?: number | null },
) {
  const { attempt, preload, known } = opts
  // 只用于首次请求（不在依赖里：同一种子内切换文件不重新请求）
  const fileRef = useRef(opts.file)
  fileRef.current = opts.file
  const [state, setState] = useState<ResolveState>({ status: src ? "loading" : "idle" })
  const [round, setRound] = useState(0)
  const key = sourceKey(src)
  // 只有非 allow 的已知结果会影响判定；allow 与未知等价（播放页 ↔ 单集页切换时不重新请求）
  const knownAction = known && known.action !== "allow" ? known.action : ""

  useEffect(() => {
    if (!src) {
      setState({ status: "idle" })
      return
    }
    if (known && knownAction && round === 0) {
      setState(stateOf(stepOfPlay({ access: known })))
      return
    }
    // 跳转后由新页面接着请求：保留 redirect 状态之外的显示为 loading
    setState((s) => (s.status === "pending" ? s : { status: "loading" }))
    if (attempt === "pending") return
    if (
      preload &&
      round === 0 &&
      src.kind === "hash" &&
      preload.play.infohash === src.ih &&
      preload.attempt === attempt &&
      Date.now() - preload.at < PRELOAD_TTL_MS &&
      preload.play.access.action === "allow" &&
      preload.play.status !== "pending"
    ) {
      setState({ status: "ready", magnet: preload.play.magnet, play: preload.play, direct: false })
      return
    }

    const ctrl = new AbortController()
    let done = false
    let es: EventSource | null = null
    const replay = () => setRound((n) => n + 1)

    const apply = (step: ResolveStep, play?: PlayResponse) => {
      if (done) return
      switch (step.kind) {
        case "play":
          return setState({ status: "ready", magnet: play!.magnet, play: play!, direct: false })
        case "pending":
          setState({ status: "pending" })
          return listen(play!.infohash)
        case "redirect":
          es?.close()
          setState({ status: "redirect", path: step.path })
          return replay()
        case "replay":
          es?.close()
          return replay()
        default:
          es?.close()
          return setState(stateOf(step))
      }
    }

    const listen = (ih: string) => {
      es = new EventSource(api.resolveEventsURL(ih), { withCredentials: true })
      let errors = 0
      for (const type of ["redirect", "ready", "denied", "timeout"]) {
        es.addEventListener(type, (e) => {
          errors = 0
          let data: unknown = null
          try {
            data = JSON.parse((e as MessageEvent).data)
          } catch {
            /* ignore */
          }
          const step = stepOfEvent(type, data)
          if (step) {
            es?.close()
            apply(step)
          }
        })
      }
      es.onerror = () => {
        // 浏览器会自动重连；连续失败（未收到任何事件）后放弃
        if (++errors >= MAX_SSE_ERRORS && !done) {
          es?.close()
          setState({ status: "timeout" })
        }
      }
    }

    let timedOut = false
    const timer = window.setTimeout(() => {
      timedOut = true
      ctrl.abort()
    }, REQUEST_TIMEOUT_MS)
    const req = src.kind === "hash" ? api.playHash(src.ih, ctrl.signal, fileRef.current) : api.playMoe(src.id, ctrl.signal)
    req
      .then((play) => apply(stepOfPlay(play), play))
      .catch((e) => {
        if (done) return
        if (isApiError(e) && !e.unavailable) {
          if (e.status === 401) return setState({ status: "login", message: e.message })
          if (e.status === 402) return setState({ status: "vip", message: e.message })
          if (e.status === 451) return setState({ status: "blocked", message: e.message })
          if (e.status === 404) return setState({ status: "error", message: "未找到该种子，链接可能已失效。" })
          return setState({ status: "error", message: e.message })
        }
        const reason = isApiError(e) ? (e.status ? `服务器错误（${e.status}）` : "无法连接服务器") : timedOut ? "服务器响应超时" : "请求失败"
        setState({ status: "error", message: `${reason}，请稍后重试。` })
      })
      .finally(() => window.clearTimeout(timer))

    return () => {
      done = true
      ctrl.abort()
      es?.close()
      window.clearTimeout(timer)
    }
    // preload 只在请求发起时读取
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [key, attempt, round, knownAction])

  return { state, skipWait: () => {}, retry: () => setRound((n) => n + 1) }
}

function stateOf(step: ResolveStep): ResolveState {
  switch (step.kind) {
    case "gate":
      if (step.action === "login") return { status: "login", message: step.message }
      if (step.action === "vip") return { status: "vip", message: step.message }
      return { status: "blocked", message: step.message }
    case "timeout":
      return { status: "timeout" }
    case "pending":
      return { status: "pending" }
    case "redirect":
      return { status: "redirect", path: step.path }
  }
  return { status: "loading" }
}
