// MP4 的字节 → 时间点：moov 里视频轨道的样本表（stco/co64 块偏移 + stsc 每块样本数 + stts 样本时长）。
// 每个块（chunk）一个点：块在文件中的偏移 → 块内第一个样本的解码时间。moov 在文件头或文件尾都可以
// （浏览器播放时本来就会读取它）；只读顶层 box 的头和 moov 本身（有上限）。

/** 读取文件 [start, end) */
export type ReadRange = (start: number, end: number) => Promise<Uint8Array>

/** moov 的读取上限（再大就不画时间映射了） */
const MAX_MOOV = 32 << 20

const u32 = (b: Uint8Array, p: number) => ((b[p] << 24) >>> 0) + (b[p + 1] << 16) + (b[p + 2] << 8) + b[p + 3]
const u64 = (b: Uint8Array, p: number) => u32(b, p) * 2 ** 32 + u32(b, p + 4)
const type = (b: Uint8Array, p: number) => String.fromCharCode(b[p], b[p + 1], b[p + 2], b[p + 3])

interface Box {
  type: string
  start: number // 数据起点（box 内，跳过头）
  end: number
}

function* boxes(b: Uint8Array, from: number, to: number): Generator<Box> {
  let p = from
  while (p + 8 <= to) {
    let size = u32(b, p)
    const t = type(b, p + 4)
    let hl = 8
    if (size === 1) {
      if (p + 16 > to) return
      size = u64(b, p + 8)
      hl = 16
    } else if (size === 0) size = to - p
    if (size < hl || p + size > to) return
    yield { type: t, start: p + hl, end: p + size }
    p += size
  }
}

const child = (b: Uint8Array, box: Box, t: string) => {
  for (const c of boxes(b, box.start, box.end)) if (c.type === t) return c
  return null
}

/** 找到并读出 moov（顶层 box 逐个读头） */
async function readMoov(read: ReadRange, size: number): Promise<Uint8Array | null> {
  let p = 0
  for (let n = 0; p + 8 <= size && n < 64; n++) {
    const h = await read(p, Math.min(size, p + 16))
    if (h.length < 8) return null
    let len = u32(h, 0)
    let hl = 8
    if (len === 1) {
      len = u64(h, 8)
      hl = 16
    } else if (len === 0) len = size - p
    if (len < hl) return null
    if (type(h, 4) === "moov") {
      if (len > MAX_MOOV) return null
      return read(p, p + len)
    }
    p += len
  }
  return null
}

export interface Mp4Points {
  /** [文件偏移, 秒] */
  points: [number, number][]
  duration: number
}

/** 解析 moov，返回视频轨道（没有则第一条有样本的轨道）的块级时间点 */
export function parseMoov(moov: Uint8Array): Mp4Points | null {
  const top = [...boxes(moov, 0, moov.length)][0]
  if (!top || top.type !== "moov") return null
  let best: Mp4Points | null = null
  for (const trak of boxes(moov, top.start, top.end)) {
    if (trak.type !== "trak") continue
    const r = parseTrak(moov, trak)
    if (!r) continue
    if (r.video) return r.p
    best ??= r.p
  }
  return best
}

function parseTrak(b: Uint8Array, trak: Box): { video: boolean; p: Mp4Points } | null {
  const mdia = child(b, trak, "mdia")
  if (!mdia) return null
  const mdhd = child(b, mdia, "mdhd")
  const hdlr = child(b, mdia, "hdlr")
  const stbl = (() => {
    const minf = child(b, mdia, "minf")
    return minf ? child(b, minf, "stbl") : null
  })()
  if (!mdhd || !stbl) return null
  const v = b[mdhd.start]
  const timescale = v === 1 ? u32(b, mdhd.start + 20) : u32(b, mdhd.start + 12)
  const mdur = v === 1 ? u64(b, mdhd.start + 24) : u32(b, mdhd.start + 16)
  if (!timescale) return null
  const video = !!hdlr && type(b, hdlr.start + 8) === "vide"

  // 编辑列表的起点（常见：B 帧导致的 composition 偏移）：解码时间减去它
  let shift = 0
  const edts = child(b, trak, "edts")
  const elst = edts ? child(b, edts, "elst") : null
  if (elst) {
    const ev = b[elst.start]
    const count = u32(b, elst.start + 4)
    let q = elst.start + 8
    for (let i = 0; i < count; i++) {
      const mediaTime = ev === 1 ? u64(b, q + 8) : u32(b, q + 4)
      if (mediaTime !== 0xffffffff && !(ev === 1 && mediaTime >= 2 ** 63)) {
        shift = mediaTime
        break
      }
      q += ev === 1 ? 20 : 12
    }
  }

  // stco / co64：块偏移
  const stco = child(b, stbl, "stco")
  const co64 = child(b, stbl, "co64")
  const offsets: number[] = []
  if (stco) {
    const n = u32(b, stco.start + 4)
    for (let i = 0; i < n; i++) offsets.push(u32(b, stco.start + 8 + 4 * i))
  } else if (co64) {
    const n = u32(b, co64.start + 4)
    for (let i = 0; i < n; i++) offsets.push(u64(b, co64.start + 8 + 8 * i))
  } else return null
  // stsc：块 → 每块样本数（按区间）
  const stsc = child(b, stbl, "stsc")
  if (!stsc) return null
  const sc: { first: number; per: number }[] = []
  {
    const n = u32(b, stsc.start + 4)
    for (let i = 0; i < n; i++) sc.push({ first: u32(b, stsc.start + 8 + 12 * i), per: u32(b, stsc.start + 12 + 12 * i) })
  }
  // stts：样本时长（按区间）
  const stts = child(b, stbl, "stts")
  if (!stts) return null
  const tt: { count: number; delta: number }[] = []
  {
    const n = u32(b, stts.start + 4)
    for (let i = 0; i < n; i++) tt.push({ count: u32(b, stts.start + 8 + 8 * i), delta: u32(b, stts.start + 12 + 8 * i) })
  }
  // 逐块：块内第一个样本的解码时间
  const points: [number, number][] = []
  let time = 0 // 下一个样本的解码时间（timescale）
  let ti = 0 // stts 区间
  let tleft = tt[0]?.count ?? 0
  let si = 0
  const advance = (k: number) => {
    // 前进 k 个样本
    while (k > 0 && ti < tt.length) {
      const n = Math.min(k, tleft)
      time += n * tt[ti].delta
      tleft -= n
      k -= n
      if (tleft === 0) {
        ti++
        tleft = tt[ti]?.count ?? 0
      }
    }
  }
  for (let c = 0; c < offsets.length; c++) {
    while (si + 1 < sc.length && sc[si + 1].first <= c + 1) si++
    const per = sc[si]?.per ?? 0
    points.push([offsets[c], Math.max(0, time - shift) / timescale])
    advance(per)
  }
  return points.length ? { video, p: { points, duration: mdur / timescale } } : null
}

/** 读取并解析 MP4 的索引（没有 moov / 超过上限 / 格式错误时为 null） */
export async function mp4Points(read: ReadRange, size: number): Promise<Mp4Points | null> {
  const moov = await readMoov(read, size)
  return moov ? parseMoov(moov) : null
}
