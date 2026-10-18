// 浏览器分块缓存的纯策略（预算、受保护范围、淘汰顺序）。不依赖 DOM / WebTorrent，可在 node --test 中测试。
// 设计见 docs/research/chunk-store.md 第 5 节。

export const MiB = 1 << 20
export const GiB = 1 << 30

/** 没有任何信息时的预算（站点配置与 estimate() 都拿不到） */
export const DEFAULT_BUDGET = 2 * GiB
/** 预算下限：太小时窗口装不下，一直超支 */
export const MIN_BUDGET = 32 * MiB

/** budget = min(site.max_push_size_bytes, 25% × quota)，两者缺失时取另一个；都缺失用默认值 */
export function computeBudget(maxPushBytes: number | null | undefined, quotaBytes: number | null | undefined): number {
  const a = maxPushBytes && maxPushBytes > 0 ? maxPushBytes : Infinity
  const b = quotaBytes && quotaBytes > 0 ? quotaBytes * 0.25 : Infinity
  const v = Math.min(a, b)
  return Math.floor(Number.isFinite(v) ? Math.max(MIN_BUDGET, v) : DEFAULT_BUDGET)
}

/** 写入遇到 QuotaExceededError：预算收缩到当前存储量的 90%（不低于 floor） */
export function shrinkOnQuota(storedBytes: number, currentBudget: number, floor: number): number {
  return Math.floor(Math.max(floor, Math.min(currentBudget, storedBytes) * 0.9))
}

export interface Protection {
  /** 读位置之前保留的字节 */
  behindBytes: number
  /** 读位置之后的读前窗口（也是窗口化读取的下载窗口） */
  aheadBytes: number
  /** 正在播放文件首尾各保留的字节（容器索引：MP4 moov、MKV Cues） */
  edgeBytes: number
}

/**
 * 受保护范围的大小。目标值：读前 max(32 MiB, 60 s 码率)、读后 8 MiB、首尾各 8 MiB；
 * 预算小时按比例缩小（读前 ≤ 40%、读后与首尾 ≤ 10%），但每项至少一个 piece（读前至少两个）。
 */
export function protectionFor(budget: number, pieceLength: number, bytesPerSec = 0): Protection {
  const pl = Math.max(1, pieceLength)
  return {
    aheadBytes: Math.floor(Math.max(2 * pl, Math.min(Math.max(32 * MiB, 60 * bytesPerSec), budget * 0.4))),
    behindBytes: Math.floor(Math.max(pl, Math.min(8 * MiB, budget * 0.1))),
    edgeBytes: Math.floor(Math.max(pl, Math.min(8 * MiB, budget * 0.1))),
  }
}

/** 读者：当前所在 piece，以及按 Protection 换算出的保护范围（含两端） */
export interface ReaderRange {
  piece: number
  from: number
  to: number
}

/** 读者所在 piece 及其保护范围（piece 序号，限定在 [first, last] 内） */
export function readerRange(absPos: number, pieceLength: number, prot: Protection, first: number, last: number): ReaderRange {
  const piece = Math.floor(absPos / pieceLength)
  return {
    piece,
    from: Math.max(first, Math.floor((absPos - prot.behindBytes) / pieceLength)),
    to: Math.min(last, Math.floor((absPos + prot.aheadBytes) / pieceLength)),
  }
}

/** 文件首尾受保护的 piece */
export function edgePieces(startPiece: number, endPiece: number, pieceLength: number, edgeBytes: number): number[] {
  const n = Math.max(1, Math.ceil(edgeBytes / pieceLength))
  const out: number[] = []
  for (let i = 0; i < n; i++) {
    if (startPiece + i <= endPiece) out.push(startPiece + i)
    if (endPiece - i >= startPiece) out.push(endPiece - i)
  }
  return out
}

/** 本 tab 正在播放的种子的状态 */
export interface LiveState {
  /** 当前读者（没有读者时为上次的读位置，没有过读位置时为空） */
  readers: ReaderRange[]
  /** 首尾、正在读取 / 写入的 piece */
  pinned: ReadonlySet<number>
}

export interface CacheEntry {
  key: string
  lastUsed: number
  /** piece 序号 → 字节数 */
  pieces: ReadonlyMap<number, number>
  /** 本 tab 正在播放；null/undefined = 闲置 */
  live?: LiveState | null
  /** 被其他 tab 占用（Web Locks），不能动 */
  busy?: boolean
}

