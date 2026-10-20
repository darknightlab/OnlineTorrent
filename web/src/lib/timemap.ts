// 字节 → 播放时间的映射（进度条是时间轴，而 piece 是字节；码率不均时二者对不齐）。
// 由容器自己的索引得到（MP4 的 moov 样本表、MKV 的 Cues，见 player/timemap），分段线性插值。

export interface TimeMap {
  /** 文件内字节偏移（递增） */
  bytes: number[]
  /** 对应的媒体时间（秒，不减） */
  times: number[]
}

/** 由（偏移, 时间）点构造单调映射：按偏移排序、时间取前缀最大值、去掉重复偏移；点太多时抽稀 */
export function buildTimeMap(points: [number, number][], maxPoints = 4000): TimeMap | null {
  const ps = points.filter(([b, t]) => Number.isFinite(b) && Number.isFinite(t) && b >= 0 && t >= 0).sort((a, b) => a[0] - b[0] || a[1] - b[1])
  if (ps.length < 2) return null
  const bytes: number[] = []
  const times: number[] = []
  let tmax = 0
  for (const [b, t] of ps) {
    tmax = Math.max(tmax, t)
    if (bytes.length && bytes[bytes.length - 1] === b) {
      times[times.length - 1] = tmax
      continue
    }
    bytes.push(b)
    times.push(tmax)
  }
  if (bytes.length < 2) return null
  if (bytes.length > maxPoints) {
    const step = bytes.length / maxPoints
    const kb: number[] = []
    const kt: number[] = []
    for (let i = 0; i < maxPoints; i++) {
      const j = Math.floor(i * step)
      kb.push(bytes[j])
      kt.push(times[j])
    }
    kb.push(bytes[bytes.length - 1])
    kt.push(times[times.length - 1])
    return { bytes: kb, times: kt }
  }
  return { bytes, times }
}

/**
 * 字节比例（0…1，相对文件长度）→ 时间比例（0…1，相对时长）。
 * 第一个索引点之前（文件头 / moov）映射到 0，最后一个之后（moov 在末尾时）映射到时长。
 */
export function byteToTimeFraction(map: TimeMap, fileLength: number, duration: number): (byteFrac: number) => number {
  const { bytes, times } = map
  const n = bytes.length
  return (f) => {
    if (!(duration > 0) || !(fileLength > 0)) return f
    const b = f * fileLength
    let t: number
    if (b <= bytes[0]) t = bytes[0] > 0 ? times[0] * (b / bytes[0]) : times[0]
    else if (b >= bytes[n - 1]) {
      const rest = fileLength - bytes[n - 1]
      t = rest > 0 ? times[n - 1] + ((duration - times[n - 1]) * (b - bytes[n - 1])) / rest : duration
    } else {
      // 二分：bytes[lo] <= b < bytes[lo + 1]
      let lo = 0
      let hi = n - 1
      while (hi - lo > 1) {
        const mid = (lo + hi) >> 1
        if (bytes[mid] <= b) lo = mid
        else hi = mid
      }
      t = times[lo] + ((times[lo + 1] - times[lo]) * (b - bytes[lo])) / (bytes[lo + 1] - bytes[lo])
    }
    return Math.min(1, Math.max(0, t / duration))
  }
}
