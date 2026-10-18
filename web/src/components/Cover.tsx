import { useState } from "react"
import { Clapperboard } from "lucide-react"
import { cn } from "@/lib/utils"

/** 番剧封面；无图或加载失败时显示品牌色占位 */
/**
 * blurFill：把竖版封面放进横向容器——同一张图模糊放大铺满背景，原图完整居中（不裁切人物/标题）。
 */
export function Cover({
  src,
  title,
  className,
  eager,
  blurFill,
}: {
  src: string | null | undefined
  title: string
  className?: string
  eager?: boolean
  blurFill?: boolean
}) {
  const [failed, setFailed] = useState(false)
  const [loaded, setLoaded] = useState(false)
  const show = src && !failed
  return (
    <div className={cn("relative overflow-hidden bg-muted", className)}>
      {show && blurFill ? (
        <>
          <img
            src={src}
            alt=""
            aria-hidden
            loading={eager ? "eager" : "lazy"}
            decoding="async"
            referrerPolicy="no-referrer"
            className={cn("absolute inset-0 h-full w-full scale-125 object-cover blur-xl brightness-90 saturate-125 transition-opacity duration-300", loaded ? "opacity-100" : "opacity-0")}
          />
          <img
            src={src}
            alt=""
            loading={eager ? "eager" : "lazy"}
            decoding="async"
            referrerPolicy="no-referrer"
            onLoad={() => setLoaded(true)}
            onError={() => setFailed(true)}
            className={cn("relative h-full w-full object-contain drop-shadow-[0_0_12px_rgba(0,0,0,0.35)] transition-opacity duration-300", loaded ? "opacity-100" : "opacity-0")}
          />
        </>
      ) : show ? (
        <img
          src={src}
          alt=""
          loading={eager ? "eager" : "lazy"}
          decoding="async"
          referrerPolicy="no-referrer"
          onLoad={() => setLoaded(true)}
          onError={() => setFailed(true)}
          className={cn("h-full w-full object-cover transition-opacity duration-300", loaded ? "opacity-100" : "opacity-0")}
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
