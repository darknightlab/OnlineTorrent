// 浏览器分块缓存的管理器（每个 tab 一个）：OPFS 扫描、预算、淘汰、多 tab 锁。
// 目录结构：OPFS 根/ot-pieces/<infohash>/{meta.json, 0, 1, 2, …}（每个已校验的 piece 一个文件）；
// 同一种子已在其他 tab 打开时用临时目录 ot-pieces/<infohash>~<tabId>，关闭或下次启动时清理。
// 根目录下除 ot-pieces 外的一切（旧版 fsa-chunk-store 的 "<name> - <ih8>" 目录、chunks 等）都算 legacy，最先清理。
// 设计见 docs/research/chunk-store.md。
import { bitfieldFromPieces, computeBudget, edgePieces, pickVictim, protectionFor, readerRange, shrinkOnQuota, sumBytes, type CacheEntry, type LegacyEntry, type LiveState, type Protection } from "./policy"
import { unverifyPiece, type PieceCtor, type Reader, type TorrentLike } from "./internals"

export const ROOT = "ot-pieces"
const META = "meta.json"
/** 种子文件（.torrent）：再次打开时直接用它添加，不必等节点 / 服务器提供元数据 */
const TORRENT = "torrent"
const LOCK_PREFIX = "ot:"
const REFRESH_MS = 5 * 60_000
const REFRESH_BYTES = 256 * (1 << 20)
const LEGACY_STALE_MS = 10 * 60_000
/** 调试 / 测试用：固定预算（字节） */
const OVERRIDE_KEY = "ot.cacheBudgetBytes"

export interface Meta {
  v: 1
  infoHash: string
  name: string
  length: number
  pieceLength: number
  numPieces: number
  lastUsed: number
}

export interface LiveCtx {
  torrent: TorrentLike
  Piece: PieceCtor | null
  readers: Set<Reader>
  /** 最近一次读位置（种子内的绝对字节偏移） */
  lastPos: number | null
  reading: Map<number, number>
  writing: Set<number>
  /** 正在播放的文件 [startPiece, endPiece] */
  focus: [number, number] | null
  bytesPerSec: number
  /** 兼容性检查没通过时为 false：只删整个闲置种子，不动正在播放的种子的内部状态 */
  evictable: boolean
}

export interface Entry {
  key: string
  ih: string
  temp: boolean
  meta: Meta | null
  pieces: Map<number, number>
  lastUsed: number
  live: LiveCtx | null
  dir: FileSystemDirectoryHandle | null
  /** 正在删除的 piece 文件（同一 piece 重新写入前要等删除完成） */
  deleting: Map<number, Promise<void>>
}

interface Legacy extends LegacyEntry {
  path: string[]
}

export type CacheLog = (level: "info" | "warn" | "error", msg: string) => void

export interface CacheStats {
  supported: boolean
  used: number
  budget: number
  videos: number
}

export interface Lease {
  key: string
  ih: string
  temp: boolean
  release(): Promise<void>
}

const fmt = (n: number) => (n >= 1 << 30 ? `${(n / (1 << 30)).toFixed(2)} GB` : `${(n / (1 << 20)).toFixed(1)} MB`)
const tabId = Math.random().toString(36).slice(2, 10)

export const opfsSupported = () =>
  typeof navigator !== "undefined" && !!navigator.storage?.getDirectory && typeof FileSystemFileHandle !== "undefined" && "createWritable" in FileSystemFileHandle.prototype

class PieceCache {
  entries = new Map<string, Entry>()
  legacy: Legacy[] = []
  budget = 0
  /** QuotaExceededError 之后的上限（本 tab 内一直有效） */
  quotaCap = Infinity
  /** 已申请、还没写完的字节 */
  pending = 0
  private maxPush: number | null = null
  private root: FileSystemDirectoryHandle | null = null
  private base: FileSystemDirectoryHandle | null = null
  private initP: Promise<void> | null = null
  private written = 0
  private timer = 0
  private logs = new Set<CacheLog>()
  private evictedPieces = 0
  private evictedBytes = 0
  private evictTimer = 0
  private releases = new Map<string, Promise<void>>()
  stats = { evicted: 0, evictedBytes: 0, quotaErrors: 0, maxStored: 0, memFallbacks: 0 }

  onLog(cb: CacheLog) {
    this.logs.add(cb)
    return () => void this.logs.delete(cb)
  }
  log(level: "info" | "warn" | "error", msg: string) {
    if (level !== "info") console[level === "error" ? "error" : "warn"]("[cache]", msg)
    this.logs.forEach((cb) => cb(level, msg))
  }

