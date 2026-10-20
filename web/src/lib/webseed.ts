// WebSeed 可用性（服务器 SSE）的纯逻辑：分块区间编码、bitfield 更新、断线重连退避。
// 不依赖 React / DOM，便于 node --test（webseed.test.ts）。协议见 internal/api/webseed_progressive.go。

/** 分块区间：闭区间 [start, end] 依次展开，如 [0, 41, 50, 50] = 0..41 与 50 */
export type Ranges = number[]

/** 在 BitTorrent 顺序的 bitfield（第 0 块 = 第 0 字节最高位）上设置 / 清除 ranges，返回实际变化的分块 */
export function applyRanges(bits: Uint8Array, numPieces: number, ranges: Ranges, value: boolean): number[] {
  const changed: number[] = []
  for (let k = 0; k + 1 < ranges.length; k += 2) {
    const from = Math.max(0, Math.floor(ranges[k]))
    const to = Math.min(numPieces - 1, Math.floor(ranges[k + 1]))
    for (let i = from; i <= to; i++) {
      const m = 128 >> (i & 7)
      const had = (bits[i >> 3] & m) !== 0
      if (had === value) continue
      if (value) bits[i >> 3] |= m
      else bits[i >> 3] &= ~m
      changed.push(i)
    }
  }
  return changed
}

export function hasPiece(bits: Uint8Array, i: number) {
  return (bits[i >> 3] & (128 >> (i & 7))) !== 0
}

/** 从 ranges 生成 bitfield */
export function bitsFromRanges(numPieces: number, ranges: Ranges): Uint8Array {
  const b = new Uint8Array(Math.ceil(numPieces / 8))
  applyRanges(b, numPieces, ranges, true)
  return b
}

/** 把 bits 变成与 ranges 完全一致：返回新增与移除的分块 */
export function resetToRanges(bits: Uint8Array, numPieces: number, ranges: Ranges): { added: number[]; removed: number[] } {
  const want = bitsFromRanges(numPieces, ranges)
  const added: number[] = []
  const removed: number[] = []
  for (let i = 0; i < numPieces; i++) {
    const a = hasPiece(bits, i)
    const b = hasPiece(want, i)
    if (a === b) continue
    if (b) added.push(i)
    else removed.push(i)
  }
  bits.set(want)
  return { added, removed }
}

export function countPieces(bits: Uint8Array, numPieces: number, from = 0, to = numPieces - 1) {
  let n = 0
  for (let i = Math.max(0, from); i <= Math.min(to, numPieces - 1); i++) if (hasPiece(bits, i)) n++
  return n
}

export function isRanges(v: unknown): v is Ranges {
  return Array.isArray(v) && v.length % 2 === 0 && v.every((x) => Number.isInteger(x) && x >= 0)
}

// ---------------------------------------------------------------- 订阅与重连

/** 重连间隔：15 s、30 s、60 s、2 min、4 min，之后每 5 min */
export const RETRY_STEPS_MS = [15_000, 30_000, 60_000, 120_000, 240_000, 300_000]

export function retryDelay(attempt: number) {
  return RETRY_STEPS_MS[Math.min(Math.max(0, attempt), RETRY_STEPS_MS.length - 1)]
}

/** 一个 WebSeed 地址（某个边缘节点）及其访问令牌 */
export interface SeedInfo {
  url: string
  pieces: number | null
  have: Ranges | null
  complete: boolean
  /** 边缘节点的访问令牌（Authorization: Bearer）与过期时间（unix 秒） */
  token?: string
  exp?: number
}

function parseSeed(v: unknown, pieces: number | null): SeedInfo | null {
  const o = (v ?? {}) as Record<string, unknown>
  if (typeof o.url !== "string" || !o.url) return null
  return {
    url: o.url,
    pieces,
    have: isRanges(o.have) ? o.have : null,
    complete: o.complete === true,
    token: typeof o.token === "string" && o.token ? o.token : undefined,
    exp: typeof o.exp === "number" ? o.exp : undefined,
  }
}

/** EventSource 的最小接口（便于测试） */
export interface EventSourceLike {
  readonly readyState: number
  addEventListener(type: string, cb: (e: { data?: unknown }) => void): void
  onerror: ((e: unknown) => void) | null
  close(): void
}

export type SubState = "connecting" | "open" | "waiting-retry" | "finished" | "stopped"

export interface SubscriptionDeps {
  open(): EventSourceLike
  setTimeout(cb: () => void, ms: number): unknown
  clearTimeout(t: unknown): void
}

export interface SubscriptionHandlers {
  /** 一个 WebSeed 地址可用（ready 里的每一项、之后的 seed 事件；重连后同一地址会再来一次） */
  seed(s: SeedInfo): void
  have(url: string, ranges: Ranges): void
  lost(url: string, ranges: Ranges): void
  /** 该地址已能提供全部分块 */
  complete(url: string): void
  /** 边缘节点的新访问令牌（到期前推送） */
  token(url: string, token: string, exp: number): void
  /** 所有地址都能提供全部分块，订阅结束 */
  done(): void
  /** 服务器还没有元数据 */
  waiting(progress: number): void
  /** 服务器说暂不可用（not_seeded / disabled / forbidden …） */
  unavailable(reason: string): void | Promise<void>
  /** 连接状态变化（只在变化时调用，用于日志） */
  state(s: SubState, info?: { retryInMs?: number; attempt?: number }): void
}

/**
 * 一个 WebSeed 状态订阅：永不放弃（直到 stop、complete），断线 / timeout / unavailable 后
 * 按 retryDelay 退避重连；页面重新可见、网络恢复时（kick）立即重连。
 */
