import { cn } from "@/lib/utils"

/**
 * 播放器里的加载圈：与 ArtPlayer 缓冲时的完全一致（40px、主色、0.9 秒一圈，见 pages/watch/artplayer.css 的 .ot-spin），
 * 播放器外壳的「正在请求播放 / 正在获取种子信息」等状态也用它，前后看起来是同一个。
 */
export function PlayerSpinner({ className }: { className?: string }) {
  return (
    <span aria-hidden className={cn("inline-flex h-10 w-10 animate-[spin_0.9s_linear_infinite] text-[#4caf50]", className)}>
      <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth={2.25} strokeLinecap="round" className="h-full w-full">
        <path d="M21 12a9 9 0 1 1-6.219-8.56" />
      </svg>
    </span>
  )
}