  get stored() {
    let n = this.pending
    for (const e of this.entries.values()) n += sumBytes(e.pieces)
    for (const l of this.legacy) n += l.bytes
    return n
  }

  /** 首次调用时扫描 OPFS；maxPush 为 site.max_push_size_bytes */
  init(maxPush?: number | null) {
    if (maxPush) this.maxPush = maxPush
    if (!this.initP) this.initP = this.doInit()
    return this.initP
  }

  private async doInit() {
    if (!opfsSupported()) throw new Error("浏览器不支持 OPFS（navigator.storage.getDirectory / createWritable）")
    this.root = await navigator.storage.getDirectory()
    this.base = await this.root.getDirectoryHandle(ROOT, { create: true })
    await this.scan()
    await this.refresh()
    this.timer = window.setInterval(() => void this.refresh(), REFRESH_MS)
  }

  private async heldLocks(): Promise<Set<string>> {
    if (!navigator.locks?.query) return new Set()
    const q = await navigator.locks.query()
    return new Set([...(q.held ?? []), ...(q.pending ?? [])].map((l) => l.name ?? ""))
  }

  /** 扫描 OPFS：本 tab 正在用的条目不动，其余按磁盘内容重建 */
  async scan() {
    const root = this.root!
    const base = this.base!
    const held = await this.heldLocks()
    const now = Date.now()
    const legacy: Legacy[] = []
    for await (const [name, h] of root.entries()) {
      if (name === ROOT) continue
      const { bytes, mtime } = await sizeOf(h)
      if (now - mtime > LEGACY_STALE_MS) {
        await root.removeEntry(name, { recursive: true }).catch(() => {})
        continue
      }
      legacy.push({ key: name, bytes, path: [name] })
    }
    const seen = new Set<string>()
    for await (const [name, h] of base.entries()) {
      if (this.entries.get(name)?.live) {
        seen.add(name)
        continue
      }
      const m = /^([0-9a-f]{40})(~[a-z0-9]+)?$/.exec(name)
      if (h.kind !== "directory" || !m) {
        legacy.push({ key: `${ROOT}/${name}`, bytes: (await sizeOf(h)).bytes, path: [ROOT, name] })
        continue
      }
      const temp = !!m[2]
      const busy = held.has(LOCK_PREFIX + name)
      if (temp && !busy) {
        await base.removeEntry(name, { recursive: true }).catch(() => {})
        continue
      }
      const dir = h as FileSystemDirectoryHandle
      const meta = await readMeta(dir)
      if ((!meta || meta.infoHash !== m[1]) && !busy) {
        legacy.push({ key: `${ROOT}/${name}`, bytes: (await sizeOf(dir)).bytes, path: [ROOT, name] })
        continue
      }
      const pieces = new Map<number, number>()
      if (meta) for await (const fname of dir.keys()) {
        if (!/^\d+$/.test(fname)) continue
        const i = Number(fname)
        if (i < meta.numPieces) pieces.set(i, pieceSize(meta, i))
        else await dir.removeEntry(fname).catch(() => {})
      }
      seen.add(name)
      this.entries.set(name, { key: name, ih: m[1], temp, meta, pieces, lastUsed: meta?.lastUsed || 0, live: null, dir, deleting: new Map() })
    }
    for (const k of [...this.entries.keys()]) if (!seen.has(k)) this.entries.delete(k)
    this.legacy = legacy
  }

  /** 重新计算预算：min(site.max_push_size_bytes, 25% quota)，并受 quota 错误后的上限约束 */
  async refresh() {
    this.written = 0
    let quota: number | undefined
    try {
      quota = (await navigator.storage.estimate()).quota
    } catch {
      /* ignore */
    }
    const override = Number(localStorage.getItem(OVERRIDE_KEY) || 0)
    const next = Math.min(override > 0 ? override : computeBudget(this.maxPush, quota), this.quotaCap)
    if (next !== this.budget) {
      const first = this.budget === 0
      this.budget = next
      if (!first) this.log("info", `缓存预算调整为 ${fmt(next)}`)
      if (this.stored > next) await this.makeRoom(null, -1, 0)
    }
  }

  snapshot(): CacheStats {
    return { supported: opfsSupported(), used: this.stored, budget: this.budget, videos: [...this.entries.values()].filter((e) => e.pieces.size > 0).length }
  }

  // ------------------------------------------------------------ 锁与条目

