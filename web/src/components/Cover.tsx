import { useState } from "react"
import { Clapperboard } from "lucide-react"
import { cn } from "@/lib/utils"

/** 番剧封面；无图或加载失败时显示品牌色占位 */
export function Cover({
  src,
  title,
  className,
  eager,
  position,
  natural,
  onRatio,
}: {
  src: string | null | undefined
  title: string
  className?: string
  eager?: boolean
  /** object-position 类名，例如横向卡片里竖版海报偏上裁切 */
  position?: string
  /**
   * 按图片原始比例显示（宽度由 className 决定，高度自动；不裁切）。
   * 加载完成前 / 无图时按 bgm 封面常见比例 1:√2（400×566）占位。
   */
  natural?: boolean
  /** 图片加载后回报原始宽高比（宽 / 高） */
  onRatio?: (ratio: number) => void
}) {
  const [failed, setFailed] = useState(false)
  const [loaded, setLoaded] = useState(false)
  const show = src && !failed
  if (natural)
    return (
      <div className={cn("relative overflow-hidden bg-muted", !(show && loaded) && "aspect-[1/1.414]", className)}>
        {show ? (
          <img
            src={src}
            alt=""
            loading={eager ? "eager" : "lazy"}
            decoding="async"
            referrerPolicy="no-referrer"
            onLoad={() => setLoaded(true)}
            onError={() => setFailed(true)}
            className={cn("block h-auto w-full transition-opacity duration-300", loaded ? "opacity-100" : "absolute inset-0 opacity-0")}
          />
        ) : (
          <Placeholder title={title} />
        )}
      </div>
    )
  return (
    <div className={cn("relative overflow-hidden bg-muted", className)}>
      {show ? (
        <img
          src={src}
          alt=""
          loading={eager ? "eager" : "lazy"}
          decoding="async"
          referrerPolicy="no-referrer"
          onLoad={(e) => {
            setLoaded(true)
            const im = e.currentTarget
            if (onRatio && im.naturalWidth > 0 && im.naturalHeight > 0) onRatio(im.naturalWidth / im.naturalHeight)
          }}
          onError={() => setFailed(true)}
          className={cn("h-full w-full object-cover transition-opacity duration-300", position, loaded ? "opacity-100" : "opacity-0")}
        />
      ) : (
        <Placeholder title={title} />
      )}
    </div>
  )
}

function Placeholder({ title }: { title: string }) {
  const ch = Array.from(title.replace(/[\s[\]【】()（）]/g, ""))[0] ?? "?"
  return (
    <div aria-hidden className="absolute inset-0 flex flex-col items-center justify-center bg-gradient-to-br from-primary/25 via-muted to-accent/25">
      <span className="text-4xl font-bold text-foreground/25">{ch}</span>
      <Clapperboard className="mt-1 h-4 w-4 text-foreground/25" />
    </div>
  )
}
