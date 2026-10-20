// webtorrent 内部接口兼容性检查（npm test 与 npm run build 的 prebuild 都会运行）。
// 1. 静态：加载浏览器实际使用的 dist 包与 node 版 lib，逐项检查 internals.ts 依赖的内部接口；
// 2. 功能：node 里用 TCP 连接三个 client，验证 淘汰 → 按需重新下载、wire 修正（远端只拉没被淘汰的 piece，无 reject 死循环）。
// 升级 webtorrent 后此测试失败，说明 src/player/store/internals.ts 需要跟着改（错误信息会列出缺少 / 变化的内部接口）。
import { test } from "node:test"
import assert from "node:assert/strict"
import { readFileSync } from "node:fs"
import { createRequire } from "node:module"
import { checkWebTorrentBundle, installFetchRecorder } from "./bundle-check.ts"
import {
  checkBundleSource,
  normSource,
  checkPieceCtor,
  checkTorrentMetadata,
  checkTorrentPrototype,
  checkWire,
  installReaders,
  installWireFix,
  unverifyPiece,
  type PieceCtor,
  type Reader,
  type TorrentLike,
} from "./internals.ts"

/* eslint-disable @typescript-eslint/no-explicit-any */
const require = createRequire(import.meta.url)
const step = (m: string) => process.env.COMPAT_DEBUG && console.log("[compat]", m)
const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms))

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

function rand(n: number, seed: number) {
  const d = new Uint8Array(n)
  let x = seed >>> 0 || 1
  for (let i = 0; i < n; i++) {
    x ^= x << 13
    x ^= x >>> 17
    x ^= x << 5
    d[i] = x & 255
  }
  return d
}

const until = async (cond: () => boolean, ms: number, what: string) => {
  const t0 = Date.now()
  while (!cond()) {
    if (Date.now() - t0 > ms) throw new Error(`timeout: ${what}`)
    await sleep(25)
  }
}

const report = (problems: string[], where: string) =>
  assert.deepEqual(problems, [], `webtorrent ${where} 与 src/player/store/internals.ts 不兼容：\n  - ${problems.join("\n  - ")}`)

const clientOpts = { dht: false, lsd: false, utp: false, tracker: false, webSeeds: false, natUpnp: false, natPmp: false }

test("webtorrent dist bundle (browser build) exposes the internals we use", async () => {
  ;(globalThis as any).self ??= globalThis
  const distPath = require.resolve("webtorrent/dist/webtorrent.min.js")
  const realFetch = globalThis.fetch
  const fetchLog = installFetchRecorder() // before the import: the bundle captures self.fetch at load
  const { default: WebTorrent } = await import("webtorrent/dist/webtorrent.min.js" as string)
  // 与 postbuild（scripts/check-built-webtorrent.ts，检查 Vite 实际输出的 chunk）相同的检查
  try {
    report(await checkWebTorrentBundle(WebTorrent, readFileSync(distPath, "utf8"), "node_modules dist", fetchLog), "dist")
  } finally {
    globalThis.fetch = realFetch
  }
  const seedC = new WebTorrent(clientOpts)
  const c = new WebTorrent(clientOpts)
  try {
    const data = rand(300_000, 3)
    const seeded: any = await new Promise((r) => seedC.seed(new File([data], "a.bin"), { pieceLength: 65536, store: Mem, announce: [] }, r))
    const t: any = c.add(seeded.torrentFile, { deselect: true, store: Mem, storeCacheSlots: 4 })
    report(checkTorrentPrototype(Object.getPrototypeOf(t)), "Torrent 原型")
    await new Promise<void>((r) => t.once("metadata", r))
    report(checkTorrentMetadata(t), "Torrent 实例")
    // 全部 piece 都已缓存时 pieces[] 里没有 Piece 实例可借构造器，引擎改用 torrent-piece 包：必须与 dist 内置的一致
    const { default: TorrentPiece } = await import("torrent-piece" as string)
    const own = t.pieces.find(Boolean).constructor
    const methods = (P: any) => Object.getOwnPropertyNames(P.prototype).sort().join(",")
    assert.equal(methods(TorrentPiece), methods(own), "torrent-piece 依赖与 webtorrent dist 内置的 Piece 不一致（升级 torrent-piece 到 webtorrent 使用的版本）")
    report(checkPieceCtor(TorrentPiece), "torrent-piece")
    await new Promise<void>((r) => (t.ready ? r() : t.once("ready", r)))
    assert.equal(t.pieces.filter(Boolean).length, t.pieces.length)
  } finally {
    await new Promise((r) => c.destroy(r))
    await new Promise((r) => seedC.destroy(r))
  }
})

