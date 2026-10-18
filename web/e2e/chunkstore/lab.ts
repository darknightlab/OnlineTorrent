// 端到端测试的页面部分。?role=seed：普通 WebTorrent（内存 store）做种 / 充当第二个下载者；
// ?role=play：用真实的 webtorrentEngine（Service Worker 流 → serveFile → 窗口化读取 → OPFS store）播放 / 读取。
/* eslint-disable @typescript-eslint/no-explicit-any */
import WebTorrent from "webtorrent/dist/webtorrent.min.js"
import { webtorrentEngine } from "../../src/player/webtorrent-engine"
import { pieceCache, ROOT } from "../../src/player/store/cache"

const MB = 1 << 20
const q = new URLSearchParams(location.search)
const tracker = q.get("tr")!

function rand(n: number, seed: number) {
  const d = new Uint8Array(n)
  let x = seed >>> 0 || 1
  for (let i = 0; i < n; i += 4) {
    x ^= x << 13
    x ^= x >>> 17
    x ^= x << 5
    d[i] = x
    d[i + 1] = x >> 8
    d[i + 2] = x >> 16
    d[i + 3] = x >> 24
  }
  return d
}
async function sha(b: Uint8Array) {
  return [...new Uint8Array(await crypto.subtle.digest("SHA-256", b as Uint8Array<ArrayBuffer>))].map((x) => x.toString(16).padStart(2, "0")).join("").slice(0, 16)
}

class Mem {
  chunkLength: number
  m = new Map<number, Uint8Array>()
  constructor(cl: number) {
    this.chunkLength = cl
  }
  put(i: number, b: Uint8Array, cb: any) {
    this.m.set(i, b)
    queueMicrotask(() => cb(null))
  }
  get(i: number, o: any, cb: any): void {
    if (typeof o === "function") return this.get(i, null, o)
    const b = this.m.get(i)
    if (!b) return queueMicrotask(() => cb(new Error("nf")))
    const off = o?.offset || 0
    queueMicrotask(() => cb(null, b.subarray(off, off + (o?.length || b.length - off))))
  }
  close(cb: any) {
    cb?.(null)
  }
  destroy(cb: any) {
    cb?.(null)
  }
}

/** 测试用的三个文件：seed 与 len 两端一致即可得到相同数据 */
const FILES: Record<string, { len: number; seed: number; pl: number }> = {
  big: { len: 30 * MB + 12345, seed: 7, pl: 256 * 1024 },
  small: { len: 2 * MB + 777, seed: 11, pl: 256 * 1024 },
  quota: { len: 12 * MB, seed: 13, pl: 256 * 1024 },
}

const L: string[] = []
const lab: any = { MB, rand, sha, FILES, log: L, Mem, WebTorrent, pieceCache, engine: webtorrentEngine }
;(window as any).lab = lab

