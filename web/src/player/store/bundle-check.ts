// 在 node 中检查一个 WebTorrent 浏览器包（node_modules 里的 dist，或 Vite 构建输出的 chunk）是否满足 internals.ts 的依赖。
// 用于 webtorrent-compat.test.ts（prebuild / npm test）与 scripts/check-built-webtorrent.ts（postbuild）。
// 除源码层面的检查外，尽量做行为检查：用假 wire 驱动 _onWireWithMetadata，看 HAVE_ALL 与 'request' 处理的实际表现。
/* eslint-disable @typescript-eslint/no-explicit-any */
import { checkBundleSource, checkPieceCtor, checkTorrentMetadata, checkTorrentPrototype, checkWire, installEdgeFetch, installWireFix, patchWebSeed, setEdgeToken } from "./internals.ts"

/** fetch calls seen by the recorder (installFetchRecorder) */
export interface FetchRecord {
  url: string
  auth: string | null
}

/**
 * Must run BEFORE the webtorrent bundle is imported (it captures self.fetch at load):
 * replaces fetch with a recorder (answers 503) wrapped by installEdgeFetch, like the engine does.
 */
export function installFetchRecorder(): FetchRecord[] {
  const log: FetchRecord[] = []
  const g = globalThis as unknown as { fetch: typeof fetch }
  g.fetch = (async (input: Request | string | URL, init?: RequestInit) => {
    const url = typeof input === "string" ? input : input instanceof URL ? input.href : (input as Request).url
    log.push({ url, auth: new Headers(init?.headers).get("authorization") })
    return new Response("not here", { status: 503 })
  }) as typeof fetch
  installEdgeFetch(g)
  return log
}

class Mem {
  chunkLength: number
  m = new Map<number, Uint8Array>()
  constructor(chunkLength: number) {
    this.chunkLength = chunkLength
  }
  put(i: number, b: Uint8Array, cb: (e: Error | null) => void) {
    this.m.set(i, b)
    queueMicrotask(() => cb(null))
  }
  get(i: number, o: any, cb: any): void {
    if (typeof o === "function") return this.get(i, null, o)
    const b = this.m.get(i)
    if (!b) return queueMicrotask(() => cb(new Error("not found")))
    const off = o?.offset || 0
    queueMicrotask(() => cb(null, b.subarray(off, off + (o?.length || b.length - off))))
  }
  close(cb?: (e: null) => void) {
    queueMicrotask(() => cb?.(null))
  }
  destroy(cb?: (e: null) => void) {
    queueMicrotask(() => cb?.(null))
  }
}

/** 足以让 Torrent._onWireWithMetadata 跑起来的假 wire；记录调用 */
function fakeWire(numPieces: number) {
  const ls = new Map<string, ((...a: any[]) => void)[]>()
  const on = (ev: string, cb: (...a: any[]) => void) => void ls.set(ev, [...(ls.get(ev) ?? []), cb])
  const calls: string[] = []
  const w: any = {
    type: "webrtc",
    hasFast: true,
    destroyed: false,
    amChoking: true,
    peerExtensions: {},
    peerPieces: { buffer: new Uint8Array(Math.ceil(numPieces / 8)), get: () => false },
    requests: [],
    calls,
    on,
    once: on,
    addListener: on,
    removeListener: (ev: string, cb: (...a: any[]) => void) => void ls.set(ev, (ls.get(ev) ?? []).filter((x) => x !== cb)),
    off: (ev: string, cb: (...a: any[]) => void) => void ls.set(ev, (ls.get(ev) ?? []).filter((x) => x !== cb)),
    removeAllListeners: () => void ls.clear(),
    emit: (ev: string, ...a: any[]) => (ls.get(ev) ?? []).forEach((cb) => cb(...a)),
    lt_donthave: { on: () => {}, donthave: () => {} },
  }
  for (const k of ["haveAll", "haveNone", "bitfield", "interested", "uninterested", "choke", "unchoke", "port", "destroy", "have"]) w[k] = () => calls.push(k)
  return w
}

const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms))

/**
 * 加载好的 WebTorrent 构造器 + 包源码 → 不兼容项列表（空 = 兼容）。
 * node 里运行浏览器包需要 globalThis.self 与 Uint8Array.prototype.toBase64（node ≥ 25）。
 */
