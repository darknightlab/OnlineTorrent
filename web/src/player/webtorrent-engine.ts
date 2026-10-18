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
  private torrent: WTTorrent
  private streaming: boolean
  private blobURL: string | null = null
  private destroyed = false

  constructor(torrent: WTTorrent, streaming: boolean) {
    this.torrent = torrent
    this.streaming = streaming
    torrent.on("metadata", () => {
      this.log("info", `已获取种子信息：${torrent.name}`)
      this.prioritizeEdges()
      this.listeners.metadata.forEach((cb) => cb())
    })
    torrent.on("ready", () => this.log("info", "种子已就绪，开始下载"))
    torrent.on("done", () => this.log("info", "全部下载完成"))
    torrent.on("warning", (e) => this.log("warn", String((e as Error)?.message ?? e)))
    torrent.on("error", (e) => this.log("error", String((e as Error)?.message ?? e)))
    let peers = 0
    torrent.on("wire", (wire) => {
      peers++
      if (peers <= 8) this.log("info", `连接到 ${wire.type === "webSeed" ? "WebSeed" : "节点"} ${wire.remoteAddress ?? ""}`.trim())
    })
    if (torrent.files?.length) queueMicrotask(() => this.listeners.metadata.forEach((cb) => cb()))
  }

  private log(level: LogLevel, msg: string) {
    this.listeners.log.forEach((cb) => cb(level, msg))
  }

  /** 每个文件首尾 piece 优先（旧版行为） */
  private prioritizeEdges() {
    for (const f of this.torrent.files) {
      if (!f.length) continue
      this.torrent.select(f._endPiece, f._endPiece, EDGE_PRIORITY)
      this.torrent.select(f._startPiece, f._startPiece, EDGE_PRIORITY)
    }
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
    const torrent = client.add(magnet, { announce: opts.announce })
    return new WTSession(torrent, streaming)
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
