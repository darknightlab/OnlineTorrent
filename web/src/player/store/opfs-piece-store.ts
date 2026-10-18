// abstract-chunk-store 实现：OPFS 里每个 piece 一个文件（ot-pieces/<key>/<index>），预算与淘汰交给 pieceCache。
// WebTorrent 只在 SHA1 校验通过后 put；put 回调出错会 destroy 整个种子，所以 put 永远不报错：
// 配额不足时收缩预算、淘汰后重试一次，仍失败就暂存内存并在几秒后把该 piece 标回未校验（之后按需重下）。
import { pieceCache, type Entry, type Lease } from "./cache"
import { unverifyPiece, type TorrentLike } from "./internals"

type Cb<T = void> = (err: Error | null, v?: T) => void
type GetOpts = { offset?: number; length?: number } | null

export interface StoreOpts {
  torrent: TorrentLike
  length: number
  name: string
  lease: Lease
}

const MEM_FALLBACK_MS = 5000

export class OpfsPieceStore {
  chunkLength: number
  private torrent: TorrentLike
  private entry: Entry
  private ready: Promise<void>
  private closed = false
  private mem = new Map<number, Uint8Array>()
  private lease: Lease

  constructor(chunkLength: number, opts: StoreOpts) {
    this.chunkLength = Number(chunkLength)
    this.torrent = opts.torrent
    this.lease = opts.lease
    const numPieces = Math.ceil(opts.length / this.chunkLength)
    const { entry, ready } = pieceCache.attach(opts.lease.key, opts.lease.ih, opts.lease.temp, opts.torrent, {
      infoHash: opts.lease.ih,
      name: String(opts.name ?? "").replace(/ - [0-9a-f]{8}$/, ""),
      length: opts.length,
      pieceLength: this.chunkLength,
      numPieces,
    })
    this.entry = entry
    this.ready = ready.catch((e) => {
      pieceCache.log("error", `缓存目录不可用：${String(e?.message ?? e)}`)
    })
  }

  private dir() {
    return this.entry.dir
  }

  put(index: number, buf: Uint8Array, cb: Cb = () => {}) {
    void this.doPut(index, buf).then(
      () => cb(null),
      (e) => {
        // 不应发生；仍然不把错误交给 WebTorrent（会 destroy 种子）
        console.error("[cache] put failed", e)
        this.keepInMemory(index, buf)
        cb(null)
      },
    )
  }

  private async doPut(index: number, buf: Uint8Array) {
    await this.ready
    if (this.closed) return
    const e = this.entry
    const live = e.live
    const size = buf.length
    live?.writing.add(index)
    try {
      await e.deleting.get(index)
      const old = e.pieces.get(index)
      if (old !== undefined) e.pieces.delete(index) // 覆盖写：先不计旧的
      await pieceCache.reserve(e, index, size)
      for (let attempt = 0; ; attempt++) {
        try {
          await this.write(index, buf)
          pieceCache.markStored(e, index, size)
          return
        } catch (err) {
          const name = (err as DOMException)?.name
          if (name === "QuotaExceededError" && attempt === 0) {
            pieceCache.unreserve(size)
            await pieceCache.onQuotaExceeded(e)
            pieceCache.pending += size
            continue
          }
          if (name === "NotFoundError" && attempt === 0) {
            // 目录被删除（清空缓存 / 其他 tab）：重建后重试
            e.dir = await (await navigator.storage.getDirectory()).getDirectoryHandle("ot-pieces", { create: true }).then((b) => b.getDirectoryHandle(e.key, { create: true }))
            void pieceCache.writeMeta(e)
            continue
          }
          pieceCache.unreserve(size)
          if (name === "QuotaExceededError") pieceCache.log("warn", "存储空间不足，部分分块暂存内存，之后需要时重新下载")
          else pieceCache.log("warn", `分块写入失败（${name || String(err)}），暂存内存`)
          this.keepInMemory(index, buf)
          return
        }
      }
    } finally {
      live?.writing.delete(index)
    }
  }

  private async write(index: number, buf: Uint8Array) {
    const dir = this.dir()
    if (!dir) throw new DOMException("no dir", "NotFoundError")
    const fh = await dir.getFileHandle(String(index), { create: true })
    const w = await fh.createWritable() // 写到 swap 文件，close() 时原子提交
    try {
      await w.write(buf as Uint8Array<ArrayBuffer>)
      await w.close()
    } catch (err) {
      await w.abort().catch(() => {})
      await dir.removeEntry(String(index)).catch(() => {})
      throw err
    }
  }

  private keepInMemory(index: number, buf: Uint8Array) {
    pieceCache.stats.memFallbacks++
    this.mem.set(index, buf)
    window.setTimeout(() => {
      if (!this.mem.delete(index)) return
      const l = this.entry.live
      if (l?.Piece && !this.torrent.destroyed) unverifyPiece(this.torrent, index, l.Piece)
    }, MEM_FALLBACK_MS)
  }

  get(index: number, opts: GetOpts | Cb<Uint8Array>, cb?: Cb<Uint8Array>): void {
    if (typeof opts === "function") return this.get(index, null, opts)
    const done = cb ?? (() => {})
    const off = opts?.offset ?? 0
    const mem = this.mem.get(index)
    if (mem) {
      const len = opts?.length ?? mem.length - off
      queueMicrotask(() => done(null, mem.subarray(off, off + len)))
      return
    }
    const live = this.entry.live
    if (live) live.reading.set(index, (live.reading.get(index) ?? 0) + 1)
    void (async () => {
      await this.ready
      if (this.closed) throw new Error("store closed")
      const dir = this.dir()
      if (!dir) throw new Error("no dir")
      const f = await (await dir.getFileHandle(String(index))).getFile()
      const len = opts?.length ?? f.size - off
      const b = new Uint8Array(await f.slice(off, off + len).arrayBuffer())
      if (b.length !== len) throw new Error(`short read ${b.length}/${len}`)
      return b
    })().then(
      (b) => {
        this.unread(live, index)
        done(null, b)
      },
      (err) => {
        this.unread(live, index)
        // 已校验却读不到（被删除）：数据不在了，让 WebTorrent 重新下载
        if (this.torrent.ready && !this.torrent.destroyed && this.torrent.bitfield?.get(index) && !this.closed) {
          this.entry.pieces.delete(index)
          queueMicrotask(() => {
            const l = this.entry.live
            if (l?.Piece) unverifyPiece(this.torrent, index, l.Piece)
          })
        }
        done(new Error(`Chunk not found: ${String((err as Error)?.message ?? err)}`))
      },
    )
  }

  private unread(live: Entry["live"], index: number) {
    if (!live) return
    const n = (live.reading.get(index) ?? 1) - 1
    if (n <= 0) live.reading.delete(index)
    else live.reading.set(index, n)
  }

  close(cb: Cb = () => {}) {
    if (this.closed) return queueMicrotask(() => cb(null))
    this.closed = true
    this.mem.clear()
    void pieceCache
      .detach(this.entry)
      .catch(() => {})
      .then(() => cb(null))
  }

  /** 不再由会话调用（离开播放页保留数据）；只在 WebTorrent 主动要求时删除整个目录 */
  destroy(cb: Cb = () => {}) {
    this.closed = true
    this.mem.clear()
    const key = this.lease.key
    void (async () => {
      await pieceCache.detach(this.entry)
      pieceCache.entries.delete(key)
      const base = await (await navigator.storage.getDirectory()).getDirectoryHandle("ot-pieces", { create: true })
      await base.removeEntry(key, { recursive: true }).catch(() => {})
    })().then(
      () => cb(null),
      () => cb(null),
    )
  }
}
