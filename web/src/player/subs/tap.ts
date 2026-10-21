// 从视频本身读取的字节流里取字幕块（Matroska 的字幕与音视频交错存放在各个 Cluster 里）。
//
// 播放器（<video> 经 Service Worker 流）读到哪里，就从哪里解析：不额外下载、不重复读取，
// 拖动后浏览器从新位置发起的请求同样被解析（先在流里找 Cluster 的起点，见 resync）。
// 视频总是先读到一个 Cluster 才播放其中的时间段，而字幕块放在其开始时间所在的 Cluster，所以显示前就已拿到。
//
// 增量解析：只缓存元素头和字幕块本身，音视频块的数据直接跳过（不复制）。
import { ID, UNKNOWN_SIZE, readHeader, readUint, readVint } from "./ebml.ts"

export interface RawBlock {
  track: number
  /** 绝对时间（毫秒） */
  timeMs: number
  durationMs: number
  /** 原始帧数据（ContentEncodings 未还原） */
  data: Uint8Array
}

export interface TapOptions {
  /** 该轨道是否为（我们处理的）字幕轨道；头部尚未解析时返回 undefined */
  isSub: (track: number) => boolean | undefined
  /** 时间单位（纳秒 / 时间戳），默认 1 ms */
  timestampScale: () => number
  onBlock: (b: RawBlock) => void
  /** 流里经过的 Tracks 元素（从文件开头读时） */
  onTracks?: (data: Uint8Array) => void
  /** 经过的 Cluster：起点（文件偏移）与时间（毫秒）——没有 Cues 时用于进度条的时间映射 */
  onCluster?: (pos: number, timeMs: number) => void
}

const MAX_SUB_BLOCK = 1 << 20
const CRC32 = 0xbf
const VOID = 0xec
/** 单个 Cluster 的合理上限（用于在任意位置找 Cluster 起点时排除误判） */
const MAX_CLUSTER = 256 << 20

type Frame = { kind: "segment" | "cluster" | "group"; end: number }

/**
 * 一个视频读取流对应一个 Tap。start 为该流在文件中的起始偏移：0 表示从文件头解析，
 * 否则先找到下一个 Cluster 再开始。
 */
export class ClusterTap {
  private o: TapOptions
  private pos: number // 下一个未处理字节的绝对偏移
  private buf: Uint8Array = new Uint8Array(0) // pos 处开始的未处理数据
  private skip = 0 // 要跳过的字节（不缓存）
  private stack: Frame[] = []
  private clusterTs = 0
  private clusterPos = -1
  private syncing: boolean
  // BlockGroup 内：Block 与 BlockDuration 可能任意顺序
  private group: { block: RawBlock | null; duration: number | null } | null = null
  private collect: { id: number; size: number } | null = null
  /** 解析出错后停止（不影响视频本身） */
  dead = false

  /** 这个解析器从哪里开始读（文件偏移） */
  readonly start: number

  constructor(start: number, o: TapOptions) {
    this.o = o
    this.start = start
    this.pos = start
    this.syncing = start !== 0
  }

  /** 下一块输入应从的文件偏移（输入必须连续） */
  get next(): number {
    return this.pos + this.buf.length
  }

  push(chunk: Uint8Array) {
    if (this.dead) return
    try {
      this.feed(chunk)
    } catch {
      this.dead = true
    }
  }

  private feed(chunk: Uint8Array) {
    let c = chunk
    if (this.skip > 0) {
      const n = Math.min(this.skip, c.length)
      this.skip -= n
      this.pos += n
      c = c.subarray(n)
      if (!c.length) return
    }
    this.buf = this.buf.length ? concat(this.buf, c) : c
    let off = 0
    for (;;) {
      if (this.syncing) {
        const at = this.findCluster(off)
        if (at < 0) {
          // 保留末尾 11 字节（可能是被截断的 Cluster 头）
          const keep = Math.min(this.buf.length - off, 11)
          this.advance(this.buf.length - keep - off)
          off = this.buf.length - keep
          break
        }
        this.advance(at - off)
        off = at
        this.syncing = false
      }
      // 退出已结束的容器
      while (this.stack.length && this.pos >= this.stack[this.stack.length - 1].end) this.leave()
      if (this.collect) {
        if (this.buf.length - off < this.collect.size) break
        const data = this.buf.subarray(off, off + this.collect.size)
        this.element(this.collect.id, data)
        this.advance(this.collect.size)
        off += this.collect.size
        this.collect = null
        continue
      }
      const h = readHeader(this.buf, off)
      if (!h) break // 需要更多数据
      if (Number.isNaN(h.id)) {
        this.resync()
        off = 0
        continue
      }
      const dataPos = this.pos + h.headerLen
      if (h.id === ID.Segment) {
        this.enter("segment", h.size, dataPos)
        this.advance(h.headerLen)
        off += h.headerLen
        continue
      }
      if (h.id === ID.Cluster) {
        // 未知大小的 Cluster 一直延续到下一个顶层元素
        while (this.stack.length && this.stack[this.stack.length - 1].kind !== "segment") this.leave()
        this.clusterTs = 0
        this.clusterPos = this.pos
        this.enter("cluster", h.size, dataPos)
        this.advance(h.headerLen)
        off += h.headerLen
        continue
      }
      const inCluster = this.top() === "cluster" || this.top() === "group"
      if (h.id === ID.BlockGroup && this.top() === "cluster") {
        this.group = { block: null, duration: null }
        this.enter("group", h.size, dataPos)
        this.advance(h.headerLen)
        off += h.headerLen
        continue
      }
      if (h.size === UNKNOWN_SIZE) {
        this.resync()
        off = 0
        continue
      }
      let want = false
      if (inCluster && (h.id === ID.Timestamp || h.id === ID.BlockDuration)) want = true
      else if (inCluster && (h.id === ID.SimpleBlock || h.id === ID.Block)) {
        // 先看轨道号：不是字幕就跳过整块（不缓存）
        const tn = readVint(this.buf, off + h.headerLen)
        if (!tn) break
        const isSub = this.o.isSub(tn[0])
        want = isSub === true && h.size <= MAX_SUB_BLOCK
      } else if (h.id === ID.Tracks && this.top() === "segment" && this.o.onTracks && h.size <= 4 << 20) want = true
      this.advance(h.headerLen)
      off += h.headerLen
      if (want) {
        this.collect = { id: h.id, size: h.size }
        continue
      }
      // 跳过数据：缓冲里有的直接丢，其余记到 skip
      const inBuf = Math.min(h.size, this.buf.length - off)
      this.advance(inBuf)
      off += inBuf
      if (inBuf < h.size) {
        this.skip = h.size - inBuf
        this.buf = new Uint8Array(0)
        return
      }
    }
    // 只保留未处理部分（复制出来，避免持有大块视频数据）
    this.buf = off < this.buf.length ? this.buf.slice(off) : new Uint8Array(0)
    if (this.buf.length > MAX_SUB_BLOCK + 64) this.resync()
  }