export interface LegacyEntry {
  key: string
  bytes: number
}

export type Victim =
  | { kind: "legacy"; key: string; bytes: number }
  | { kind: "torrent"; key: string; bytes: number }
  | { kind: "piece"; key: string; index: number; bytes: number; reason: "behind" | "ahead"; distance: number }

/** 某个 piece 是否受保护（读窗口内或被钉住） */
export function isProtected(live: LiveState, index: number): boolean {
  if (live.pinned.has(index)) return true
  for (const r of live.readers) if (index >= r.from && index <= r.to) return true
  return false
}

/**
 * 选出下一个淘汰对象，顺序：
 * (0) legacy / 未知的 OPFS 条目；
 * (1) 不在播放（且未被其他 tab 占用）的种子，按整个种子 LRU；
 * (2) 正在播放的种子里读位置之后（在所有读者之前）的 piece，最远的优先；
 * (3) 读前窗口之外的 piece，离最近的（位于其前面的）读者最远的优先。
 * 受保护的 piece 永不淘汰；没有可淘汰的返回 null（允许暂时超出预算，由 quota 错误兜底）。
 * TODO（docs/plan.md）：按稀有度淘汰。
 */
export function pickVictim(legacy: readonly LegacyEntry[], entries: readonly CacheEntry[]): Victim | null {
  const lg = legacy.find((l) => l.bytes > 0) ?? legacy[0]
  if (lg) return { kind: "legacy", key: lg.key, bytes: lg.bytes }

  let idle: CacheEntry | null = null
  for (const e of entries) {
    if (e.live || e.busy || e.pieces.size === 0) continue
    if (!idle || e.lastUsed < idle.lastUsed) idle = e
  }
  if (idle) return { kind: "torrent", key: idle.key, bytes: sumBytes(idle.pieces) }

  let best: Extract<Victim, { kind: "piece" }> | null = null
  for (const e of entries) {
    const live = e.live
    if (!live || e.busy) continue
    const positions = live.readers.map((r) => r.piece)
    const minPos = positions.length ? Math.min(...positions) : 0
    for (const [index, bytes] of e.pieces) {
      if (isProtected(live, index)) continue
      let reason: "behind" | "ahead"
      let distance: number
      if (index < minPos) {
        reason = "behind"
        distance = minPos - index
      } else {
        reason = "ahead"
        // 距离它前面最近的读者
        let d = Infinity
        for (const p of positions) if (p <= index && index - p < d) d = index - p
        distance = Number.isFinite(d) ? d : index - minPos
      }
      if (!best || rank(reason) > rank(best.reason) || (reason === best.reason && distance > best.distance)) {
        best = { kind: "piece", key: e.key, index, bytes, reason, distance }
      }
    }
  }
  return best
}

const rank = (r: "behind" | "ahead") => (r === "behind" ? 2 : 1)

export function sumBytes(pieces: ReadonlyMap<number, number>) {
  let n = 0
  for (const b of pieces.values()) n += b
  return n
}

/** 由 piece 序号列表生成 WebTorrent 的启动 bitfield（长度必须是 ceil(numPieces/8)，高位在前） */
export function bitfieldFromPieces(numPieces: number, pieces: Iterable<number>): Uint8Array {
  const b = new Uint8Array(Math.ceil(numPieces / 8))
  for (const i of pieces) if (i >= 0 && i < numPieces) b[i >> 3] |= 128 >> (i % 8)
  return b
}

const B32 = "ABCDEFGHIJKLMNOPQRSTUVWXYZ234567"

/** 磁力链接里的 infohash（btih，hex 或 base32），统一为小写 hex；拿不到返回 null */
export function infoHashFromMagnet(magnet: string): string | null {
  const m = /xt=urn:btih:([0-9a-z]+)/i.exec(magnet)
  if (!m) return null
  const v = m[1]
  if (/^[0-9a-f]{40}$/i.test(v)) return v.toLowerCase()
  if (!/^[a-z2-7]{32}$/i.test(v)) return null
  let bits = 0
  let acc = 0
  let hex = ""
  for (const c of v.toUpperCase()) {
    acc = (acc << 5) | B32.indexOf(c)
    bits += 5
    while (bits >= 8) {
      bits -= 8
      hex += ((acc >> bits) & 255).toString(16).padStart(2, "0")
    }
  }
  return hex
}
