// 对 WebTorrent 3 内部状态的操作：淘汰已校验的 piece、wire 修正、窗口化读取，以及兼容性检查。
// 不依赖 DOM，可在 node 中对真实 webtorrent 运行（见 webtorrent-compat.test.ts，`npm run build` / `npm test` 都会跑）。
// webtorrent 升级后如果这里用到的内部接口变了，构建直接失败；运行时检查失败则报错（不静默回退）。
/* eslint-disable @typescript-eslint/no-explicit-any */

export interface PieceLike {
  length: number
  missing: number
}
export type PieceCtor = new (length: number) => PieceLike

export interface BitFieldLike {
  buffer: Uint8Array
  get(i: number): boolean
  set(i: number, v?: boolean): void
}

export interface WireLike {
  hasFast: boolean
  destroyed?: boolean
  isSeeder?: boolean
  amChoking?: boolean
  unchoke(): void
  peerPieces: BitFieldLike
  haveAll(): void
  bitfield(b: BitFieldLike | Uint8Array): void
  have(i: number): void
  reject(i: number, offset: number, length: number): void
  on(ev: string, cb: (...a: any[]) => void): void
  lt_donthave?: { donthave(i: number): void; on?(ev: string, cb: (i: number) => void): void }
}

export interface FileLike {
  name: string
  length: number
  offset: number
  done: boolean
  _startPiece: number
  _endPiece: number
  _torrent: TorrentLike | null
  _iterators: Set<{ destroy(): void }>
  includes(i: number): boolean
  [Symbol.asyncIterator]: (opts?: { start?: number; end?: number }) => AsyncIterator<Uint8Array>
}

export interface StoreLike {
  get(i: number, opts: { offset?: number; length?: number } | null, cb: (err: Error | null, buf?: Uint8Array) => void): void
  /** ImmediateChunkStore → CacheChunkStore（storeCacheSlots > 0 时） */
  store?: { cache?: { remove(i: number): void } | null }
}

export interface TorrentLike {
  infoHash: string
  pieceLength: number
  lastPieceLength: number
  length: number
  pieces: (PieceLike | null)[]
  bitfield: BitFieldLike
  _reservations: unknown[]
  files: FileLike[]
  wires: WireLike[]
  done: boolean
  destroyed: boolean
  ready: boolean
  store: StoreLike | null
  _select(from: number, to: number, priority: number, notify: null, isStream: boolean): void
  _deselect(from: number, to: number, isStream: boolean): void
  critical(from: number, to: number): void
  on(ev: string, cb: (...a: any[]) => void): void
  once(ev: string, cb: (...a: any[]) => void): void
  removeListener(ev: string, cb: (...a: any[]) => void): void
  emit(ev: string, ...a: unknown[]): boolean
}

// ---------------------------------------------------------------- 兼容性检查

const fn = (o: any, k: string | symbol) => typeof o?.[k] === "function"
/**
 * 函数源码，引号统一成 "（Vite/esbuild 再压缩时会把 'x' / "x" 改成模板字符串 `x`）。
 * 下面的正则只依赖属性名与字符串内容：局部变量名、let/const、空白、分号、!= / !== 都可能被压缩器改掉。
 */
