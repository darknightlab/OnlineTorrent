/**
 * 将文件的 piece 区间映射为 n 段进度（移植自旧版 200 段进度条）。
 * 0 = 未下载，1 = 部分（含正在下载的 piece），2 = 完成。
 */
export function computeSegments(
  start: number,
  end: number,
  n: number,
  isDone: (piece: number) => boolean,
  isPartial: (piece: number) => boolean,
): Uint8Array {
  const out = new Uint8Array(n)
  const total = end - start + 1
  if (total <= 0) return out
  const per = total / n
  for (let k = 0; k < n; k++) {
    const from = start + Math.floor(per * k)
    const to = Math.max(from, start + Math.ceil(per * (k + 1)) - 1)
    let done = 0
    let any = false
    let count = 0
    for (let p = from; p <= Math.min(to, end); p++) {
      count++
      if (isDone(p)) {
        done++
        any = true
      } else if (!any && isPartial(p)) any = true
    }
    out[k] = count > 0 && done === count ? 2 : any ? 1 : 0
  }
  return out
}
