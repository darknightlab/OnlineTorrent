import { cn } from "@/lib/utils"

export function Skeleton({ className }: { className?: string }) {
  return <div aria-hidden className={cn("animate-pulse rounded-md bg-muted", className)} />
}

/**
 * 一行文字的占位：className 给与真实文字相同的字号 / 行高（高度即一行文字的高度），
 * bar 给条的宽度（条高 0.75em，垂直居中）。
 */
export function SkeletonLine({ className, bar }: { className?: string; bar: string }) {
  return (
    <div aria-hidden className={className}>
      <span className={cn("inline-block h-[0.75em] animate-pulse rounded bg-muted align-middle", bar)} />
    </div>
  )
}
