// 基于 webtorrent@3 的真实播放引擎（浏览器内 P2P + Service Worker 流式）。
// 该模块只在播放页懒加载，webtorrent 本体再按需动态 import。
import type WebTorrentT from "webtorrent/dist/webtorrent.min.js"
import type { WTTorrent } from "webtorrent/dist/webtorrent.min.js"
import TorrentPiece from "torrent-piece"
import { computeSegments } from "./segments"
import { fileKind, type Engine, type LogLevel, type PeerSnap, type Session, type SessionEvents, type StartOptions, type TorrentSnap } from "./types"
import { formatCacheBytes, pieceCache, type Lease } from "./store/cache"
import { OpfsPieceStore } from "./store/opfs-piece-store"
import {
  IncompatibleError,
  checkPieceCtor,
  checkTorrentMetadata,
  checkTorrentPrototype,
  checkWire,
  installReaders,
  installEdgeFetch,
  installWireFix,
  setEdgeToken,
  patchWebSeed,
  type FileLike,
  type PieceCtor,
  type WebSeedHandle,
  type TorrentLike,
} from "./store/internals"
import { infoHashFromMagnet } from "./store/policy"
import { applyRanges, bitsFromRanges, countPieces, resetToRanges } from "@/lib/webseed"
import { SubtitleSource } from "./subs/source"
import type { ClusterTap } from "./subs/tap"
import type { WebSeedAvail, WebSeedUpdate } from "./types"

const SW_URL = "/sw.min.js"
/** 首尾 piece 的优先级（与旧版一致），便于尽快拿到 moov/时长信息 */
const EDGE_PRIORITY = 10
/** 文件大于预算的这个比例时不整文件后台下载，只靠窗口化读取（否则会下载 → 淘汰 → 再下载） */
const WINDOWED_RATIO = 0.8
/** CacheChunkStore 在内存里缓存的完整 piece 数（piece 可能有 16 MiB） */
const STORE_CACHE_SLOTS = 4

/** 内核不兼容：明确报错（不静默回退），见 store/internals.ts 与 webtorrent-compat.test.ts */
function incompatible(problems: string[]): never {
  const err = new IncompatibleError(problems)
  console.error("[webtorrent]", err.message)
  throw err
}

let clientPromise: Promise<WebTorrentT> | null = null
let streamPromise: Promise<boolean> | null = null

async function getClient(iceServers: RTCIceServer[]) {
  if (!clientPromise) {
    // webtorrent 的浏览器包加载时就取走 self.fetch：先装上边缘节点令牌注入（见 internals.ts）
    installEdgeFetch()
    clientPromise = import("webtorrent/dist/webtorrent.min.js").then(({ default: WebTorrent }) => {
      const client = new WebTorrent({ tracker: { rtcConfig: { iceServers } } })
      client.on("error", (e) => console.error("[webtorrent]", e))
      return client
    })
  }
  return clientPromise
}

function waitActivated(reg: ServiceWorkerRegistration, timeoutMs = 10_000) {
  return new Promise<void>((resolve, reject) => {
    const w = reg.active ?? reg.waiting ?? reg.installing
    if (!w) return reject(new Error("no service worker"))
    if (w.state === "activated") return resolve()
    const t = window.setTimeout(() => reject(new Error("service worker activate timeout")), timeoutMs)
    w.addEventListener("statechange", () => {
      if (w.state === "activated") {
        window.clearTimeout(t)
        resolve()
      }
    })
  })
}

async function setupStreaming(client: WebTorrentT): Promise<boolean> {
  if (!("serviceWorker" in navigator) || !window.isSecureContext) return false
  try {
    const reg = await navigator.serviceWorker.register(SW_URL, { scope: "/" })
    await waitActivated(reg)
    client.createServer({ controller: reg })
    return true
  } catch (e) {
    console.warn("[webtorrent] service worker unavailable", e)
    return false
  }
}

/** 以自定义 store 添加种子；原型层面的兼容检查不通过直接报错 */
function addTorrent(client: WebTorrentT, id: string | Uint8Array, announce: string[], lease: Lease, bitfield: Uint8Array | undefined) {
  const torrent = client.add(id, {
    announce,
    deselect: true,
    store: OpfsPieceStore,
    storeOpts: { lease },
    storeCacheSlots: STORE_CACHE_SLOTS,
    bitfield,
  })
  const problems = checkTorrentPrototype(Object.getPrototypeOf(torrent))
  if (problems.length) {
    torrent.destroy({ destroyStore: false })
    incompatible(problems)
  }
  return torrent
}