test("source checks survive re-minification (template-literal quotes, renamed locals, let/const)", () => {
  // Vite 生产构建再压缩后的 _onWireWithMetadata / _onRequest / serveFile 片段（2026-10 线上事故：引号变成反引号）
  const proto = {
    _select() {}, _deselect() {}, critical() {}, select() {}, deselect() {}, _markVerified() {}, _checkDone() {}, _gcSelections() {},
    _verifyPiecesUsingBitfield() {}, _hasAllPieces() {}, destroy() {},
  } as any
  const variants = [
    "function(e){let t=null;e.on(`request`,(t,n,i,s)=>{if(i>131072)return e.destroy();this.pieces[t]||this.store.get(t,{offset:n,length:i},s)}),e.hasFast&&this._hasAllPieces()?e.haveAll():e.bitfield(this.bitfield)}",
    "function(e){const r=this;e.on('request',function(t,n,i,s){if(i>131072)return e.destroy();if(r.pieces[t])return;r.store.get(t,{offset:n,length:i},s)});e.hasFast&&r._hasAllPieces()?e.haveAll( ):e.bitfield(r.bitfield)}",
    'function (wire) { wire.on( "request" , (index, offset, length, cb) => { if (this.pieces[index] != null) { return } this.store.get(index, { offset, length }, cb) }); if (wire.hasFast && this._hasAllPieces()) wire.haveAll() }',
  ]
  for (const v of variants) {
    // Function.prototype.toString 需要真函数：用 new Function 造出源码一致的函数
    const pr = { ...proto, _onWireWithMetadata: new Function(`return ${v}`)() }
    pr._onMetadata = new Function("return function(){this._hasStartupBitfield;this._storeCacheSlots;this._reservations}")()
    pr._select = new Function("return function(e,t,n,i,r=!1){this._selections.insert({from:e,to:t,isStreamSelection:r})}")()
    assert.deepEqual(checkTorrentPrototype(pr), [], v)
  }
  assert.deepEqual(checkBundleSource("let i=e[Symbol.asyncIterator](r),a=null;e.emit(`iterator`,{iterator:i,req:t,file:e},e=>{a=e});const o=U.Readable.from(a||i);lt_donthave;let Se=self.fetch||(()=>{})"), [])
  assert.deepEqual(checkBundleSource("let i=e[Symbol.asyncIterator](r),a=null;e.emit('iterator',{iterator:i,req:t,file:e},(n)=>{ a = n });const o=Readable.from(a || i);lt_donthave;let Se=self.fetch||(()=>{})"), [])
  // replacement ignored (only the original iterator streamed): subtitles would silently get no cues
  assert.ok(checkBundleSource("let i=e[Symbol.asyncIterator](r),a=null;e.emit(`iterator`,{iterator:i,req:t,file:e},e=>{a=e});const o=U.Readable.from(i);lt_donthave;let Se=self.fetch||(()=>{})").some((x) => /内封字幕/.test(x)))
  assert.equal(normSource("a(`x`,'y',\"z\")"), 'a("x","y","z")')
  const wire = { _onRequest: new Function("return function(e,t,n){if(this.amChoking){this.hasFast&&this.reject(e,t,n);return}this.emit(`request`,e,t,n,()=>{})}")() } as any
  assert.ok(!checkWire(wire).some((x) => /_onRequest/.test(x)))
})

test("compat checks name what is missing", () => {
  const p = checkTorrentPrototype({ _deselect() {}, critical() {} })
  assert.ok(p.includes("Torrent._select()") && p.includes("Torrent._onWireWithMetadata()"), p.join("; "))
  const m = checkTorrentMetadata({ pieces: [], files: [] })
  assert.ok(m.includes("Torrent.bitfield（get/set/buffer）") && m.includes("Torrent.files"), m.join("; "))
  assert.ok(checkWire({}).includes("Wire.lt_donthave.donthave()（lt_donthave 扩展）"))
})