  /**
   * 打开种子前取得 Web Lock `ot:<ih>`；已被其他 tab 持有时改用本 tab 的临时目录（同样加锁，防止被别的 tab 清理）。
   * 同一 tab 内上一个会话尚未释放时先等它。
   */
  async acquire(ih: string): Promise<Lease> {
    await this.releases.get(ih)
    let release: () => void = () => {}
    const tryLock = (name: string, ifAvailable: boolean) =>
      new Promise<boolean>((resolve) => {
        if (!navigator.locks?.request) return resolve(true)
        navigator.locks
          .request(name, { ifAvailable }, (lock) => {
            if (!lock) {
              resolve(false)
              return
            }
            resolve(true)
            return new Promise<void>((r) => (release = r))
          })
          .catch(() => resolve(false))
      })
    let key = ih
    let temp = false
    if (!(await tryLock(LOCK_PREFIX + ih, true))) {
      key = `${ih}~${tabId}`
      temp = true
      await tryLock(LOCK_PREFIX + key, false)
    }
    let released: Promise<void> | null = null
    return {
      key,
      ih,
      temp,
      release: () => {
        if (released) return released
        released = (async () => {
          const e = this.entries.get(key)
          if (e) e.live = null
          if (temp) {
            this.entries.delete(key)
            await this.base?.removeEntry(key, { recursive: true }).catch(() => {})
          }
          release()
        })()
        this.releases.set(ih, released)
        void released.then(() => this.releases.get(ih) === released && this.releases.delete(ih))
        return released
      },
    }
  }

  /**
   * 启动 bitfield：只包含本目录里已有文件、且文件大小正确的 piece（文件只在 SHA1 校验通过后写入，
   * createWritable 在 close() 时原子提交，所以存在即校验过）。meta 缺失 / 损坏返回 undefined（WebTorrent 正常校验）。
   */
  async resumeBitfield(key: string): Promise<Uint8Array | undefined> {
    const e = this.entries.get(key)
    const meta = e?.meta
    if (!e || !meta || !e.pieces.size) return undefined
    try {
      const dir = e.dir ?? (await this.base!.getDirectoryHandle(key))
      const ok: number[] = []
      const idx = [...e.pieces.keys()]
      for (let k = 0; k < idx.length; k += 32) {
        await Promise.all(
          idx.slice(k, k + 32).map(async (i) => {
            try {
              const f = await (await dir.getFileHandle(String(i))).getFile()
              if (f.size === pieceSize(meta, i)) ok.push(i)
              else {
                e.pieces.delete(i)
                await dir.removeEntry(String(i)).catch(() => {})
              }
            } catch {
              e.pieces.delete(i)
            }
          }),
        )
      }
      return ok.length ? bitfieldFromPieces(meta.numPieces, ok) : undefined
    } catch {
      return undefined
    }
  }

  /** store 构造时调用：登记为本 tab 正在播放，准备目录与 meta.json */
  attach(key: string, ih: string, temp: boolean, torrent: TorrentLike, meta: Omit<Meta, "v" | "lastUsed">) {
    let e = this.entries.get(key)
    if (!e) {
      e = { key, ih, temp, meta: null, pieces: new Map(), lastUsed: Date.now(), live: null, dir: null, deleting: new Map() }
      this.entries.set(key, e)
    }
    const entry = e
    entry.lastUsed = Date.now()
    entry.live = { torrent, Piece: null, readers: new Set(), lastPos: null, reading: new Map(), writing: new Set(), focus: null, bytesPerSec: 0, evictable: true }
    const ready = (async () => {
      const dir = await this.base!.getDirectoryHandle(key, { create: true })
      entry.dir = dir
      const old = entry.meta
      if (old && (old.pieceLength !== meta.pieceLength || old.length !== meta.length || old.numPieces !== meta.numPieces)) {
        // 与种子不符（不应发生）：清空
        for (const i of entry.pieces.keys()) await dir.removeEntry(String(i)).catch(() => {})
        entry.pieces.clear()
      }
      entry.meta = { v: 1, ...meta, lastUsed: Date.now() }
      await this.writeMeta(entry)
    })()
    return { entry, ready }
  }

  async writeMeta(e: Entry) {
    if (!e.meta || !e.dir) return
    e.meta.lastUsed = e.lastUsed
    try {
      const w = await (await e.dir.getFileHandle(META, { create: true })).createWritable()
      await w.write(JSON.stringify(e.meta))
      await w.close()
    } catch {
      /* 配额不足等：meta 只影响 LRU 与续传，下次再写 */
    }
  }

