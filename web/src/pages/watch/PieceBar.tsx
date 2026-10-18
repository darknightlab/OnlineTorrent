import { memo } from "react"

/** 分段数：按当前文件的 piece 数取（每段约 1 个 piece），上限避免过多 DOM */
export const MAX_SEGMENTS = 400

export function segmentCount(fileLength: number, pieceLength: number) {
  if (!fileLength || !pieceLength) return 1
  // +1：文件首尾可能各跨半个 piece
  return Math.max(1, Math.min(MAX_SEGMENTS, Math.ceil(fileLength / pieceLength) + 1))
}

/**
 * 进度条内的 piece 可用性图层（类似 YouTube 的已缓冲条，但按当前文件的 piece 分段）：
 * 已下载 = 较亮的白色，部分下载 = 较淡的白色，未下载 = 透明（露出轨道背景）。
 * 相邻同状态的段合并为一个元素。
 */
export const PieceLayer = memo(function PieceLayer({ segments }: { segments: Uint8Array }) {
  const n = segments.length
  const runs: [number, number, number][] = []
  for (let i = 0; i < n; ) {
    const v = segments[i]
    let j = i + 1
    while (j < n && segments[j] === v) j++
    if (v) runs.push([i, j, v])
    i = j
  }
  return (
    <div aria-hidden className="pointer-events-none absolute inset-0">
      {runs.map(([a, b, v]) => (
        <span
          key={a}
          className={v === 2 ? "absolute inset-y-0 bg-white/55" : "absolute inset-y-0 bg-white/30"}
          style={{ left: `${(a / n) * 100}%`, width: `${((b - a) / n) * 100}%` }}
        />
      ))}
    </div>
  )
})

/** 已下载比例（0~1） */
export function segmentsDone(segments: Uint8Array) {
  if (!segments.length) return 0
  let done = 0
  for (const v of segments) if (v === 2) done++
  return done / segments.length
}