class WTSession implements Session {
  private listeners: { [K in keyof SessionEvents]: Set<SessionEvents[K]> } = { metadata: new Set(), log: new Set(), fatal: new Set() }
  private torrent!: WTTorrent
  private client: WebTorrentT
  private streaming: boolean
  private announce: string[]
  /** 磁力带 xs=（服务器提供的 .torrent 地址） */
  private xs: boolean
  private blobURL: string | null = null
  private destroyed = false
  private swapping = false
  private lease: Lease
  private fatal: string | null = null
  private doneLogged = false
  /** 窗口模式：只选首尾 piece，其余靠窗口化读取下载 */
  private windowed = false
  private unsubCache: () => void

  constructor(client: WebTorrentT, torrent: WTTorrent, streaming: boolean, announce: string[], xs: boolean, lease: Lease) {
    this.client = client
    this.streaming = streaming
    this.announce = announce
    this.xs = xs
    this.lease = lease
    this.unsubCache = pieceCache.onLog((level, msg) => this.log(level, msg))
    this.attach(torrent)
    if (torrent.files?.length) queueMicrotask(() => this.listeners.metadata.forEach((cb) => cb()))
    // 调用方在 await start() 之后才订阅 log：推迟到下一个任务
    window.setTimeout(() => {
      this.log("info", `缓存预算 ${formatCacheBytes(pieceCache.budget)}（已用 ${formatCacheBytes(pieceCache.stored)}）`)
      if (lease.temp) this.log("info", "该视频已在其他标签页中播放，本页使用临时缓存（关闭后清理）")
    }, 0)
  }

  private setFatal(msg: string) {
    if (this.fatal || this.destroyed) return
    this.fatal = msg
    console.error("[webtorrent]", msg)
    this.log("error", msg)
    this.listeners.fatal.forEach((cb) => cb(msg))
    try {
      this.torrent.destroy({ destroyStore: false })
    } catch {
      /* ignore */
    }
  }

  fatalError() {
    return this.fatal
  }

  /** 播放器报告时长（用于按码率估算读前窗口） */
  setMediaDuration(seconds: number) {
    const live = pieceCache.entries.get(this.lease.key)?.live
    const f = this.focused !== null ? this.torrent?.files?.[this.focused] : undefined
    if (live && f && seconds > 0 && Number.isFinite(seconds)) live.bytesPerSec = f.length / seconds
  }

  /** 给新添加的种子装上：wire 修正、元数据时的兼容检查、窗口化读取 */
  private instrument(torrent: WTTorrent) {
    const t = torrent as unknown as TorrentLike
    let wireChecked = false
    installWireFix(t, (w) => {
      if (wireChecked) return
      wireChecked = true
      const p = checkWire(w)
      if (p.length) this.setFatal(new IncompatibleError(p).message)
    })
    t.once("metadata", () => {
      const p = checkTorrentMetadata(t)
      const own = t.pieces.find(Boolean)?.constructor as PieceCtor | undefined
      const Piece = own ?? (TorrentPiece as unknown as PieceCtor)
      p.push(...checkPieceCtor(Piece))
      if (p.length) return this.setFatal(new IncompatibleError(p).message)
      const live = pieceCache.entries.get(this.lease.key)?.live
      if (!live || live.torrent !== t) return this.setFatal("播放器内核不兼容：自定义缓存没有被 WebTorrent 使用（store 选项无效）")
      live.Piece = Piece
      const tf = (torrent as unknown as { torrentFile?: Uint8Array }).torrentFile
      if (!this.lease.temp && tf) void pieceCache.saveTorrentFile(this.lease.key, tf)
      installReaders(t, Piece, {
        windowBytes: () => pieceCache.protection(pieceCache.entries.get(this.lease.key)!).aheadBytes,
        register: (r) => live.readers.add(r),
        unregister: (r) => {
          live.readers.delete(r)
          if (!r.aux) live.lastPos = r.file.offset + Math.min(r.pos, r.end)
        },
        reading: (i, on) => {
          if (on) live.reading.set(i, (live.reading.get(i) ?? 0) + 1)
          else {
            const n = (live.reading.get(i) ?? 1) - 1
            if (n <= 0) live.reading.delete(i)
            else live.reading.set(i, n)
          }
        },
      })
      this.installSubtitleTaps(torrent)
    })
    t.on("evicted", (i: number) => {
      // 整文件后台下载时淘汰了正在播放的文件的 piece：改为窗口模式，避免下载 → 淘汰 → 再下载
      const f = this.focused !== null ? torrent.files?.[this.focused] : undefined
      if (!this.windowed && f && i >= f._startPiece && i <= f._endPiece && this.torrent === torrent) {
        this.windowed = true
        this.log("info", "缓存空间不足以容纳整个视频，改为只缓存播放位置附近的数据")
        this.applied = null
        this.applyFocus(true)
      }
    })
  }

