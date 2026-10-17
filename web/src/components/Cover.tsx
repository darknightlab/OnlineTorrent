import { useState } from "react"
import { Clapperboard } from "lucide-react"
import { cn } from "@/lib/utils"

/** 番剧封面；无图或加载失败时显示品牌色占位 */
export function Cover({ src, title, className, eager }: { src: string | null | undefined; title: string; className?: string; eager?: boolean }) {
  const [failed, setFailed] = useState(false)
  const [loaded, setLoaded] = useState(false)
  const show = src && !failed
  return (
    <div className={cn("relative overflow-hidden bg-muted", className)}>
      {show ? (
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