test("evict → re-download on demand, startup bitfield, wire fix (node, TCP)", { timeout: 60_000 }, async () => {
  const { default: WebTorrent } = await import("webtorrent" as string)
  const S = new WebTorrent(clientOpts)
  const D = new WebTorrent(clientOpts)
  const L = new WebTorrent(clientOpts)
  const D2 = new WebTorrent(clientOpts)
  try {
    const PL = 65536
    const data = rand(80 * PL + 12_345, 7) // 81 个 piece，最后一个较短
    const st: any = await new Promise((r) => S.seed(Buffer.from(data), { pieceLength: PL, name: "movie.bin", store: Mem, announce: [] }, r))
    // S 代表运行本站代码的远端浏览器：同样装上 wire 修正（stock WebTorrent 会永久 choke 曾经完整的节点，见 internals.ts）
    installWireFix(st)
    await until(() => S.torrentPort > 0, 5000, "S listen")
    report(checkTorrentPrototype(Object.getPrototypeOf(st)), "Torrent 原型（node lib）")

    // ---- D：下载者，装上 wire 修正与窗口化读取
    const t: any = D.add(st.torrentFile, { store: Mem, storeCacheSlots: 4 })
    let Piece: PieceCtor | null = null
    const readers = new Set<Reader>()
    installWireFix(t)
    t.once("metadata", () => {
      report(checkTorrentMetadata(t), "Torrent 实例（node lib）")
      Piece = t.pieces.find(Boolean).constructor
      report(checkPieceCtor(Piece), "Piece")
      installReaders(t, Piece!, { windowBytes: () => 4 * PL, register: (r) => readers.add(r), unregister: (r) => readers.delete(r) })
    })
    await new Promise<void>((r) => t.once("ready", r))
    t.addPeer(`127.0.0.1:${S.torrentPort}`)
    step('await until(() => t.done, 20_0')
    await until(() => t.done, 20_000, "D done")
    report(checkWire(t.wires[0]), "Wire")
    const store: Mem = t.store.store.store // ImmediateChunkStore → CacheChunkStore → Mem

    // 淘汰 10..19：不会自发重下；读到这一段时按需重新下载，字节一致
    for (let i = 10; i < 20; i++) {
      assert.ok(unverifyPiece(t, i, Piece!))
      store.m.delete(i)
    }
    assert.equal(t.bitfield.get(10), false)
    assert.equal(t.done, false)
    assert.ok(t.progress < 1)
    const r0 = t.received
    await sleep(800)
    assert.ok(t.received - r0 < PL, "evicted pieces must not be re-downloaded spontaneously") // 只有协议消息
    const f = t.files[0]
    const start = 10 * PL - 100
    const end = 20 * PL + 99
    step('const got: Uint8Array')
    const got: Uint8Array = new Uint8Array(await f.arrayBuffer({ start, end }))
    assert.deepEqual(got, data.subarray(start, end + 1))
    assert.ok(t.received - r0 >= 10 * PL, `re-downloaded ${t.received - r0}`)
    assert.equal(readers.size, 0, "reader unregistered after the read")

    // store 里的数据悄悄没了（其他 tab / 清空缓存），bitfield 仍说有：读取时自动 unverify 并重下
    store.m.delete(30)
    store.m.delete(31)
    t.store.store?.cache?.remove(30)
    t.store.store?.cache?.remove(31)
    step('const got2')
    const got2 = new Uint8Array(await f.arrayBuffer({ start: 30 * PL, end: 32 * PL - 1 }))
    assert.deepEqual(got2, data.subarray(30 * PL, 32 * PL))

    // 整个文件（窗口化读取，含最后一个短 piece）
    step('const all =')
    const all = new Uint8Array(await f.arrayBuffer())
    assert.deepEqual(all, data)

    // ---- 启动 bitfield：D2 带着 D 的 store 内容与 bitfield 启动，不必重算全部哈希
    const pre = new Mem(PL)
    for (const [i, b] of store.m) pre.m.set(i, b)
    pre.m.delete(5)
    const bf = new Uint8Array(Math.ceil(81 / 8))
    for (const i of pre.m.keys()) bf[i >> 3] |= 128 >> (i % 8)
    const t2: any = D2.add(st.torrentFile, { preloadedStore: pre, bitfield: bf, storeCacheSlots: 0, deselect: true })
    let hashed = 0
    const og = pre.get.bind(pre)
    pre.get = (i: number, o: any, cb: any) => (hashed++, og(i, o, cb))
    step('await new Promise<void>((r) =>')
    await new Promise<void>((r) => t2.once("ready", r))
    assert.ok(hashed <= 3, `startup bitfield should only spot-check pieces (got ${hashed} reads)`)
    assert.equal(t2.bitfield.get(5), false)
    assert.equal(t2.bitfield.get(6), true)

    // ---- wire 修正：L 只连 D（S 退出），D 淘汰 0..4 后 L 选中全部 → 拿到其余全部 piece，reject 不死循环，连接保持
    step('await new Promise((r) => st.de')
    await new Promise((r) => st.destroy(r))
    await until(() => t.done, 20_000, "D done again")
    const lt: any = L.add(st.torrentFile, { deselect: true, store: Mem })
    await new Promise<void>((r) => lt.once("ready", r))
    lt.addPeer(`127.0.0.1:${D.torrentPort}`)
    await until(() => lt.wires.length > 0 && lt.wires[0].peerPieces.get(3), 10_000, "L sees D's pieces")
    const lw = lt.wires[0]
    let rejects = 0
    let closed = false
    lw.on("reject", () => rejects++)
    lw.once("close", () => (closed = true))
    for (let i = 0; i < 5; i++) {
      unverifyPiece(t, i, Piece!)
      store.m.delete(i)
    }
    await sleep(200)
    assert.equal(lw.peerPieces.get(3), false, "lt_donthave must reach the remote peer")
    step('lt.select(0, lt.pieces.length ')
    lt.select(0, lt.pieces.length - 1)
    await until(() => [...Array(81).keys()].filter((i) => lt.bitfield.get(i)).length === 76, 15_000, "L gets the 76 remaining pieces")
    await sleep(500)
    assert.ok(rejects < 20, `reject loop: ${rejects}`)
    assert.equal(closed, false)
  } finally {
    for (const c of [S, D, L, D2]) await new Promise((r) => c.destroy(r))
  }
})