export const normSource = (code: string) => code.replace(/[`']/g, '"')
const src = (o: any, k: string) => (fn(o, k) ? normSource(Function.prototype.toString.call(o[k])) : "")
/** 标识符或 this（压缩器可能写成 let n=this; n.pieces[...]） */
const ID = String.raw`(?:this|[\w$]+)`
const RE = {
  haveAll: /\.haveAll\(\s*\)/,
  onRequest: /\.on\(\s*"request"\s*,/,
  // 'request' 处理里先看 pieces[i]（缺失就不回应），再 store.get
  requestChecksPieces: new RegExp(String.raw`\.on\(\s*"request"\s*,[\s\S]{0,400}?${ID}\.pieces\[[^\]]{1,40}\][\s\S]{0,160}?${ID}\.store\.get\(`),
  emitRequest: /\.emit\(\s*"request"\s*,/,
  reject: /\.reject\(/,
  serveIterator: /\[Symbol\.asyncIterator\]\(\s*[\w$]*\s*\)[\s\S]{0,300}?\.emit\(\s*"iterator"\s*,/,
}

/** Torrent 原型（client.add 返回后立即可查）。返回不兼容项的描述，空数组表示兼容。 */
export function checkTorrentPrototype(proto: any): string[] {
  const p: string[] = []
  for (const k of ["_select", "_deselect", "critical", "select", "deselect", "_markVerified", "_checkDone", "_gcSelections", "_onWireWithMetadata", "_verifyPiecesUsingBitfield", "_onMetadata", "_hasAllPieces", "destroy"]) {
    if (!fn(proto, k)) p.push(`Torrent.${k}()`)
  }
  const wm = src(proto, "_onWireWithMetadata")
  if (wm && !RE.haveAll.test(wm)) p.push("Torrent._onWireWithMetadata 不再调用 wire.haveAll()")
  if (wm && !RE.onRequest.test(wm)) p.push("Torrent._onWireWithMetadata 不再监听 wire 'request'")
  else if (wm && !RE.requestChecksPieces.test(wm)) p.push("Torrent._onWireWithMetadata 的请求处理不再按 pieces[i] 判断是否缺失")
  const md = src(proto, "_onMetadata")
  if (md && !/_hasStartupBitfield/.test(md)) p.push("Torrent._onMetadata 不再支持启动 bitfield（_hasStartupBitfield）")
  if (md && !/_storeCacheSlots/.test(md)) p.push("Torrent._onMetadata 不再使用 storeCacheSlots")
  if (md && !/_reservations/.test(md)) p.push("Torrent._reservations")
  const sel = src(proto, "_select")
  if (sel && !/isStreamSelection/.test(sel)) p.push("Torrent._select 不再支持 isStreamSelection 参数")
  return p
}

/** 元数据就绪后（'metadata' 事件里）检查实例字段 */
export function checkTorrentMetadata(t: any): string[] {
  const p: string[] = []
  const n = t?.pieces?.length
  if (!Array.isArray(t?.pieces)) p.push("Torrent.pieces")
  if (!t?.bitfield || !fn(t.bitfield, "get") || !fn(t.bitfield, "set") || !(t.bitfield.buffer instanceof Uint8Array)) p.push("Torrent.bitfield（get/set/buffer）")
  if (!Array.isArray(t?._reservations) || t._reservations.length !== n) p.push("Torrent._reservations")
  if (!t?._selections || !Array.isArray(t._selections._items)) p.push("Torrent._selections._items")
  if (!t?._rarityMap) p.push("Torrent._rarityMap")
  if (!Array.isArray(t?._hashes) || t._hashes.length !== n) p.push("Torrent._hashes")
  if (!Array.isArray(t?._critical)) p.push("Torrent._critical")
  if (typeof t?.lastPieceLength !== "number" || typeof t?.pieceLength !== "number") p.push("Torrent.pieceLength / lastPieceLength")
  if (!t?.store || !fn(t.store, "get") || !fn(t.store, "put")) p.push("Torrent.store（ImmediateChunkStore）")
  else if (t._storeCacheSlots > 0 && !(t.store.store && t.store.store.cache && fn(t.store.store.cache, "remove"))) p.push("Torrent.store.store.cache（CacheChunkStore 的 LRU）")
  const f = t?.files?.[0]
  if (!f) p.push("Torrent.files")
  else {
    for (const k of ["_startPiece", "_endPiece", "offset", "length"]) if (typeof f[k] !== "number") p.push(`File.${k}`)
    if (!(f._iterators instanceof Set)) p.push("File._iterators")
    if (f._torrent !== t) p.push("File._torrent")
    for (const k of ["includes", "_destroy", "createReadStream"]) if (!fn(f, k)) p.push(`File.${k}()`)
    if (!fn(f, Symbol.asyncIterator)) p.push("File[Symbol.asyncIterator]()")
  }
  const piece = (t?.pieces ?? []).find(Boolean)
  if (piece) p.push(...checkPieceCtor(piece.constructor))
  return p
}

/** WebTorrent 内部的 Piece 类（torrent-piece）：unverify 时要为 pieces[i] 换上新实例 */
export function checkPieceCtor(P: any): string[] {
  const p: string[] = []
  try {
    const x = new P(40000)
    if (x.length !== 40000 || x.missing !== 40000) p.push("Piece(length).length/missing")
    for (const k of ["reserve", "set", "flush", "cancel", "chunkLength", "chunkOffset"]) if (!fn(x, k)) p.push(`Piece.${k}()`)
  } catch (e) {
    p.push(`Piece 构造失败：${String(e)}`)
  }
  return p
}

/** bittorrent-protocol Wire（含 fast 扩展与 lt_donthave） */
export function checkWire(w: any): string[] {
  const p: string[] = []
  for (const k of ["haveAll", "bitfield", "have", "reject", "on", "extended", "unchoke"]) if (!fn(w, k)) p.push(`Wire.${k}()`)
  if (typeof w?.amChoking !== "boolean") p.push("Wire.amChoking")
  if (!fn(w?.lt_donthave, "on")) p.push("Wire.lt_donthave 的 'donthave' 事件")
  if (typeof w?.hasFast !== "boolean") p.push("Wire.hasFast")
  if (!w?.peerPieces || !fn(w.peerPieces, "get")) p.push("Wire.peerPieces")
  if (!w?.lt_donthave || !fn(w.lt_donthave, "donthave")) p.push("Wire.lt_donthave.donthave()（lt_donthave 扩展）")
  const req = src(w, "_onRequest")
  if (req && !RE.emitRequest.test(req)) p.push("Wire._onRequest 不再 emit('request', …, cb)")
  if (req && !RE.reject.test(req)) p.push("Wire._onRequest 出错时不再回 reject（fast 扩展）")
  return p
}

/** dist 包源码层面的检查：ServerBase.serveFile 通过 file[Symbol.asyncIterator](range) 取数据 */
export function checkBundleSource(code: string): string[] {
  const p: string[] = []
  if (!RE.serveIterator.test(normSource(code))) p.push("server.serveFile 不再用 file[Symbol.asyncIterator](range) 读取文件")
  if (!/lt_donthave/.test(code)) p.push("lt_donthave 扩展")
  return p
}

export class IncompatibleError extends Error {
  problems: string[]
  constructor(problems: string[]) {
    super(`播放器内核不兼容：缺少 ${problems.join("；")}`)
    this.name = "IncompatibleError"
    this.problems = problems
  }
}

export function assertCompatible(problems: string[]) {
  if (problems.length) throw new IncompatibleError(problems)
}

// ---------------------------------------------------------------- 淘汰

export function pieceLen(t: TorrentLike, i: number) {
  return i === t.pieces.length - 1 ? t.lastPieceLength : t.pieceLength
}

/**
 * 让 WebTorrent 忘掉一个已校验的 piece（数据随后由 store 删除）；之后需要时会重新下载。
 * 不能用 torrent._markUnverified：它会 select(i,i)，导致后台立刻重下。
 */
export function unverifyPiece(t: TorrentLike, i: number, Piece: PieceCtor, donthave = true): boolean {
  if (t.destroyed || !t.bitfield?.get(i)) return false
  t.bitfield.set(i, false)
  t.pieces[i] = new Piece(pieceLen(t, i)) // _request / _updateWireInterest / 对方请求都按 pieces[i] 是否为 null 判断
  t._reservations[i] = []
  for (const f of t.files) if (f.done && f.includes(i)) f.done = false
  t.done = false // 之后 _checkDone 会再次 emit 'done'
  try {
    t.store?.store?.cache?.remove(i)
  } catch {
    /* ignore */
  }
  if (donthave) {
    for (const w of t.wires) {
      try {
        w.lt_donthave?.donthave(i)
      } catch {
        /* ignore */
      }
    }
  }
  t.emit("evicted", i)
  return true
}

/**
 * 远端请求我们已淘汰的 piece 时：WebTorrent 默认不回应（对方 30 s 后断开）。三件一起做：
 * 不发 HAVE_ALL（远端的 HaveAllBitField 会忽略之后的 lt_donthave）、淘汰时发 lt_donthave（unverifyPiece）、
 * 对缺失的 piece 回 reject。必须在 'wire' 事件里同步注册（早于 _onWireWithMetadata）。
 */
export function installWireFix(t: TorrentLike, onWire?: (w: WireLike) => void) {
  t.on("wire", (wire: WireLike) => {
    wire.haveAll = () => wire.bitfield(t.bitfield)
    wire.on("request", (index: number, _o: number, _l: number, cb: (err: Error | null) => void) => {
      if (t.pieces[index]) cb(new Error("piece not available"))
    })
    // 反方向（对方淘汰了 piece）：WebTorrent 一旦把对方当成 seeder 就永远不改回，rechoke 会一直 choke 它；
    // 对方重新 interested 时也只在第一次 unchoke（wire.once）。对方重新缺 piece 时都要能再下载。
    wire.lt_donthave?.on?.("donthave", () => {
      wire.isSeeder = false
    })
    wire.on("interested", () => {
      if (wire.amChoking && !t.destroyed) wire.unchoke()
    })
    onWire?.(wire)
  })
}

// ---------------------------------------------------------------- 窗口化读取

export interface Reader {
  file: FileLike
  start: number
  end: number
  /** 文件内的读位置（下一个要读的字节） */
  pos: number
}

export interface ReaderHooks {
  /** 下载窗口（读位置之后选中的字节数） */
  windowBytes(): number
  register(r: Reader): void
  unregister(r: Reader): void
  /** 读取开始 / 结束（期间该 piece 不可淘汰） */
  reading?(index: number, on: boolean): void
}

/** 单次产出的最大字节数（piece 很大时分段产出，读位置更精确） */
const MAX_CHUNK = 2 * (1 << 20)

/**
 * 替换 WebTorrent 的 FileIterator（它把起点到 EOF 整段 select 上且 offset 只增不减：
 * 预算小于文件时会无限超前下载再被淘汰，piece 被淘汰后永久卡住）。
 * 这里维护滑动的 stream selection [当前 piece, 读位置 + 窗口]，缺 piece 就 critical 并等 'verified'，
 * 读失败（piece 被删）就 unverify 后重新等待，不让流断掉。读位置通过 hooks 报给预算管理器。
 */
export function windowedIterator(file: FileLike, opts: { start?: number; end?: number } | null | undefined, Piece: PieceCtor, hooks: ReaderHooks) {
  const t = file._torrent!
  const start = opts?.start ?? 0
  const end = opts?.end !== undefined && opts.end < file.length ? opts.end : file.length - 1
  const pl = t.pieceLength
  const r: Reader = { file, start, end, pos: start }
  let sel: [number, number] | null = null
  let finished = false
  let abort: (() => void) | null = null
  hooks.register(r)

  // 窗口内的 piece 都已校验时 WebTorrent 的 _gcSelections 会移除这个 selection；之后其中的 piece 被淘汰，
  // 就要重新插入（force），否则没有任何 selection 覆盖它，永远不会被请求
  const setWindow = (p: number, force = false) => {
    const to = Math.max(p, Math.min(file._endPiece, Math.floor((file.offset + Math.min(end, r.pos + hooks.windowBytes())) / pl)))
    if (!force && sel && sel[0] === p && sel[1] === to) return
    const old = sel
    sel = [p, to]
    t._select(p, to, 1, null, true)
    if (old) t._deselect(old[0], old[1], true)
  }

  const waitPiece = (i: number) =>
    new Promise<void>((resolve, reject) => {
      if (t.bitfield.get(i)) return resolve()
      const done = (ok: boolean) => {
        t.removeListener("verified", onVerified)
        t.removeListener("close", onClose)
        abort = null
        if (ok) resolve()
        else reject(new Error("aborted"))
      }
      const onVerified = (j: number) => j === i && done(true)
      const onClose = () => done(false)
      abort = () => done(false)
      t.on("verified", onVerified)
      t.once("close", onClose)
      t.critical(i, Math.min(i + 1, file._endPiece))
    })

  const read = (i: number, offset: number, length: number) =>
    new Promise<Uint8Array>((resolve, reject) => {
      if (!t.store) return reject(new Error("store closed"))
      hooks.reading?.(i, true)
      t.store.get(i, { offset, length }, (err, buf) => {
        hooks.reading?.(i, false)
        if (err || !buf) reject(err ?? new Error("empty read"))
        else if (buf.length !== length) reject(new Error(`short read ${buf.length}/${length}`))
        else resolve(buf)
      })
    })

  const it = {
    async next(): Promise<IteratorResult<Uint8Array>> {
      if (finished || r.pos > end || t.destroyed) {
        it.destroy()
        return { done: true, value: undefined }
      }
      const abs = file.offset + r.pos
      const i = Math.floor(abs / pl)
      const off = abs - i * pl
      const len = Math.min(end - r.pos + 1, pl - off, MAX_CHUNK)
      setWindow(i)
      for (let tries = 0; ; tries++) {
        if (!t.bitfield.get(i)) setWindow(i, true)
        try {
          await waitPiece(i)
        } catch {
          return { done: true, value: undefined }
        }
        if (finished || t.destroyed) return { done: true, value: undefined }
        try {
          const b = await read(i, off, len)
          if (finished) return { done: true, value: undefined }
          r.pos += b.length
          return { value: b, done: false }
        } catch (err) {
          if (finished || t.destroyed) return { done: true, value: undefined }
          // 数据不在了（被淘汰 / 被其他 tab 删除 / 被清空缓存）：重新下载
          if (tries >= 4) throw err
          if (t.bitfield.get(i)) unverifyPiece(t, i, Piece)
        }
      }
    },
    async return(): Promise<IteratorResult<Uint8Array>> {
      it.destroy()
      return { done: true, value: undefined }
    },
    async throw(err: unknown): Promise<IteratorResult<Uint8Array>> {
      it.destroy()
      throw err
    },
    destroy() {
      if (finished) return
      finished = true
      hooks.unregister(r)
      file._iterators?.delete(it)
      abort?.()
      if (sel && !t.destroyed) {
        try {
          t._deselect(sel[0], sel[1], true)
        } catch {
          /* ignore */
        }
      }
      sel = null
    },
    [Symbol.asyncIterator]() {
      return it
    },
    reader: r,
  }
  file._iterators?.add(it) // torrent destroy 时 File._destroy 会调用 destroy()
  return it
}

/** 给每个 File 实例换上窗口化读取（serveFile / createReadStream / arrayBuffer 都会走这里） */
export function installReaders(t: TorrentLike, Piece: PieceCtor, hooks: ReaderHooks) {
  for (const f of t.files) {
    if (f.length === 0) continue
    ;(f as { [Symbol.asyncIterator]: unknown })[Symbol.asyncIterator] = (opts?: { start?: number; end?: number }) => windowedIterator(f, opts, Piece, hooks)
  }
}