  /** 绑定种子事件；替换种子（loadTorrentFile）后旧种子的事件一律忽略 */
  private attach(torrent: WTTorrent) {
    this.torrent = torrent
    this.instrument(torrent)
    const live = () => this.torrent === torrent && !this.destroyed
    torrent.on("metadata", () => {
      if (!live()) return
      // ut_metadata 需要已连接的节点；没有任何连接就拿到元数据，说明来自 xs=（HTTP）
      if (this.xs && torrent.wires.length === 0) this.log("info", "已通过 HTTP 从服务器获取种子信息")
      this.log("info", `已获取种子信息：${torrent.name}`)
      this.applyFocus()
      this.listeners.metadata.forEach((cb) => cb())
    })
    torrent.on("ready", () => live() && this.log("info", "种子已就绪，开始下载"))
    torrent.on("done", () => {
      // 淘汰后再补齐时 WebTorrent 会再次 emit 'done'，只记一次
      if (!live() || this.doneLogged) return
      this.doneLogged = true
      this.log("info", "全部下载完成")
    })
    torrent.on("warning", (e) => {
      const msg = String((e as Error)?.message ?? e)
      // 浏览器只能用 ws(s) tracker；.torrent 里的 udp/http tracker 忽略是预期行为，不刷日志
      if (live() && !msg.startsWith("Unsupported tracker protocol")) this.log("warn", msg)
    })
    torrent.on("error", (e) => live() && this.log("error", String((e as Error)?.message ?? e)))
    let peers = 0
    torrent.on("wire", (wire) => {
      if (!live()) return
      peers++
      if (peers <= 8) this.log("info", `连接到 ${wire.type === "webSeed" ? "WebSeed" : "节点"} ${wire.remoteAddress ?? ""}`.trim())
    })
  }

  hasMetadata() {
    return !!this.torrent?.files?.length
  }

  /**
   * 元数据还没拿到时，用服务器提供的 .torrent 替换会话中的种子（webtorrent 不支持给已有种子
   * 补元数据：移除后用 .torrent 重新添加；尚无元数据，所以没有已下载的数据会丢失）。
   * 返回是否已替换。
   */
  async loadTorrentFile(data: Uint8Array) {
    if (this.destroyed || this.swapping || this.hasMetadata()) return false
    this.swapping = true
    try {
      const old = this.torrent
      const ih = old.infoHash
      await new Promise<void>((r) => old.destroy({ destroyStore: false }, () => r()))
      if (this.destroyed) return false
      const t = addTorrent(this.client, data, this.announce, this.lease, undefined)
      this.xs = false // 元数据来自 .torrent，不再是 xs=
      this.applied = null
      this.attach(t)
      t.on("infoHash", () => {
        if (ih && t.infoHash !== ih) this.log("error", `服务器返回的种子文件与磁力不符（${t.infoHash}）`)
      })
      // 元数据到了以后 webseed 才会被 patch（attachWebSeed 需要种子已有 _peers）
      for (const [url, e] of this.webseeds) this.attachWebSeed(t, url, e)
      return true
    } finally {
      this.swapping = false
    }
  }

  private log(level: LogLevel, msg: string) {
    this.listeners.log.forEach((cb) => cb(level, msg))
  }

  /**
   * 只下载正在播放的文件：种子以 deselect 方式添加（webtorrent 默认会选中整个种子，合集里每一集都会被下载；
   * 浏览器按 OPFS 文件逻辑大小计配额，写一下文件末尾就会占满整集大小）。
   * 选中当前文件的全部 piece，首尾 piece 优先（容器索引 moov / Cues 通常在首尾，起播与拖动都需要）。
   * 播放器读取（拖动进度条）时 webtorrent 会为读取范围另加高优先级选择，不受影响。
   */
  private focused: number | null = null
  private applied: { start: number; end: number } | null = null

  focusFile(index: number | null) {
    this.focused = index
    this.applyFocus()
    // 内封字幕：在 <video> 开始读取之前建好，第一个请求（从文件头开始）就能被解析
    if (index !== null) this.subtitles(index)
  }