export async function checkWebTorrentBundle(WebTorrent: any, code: string, label: string, fetchLog?: FetchRecord[]): Promise<string[]> {
  const p: string[] = []
  const add = (where: string, list: string[]) => list.forEach((x) => p.push(`[${label} ${where}] ${x}`))
  add("源码", checkBundleSource(code))
  const opts = { dht: false, lsd: false, utp: false, tracker: false, webSeeds: true }
  const seedC = new WebTorrent(opts)
  const c = new WebTorrent(opts)
  const timeout = <T>(pr: Promise<T>, what: string) => Promise.race([pr, sleep(15_000).then(() => Promise.reject(new Error(`timeout: ${what}`)))])
  try {
    const data = new Uint8Array(300_000).map((_, i) => (i * 31) & 255)
    const seeded: any = await timeout(new Promise((r) => seedC.seed(new File([data], "a.bin"), { pieceLength: 65536, store: Mem, announce: [] }, r)), "seed")
    const t: any = c.add(seeded.torrentFile, { deselect: true, store: Mem, storeCacheSlots: 4 })
    add("Torrent 原型", checkTorrentPrototype(Object.getPrototypeOf(t)))
    await timeout(new Promise<void>((r) => t.once("metadata", r)), "metadata")
    add("Torrent 实例", checkTorrentMetadata(t))
    const piece = t.pieces.find(Boolean)
    if (!piece) p.push(`[${label}] 新添加的种子 pieces[] 里没有 Piece 实例`)
    else add("Piece", checkPieceCtor(piece.constructor))
    await timeout(new Promise<void>((r) => (t.ready ? r() : t.once("ready", r))), "ready")

    // 行为 1：完整的种子对支持 fast 扩展的 wire 发 HAVE_ALL（我们要把它换成 bitfield）
    const ws = fakeWire(seeded.pieces.length)
    seeded._onWireWithMetadata(ws)
    if (!ws.calls.includes("haveAll")) p.push(`[${label} 行为] 完整的种子不再对 fast wire 调用 haveAll()（wire 修正依赖它）`)
    // 行为 2：有数据时 'request' 由 store.get 应答
    const served = await new Promise<boolean>((r) => {
      ws.emit("request", 0, 0, 16384, (err: Error | null, buf?: Uint8Array) => r(!err && buf?.length === 16384))
      setTimeout(() => r(false), 2000)
    })
    if (!served) p.push(`[${label} 行为] wire 'request' 不再由 store.get 应答`)
    ws.emit("close")
    // 行为 3：缺失的 piece：WebTorrent 本身不回应、不读 store；装上 installWireFix 后回 reject（cb(err)）
    const wl = fakeWire(t.pieces.length)
    let storeReads = 0
    const get0 = t.store.get.bind(t.store)
    t.store.get = (...a: any[]) => (storeReads++, get0(...a))
    t._onWireWithMetadata(wl)
    const stock = await new Promise<string>((r) => {
      wl.emit("request", 0, 0, 16384, (err: Error | null) => r(err ? "error" : "data"))
      setTimeout(() => r("silent"), 300)
    })
    if (stock !== "silent" || storeReads) p.push(`[${label} 行为] 对缺失 piece 的请求不再被忽略（实际：${stock}，store 读取 ${storeReads} 次）`)
    wl.emit("close")
    installWireFix(t)
    const wf = fakeWire(t.pieces.length)
    t.emit("wire", wf)
    t._onWireWithMetadata(wf)
    if (wf.calls.includes("haveAll")) p.push(`[${label} 行为] installWireFix 之后仍发送 HAVE_ALL`)
    const fixed = await new Promise<string>((r) => {
      wf.emit("request", 0, 0, 16384, (err: Error | null) => r(err ? "error" : "data"))
      setTimeout(() => r("silent"), 300)
    })
    if (fixed !== "error") p.push(`[${label} 行为] installWireFix 之后对缺失 piece 的请求没有回 reject（实际：${fixed}）`)
    wf.emit("close")

    // 真实的 Wire（bittorrent-protocol）：WebSeed 连接也是 Wire 的子类，且经过 torrent._onWire（装上 lt_donthave）
    t.addWebSeed("http://127.0.0.1:9/never/")
    const wire = await timeout(
      (async () => {
        for (;;) {
          const w = t.wires.find((x: any) => x.type === "webSeed")
          if (w) return w
          await sleep(20)
        }
      })(),
      "webseed wire",
    )
    add("Wire", checkWire(wire))

    // 行为 4：WebSeed 只宣告服务器有的分块（patchWebSeed），之后 HAVE / lt_donthave 增减
    const url2 = "http://127.0.0.1:9/patched/"
    t.addWebSeed(url2)
    const n = t.pieces.length
    const bits = new Uint8Array(Math.ceil(n / 8))
    bits[0] = 0b11000000 // pieces 0, 1
    let handle: ReturnType<typeof patchWebSeed> | null = null
    try {
      handle = patchWebSeed(t, url2, bits)
    } catch (e) {
      p.push(`[${label} WebSeed] ${String((e as Error).message)}`)
    }
    if (handle) {
      const tw = () => t._peers.get(url2)?.wire
      const pp = (i: number) => !!tw()?.peerPieces?.get(i)
      const waitFor = async (cond: () => boolean, what: string) => {
        for (let k = 0; k < 200; k++) {
          if (cond()) return true
          await sleep(10)
        }
        p.push(`[${label} 行为] WebSeed ${what}`)
        return false
      }
      if (await waitFor(() => pp(0) && pp(1), "初始 bitfield 没有到达 torrent 一侧的 wire")) {
        if (pp(2) || pp(n - 1)) p.push(`[${label} 行为] WebSeed 仍宣告了服务器没有的分块（bitfield 未被替换）`)
        bits[0] |= 0b00100000
        handle.add([2])
        await waitFor(() => pp(2), "add() 之后 torrent 一侧没有收到 HAVE")
        const conn = t._peers.get(url2).conn
        await waitFor(() => !!conn.lt_donthave?._peerSupports, "lt_donthave 扩展握手没有完成")
        bits[0] &= ~0b10000000
        handle.remove([0])
        await waitFor(() => !pp(0), "remove() 之后 torrent 一侧没有收到 lt_donthave")
        // WebConn 请求失败后的重新宣告（have）只放行服务器仍有的分块
        conn.have(0)
        conn.have(1)
        await sleep(50)
        if (pp(0)) p.push(`[${label} 行为] WebSeed 重新宣告了服务器没有的分块`)
      }
    }

    // 行为 5：边缘节点的 WebSeed 请求带 Authorization（installEdgeFetch 在 import 之前装好），其他地址不带
    if (fetchLog) {
      const edgeURL = `http://edge.test/webseed/hash/${t.infoHash}/`
      setEdgeToken(edgeURL, "tok-1")
      t.addWebSeed(edgeURL)
      const all = new Uint8Array(Math.ceil(n / 8)).fill(255)
      try {
        patchWebSeed(t, edgeURL, all)
      } catch (e) {
        p.push(`[${label} WebSeed] ${String((e as Error).message)}`)
      }
      t.select(0, n - 1, 1)
      let rec: FetchRecord | undefined
      for (let k = 0; k < 300 && !rec; k++) {
        rec = fetchLog.find((r) => r.url.startsWith(edgeURL))
        if (!rec) await sleep(10)
      }
      if (!rec) p.push(`[${label} 行为] WebSeed 请求没有经过 self.fetch（加载时捕获的 fetch）`)
      else if (rec.auth !== "Bearer tok-1") p.push(`[${label} 行为] 边缘节点 WebSeed 请求没有带上访问令牌（Authorization: ${rec.auth}）`)
      const other = fetchLog.find((r) => !r.url.startsWith(edgeURL))
      if (other?.auth) p.push(`[${label} 行为] 非边缘节点的请求也带上了 Authorization`)
      t.deselect(0, n - 1)
      setEdgeToken(edgeURL, null)
    }
  } catch (e) {
    p.push(`[${label}] 检查失败：${String((e as Error)?.message ?? e)}`)
  } finally {
    await new Promise((r) => c.destroy(r))
    await new Promise((r) => seedC.destroy(r))
  }
  return p
}