  /** 已处理 n 字节（buf 的游标由调用方维护） */
  private advance(n: number) {
    this.pos += n
  }

  private top() {
    return this.stack.length ? this.stack[this.stack.length - 1].kind : null
  }

  private enter(kind: Frame["kind"], size: number, dataPos: number) {
    this.stack.push({ kind, end: size === UNKNOWN_SIZE ? Infinity : dataPos + size })
  }

  private leave() {
    const f = this.stack.pop()
    if (f?.kind === "group") this.flushGroup()
  }

  /** 解析错误：放弃当前状态，重新找 Cluster */
  private resync() {
    this.clusterPos = -1
    this.stack = []
    this.group = null
    this.collect = null
    this.syncing = true
  }

  private element(id: number, data: Uint8Array) {
    switch (id) {
      case ID.Timestamp:
        this.clusterTs = readUint(data)
        if (this.clusterPos >= 0) this.o.onCluster?.(this.clusterPos, (this.clusterTs * this.o.timestampScale()) / 1e6)
        this.clusterPos = -1
        break
      case ID.BlockDuration:
        if (this.group) this.group.duration = readUint(data)
        break
      case ID.Tracks:
        this.o.onTracks?.(data.slice())
        break
      case ID.SimpleBlock:
      case ID.Block: {
        const b = this.block(data)
        if (!b) break
        if (id === ID.Block && this.group) this.group.block = b
        else this.o.onBlock(b)
        break
      }
    }
  }

  private block(data: Uint8Array): RawBlock | null {
    const tn = readVint(data, 0)
    if (!tn || data.length < tn[1] + 3) return null
    const rel = (data[tn[1]] << 24) >> 16 | data[tn[1] + 1] // int16
    const flags = data[tn[1] + 2]
    if (flags & 0x06) return null // 分帧（lacing）：字幕不会用
    const scale = this.o.timestampScale() / 1e6
    return { track: tn[0], timeMs: (this.clusterTs + rel) * scale, durationMs: 0, data: data.slice(tn[1] + 3) }
  }

  private flushGroup() {
    const g = this.group
    this.group = null
    if (!g?.block) return
    if (g.duration !== null) g.block.durationMs = g.duration * (this.o.timestampScale() / 1e6)
    this.o.onBlock(g.block)
  }

  /** 在 buf[from..] 中找可信的 Cluster 起点：ID + 合理大小 + 第一个子元素是 Timestamp */
  private findCluster(from: number): number {
    const b = this.buf
    for (let i = from; i + 4 <= b.length; i++) {
      if (b[i] !== 0x1f || b[i + 1] !== 0x43 || b[i + 2] !== 0xb6 || b[i + 3] !== 0x75) continue
      const h = readHeader(b, i)
      if (!h) return i // 头部不完整：交给下一轮（保留数据）
      if (Number.isNaN(h.id) || (h.size !== UNKNOWN_SIZE && h.size > MAX_CLUSTER)) continue
      // 第一个子元素是 Timestamp；之前可以有 CRC-32（ffmpeg 默认写）或 Void
      let q = i + h.headerLen
      let c = readHeader(b, q)
      while (c && (c.id === CRC32 || c.id === VOID) && c.size >= 0 && c.size <= 64) {
        q += c.headerLen + c.size
        c = readHeader(b, q)
      }
      if (!c) return i
      if (c.id === ID.Timestamp && c.size > 0 && c.size <= 8) {
        // 拖动到中途：所在 Segment 的边界未知，当作无限
        this.stack = [{ kind: "segment", end: Infinity }]
        return i
      }
    }
    return -1
  }
}

function concat(a: Uint8Array, b: Uint8Array) {
  const out = new Uint8Array(a.length + b.length)
  out.set(a)
  out.set(b, a.length)
  return out
}
