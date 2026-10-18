// 基于 webtorrent@3 的真实播放引擎（浏览器内 P2P + Service Worker 流式）。
// 该模块只在播放页懒加载，webtorrent 本体再按需动态 import。
import type WebTorrentT from "webtorrent/dist/webtorrent.min.js"
import type { WTTorrent } from "webtorrent/dist/webtorrent.min.js"
import { computeSegments } from "./segments"
import { fileKind, type Engine, type LogLevel, type PeerSnap, type Session, type SessionEvents, type StartOptions, type TorrentSnap } from "./types"

const SW_URL = "/sw.min.js"
/** 首尾 piece 的优先级（与旧版一致），便于尽快拿到 moov/时长信息 */
const EDGE_PRIORITY = 10

let clientPromise: Promise<WebTorrentT> | null = null
let streamPromise: Promise<boolean> | null = null

async function getClient(iceServers: RTCIceServer[]) {
  if (!clientPromise) {
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

class WTSession implements Session {
  private listeners: { [K in keyof SessionEvents]: Set<SessionEvents[K]> } = { metadata: new Set(), log: new Set() }
  private torrent!: WTTorrent
  private client: WebTorrentT
  private streaming: boolean
  private announce: string[]
  /** 磁力带 xs=（服务器提供的 .torrent 地址） */
  private xs: boolean
  private blobURL: string | null = null
  private destroyed = false
  private swapping = false

  constructor(client: WebTorrentT, torrent: WTTorrent, streaming: boolean, announce: string[], xs: boolean) {
    this.client = client
    this.streaming = streaming
    this.announce = announce
    this.xs = xs
    this.attach(torrent)
    if (torrent.files?.length) queueMicrotask(() => this.listeners.metadata.forEach((cb) => cb()))
  }

  /** 绑定种子事件；替换种子（loadTorrentFile）后旧种子的事件一律忽略 */
  private attach(torrent: WTTorrent) {
    this.torrent = torrent
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
    torrent.on("done", () => live() && this.log("info", "全部下载完成"))
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
      await new Promise<void>((r) => old.destroy({ destroyStore: true }, () => r()))
      if (this.destroyed) return false
      const t = this.client.add(data, { announce: this.announce, deselect: true })
      this.xs = false // 元数据来自 .torrent，不再是 xs=
      this.applied = null
      this.attach(t)
      t.on("infoHash", () => {
        if (ih && t.infoHash !== ih) this.log("error", `服务器返回的种子文件与磁力不符（${t.infoHash}）`)
      })
      for (const url of this.webseeds) {
        try {
          t.addWebSeed(url)
        } catch {
          /* ignore */
        }
      }
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
  }

  private applyFocus() {
    const t = this.torrent
    if (this.destroyed || !t?.files?.length) return
    const f = this.focused !== null ? t.files[this.focused] : undefined
    const next = f && f.length ? { start: f._startPiece, end: f._endPiece } : null
    const prev = this.applied
    if (prev && next && prev.start === next.start && prev.end === next.end) return
    if (prev) {
      t.deselect(prev.start, prev.end)
      t.deselect(prev.start, prev.start)
      t.deselect(prev.end, prev.end)
    }
    this.applied = next
    if (!next) return
    t.select(next.start, next.end, 0)
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

  private webseeds = new Set<string>()

  addWebSeed(url: string) {
    if (this.destroyed || this.webseeds.has(url)) return false
    this.webseeds.add(url)
    try {
      this.torrent.addWebSeed(url)
      return true
    } catch (e) {
      this.log("warn", `WebSeed 添加失败：${String(e)}`)
      return false
    }
  }

  destroy() {
    if (this.destroyed) return
    this.destroyed = true
    this.listeners.metadata.clear()
    this.listeners.log.clear()
    if (this.blobURL) URL.revokeObjectURL(this.blobURL)
    try {
      this.torrent.destroy({ destroyStore: true })
    } catch {
      /* ignore */
    }
  }
}

export const webtorrentEngine: Engine = {
  kind: "webtorrent",
  async start(magnet: string, opts: StartOptions) {
    const client = await getClient(opts.iceServers)
    if (!streamPromise) streamPromise = setupStreaming(client)
    const streaming = await streamPromise
    // 同一 infohash 已存在（例如 StrictMode 重复挂载）时先移除
    const existing = await client.get(magnet).catch(() => null)
    if (existing) await new Promise<void>((r) => existing.destroy({ destroyStore: true }, () => r()))
    const torrent = client.add(magnet, { announce: opts.announce, deselect: true })
    return new WTSession(client, torrent, streaming, opts.announce, /[?&]xs=https?%3A/i.test(magnet))
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
