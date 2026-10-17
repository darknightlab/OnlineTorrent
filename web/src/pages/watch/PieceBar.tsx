import { memo } from "react"
import { cn } from "@/lib/utils"

export const SEGMENTS = 200

/** 200 段 piece 进度条：绿=已完成，蓝=部分/下载中 */
export const PieceBar = memo(function PieceBar({ segments, className }: { segments: Uint8Array; className?: string }) {
  let done = 0
  for (const v of segments) if (v === 2) done++
  return (
    <div
      role="progressbar"
      aria-label="分段下载进度"
      aria-valuemin={0}
      aria-valuemax={100}
      aria-valuenow={Math.round((done / segments.length) * 100)}
      className={cn("flex h-1.5 w-full overflow-hidden bg-piece-empty", className)}
    >
      {Array.from(segments, (v, i) => (
        <span key={i} className={cn("h-full flex-1", v === 2 ? "bg-primary" : v === 1 ? "bg-accent" : "")} />
      ))}
    </div>
  )
})
