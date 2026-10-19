/** 分段数：按当前文件的 piece 数取（每段约 1 个 piece），上限避免过长的渐变 */
export const MAX_SEGMENTS = 400

export function segmentCount(fileLength: number, pieceLength: number) {
  if (!fileLength || !pieceLength) return 1
  // +1：文件首尾可能各跨半个 piece
  return Math.max(1, Math.min(MAX_SEGMENTS, Math.ceil(fileLength / pieceLength) + 1))
}

/** 进度条 piece 图层的颜色（CSS 变量定义在 pages/watch/artplayer.css） */
export const PIECE_COLORS = ["transparent", "var(--ot-piece-partial)", "var(--ot-piece-done)"] as const

/**
 * 进度条内的 piece 可用性图层（类似 YouTube 的已缓冲条，但按当前文件的 piece 分段）：
 * 0 未下载 = 透明（露出轨道），1 部分下载，2 已下载。相邻同状态的段合并，生成一个硬边的 linear-gradient。
 */
export function segmentsGradient(segments: Uint8Array): string {
  const n = segments.length
  if (!n) return "none"
  const stops: string[] = []
  for (let i = 0; i < n; ) {
    const v = segments[i]
    let j = i + 1
    while (j < n && segments[j] === v) j++
    const c = PIECE_COLORS[v] ?? PIECE_COLORS[0]
    stops.push(`${c} ${pct(i / n)} ${pct(j / n)}`)
    i = j
  }
  if (stops.length === 1 && segments[0] === 0) return "none"
  return `linear-gradient(to right, ${stops.join(", ")})`
}

const pct = (x: number) => `${+(x * 100).toFixed(3)}%`
