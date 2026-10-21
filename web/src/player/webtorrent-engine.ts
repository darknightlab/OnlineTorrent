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
  observeWebSeed,
  type TorrentLike,
} from "./store/internals"
import { infoHashFromMagnet } from "./store/policy"
import { applyRanges, bitsFromRanges, countPieces, resetToRanges } from "@/lib/webseed"
import { classifyResponse, WebSeedHealth } from "@/lib/webseedhealth"
import { SubtitleSource } from "./subs/source"
import { externalSubsFor, externalToAss, fontFiles } from "./subs/external"
import type { ExternalTrack, SubtitleSet } from "./subs/set"
import { readCuePoints } from "./subs/mkv"
import { mp4Points } from "./timemap/mp4"
import { buildTimeMap } from "@/lib/timemap"
import type { TimeMapResult } from "./types"
import type { ClusterTap } from "./subs/tap"
import type { WebSeedAvail, WebSeedUpdate } from "./types"

const SW_URL = "/sw.js" // 站点唯一的 Service Worker（src/sw/sw.js；应用启动时已由 PWAUpdatePrompt 注册，这里得到同一个注册）
/** 首尾 piece 的优先级（与旧版一致），便于尽快拿到 moov/时长信息 */
const EDGE_PRIORITY = 10
/** 外挂字幕文件的上限（再大不是正常字幕） */
const MAX_EXTERNAL_SUB = 16 << 20
/** 种子附带字体的下载上限（按需要下载的 piece 计） */
const MAX_TORRENT_FONTS = 64 << 20
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

