// Matroska 头部解析（随机读取）：Info（时间单位）、Tracks（字幕轨道）、Attachments（字体位置，数据按需读取）。
// 只读到第一个 Cluster 为止；Tracks / Attachments 不在 Cluster 之前时（少见）按 SeekHead 的位置读取。
import { ID, UNKNOWN_SIZE, children, readFloat, readHeader, readString, readUint } from "./ebml.ts"

/** 读取文件 [start, end) 的字节（可能触发下载） */
export type ReadRange = (start: number, end: number) => Promise<Uint8Array>

export type SubCodec = "ass" | "ssa" | "srt"

export interface ContentEncoding {
  /** 0 = zlib，3 = 头部剥离 */
  algo: number
  /** 头部剥离时补回的字节 */
  settings: Uint8Array | null
  /** 作用范围：1 = 帧数据，2 = CodecPrivate */
  scope: number
}

export interface SubTrack {
  number: number
  codec: SubCodec
  /** ASS/SSA 文件头（[Script Info] [V4+ Styles] … [Events] Format）；SRT 为空 */
  header: string
  language: string
  languageIETF: string
  name: string
  isDefault: boolean
  forced: boolean
  encodings: ContentEncoding[]
}

export interface AttachmentRef {
  name: string
  mime: string
  /** FileData 在文件中的位置与长度 */
  offset: number
  size: number
}

export interface MkvHeader {
  /** 每个时间戳单位的纳秒数（默认 1 000 000 = 1 ms） */
  timestampScale: number
  durationMs: number
  /** 所有轨道号 → 类型（17 = 字幕） */
  trackTypes: Map<number, number>
  subtitles: SubTrack[]
  attachments: AttachmentRef[]
}

const CODECS: Record<string, SubCodec> = { "S_TEXT/ASS": "ass", S_ASS: "ass", "S_TEXT/SSA": "ssa", S_SSA: "ssa", "S_TEXT/UTF8": "srt" }

/** 带 64 KiB 块缓存的随机读取 */
class Reader {
  private cache = new Map<number, Uint8Array>()
  private readonly read: ReadRange
  readonly size: number
  constructor(read: ReadRange, size: number) {
    this.read = read
    this.size = size
  }
  private static B = 64 << 10
  async get(start: number, end: number): Promise<Uint8Array> {
    end = Math.min(end, this.size)
    if (end <= start) return new Uint8Array(0)
    if (end - start > Reader.B) return this.read(start, end)
    const out = new Uint8Array(end - start)
    for (let blk = Math.floor(start / Reader.B); blk * Reader.B < end; blk++) {
      let b = this.cache.get(blk)
      if (!b) {
        b = await this.read(blk * Reader.B, Math.min((blk + 1) * Reader.B, this.size))
        if (this.cache.size > 64) this.cache.clear()
        this.cache.set(blk, b)
      }
      const bs = blk * Reader.B
      const s = Math.max(start, bs)
      const e = Math.min(end, bs + b.length)
      out.set(b.subarray(s - bs, e - bs), s - start)
    }
    return out
  }
  async header(pos: number) {
    const b = await this.get(pos, pos + 12)
    const h = readHeader(b, 0)
    if (!h || Number.isNaN(h.id)) throw new Error(`EBML 元素头损坏（${pos}）`)
    return h
  }
}

export class NotMatroskaError extends Error {}