  // ---- 内封字幕（MKV）----

  /** 文件序号 → 字幕源（当前种子） */
  private subs = new Map<number, SubtitleSource>()

  /** 读取文件 [start, end) 的字节（辅助读取，经窗口化读取器：缺的 piece 会下载） */
  async readRange(fileIndex: number, start: number, end: number): Promise<Uint8Array> {
    const f = this.torrent?.files?.[fileIndex] as unknown as FileLike | undefined
    if (!f || end <= start) return new Uint8Array(0)
    const it = (f[Symbol.asyncIterator] as (o: { start: number; end: number; aux: boolean }) => AsyncIterator<Uint8Array>)({ start, end: Math.min(end, f.length) - 1, aux: true })
    const parts: Uint8Array[] = []
    let n = 0
    try {
      for (;;) {
        const r = await it.next()
        if (r.done) break
        parts.push(r.value)
        n += r.value.length
      }
    } finally {
      await it.return?.()
    }
    const out = new Uint8Array(n)
    let o = 0
    for (const p of parts) {
      out.set(p, o)
      o += p.length
    }
    return out
  }

  subtitles(fileIndex: number): SubtitleSource | null {
    const f = this.torrent?.files?.[fileIndex]
    if (!f || this.destroyed || !/\.mk[vs]$/i.test(f.name)) return null
    let s = this.subs.get(fileIndex)
    if (!s) {
      s = new SubtitleSource((a, b) => this.readRange(fileIndex, a, b), f.length)
      this.subs.set(fileIndex, s)
    }
    return s
  }

  /** 播放器读取文件时（serveFile 的 "iterator" 事件）把同样的字节交给字幕解析器 */
  private installSubtitleTaps(t: WTTorrent) {
    t.files.forEach((f, index) => {
      if (!/\.mk[vs]$/i.test(f.name)) return
      const em = f as unknown as { on(ev: "iterator", cb: (ev: { iterator: AsyncIterator<Uint8Array> & { reader?: { pos: number } }; req?: { headers?: Record<string, string> } }, replace: (it: unknown) => void) => void): void }
      em.on("iterator", (ev, replace) => {
        const src = this.torrent === t ? this.subs.get(index) : undefined
        if (!src) return
        const start = ev.iterator.reader?.pos ?? Number(/bytes=(\d+)/.exec(ev.req?.headers?.range ?? "")?.[1] ?? 0)
        replace(tapIterator(ev.iterator, src.tap(start)))
      })
    })
  }

  private applyFocus(force = false) {
    const t = this.torrent
    if (this.destroyed || !t?.files?.length) return
    const f = this.focused !== null ? t.files[this.focused] : undefined
    const next = f && f.length ? { start: f._startPiece, end: f._endPiece } : null
    const prev = this.applied
    if (!force && prev && next && prev.start === next.start && prev.end === next.end) return
    // 非 stream selection 的 deselect 按区间裁剪，整段 deselect 一次即可清掉文件与首尾的选择
    if (prev) t.deselect(prev.start, prev.end)
    else if (force && next) t.deselect(next.start, next.end)
    this.applied = next
    const live = pieceCache.entries.get(this.lease.key)?.live
    if (live) live.focus = next ? [next.start, next.end] : null
    if (!next || !f) return
    if (!this.windowed && f.length > WINDOWED_RATIO * pieceCache.budget) {
      this.windowed = true
      this.log("info", `视频（${formatCacheBytes(f.length)}）大于缓存预算的 80%，只缓存播放位置附近的数据`)
    }
    if (!this.windowed) t.select(next.start, next.end, 0)
    t.select(next.end, next.end, EDGE_PRIORITY)
    t.select(next.start, next.start, EDGE_PRIORITY)
  }

  on<K extends keyof SessionEvents>(ev: K, cb: SessionEvents[K]) {
    const set = this.listeners[ev] as Set<SessionEvents[K]>
    set.add(cb)
    return () => void set.delete(cb)
  }