export class WebseedSubscription {
  private es: EventSourceLike | null = null
  private timer: unknown = null
  private attempt = 0
  private st: SubState = "stopped"
  private deps: SubscriptionDeps
  private h: SubscriptionHandlers

  constructor(deps: SubscriptionDeps, h: SubscriptionHandlers) {
    this.deps = deps
    this.h = h
  }

  get state() {
    return this.st
  }

  get attempts() {
    return this.attempt
  }

  private set(s: SubState, info?: { retryInMs?: number; attempt?: number }) {
    if (s === this.st) return
    this.st = s
    this.h.state(s, info)
  }

  start() {
    if (this.st === "finished") return
    this.connect()
  }

  stop() {
    this.closeES()
    this.clearTimer()
    this.set("stopped")
  }

  /**
   * 立即重新连接：需要新的边缘节点令牌时（服务器在连接时重发 seed 及令牌）。已结束（done：全部完整，
   * 服务器不再推送令牌刷新）的订阅也重新打开——否则令牌过期后边缘节点只会回 401。stop 之后不做。
   */
  reconnect() {
    if (this.st === "stopped") return
    this.clearTimer()
    this.connect()
  }

  /** 页面可见 / 网络恢复：如果正在等待重试，立即重连 */
  kick() {
    if (this.st !== "waiting-retry") return
    this.clearTimer()
    this.connect()
  }

  private clearTimer() {
    if (this.timer !== null) this.deps.clearTimeout(this.timer)
    this.timer = null
  }

  private closeES() {
    const es = this.es
    this.es = null
    es?.close()
  }

  private connect() {
    this.closeES()
    this.set("connecting")
    const es = this.deps.open()
    this.es = es
    const mine = (fn: (data: unknown) => void) => (e: { data?: unknown }) => {
      if (this.es !== es) return
      let data: unknown = null
      try {
        data = typeof e.data === "string" ? JSON.parse(e.data) : null
      } catch {
        data = null
      }
      fn(data)
    }
    const got = (progress = true) => {
      // 收到事件：连接正常；有进展的事件（ready/have/lost/waiting/complete）才把退避清零，
      // unavailable / timeout 不清零（否则「未做种」会一直每 15 s 重连）
      if (progress) this.attempt = 0
      if (this.st === "connecting") this.set("open")
    }
    let pieces: number | null = null
    es.addEventListener(
      "ready",
      mine((d) => {
        got()
        const r = (d ?? {}) as { pieces?: unknown; seeds?: unknown }
        pieces = typeof r.pieces === "number" && r.pieces > 0 ? r.pieces : null
        for (const x of Array.isArray(r.seeds) ? r.seeds : []) {
          const sd = parseSeed(x, pieces)
          if (sd) this.h.seed(sd)
        }
      }),
    )
    es.addEventListener(
      "seed",
      mine((d) => {
        got()
        const sd = parseSeed(d, pieces)
        if (sd) this.h.seed(sd)
      }),
    )
    const urlRanges = (d: unknown) => {
      const o = (d ?? {}) as { url?: unknown; ranges?: unknown }
      return typeof o.url === "string" && isRanges(o.ranges) ? { url: o.url, ranges: o.ranges } : null
    }
    es.addEventListener(
      "have",
      mine((d) => {
        got()
        const x = urlRanges(d)
        if (x) this.h.have(x.url, x.ranges)
      }),
    )
    es.addEventListener(
      "lost",
      mine((d) => {
        got()
        const x = urlRanges(d)
        if (x) this.h.lost(x.url, x.ranges)
      }),
    )
    es.addEventListener(
      "complete",
      mine((d) => {
        got()
        const u = (d as { url?: unknown } | null)?.url
        if (typeof u === "string") this.h.complete(u)
      }),
    )
    es.addEventListener(
      "token",
      mine((d) => {
        got()
        const o = (d ?? {}) as { url?: unknown; token?: unknown; exp?: unknown }
        if (typeof o.url === "string" && typeof o.token === "string" && o.token) this.h.token(o.url, o.token, typeof o.exp === "number" ? o.exp : 0)
      }),
    )
    es.addEventListener(
      "done",
      mine(() => {
        got()
        this.h.done()
        this.finish()
      }),
    )
    es.addEventListener(
      "waiting",
      mine((d) => {
        got()
        const p = (d as { progress?: unknown } | null)?.progress
        this.h.waiting(typeof p === "number" ? p : 0)
      }),
    )
    const end = (reason: string) => {
      this.closeES()
      void Promise.resolve(this.h.unavailable(reason)).finally(() => {
        if (this.st !== "stopped" && this.st !== "finished") this.retryLater()
      })
    }
    es.addEventListener(
      "unavailable",
      mine((d) => {
        got(false)
        const reason = (d as { reason?: unknown } | null)?.reason
        end(typeof reason === "string" ? reason : "unknown")
      }),
    )
    es.addEventListener(
      "timeout",
      mine(() => {
        got(false)
        end("timeout")
      }),
    )
    es.onerror = () => {
      if (this.es !== es) return
      // 不用浏览器自带的重连（固定间隔）：关掉，按自己的退避重连
      this.closeES()
      this.retryLater()
    }
  }

  private retryLater() {
    this.clearTimer()
    const ms = retryDelay(this.attempt)
    this.attempt++
    this.set("waiting-retry", { retryInMs: ms, attempt: this.attempt })
    this.timer = this.deps.setTimeout(() => {
      this.timer = null
      if (this.st === "waiting-retry") this.connect()
    }, ms)
  }

  private finish() {
    this.closeES()
    this.clearTimer()
    this.set("finished")
  }
}