/** 解析头部。只有字幕轨道与附件信息；附件数据不读取（见 readAttachment） */
export async function parseMkvHeader(read: ReadRange, fileSize: number): Promise<MkvHeader> {
  const r = new Reader(read, fileSize)
  const ebml = await r.header(0)
  if (ebml.id !== ID.EBML) throw new NotMatroskaError("不是 Matroska 文件")
  const segPos = ebml.headerLen + ebml.size
  const seg = await r.header(segPos)
  if (seg.id !== ID.Segment) throw new NotMatroskaError("找不到 Segment")
  const segData = segPos + seg.headerLen
  const segEnd = seg.size === UNKNOWN_SIZE ? fileSize : Math.min(fileSize, segData + seg.size)
  const out: MkvHeader = { timestampScale: 1_000_000, durationMs: 0, trackTypes: new Map(), subtitles: [], attachments: [] }
  const seek = new Map<number, number>() // 元素 ID → 绝对位置
  let gotTracks = false
  let gotAttachments = false
  let gotInfo = false

  const parseInfo = (b: Uint8Array) => {
    gotInfo = true
    let dur = 0
    for (const c of children(b)) {
      if (c.id === ID.TimestampScale) out.timestampScale = readUint(c.data) || 1_000_000
      else if (c.id === ID.Duration) dur = readFloat(c.data)
    }
    out.durationMs = (dur * out.timestampScale) / 1e6
  }
  const parseTracks = async (b: Uint8Array) => {
    gotTracks = true
    for (const t of children(b)) if (t.id === ID.TrackEntry) await parseTrack(t.data, out)
  }
  const parseAttachments = async (pos: number, size: number) => {
    gotAttachments = true
    let p = pos
    const end = pos + size
    while (p < end) {
      const h = await r.header(p)
      const data = p + h.headerLen
      if (h.id === ID.AttachedFile) {
        const a: AttachmentRef = { name: "", mime: "", offset: 0, size: 0 }
        let q = data
        while (q < data + h.size) {
          const c = await r.header(q)
          const cd = q + c.headerLen
          if (c.id === ID.FileName) a.name = readString(await r.get(cd, cd + c.size))
          else if (c.id === ID.FileMimeType) a.mime = readString(await r.get(cd, cd + c.size))
          else if (c.id === ID.FileData) {
            a.offset = cd
            a.size = c.size
          }
          q = cd + c.size
        }
        if (a.size > 0) out.attachments.push(a)
      }
      if (h.size === UNKNOWN_SIZE) break
      p = data + h.size
    }
  }

  // Segment 的子元素：读到第一个 Cluster 为止
  let pos = segData
  while (pos < segEnd) {
    const h = await r.header(pos)
    const data = pos + h.headerLen
    if (h.id === ID.Cluster || h.size === UNKNOWN_SIZE) break
    if (h.id === ID.SeekHead) {
      for (const s of children(await r.get(data, data + h.size))) {
        if (s.id !== ID.Seek) continue
        let id = 0
        let p = -1
        for (const c of children(s.data)) {
          if (c.id === ID.SeekID) id = readUint(c.data)
          else if (c.id === ID.SeekPosition) p = readUint(c.data)
        }
        if (id && p >= 0) seek.set(id, segData + p)
      }
    } else if (h.id === ID.Info) parseInfo(await r.get(data, data + h.size))
    else if (h.id === ID.Tracks) await parseTracks(await r.get(data, data + h.size))
    else if (h.id === ID.Attachments) await parseAttachments(data, h.size)
    pos = data + h.size
  }
  // Cluster 之后的（SeekHead 指向）
  const at = async (id: number) => {
    const p = seek.get(id)
    if (p === undefined || p >= fileSize) return null
    const h = await r.header(p)
    return h.id === id ? { data: p + h.headerLen, size: h.size } : null
  }
  if (!gotInfo) {
    const e = await at(ID.Info)
    if (e) parseInfo(await r.get(e.data, e.data + e.size))
  }
  if (!gotTracks) {
    const e = await at(ID.Tracks)
    if (e) await parseTracks(await r.get(e.data, e.data + e.size))
  }
  if (!gotAttachments) {
    const e = await at(ID.Attachments)
    if (e) await parseAttachments(e.data, e.size)
  }
  return out
}