  async saveTorrentFile(key: string, buf: Uint8Array) {
    const e = this.entries.get(key)
    if (!e || e.temp || !buf?.length) return
    try {
      const dir = e.dir ?? (await this.base!.getDirectoryHandle(key, { create: true }))
      const w = await (await dir.getFileHandle(TORRENT, { create: true })).createWritable()
      await w.write(buf as Uint8Array<ArrayBuffer>)
      await w.close()
    } catch {
      /* ignore */
    }
  }

  async loadTorrentFile(key: string): Promise<Uint8Array | null> {
    const e = this.entries.get(key)
    if (!e?.meta || !e.pieces.size) return null
    try {
      const dir = e.dir ?? (await this.base!.getDirectoryHandle(key))
      return new Uint8Array(await (await (await dir.getFileHandle(TORRENT)).getFile()).arrayBuffer())
    } catch {
      return null
    }
  }

  async dropTorrentFile(key: string) {
    try {
      await (await this.base!.getDirectoryHandle(key)).removeEntry(TORRENT)
    } catch {
      /* ignore */
    }
  }

  async detach(e: Entry) {
    e.lastUsed = Date.now()
    e.live = null
    if (!e.temp) await this.writeMeta(e)
  }

  // ------------------------------------------------------------ 预算与淘汰

  protection(e: Entry): Protection {
    return protectionFor(this.budget, e.meta?.pieceLength ?? 1 << 20, e.live?.bytesPerSec ?? 0)
  }

  private liveState(e: Entry): LiveState | null {
    const l = e.live
    if (!l) return null
    const meta = e.meta
    const pl = meta?.pieceLength ?? l.torrent.pieceLength
    const last = (meta?.numPieces ?? l.torrent.pieces?.length ?? 1) - 1
    const prot = this.protection(e)
    const positions = [...l.readers].map((r) => r.file.offset + r.pos)
    if (!positions.length && l.lastPos !== null) positions.push(l.lastPos)
    const pinned = new Set<number>([...l.reading.keys(), ...l.writing])
    if (l.focus) for (const i of edgePieces(l.focus[0], l.focus[1], pl, prot.edgeBytes)) pinned.add(i)
    return { readers: positions.map((p) => readerRange(Math.min(p, Math.max(0, (meta?.length ?? p + 1) - 1)), pl, prot, 0, last)), pinned }
  }

  /** 为 entry 的 piece index 申请 size 字节：必要时按策略淘汰，然后计入 pending */
  async reserve(e: Entry, index: number, size: number) {
    await this.makeRoom(e, index, size)
    this.pending += size
    this.stats.maxStored = Math.max(this.stats.maxStored, this.stored)
  }

  unreserve(size: number) {
    this.pending -= size
  }

  /** 淘汰直到 stored + size ≤ budget；没有可淘汰的就允许暂时超出 */
  async makeRoom(_e: Entry | null, _index: number, size: number) {
    let held: Set<string> | null = null
    while (this.stored + size > this.budget) {
      if (!held) held = await this.heldLocks()
      const cands: CacheEntry[] = []
      for (const x of this.entries.values()) {
        const live = x.live ? (x.live.evictable ? this.liveState(x) : { readers: [], pinned: new Set(x.pieces.keys()) }) : null
        cands.push({ key: x.key, lastUsed: x.lastUsed, pieces: x.pieces, live, busy: !x.live && held.has(LOCK_PREFIX + x.key) })
      }
      const v = pickVictim(this.legacy, cands)
      if (!v) return
      if (v.kind === "legacy") {
        const l = this.legacy.find((x) => x.key === v.key)!
        this.legacy.splice(this.legacy.indexOf(l), 1)
        await removePath(this.root!, l.path)
        this.log("info", `已清理旧版缓存 ${l.key}（${fmt(l.bytes)}）`)
      } else if (v.kind === "torrent") {
        const x = this.entries.get(v.key)!
        this.entries.delete(v.key)
        await this.base!.removeEntry(v.key, { recursive: true }).catch(() => {})
        this.log("info", `已清理较早观看的视频缓存「${x.meta?.name ?? v.key}」（${fmt(v.bytes)}）`)
      } else {
        this.evictPiece(this.entries.get(v.key)!, v.index)
      }
    }
  }