// 第一次打开网站时 Service Worker 要先完成应用壳的预缓存（约 1.5 MB）才激活：给足时间
function waitActivated(reg: ServiceWorkerRegistration, timeoutMs = 30_000) {
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
  private listeners: { [K in keyof SessionEvents]: Set<SessionEvents[K]> } = { metadata: new Set(), log: new Set(), fatal: new Set(), webseedToken: new Set() }
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
        // 起播时先取文件首尾（见 edgesPending）：此前读取不预取后面的 piece
        windowBytes: () => (this.edgesPending() ? 0 : pieceCache.protection(pieceCache.entries.get(this.lease.key)!).aheadBytes),
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
    torrent.on("warning", (err: unknown) => {
      const m = live() && /^Piece (\d+) failed verification/.exec(String((err as Error)?.message ?? err))
      if (m) this.onHashFail(Number(m[1]))
    })
    torrent.on("verified", (i: number) => {
      if (live() && this.webseeds.size) this.onVerified(i)
    })
    // 起播闸门（edgesPending）：首尾到了、或来源的可用分块变了（新连接、bitfield、have；节点与 WebSeed 都是 Wire）
    const gateCheck = () => {
      if (live()) this.edgesPending()
    }
    torrent.on("verified", gateCheck)
    torrent.on("wire", (w) => {
      gateCheck()
      w.on("bitfield", gateCheck)
      w.on("have", gateCheck)
    })
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
    if (index !== null) this.mkvSource(index)
  }

  // ---- 内封字幕（MKV）----

  /** 文件序号 → 字幕源（当前种子） */
  private subs = new Map<number, SubtitleSource>()

  /** 读取文件 [start, end) 的字节（辅助读取，经窗口化读取器：缺的 piece 会下载） */
  async readRange(fileIndex: number, start: number, end: number, background = false): Promise<Uint8Array> {
    const f = this.torrent?.files?.[fileIndex] as unknown as FileLike | undefined
    if (!f || end <= start) return new Uint8Array(0)
    const it = (f[Symbol.asyncIterator] as (o: { start: number; end: number; aux: boolean; background: boolean }) => AsyncIterator<Uint8Array>)({ start, end: Math.min(end, f.length) - 1, aux: true, background })
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

  // ---- 字幕：内封 + 外挂 ----

  private sets = new Map<number, SubtitleSet>()
  private torrentFontsP: Promise<Uint8Array[]> | null = null

  /** 视频文件的全部字幕（内封 + 种子里的外挂文件）；都没有时为 null */
  subtitles(fileIndex: number): SubtitleSet | null {
    const t = this.torrent
    const f = t?.files?.[fileIndex]
    if (!t || !f || this.destroyed) return null
    const cached = this.sets.get(fileIndex)
    if (cached) return cached
    const files = t.files.map((x) => ({ path: x.path, length: x.length }))
    const embedded = this.mkvSource(fileIndex)
    const external: ExternalTrack[] = externalSubsFor(files, fileIndex).map((info) => {
      let p: Promise<string> | null = null
      return {
        ...info,
        load: () =>
          (p ??= (async () => {
            const len = files[info.fileIndex].length
            if (len > MAX_EXTERNAL_SUB) throw new Error(`文件过大（${formatCacheBytes(len)}）`)
            return externalToAss(info.format, await this.readRange(info.fileIndex, 0, len, true))
          })()),
      }
    })
    if (!embedded && !external.length) return null
    const fontIdx = fontFiles(files)
    const set: SubtitleSet = {
      embedded,
      external,
      fonts: async () => {
        const [att, own] = await Promise.all([embedded?.fonts() ?? [], this.torrentFonts(fontIdx)])
        return { fonts: [...att, ...own], attachments: att.length, files: own.length }
      },
    }
    this.sets.set(fileIndex, set)
    return set
  }

  companionFiles(fileIndex: number): number[] {
    const t = this.torrent
    const set = this.subtitles(fileIndex)
    if (!t?.files || !set?.external.length) return []
    return [...set.external.map((e) => e.fileIndex), ...fontFiles(t.files.map((x) => ({ path: x.path, length: x.length })))]
  }

  /** 种子 Fonts/ 文件夹里的字体：按需下载的字节（含共享的首尾 piece）合计不超过上限时才读取 */
  private torrentFonts(idx: number[]): Promise<Uint8Array[]> {
    const t = this.torrent
    if (!t || !idx.length) return Promise.resolve([])
    this.torrentFontsP ??= (async () => {
      const pieces = new Set<number>()
      for (const i of idx) for (let p = t.files[i]._startPiece; p <= t.files[i]._endPiece; p++) if (!t.bitfield?.get(p)) pieces.add(p)
      const cost = pieces.size * t.pieceLength
      if (cost > MAX_TORRENT_FONTS) {
        this.log("info", `种子附带的字体需下载 ${formatCacheBytes(cost)}，超过上限，未加载`)
        return []
      }
      const out: Uint8Array[] = []
      for (const i of idx) {
        try {
          out.push(await this.readRange(i, 0, t.files[i].length, true))
        } catch {
          /* 单个字体失败不影响其他 */
        }
      }
      return out
    })()
    return this.torrentFontsP
  }

  /** MKV 内封字幕源（tap 与时间映射也用它） */
  private mkvSource(fileIndex: number): SubtitleSource | null {
    const f = this.torrent?.files?.[fileIndex]
    if (!f || this.destroyed || !/\.mk[vs]$/i.test(f.name)) return null
    let s = this.subs.get(fileIndex)
    if (!s) {
      s = new SubtitleSource((a, b) => this.readRange(fileIndex, a, b), f.length)
      this.subs.set(fileIndex, s)
    }
    return s
  }

  // ---- 进度条的字节 → 时间映射（容器索引）----

  private maps = new Map<number, Promise<TimeMapResult | null>>()

  /**
   * 文件的字节 → 时间映射：MP4 用 moov 样本表，MKV 用 Cues（没有 Cues 时用播放中读到的 Cluster，partial）。
   * 在 <video> 读完头部之后调用（moov / Cues 浏览器本来就会读取；这里只做有上限的辅助读取）。
   */
  timeMap(fileIndex: number): Promise<TimeMapResult | null> {
    const f = this.torrent?.files?.[fileIndex]
    if (!f || this.destroyed) return Promise.resolve(null)
    const read = (a: number, b: number) => this.readRange(fileIndex, a, b)
    const cached = this.maps.get(fileIndex)
    if (cached) return cached
    let p: Promise<TimeMapResult | null>
    if (/\.(mp4|m4v|mov)$/i.test(f.name)) {
      p = mp4Points(read, f.length).then((r) => {
        const map = r && buildTimeMap(r.points)
        return map ? { map, partial: false } : null
      })
    } else if (/\.mk[vs]$/i.test(f.name)) {
      const src = this.mkvSource(fileIndex)
      p = (async () => {
        const h = src && (await src.header)
        const pts = h ? await readCuePoints(read, h).catch(() => null) : null
        const map = pts && buildTimeMap(pts)
        if (map) return { map, partial: false }
        // Cues 缺失：用视频已读到的 Cluster（稀疏，随播放增多）；不缓存
        this.maps.delete(fileIndex)
        const seen = src ? buildTimeMap([...src.clusters]) : null
        return seen ? { map: seen, partial: true } : null
      })()
    } else return Promise.resolve(null)
    p = p.catch(() => null)
    this.maps.set(fileIndex, p)
    return p
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
    t.select(next.end, next.end, EDGE_PRIORITY)
    t.select(next.start, next.start, EDGE_PRIORITY)
    // 窗口模式也走闸门（预取），只是闸门打开时不选整个文件
    this.selectWholeAfterEdges(t, next)
  }

  /**
   * 起播先取当前文件的首尾 piece：MKV 的 Cues、MP4 尾部的 moov 在文件尾，浏览器起播前就要读。
   * WebTorrent 不按优先级先后取 piece——新连接（WebSeed）的第一个请求取最后一个（优先级最低的）选择的末尾，
   * 之后在所有优先级非 0 的选择之间轮流取——所以首尾的高优先级选择会被读取窗口的预取与整文件下载挤到后面
   * （线上：1 GB 的 MKV 尾部 piece 到第 26 秒才到，起播要 25 秒）。
   * 闸门：首 / 尾中还缺、且已连接的来源（节点 / WebSeed 宣告的分块，即边缘节点的 have）里有的那些 piece
   * 到齐之前，读取不预取（windowBytes = 0，只取正在读的 piece）、不选整个文件——可请求的只有首、尾和正在读的
   * piece。只有一端能取到时（边缘节点还在下载这一集，尾部还没有）就只等那一端，从不等没人有的 piece。
   * 视频已经可以播放（canplay：MP4 的 moov 在开头、MKV 不需要 Cues 时）也立即打开（notePlayable）。
   * 还没有任何连接时先关着（不知道谁有）。打开后不再关上：之后才出现的首 / 尾仍由高优先级选择尽早取到，
   * 但不再挡住预取与播放。
   */
  private gate: { range: { start: number; end: number }; open: boolean } | null = null

  private edgesPending(): boolean {
    const t = this.torrent
    const g = this.gate
    if (!t || !g || g.open || g.range !== this.applied) return false
    const missing = [g.range.start, g.range.end].filter((i) => !t.bitfield?.get(i))
    const wires = t.wires ?? []
    const offered = missing.some((i) => wires.some((w) => w.peerPieces?.get(i)))
    if (!missing.length || (wires.length > 0 && !offered)) {
      g.open = true
      if (!this.windowed && !this.destroyed) t.select(g.range.start, g.range.end, 0)
      return false
    }
    return true
  }

  /** 播放器：当前文件已可播放（canplay）——起播闸门不再等首尾 */
  notePlayable(fileIndex: number) {
    const t = this.torrent
    const g = this.gate
    if (!t || !g || g.open || this.focused !== fileIndex || g.range !== this.applied) return
    g.open = true
    if (!this.windowed && !this.destroyed) t.select(g.range.start, g.range.end, 0)
  }

  /** 整文件后台下载（非窗口模式）在闸门打开时选上（见 edgesPending；之后的检查由 attach 里的事件触发） */
  private selectWholeAfterEdges(_t: WTTorrent, range: { start: number; end: number }) {
    this.gate = { range, open: false }
    this.edgesPending()
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

  /** WebSeed → 服务器可提供的分块（null = 全部，旧协议 / 文件已完整）、健康状态 */
  private webseeds = new Map<string, WebSeedEntry>()

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
    const e: WebSeedEntry = { bits, pieces, handle: null, health: null as unknown as WebSeedHealth, unobserve: () => {} }
    // 健康：连续失败停用、退避重试（lib/webseedhealth）；日志只在状态变化时
    e.health = new WebSeedHealth({
      onDisable: (n, ms) => {
        e.handle?.pause()
        this.log("warn", `WebSeed 暂时不可用，已停用（${n} 次失败），${Math.round(ms / 1000)} 秒后重试`)
      },
      onEnable: () => e.handle?.resume(),
      onRecovered: () => this.log("info", "WebSeed 已恢复"),
      onTokenRefresh: () => this.listeners.webseedToken.forEach((cb) => cb(url)),
    })
    e.unobserve = observeWebSeed(url, (o) => {
      if (this.destroyed) return
      if ("error" in o) return e.health.failure("network")
      const c = classifyResponse(o.status, o.body)
      if (c === "ok") e.health.success("transport")
      else if (c !== "expected") e.health.failure(c)
    })
    this.webseeds.set(url, e)
    if (avail?.token) setEdgeToken(url, avail.token)
    return this.attachWebSeed(this.torrent, url, e)
  }

  setWebSeedToken(url: string, token: string) {
    const e = this.webseeds.get(url)
    if (this.destroyed || !e) return
    setEdgeToken(url, token)
    e.health.refreshed("token")
  }

  webSeedRefreshed(url: string) {
    this.webseeds.get(url)?.health.refreshed("seed")
  }

  /** 校验失败的分块：归到最近提供它的 WebSeed */
  private onHashFail(i: number) {
    for (const e of this.webseeds.values()) if (e.handle?.delivered(i)) e.health.failure("hash")
  }

  /** 校验通过的分块：提供它的 WebSeed 数据是好的 */
  private onVerified(i: number) {
    for (const e of this.webseeds.values()) if (e.handle?.delivered(i)) e.health.success("verified")
  }

  private attachWebSeed(t: WTTorrent, url: string, e: WebSeedEntry) {
    try {
      t.addWebSeed(url)
    } catch (err) {
      this.log("warn", `WebSeed 添加失败：${String(err)}`)
      return false
    }
    // 元数据还没到：连接要等元数据（换种子后 swap 里重新 attach）
    const n = t.pieces?.length ?? 0
    if (!e.bits && !n) return true
    try {
      // 只宣告服务器已有的分块（qB 还在下载时），之后由 updateWebSeed 增减；完整的服务器宣告全部
      // （同样经过 patch：停用时要能撤回宣告）
      e.handle = patchWebSeed(t as unknown as TorrentLike, url, e.bits ?? fullBits(n))
      if (e.health.state === "disabled") e.handle.pause()
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
    for (const [url, e] of this.webseeds) {
      setEdgeToken(url, null)
      e.unobserve()
      e.health.destroy()
    }
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

interface WebSeedEntry {
  bits: Uint8Array | null
  pieces: number
  handle: WebSeedHandle | null
  health: WebSeedHealth
  unobserve: () => void
}

function fullBits(n: number) {
  const b = new Uint8Array(Math.ceil(n / 8)).fill(255)
  if (n % 8) b[b.length - 1] = (255 << (8 - (n % 8))) & 255
  return b
}