  snapshot(): TorrentSnap | null {
    const t = this.torrent
    if (!t || !t.files?.length) return null
    const peers: PeerSnap[] = t.wires.map((w, i) => ({
      id: `${w.peerId ?? i}`,
      type: w.type === "webrtc" ? "webrtc" : w.type === "webSeed" ? "webSeed" : "other",
      addr: w.type === "webSeed" ? (w.remoteAddress ?? "webseed") : w.remoteAddress ? `${w.remoteAddress}${w.remotePort ? `:${w.remotePort}` : ""}` : "WebRTC",
      downloadSpeed: w.downloadSpeed(),
      uploadSpeed: w.uploadSpeed(),
    }))
    return {
      name: t.name,
      infoHash: t.infoHash,
      length: t.length,
      pieceLength: t.pieceLength,
      numPieces: t.pieces?.length ?? 0,
      progress: t.progress,
      downloaded: t.downloaded,
      uploaded: t.uploaded,
      downloadSpeed: t.downloadSpeed,
      uploadSpeed: t.uploadSpeed,
      numPeers: t.numPeers,
      timeRemaining: t.timeRemaining,
      done: t.done,
      files: t.files.map((f, index) => ({
        index,
        name: f.name,
        path: f.path,
        length: f.length,
        progress: f.progress,
        kind: fileKind(f.name),
      })),
      peers,
    }
  }

  segments(fileIndex: number, n: number) {
    const t = this.torrent
    const f = t?.files?.[fileIndex]
    if (!f || !t.pieces?.length) return new Uint8Array(n)
    return computeSegments(
      f._startPiece,
      f._endPiece,
      n,
      (p) => (t.bitfield ? t.bitfield.get(p) : t.pieces[p] === null),
      (p) => {
        const pc = t.pieces[p]
        return !!pc && pc.missing < pc.length
      },
    )
  }

  fileURL(fileIndex: number) {
    if (!this.streaming) return null
    const f = this.torrent?.files?.[fileIndex]
    try {
      return f ? f.streamURL : null
    } catch {
      return null
    }
  }

  torrentFileURL() {
    if (this.blobURL) return this.blobURL
    const b = this.torrent?.torrentFileBlob
    if (!b) return null
    this.blobURL = URL.createObjectURL(b)
    return this.blobURL
  }

  /** WebSeed → 服务器可提供的分块（null = 全部，旧协议 / 文件已完整） */
  private webseeds = new Map<string, { bits: Uint8Array | null; pieces: number; handle: WebSeedHandle | null }>()

  addWebSeed(url: string, avail?: WebSeedAvail) {
    if (this.destroyed || this.webseeds.has(url)) return false
    const n = this.torrent?.pieces?.length ?? 0
    let bits: Uint8Array | null = null
    let pieces = 0
    if (avail?.pieces && avail.have && !avail.complete) {
      if (n && avail.pieces !== n) {
        this.log("warn", `WebSeed 分块数与种子不符（${avail.pieces} ≠ ${n}），不启用`)
        return false
      }
      pieces = avail.pieces
      bits = bitsFromRanges(pieces, avail.have)
    }
    const e = { bits, pieces, handle: null as WebSeedHandle | null }
    this.webseeds.set(url, e)
    if (avail?.token) setEdgeToken(url, avail.token)
    return this.attachWebSeed(this.torrent, url, e)
  }

  setWebSeedToken(url: string, token: string) {
    if (!this.destroyed && this.webseeds.has(url)) setEdgeToken(url, token)
  }

  private attachWebSeed(t: WTTorrent, url: string, e: { bits: Uint8Array | null; handle: WebSeedHandle | null }) {
    try {
      t.addWebSeed(url)
    } catch (err) {
      this.log("warn", `WebSeed 添加失败：${String(err)}`)
      return false
    }
    if (!e.bits) return true
    try {
      // 只宣告服务器已有的分块（qB 还在下载时），之后由 updateWebSeed 增减
      e.handle = patchWebSeed(t as unknown as TorrentLike, url, e.bits)
    } catch (err) {
      if (err instanceof IncompatibleError) this.setFatal(err.message)
      else this.log("warn", `WebSeed 分块宣告失败：${String(err)}`)
      return false
    }
    return true
  }

  updateWebSeed(url: string, ev: WebSeedUpdate) {
    const e = this.webseeds.get(url)
    if (!e) return
    if (ev.complete) {
      if (!e.bits) return
      ev = { reset: [0, e.pieces - 1] }
    }
    if (!e.bits) return // 已宣告全部
    if (ev.reset) {
      const { added, removed } = resetToRanges(e.bits, e.pieces, ev.reset)
      e.handle?.add(added)
      e.handle?.remove(removed)
    }
    if (ev.have) e.handle?.add(applyRanges(e.bits, e.pieces, ev.have, true))
    if (ev.lost) e.handle?.remove(applyRanges(e.bits, e.pieces, ev.lost, false))
  }

