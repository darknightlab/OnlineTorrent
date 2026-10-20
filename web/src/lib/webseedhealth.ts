// WebSeed 健康状态（每个地址一份）：连续失败达到阈值时停用，按退避时间重试；
// 主站 SSE 推送了该地址的新 seed / token / ready 时立即重新启用。
//
// webtorrent 自己对失败的 WebSeed 只是 10 s 后重新宣告该分块、无限重试（过时 / 掉线的边缘节点会被
// 一直请求）。这里在 fetch 层观察响应（internals.observeWebSeed），停用时让连接不再宣告任何分块。

/** 失败种类 */
export type FailKind = "network" | "server" | "auth" | "unavailable" | "hash"

/**
 * 响应分类：ok = 拿到数据；expected = 正常的「暂时没有」（不算失败也不算成功）；其余为失败。
 * 边缘节点（internal/edge/agent.go serveWebseed）用 503 + 正文区分：
 *   "pieces not downloaded yet"（qB 还没下载到，边下边供的正常情况）、"busy"（并发上限）→ expected；
 *   "edge not ready"、"read failed" 等 → 失败。429（限流）也是 expected。
 */
export function classifyResponse(status: number, body = ""): "ok" | "expected" | FailKind {
  if (status >= 200 && status < 300) return "ok"
  if (status === 429) return "expected"
  if (status === 503 && /pieces not downloaded yet|^\s*busy\s*$/i.test(body)) return "expected"
  if (status === 401 || status === 403) return "auth"
  if (status === 503) return "unavailable"
  if (status >= 500) return "server"
  if (status === 404) return "unavailable" // 边缘节点上没有这个种子 / 文件
  return "server"
}

export interface HealthOptions {
  now?: () => number
  setTimer?: (cb: () => void, ms: number) => unknown
  clearTimer?: (t: unknown) => void
  /** 停用：n = 窗口内的连续失败次数，retryMs = 多久后重试 */
  onDisable: (n: number, retryMs: number, last: FailKind) => void
  /** 重新启用（retry：退避到时；sse：服务器推送了新状态） */
  onEnable: (why: "retry" | "sse") => void
  /** 停用后第一次成功响应 */
  onRecovered: () => void
  /** 401/403：请求刷新令牌（重新连接 SSE，服务器会重发 seed 及新令牌） */
  onTokenRefresh: () => void
  threshold?: number
  windowMs?: number
  /** 退避：base × 2^k，上限 max */
  baseMs?: number
  maxMs?: number
  /** 请求刷新令牌后，这段时间内的 401/403 视为旧令牌的在途请求，不计 */
  tokenGraceMs?: number
  /** 两次令牌刷新请求的最小间隔；间隔内（已刷新过）再出现的 401/403 计为失败 */
  tokenRetryMs?: number
}

export class WebSeedHealth {
  state: "ok" | "disabled" = "ok"
  /** 本轮连续失败（时间、种类）：传输成功清掉传输类失败，分块校验通过才清掉校验失败 */
  private fails: { t: number; kind: FailKind }[] = []
  private lastKind: FailKind = "network"
  /** 退避级别：停用一次 +1，成功后归零 */
  private level = 0
  /** 停用过、还没见到成功 */
  private recovering = false
  private timer: unknown = null
  private tokenAskedAt = -Infinity
  private tokenArrivedAt = -Infinity
  private o: Required<HealthOptions>

  constructor(o: HealthOptions) {
    this.o = {
      now: () => Date.now(),
      setTimer: (cb, ms) => setTimeout(cb, ms),
      clearTimer: (t) => clearTimeout(t as ReturnType<typeof setTimeout>),
      threshold: 3,
      windowMs: 30_000,
      baseMs: 15_000,
      maxMs: 300_000,
      tokenGraceMs: 10_000,
      tokenRetryMs: 60_000,
      ...o,
    }
  }

  /** 当前停用的退避时间 */
  backoffMs(level = this.level) {
    return Math.min(this.o.maxMs, this.o.baseMs * 2 ** level)
  }

  /**
   * transport：拿到了 2xx 响应（数据未必正确）；verified：它提供的分块通过了校验。
   * 返回错误数据的服务器每个请求都是 2xx，所以 2xx 不清掉校验失败，也不算「已恢复」。
   */
  success(kind: "transport" | "verified" = "verified") {
    this.fails = kind === "verified" ? [] : this.fails.filter((f) => f.kind === "hash")
    if (this.state !== "ok") return // 停用前发出的请求晚到的成功：不算恢复
    if (kind === "transport" && this.lastKind === "hash" && this.recovering) return
    this.level = 0
    if (this.recovering) {
      this.recovering = false
      this.o.onRecovered()
    }
  }

  failure(kind: FailKind) {
    if (this.state === "disabled") return // 停用前在途的请求
    const now = this.o.now()
    if (kind === "auth") {
      // 先刷新令牌再说：第一次 401/403 请求新令牌；等待期间（新令牌未到、宽限期内）旧令牌的请求不计
      if (now - this.tokenAskedAt >= this.o.tokenRetryMs) {
        this.tokenAskedAt = now
        this.o.onTokenRefresh()
        return
      }
      if (this.tokenArrivedAt < this.tokenAskedAt && now - this.tokenAskedAt < this.o.tokenGraceMs) return
    }
    this.lastKind = kind
    this.fails = this.fails.filter((f) => now - f.t < this.o.windowMs)
    this.fails.push({ t: now, kind })
    if (this.fails.length >= this.o.threshold) this.disable()
  }

  /** SSE 推送了该地址的 seed / token / ready：新令牌到达；停用中则立即重新启用 */
  refreshed(kind: "seed" | "token" | "ready") {
    if (kind === "token" || kind === "seed") this.tokenArrivedAt = this.o.now()
    if (this.state === "disabled") this.enable("sse")
  }

  destroy() {
    if (this.timer !== null) this.o.clearTimer(this.timer)
    this.timer = null
  }

  private disable() {
    const n = this.fails.length
    const ms = this.backoffMs()
    this.level++
    this.state = "disabled"
    this.recovering = true
    this.fails = []
    this.o.onDisable(n, ms, this.lastKind)
    this.timer = this.o.setTimer(() => {
      this.timer = null
      this.enable("retry")
    }, ms)
  }

  private enable(why: "retry" | "sse") {
    if (this.state === "ok") return
    if (this.timer !== null) this.o.clearTimer(this.timer)
    this.timer = null
    this.state = "ok"
    this.fails = []
    this.o.onEnable(why)
  }
}