if (q.get("role") === "seed") {
  lab.S = new WebTorrent({ tracker: { rtcConfig: { iceServers: [] } } } as any)
  lab.seed = (name: string) =>
    new Promise((res) => {
      const f = FILES[name]
      ;(lab.S as any).seed(new File([rand(f.len, f.seed)], `${name}.bin`), { announce: [tracker], pieceLength: f.pl, name: `${name}.bin`, store: Mem }, (t: any) => res({ magnet: t.magnetURI, ih: t.infoHash, pieces: t.pieces.length, torrentFile: btoa(String.fromCharCode(...t.torrentFile)) }))
    })
  lab.unseed = (ih: string) => new Promise((r) => (lab.S as any).get(ih).then((t: any) => (t ? t.destroy(r) : r(null))))
  /** 第二个下载者 C（内存 store，只连得到 B）：选中全部，统计拿到的 piece 与 reject */
  lab.leech = async (magnet: string, ms: number) => {
    const C: any = new WebTorrent({ tracker: { rtcConfig: { iceServers: [] } } } as any)
    const t = C.add(magnet, { announce: [tracker], store: Mem, deselect: true })
    await new Promise((r) => t.once("ready", r))
    let rejects = 0
    let closed = 0
    let maxWires = 0
    t.on("wire", (w: any) => {
      w.on("reject", () => rejects++)
      w.once("close", () => closed++)
    })
    for (const w of t.wires) w.on("reject", () => rejects++)
    t.select(0, t.pieces.length - 1)
    const t0 = Date.now()
    let last = -1
    let stableSince = Date.now()
    while (Date.now() - t0 < ms) {
      await new Promise((r) => setTimeout(r, 250))
      maxWires = Math.max(maxWires, t.wires.length)
      let have = 0
      for (let i = 0; i < t.pieces.length; i++) if (t.bitfield.get(i)) have++
      if (have !== last) {
        last = have
        stableSince = Date.now()
      } else if (have > 0 && Date.now() - stableSince > 4000) break
    }
    const r = { have: last, of: t.pieces.length, rejects, closedWires: closed, wiresNow: t.wires.length, maxWires, peerHas: t.wires.map((w: any) => { let n = 0; for (let i = 0; i < t.pieces.length; i++) if (w.peerPieces.get(i)) n++; return n }) }
    await new Promise((res) => C.destroy(res))
    return r
  }
  lab.ready = true
} else {
  /** 列出 OPFS ot-pieces/<key> 中的文件，返回 {files, bytes} */
  lab.opfs = async (key?: string) => {
    const root = await navigator.storage.getDirectory()
    const out: Record<string, { files: number; bytes: number }> = {}
    let total = 0
    let base: FileSystemDirectoryHandle
    try {
      base = await root.getDirectoryHandle(ROOT)
    } catch {
      return { dirs: out, total, rootNames: [] as string[] }
    }
    for await (const [name, h] of base.entries()) {
      if (key && name !== key) continue
      if (h.kind !== "directory") continue
      let files = 0
      let bytes = 0
      for await (const [fn, fh] of (h as FileSystemDirectoryHandle).entries()) {
        if (!/^\d+$/.test(fn)) continue
        try {
          bytes += (await (fh as FileSystemFileHandle).getFile()).size
          files++
        } catch {
          /* 刚被删除 */
        }
      }
      out[name] = { files, bytes }
      total += bytes
    }
    const rootNames: string[] = []
    for await (const n of root.keys()) rootNames.push(n)
    return { dirs: out, total, rootNames }
  }
  lab.torrent = (ih: string) => pieceCache.entries.get(ih)?.live?.torrent as any
  /** torrentFileB64：没有节点能提供元数据时（做种者已离开），像 xs= 那样直接给 .torrent */
  lab.start = async (magnet: string, torrentFileB64?: string) => {
    const logs: string[] = []
    const s: any = await webtorrentEngine.start(magnet, { announce: [tracker], iceServers: [], maxCacheBytes: 4 << 30 })
    s.on("log", (lv: string, m: string) => logs.push(`${lv}: ${m}`))
    s.on("fatal", (m: string) => logs.push(`FATAL: ${m}`))
    if (torrentFileB64) await s.loadTorrentFile(Uint8Array.from(atob(torrentFileB64), (c) => c.charCodeAt(0)))
    await new Promise<void>((r) => (s.hasMetadata() ? r() : s.on("metadata", r)))
    s.focusFile(0)
    lab.s = s
    lab.slog = logs
    return s
  }
  /** 通过 SW 流按顺序读取 [start, end]（Range），每 chunkBytes 暂停 pauseMs 模拟播放速度；期间每 100 ms 记录存储量 */
  lab.read = async (url: string, start: number, end: number, { pauseMs = 0, chunkBytes = 512 * 1024 } = {}) => {
    const res = await fetch(url, { headers: { range: `bytes=${start}-${end}` } })
    const reader = res.body!.getReader()
    const out = new Uint8Array(end - start + 1)
    let o = 0
    let nextPause = chunkBytes
    let maxStored = 0
    let maxOpfs = 0
    let sampling = true
    const sampler = (async () => {
      while (sampling) {
        maxStored = Math.max(maxStored, pieceCache.stored)
        maxOpfs = Math.max(maxOpfs, (await lab.opfs().catch(() => ({ total: 0 }))).total)
        await new Promise((r) => setTimeout(r, 100))
      }
    })()
    for (;;) {
      const { value, done } = await reader.read()
      if (done) break
      out.set(value, o)
      o += value.length
      if (pauseMs && o >= nextPause) {
        nextPause += chunkBytes
        await new Promise((r) => setTimeout(r, pauseMs))
      }
    }
    sampling = false
    await sampler
    return { status: res.status, bytes: o, data: out, maxStored, maxOpfs }
  }
  lab.ready = true
}