async function parseTrack(b: Uint8Array, out: MkvHeader) {
  let num = 0
  let type = 0
  let codecId = ""
  let priv: Uint8Array | null = null
  const t: Omit<SubTrack, "number" | "codec" | "header"> = { language: "eng", languageIETF: "", name: "", isDefault: true, forced: false, encodings: [] }
  for (const c of children(b)) {
    switch (c.id) {
      case ID.TrackNumber:
        num = readUint(c.data)
        break
      case ID.TrackType:
        type = readUint(c.data)
        break
      case ID.CodecID:
        codecId = readString(c.data)
        break
      case ID.CodecPrivate:
        priv = c.data
        break
      case ID.Language:
        t.language = readString(c.data)
        break
      case ID.LanguageIETF:
        t.languageIETF = readString(c.data)
        break
      case ID.Name:
        t.name = readString(c.data)
        break
      case ID.FlagDefault:
        t.isDefault = readUint(c.data) !== 0
        break
      case ID.FlagForced:
        t.forced = readUint(c.data) !== 0
        break
      case ID.ContentEncodings:
        for (const e of children(c.data)) if (e.id === ID.ContentEncoding) t.encodings.push(parseEncoding(e.data))
        break
    }
  }
  if (!num) return
  out.trackTypes.set(num, type)
  const codec = CODECS[codecId]
  if (type !== 17 || !codec) return
  let header = ""
  if (priv) {
    const raw = await decodeContent(priv, t.encodings, 2)
    header = readString(raw)
  }
  out.subtitles.push({ number: num, codec, header, ...t })
}

function parseEncoding(b: Uint8Array): ContentEncoding {
  const e: ContentEncoding = { algo: -1, settings: null, scope: 1 }
  let type = 0
  for (const c of children(b)) {
    if (c.id === ID.ContentEncodingScope) e.scope = readUint(c.data)
    else if (c.id === ID.ContentEncodingType) type = readUint(c.data)
    else if (c.id === ID.ContentCompression) {
      e.algo = 0
      for (const x of children(c.data)) {
        if (x.id === ID.ContentCompAlgo) e.algo = readUint(x.data)
        else if (x.id === ID.ContentCompSettings) e.settings = x.data
      }
    }
  }
  if (type !== 0) e.algo = -2 // 加密：不支持
  return e
}

/** 按轨道的 ContentEncodings 还原数据（scope：1 = 帧，2 = CodecPrivate）。zlib 用浏览器自带的 DecompressionStream */
export async function decodeContent(data: Uint8Array, encodings: ContentEncoding[], scope: number): Promise<Uint8Array> {
  let out = data
  // 编码按写入顺序的逆序还原
  for (const e of [...encodings].reverse()) {
    if (!(e.scope & scope)) continue
    if (e.algo === 3 && e.settings) {
      const b = new Uint8Array(e.settings.length + out.length)
      b.set(e.settings)
      b.set(out, e.settings.length)
      out = b
    } else if (e.algo === 0) {
      out = await inflate(out)
    } else {
      throw new Error(`不支持的字幕编码（ContentCompAlgo ${e.algo}）`)
    }
  }
  return out
}

/** 读取一个附件（字体）的数据 */
export function readAttachment(read: ReadRange, a: AttachmentRef) {
  return read(a.offset, a.offset + a.size)
}

/** 附件是否是字体（按 MIME 与扩展名） */
export function isFont(a: AttachmentRef) {
  return /font|truetype|opentype|sfnt|woff/i.test(a.mime) || /\.(ttf|otf|ttc|otc|woff2?)$/i.test(a.name)
}

/** zlib 解压（浏览器与 Node 都有 DecompressionStream） */
async function inflate(data: Uint8Array): Promise<Uint8Array> {
  const input = data
  const rs = new ReadableStream<Uint8Array>({
    start(c) {
      c.enqueue(input)
      c.close()
    },
  }).pipeThrough(new DecompressionStream("deflate") as unknown as TransformStream<Uint8Array, Uint8Array>)
  const parts: Uint8Array[] = []
  let n = 0
  const r = rs.getReader()
  for (;;) {
    const { value, done } = await r.read()
    if (done) break
    parts.push(value)
    n += value.length
  }
  const out = new Uint8Array(n)
  let o = 0
  for (const p of parts) {
    out.set(p, o)
    o += p.length
  }
  return out
}