  /** 淘汰正在播放的种子里的一个 piece：先让 WebTorrent 忘掉它（并通知节点），再删文件 */
  evictPiece(e: Entry, i: number) {
    const size = e.pieces.get(i)
    if (size === undefined) return
    e.pieces.delete(i)
    const l = e.live
    if (l?.Piece && !l.torrent.destroyed) unverifyPiece(l.torrent, i, l.Piece)
    const p = (async () => {
      try {
        await (e.dir ?? (await this.base!.getDirectoryHandle(e.key))).removeEntry(String(i))
      } catch {
        /* ignore */
      }
    })()
    e.deleting.set(i, p)
    void p.then(() => e.deleting.get(i) === p && e.deleting.delete(i))
    this.stats.evicted++
    this.stats.evictedBytes += size
    this.evictedPieces++
    this.evictedBytes += size
    if (!this.evictTimer) {
      this.evictTimer = window.setTimeout(() => {
        this.evictTimer = 0
        this.log("info", `已清理 ${this.evictedPieces} 个分块（${fmt(this.evictedBytes)}）以腾出空间`)
        this.evictedPieces = 0
        this.evictedBytes = 0
      }, 3000)
    }
  }

  /** 写入成功后登记 */
  markStored(e: Entry, i: number, size: number) {
    this.pending -= size
    e.pieces.set(i, size)
    this.written += size
    if (this.written >= REFRESH_BYTES) void this.refresh()
  }

  /** QuotaExceededError：预算收缩到当前存储量的 90% 并淘汰 */
  async onQuotaExceeded(e: Entry) {
    this.stats.quotaErrors++
    const floor = Math.max(4 * (e.meta?.pieceLength ?? 1 << 20), 2 << 20)
    const next = shrinkOnQuota(this.stored, this.budget, floor)
    if (next < this.budget) {
      this.budget = next
      this.quotaCap = next
      this.log("warn", `浏览器存储配额不足，缓存预算降为 ${fmt(next)}`)
    }
    await this.makeRoom(e, -1, 0)
  }

  // ------------------------------------------------------------ 我的 → 缓存

  /** 清空缓存：删除所有闲置条目与 legacy；正在播放（本 tab 或其他 tab）的跳过。返回跳过的数量 */
  async clearAll(): Promise<number> {
    await this.init()
    await this.scan()
    const held = await this.heldLocks()
    let skipped = 0
    for (const l of this.legacy) await removePath(this.root!, l.path)
    this.legacy = []
    for (const e of [...this.entries.values()]) {
      if (e.live || held.has(LOCK_PREFIX + e.key)) {
        skipped++
        continue
      }
      this.entries.delete(e.key)
      await this.base!.removeEntry(e.key, { recursive: true }).catch(() => {})
    }
    return skipped
  }

  async statsFresh(maxPush?: number | null): Promise<CacheStats> {
    if (!opfsSupported()) return { supported: false, used: 0, budget: 0, videos: 0 }
    await this.init(maxPush)
    if (maxPush && maxPush !== this.maxPush) {
      this.maxPush = maxPush
      await this.refresh()
    }
    await this.scan()
    return this.snapshot()
  }

  dispose() {
    window.clearInterval(this.timer)
  }
}

export function pieceSize(m: { length: number; pieceLength: number; numPieces: number }, i: number) {
  return i === m.numPieces - 1 ? m.length - m.pieceLength * (m.numPieces - 1) : m.pieceLength
}

async function readMeta(dir: FileSystemDirectoryHandle): Promise<Meta | null> {
  try {
    const m = JSON.parse(await (await (await dir.getFileHandle(META)).getFile()).text()) as Meta
    if (m.v !== 1 || !(m.pieceLength > 0) || !(m.length > 0) || m.numPieces !== Math.ceil(m.length / m.pieceLength)) return null
    return m
  } catch {
    return null
  }
}

async function sizeOf(h: FileSystemHandle): Promise<{ bytes: number; mtime: number }> {
  try {
    if (h.kind === "file") {
      const f = await (h as FileSystemFileHandle).getFile()
      return { bytes: f.size, mtime: f.lastModified }
    }
    let bytes = 0
    let mtime = 0
    for await (const c of (h as FileSystemDirectoryHandle).values()) {
      const s = await sizeOf(c)
      bytes += s.bytes
      mtime = Math.max(mtime, s.mtime)
    }
    return { bytes, mtime }
  } catch {
    return { bytes: 0, mtime: Date.now() }
  }
}

async function removePath(root: FileSystemDirectoryHandle, path: string[]) {
  try {
    let d = root
    for (const p of path.slice(0, -1)) d = await d.getDirectoryHandle(p)
    await d.removeEntry(path[path.length - 1], { recursive: true })
  } catch {
    /* 已不存在 / 正被其他 tab 的旧版代码使用 */
  }
}

/** 本 tab 的缓存管理器 */
export const pieceCache = new PieceCache()
export type { PieceCache }
export { fmt as formatCacheBytes }
