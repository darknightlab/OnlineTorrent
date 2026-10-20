// EBML（Matroska 的二进制格式）最小实现：变长整数、元素头、数值 / 字符串。只覆盖读取内封字幕需要的部分。

/** Matroska 元素 ID（含长度标记位，与规范中的十六进制写法一致） */
export const ID = {
  EBML: 0x1a45dfa3,
  Segment: 0x18538067,
  SeekHead: 0x114d9b74,
  Seek: 0x4dbb,
  SeekID: 0x53ab,
  SeekPosition: 0x53ac,
  Info: 0x1549a966,
  TimestampScale: 0x2ad7b1,
  Duration: 0x4489,
  Tracks: 0x1654ae6b,
  TrackEntry: 0xae,
  TrackNumber: 0xd7,
  TrackType: 0x83,
  FlagDefault: 0x88,
  FlagForced: 0x55aa,
  CodecID: 0x86,
  CodecPrivate: 0x63a2,
  Language: 0x22b59c,
  LanguageIETF: 0x22b59d,
  Name: 0x536e,
  ContentEncodings: 0x6d80,
  ContentEncoding: 0x6240,
  ContentEncodingScope: 0x5032,
  ContentEncodingType: 0x5033,
  ContentCompression: 0x5034,
  ContentCompAlgo: 0x4254,
  ContentCompSettings: 0x4255,
  Attachments: 0x1941a469,
  AttachedFile: 0x61a7,
  FileName: 0x466e,
  FileMimeType: 0x4660,
  FileData: 0x465c,
  Cluster: 0x1f43b675,
  Timestamp: 0xe7,
  SimpleBlock: 0xa3,
  BlockGroup: 0xa0,
  Block: 0xa1,
  BlockDuration: 0x9b,
  Cues: 0x1c53bb6b,
  Tags: 0x1254c367,
  Chapters: 0x1043a770,
} as const

/** 大小字段全为 1：未知大小（直播式写法） */
export const UNKNOWN_SIZE = -1

/** 读 EBML 变长整数（大小字段，去掉长度标记位）。返回 [值, 字节数]；数据不够时返回 null */
export function readVint(b: Uint8Array, pos: number): [number, number] | null {
  if (pos >= b.length) return null
  const first = b[pos]
  if (first === 0) return [NaN, 0] // 非法（超过 8 字节）
  const len = Math.clz32(first) - 23 // 1..8
  if (pos + len > b.length) return null
  let v = first & (0xff >> len)
  let allOnes = v === 0xff >> len
  for (let i = 1; i < len; i++) {
    v = v * 256 + b[pos + i]
    if (b[pos + i] !== 0xff) allOnes = false
  }
  return [allOnes ? UNKNOWN_SIZE : v, len]
}

/** 读元素 ID（保留长度标记位）。返回 [ID, 字节数]；数据不够时返回 null */
export function readId(b: Uint8Array, pos: number): [number, number] | null {
  if (pos >= b.length) return null
  const first = b[pos]
  if (first === 0 || first < 0x10) return [NaN, 0] // ID 最多 4 字节
  const len = Math.clz32(first) - 23
  if (pos + len > b.length) return null
  let v = 0
  for (let i = 0; i < len; i++) v = v * 256 + b[pos + i]
  return [v, len]
}

export interface ElementHeader {
  id: number
  /** 数据长度；UNKNOWN_SIZE 表示未知 */
  size: number
  /** 头部长度（ID + 大小字段） */
  headerLen: number
}

/** 读元素头；数据不够返回 null，格式错误返回 NaN id */
export function readHeader(b: Uint8Array, pos: number): ElementHeader | null {
  const id = readId(b, pos)
  if (!id) return null
  if (!id[1]) return { id: NaN, size: 0, headerLen: 0 }
  const size = readVint(b, pos + id[1])
  if (!size) return null
  if (!size[1]) return { id: NaN, size: 0, headerLen: 0 }
  return { id: id[0], size: size[0], headerLen: id[1] + size[1] }
}

export function readUint(b: Uint8Array): number {
  let v = 0
  for (const x of b) v = v * 256 + x
  return v
}

export function readFloat(b: Uint8Array): number {
  const dv = new DataView(b.buffer, b.byteOffset, b.byteLength)
  if (b.length === 4) return dv.getFloat32(0)
  if (b.length === 8) return dv.getFloat64(0)
  return 0
}

const utf8 = new TextDecoder("utf-8")
export function readString(b: Uint8Array): string {
  let end = b.length
  while (end > 0 && b[end - 1] === 0) end--
  return utf8.decode(b.subarray(0, end))
}

/** 遍历一段已在内存中的子元素 */
export function* children(b: Uint8Array): Generator<{ id: number; data: Uint8Array }> {
  let pos = 0
  while (pos < b.length) {
    const h = readHeader(b, pos)
    if (!h || Number.isNaN(h.id) || h.size === UNKNOWN_SIZE) return
    const start = pos + h.headerLen
    const end = start + h.size
    if (end > b.length) return
    yield { id: h.id, data: b.subarray(start, end) }
    pos = end
  }
}