  /** 该 WebSeed 已有的分块：只算正在播放的文件（服务器只下载被请求的文件），没有选中文件时算整个种子 */
  webSeedPieces(url: string) {
    const e = this.webseeds.get(url)
    if (!e) return null
    const f = this.focused !== null ? this.torrent?.files?.[this.focused] : undefined
    const [from, to] = f && f.length ? [f._startPiece, f._endPiece] : [0, e.pieces - 1]
    const of = to - from + 1
    return e.bits ? { have: countPieces(e.bits, e.pieces, from, to), of } : { have: of, of }
  }

  destroy() {
    if (this.destroyed) return
    this.destroyed = true
    for (const url of this.webseeds.keys()) setEdgeToken(url, null)
    this.listeners.metadata.clear()
    this.listeners.log.clear()
    this.listeners.fatal.clear()
    this.unsubCache()
    if (this.blobURL) URL.revokeObjectURL(this.blobURL)
    // 保留缓存数据（再次打开同一视频可直接从缓存播放），只由预算管理器清理
    const lease = this.lease
    try {
      this.torrent.destroy({ destroyStore: false }, () => void lease.release())
    } catch {
      void lease.release()
    }
  }
}

export const webtorrentEngine: Engine = {
  kind: "webtorrent",
  async start(magnet: string, opts: StartOptions) {
    const client = await getClient(opts.iceServers)
    if (!streamPromise) streamPromise = setupStreaming(client)
    const ih = infoHashFromMagnet(magnet)
    if (!ih) throw new Error("磁力链接缺少 infohash")
    const [streaming] = await Promise.all([streamPromise, pieceCache.init(opts.maxCacheBytes)])
    // 同一 infohash 已存在（例如 StrictMode 重复挂载）时先关闭（保留缓存数据）
    const existing = await client.get(magnet).catch(() => null)
    if (existing) await new Promise<void>((r) => existing.destroy({ destroyStore: false }, () => r()))
    const lease = await pieceCache.acquire(ih)
    try {
      // 已缓存的 piece 直接作为启动 bitfield（WebTorrent 每个文件只抽查一两个 piece，不必重算几 GB 的哈希）
      const bitfield = lease.temp ? undefined : await pieceCache.resumeBitfield(lease.key)
      // 有缓存的 .torrent 时直接用它添加：元数据立即可用（不必等节点 / xs= / 服务器）
      let torrent: WTTorrent | null = null
      const tf = bitfield ? await pieceCache.loadTorrentFile(lease.key) : null
      if (tf) {
        const t = addTorrent(client, tf, opts.announce, lease, bitfield)
        const ok = await new Promise<boolean>((res) => {
          t.on("infoHash", () => res(t.infoHash === ih))
          t.on("error", () => res(false))
        })
        if (ok) torrent = t
        else {
          await new Promise<void>((r) => (t.destroyed ? r() : t.destroy({ destroyStore: false }, () => r())))
          await pieceCache.dropTorrentFile(lease.key)
        }
      }
      torrent ??= addTorrent(client, magnet, opts.announce, lease, bitfield)
      // 磁力里的 x.pe / tr 等仍由 announce 提供；用 .torrent 添加时没有 xs=，元数据已经有了
      return new WTSession(client, torrent, streaming, opts.announce, !tf && /[?&]xs=https?%3A/i.test(magnet), lease)
    } catch (e) {
      void lease.release()
      throw e
    }
  },
  streamingReady() {
    return streamPromise ?? Promise.resolve(false)
  },
  async resetServiceWorker() {
    if (!("serviceWorker" in navigator)) return
    const regs = await navigator.serviceWorker.getRegistrations()
    await Promise.all(regs.map((r) => r.unregister()))
    location.reload()
  },
}

/** 透传迭代器，同时把每块数据交给字幕解析器（return / throw 原样转发，读取器照常释放） */
function tapIterator<T extends AsyncIterator<Uint8Array>>(inner: T, tap: ClusterTap) {
  const it = {
    async next() {
      const r = await inner.next()
      if (!r.done && r.value) tap.push(r.value)
      return r
    },
    return(v?: unknown) {
      return inner.return ? inner.return(v) : Promise.resolve({ done: true as const, value: undefined })
    },
    throw(e?: unknown) {
      return inner.throw ? inner.throw(e) : Promise.reject(e)
    },
    [Symbol.asyncIterator]() {
      return it
    },
    reader: (inner as unknown as { reader?: unknown }).reader,
  }
  return it
}
