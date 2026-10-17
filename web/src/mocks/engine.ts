// 模拟播放引擎：不联网，按时间推进下载进度，用于 mock 模式下开发与截图。
import { computeSegments } from "@/player/segments"
import { fileKind, type Engine, type Session, type SessionEvents, type TorrentSnap } from "@/player/types"

const PIECE = 1024 * 1024

class MockSession implements Session {
  private listeners: { [K in keyof SessionEvents]: Set<SessionEvents[K]> } = { metadata: new Set(), log: new Set() }
  private timer = 0
  private ready = false
  private done: Uint8Array = new Uint8Array(0)
  private partial = new Set<number>()
  private files: { name: string; path: string; length: number; start: number; end: number }[] = []
  private name: string
  private infoHash: string
  private uploaded = 0
  private speed = 0
  private cursor = 0

  constructor(magnet: string) {
    const dn = magnet.match(/dn=([^&]+)/)?.[1]
    this.name = dn ? decodeURIComponent(dn) : "[Mock] Sample Video - 01 [1080p].mp4"
    this.infoHash = magnet.match(/btih:([0-9a-z]+)/i)?.[1] ?? "0".repeat(40)
    window.setTimeout(() => this.emitLog("info", "正在通过 tracker 寻找节点…"), 200)
    window.setTimeout(() => this.emitLog("info", "连接到节点 WebRTC (ams)"), 700)
    window.setTimeout(() => this.init(), 1100)
  }

  private emitLog(level: "info" | "warn" | "error", msg: string) {
    this.listeners.log.forEach((cb) => cb(level, msg))
  }

  private init() {
    const base = this.name.replace(/\.(mkv|mp4)$/i, "")
    const video = `${base}${/HEVC|MKV/i.test(base) ? ".mkv" : ".mp4"}`
    const specs = [
      { name: video, length: 368 * PIECE + 123456 },
      { name: `${base}.sc.ass`, length: 46 * 1024 },
      { name: `${base}.tc.ass`, length: 47 * 1024 },
      { name: "cover.jpg", length: 412 * 1024 },
      { name: "readme.txt", length: 2 * 1024 },
    ]
    let off = 0
    this.files = specs.map((s) => {
      const f = { ...s, path: `${base}/${s.name}`, start: Math.floor(off / PIECE), end: Math.floor((off + s.length - 1) / PIECE) }
      off += s.length
      return f
    })
    this.done = new Uint8Array(Math.ceil(off / PIECE))
    // 首尾优先
    for (const f of this.files) {
      this.done[f.start] = 1
      this.done[f.end] = 1
    }
    // 预置一段已下载，便于截图展示分段进度
    for (let i = 0; i < 96; i++) this.done[i] = 1
    for (let i = 140; i < 162; i++) this.done[i] = 1
    this.cursor = 96
    this.ready = true
    this.emitLog("info", `已获取种子信息：${this.name}`)
    this.listeners.metadata.forEach((cb) => cb())
    this.timer = window.setInterval(() => this.tick(), 500)
  }

  private tick() {
    this.speed = (2.2 + Math.random() * 3.5) * 1024 * 1024
    this.uploaded += this.speed * 0.18 * 0.5
    const step = Math.max(1, Math.round((this.speed * 0.5) / PIECE))
    this.partial.clear()
    for (let i = 0; i < step && this.cursor < this.done.length; ) {
      if (!this.done[this.cursor]) {
        this.done[this.cursor] = 1
        i++
      }
      this.cursor++
    }
    for (let k = 1; k <= 3; k++) if (this.cursor + k < this.done.length && !this.done[this.cursor + k]) this.partial.add(this.cursor + k)
    if (this.cursor >= this.done.length) {
      window.clearInterval(this.timer)
      this.speed = 0
    }
  }

  on<K extends keyof SessionEvents>(ev: K, cb: SessionEvents[K]) {
    const set = this.listeners[ev] as Set<SessionEvents[K]>
    set.add(cb)
    if (ev === "metadata" && this.ready) queueMicrotask(() => (cb as () => void)())
    return () => void set.delete(cb)
  }

  private fileProgress(f: { start: number; end: number }) {
    let c = 0
    for (let i = f.start; i <= f.end; i++) c += this.done[i]
    return c / (f.end - f.start + 1)
  }

  snapshot(): TorrentSnap | null {
    if (!this.ready) return null
    const length = this.files.reduce((s, f) => s + f.length, 0)
    const doneCount = this.done.reduce((s, v) => s + v, 0)
    const progress = doneCount / this.done.length
    const peers = 3 + Math.round(Math.random())
    return {
      name: this.name,
      infoHash: this.infoHash,
      length,
      pieceLength: PIECE,
      numPieces: this.done.length,
      progress,
      downloaded: Math.round(progress * length),
      uploaded: Math.round(this.uploaded),
      downloadSpeed: this.speed,
      uploadSpeed: this.speed * 0.18,
      numPeers: peers,
      timeRemaining: this.speed ? ((1 - progress) * length * 1000) / this.speed : 0,
      done: progress >= 1,
      files: this.files.map((f, index) => ({ index, name: f.name, path: f.path, length: f.length, progress: this.fileProgress(f), kind: fileKind(f.name) })),
      peers: [
        { id: "a", type: "webrtc", addr: "ams · 45.142.xx.xx:16881", downloadSpeed: this.speed * 0.62, uploadSpeed: 0 },
        { id: "b", type: "webrtc", addr: "183.17.xx.xx:51413", downloadSpeed: this.speed * 0.25, uploadSpeed: this.speed * 0.12 },
        { id: "c", type: "webrtc", addr: "240e:3b7:xx::1", downloadSpeed: this.speed * 0.13, uploadSpeed: this.speed * 0.06 },
        ...(peers > 3 ? [{ id: "d", type: "webrtc" as const, addr: "116.21.xx.xx:6881", downloadSpeed: 0, uploadSpeed: 0 }] : []),
        ...this.webseeds.map((u, i) => ({ id: `ws${i}`, type: "webSeed" as const, addr: u, downloadSpeed: this.speed * 0.3, uploadSpeed: 0 })),
      ],
    }
  }

  segments(fileIndex: number, n: number) {
    const f = this.files[fileIndex]
    if (!f) return new Uint8Array(n)
    return computeSegments(f.start, f.end, n, (p) => this.done[p] === 1, (p) => this.partial.has(p))
  }

  fileURL() {
    return null
  }

  torrentFileURL() {
    return null
  }

  addWebSeeds(urls: string[]) {
    for (const u of urls) {
      this.webseeds.push(u)
      this.emitLog("info", "已启用 HTTP WebSeed 加速")
    }
  }
  private webseeds: string[] = []

  destroy() {
    window.clearInterval(this.timer)
    this.listeners.metadata.clear()
    this.listeners.log.clear()
  }
}

export const mockEngine: Engine = {
  kind: "mock",
  async start(magnet: string) {
    return new MockSession(magnet)
  },
  async streamingReady() {
    return false
  },
  async resetServiceWorker() {},
}
