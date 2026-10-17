import { useState, type FormEvent } from "react"
import { useNavigate } from "react-router-dom"
import { Magnet, Search } from "lucide-react"
import { cn, parseTorrentInput, watchHref } from "@/lib/utils"
import { useMediaQuery } from "@/lib/hooks"

/**
 * 统一输入框：磁力链接/infohash → 直接播放；其他文本 → 种子搜索。
 */
export function TorrentSearchBox({
  size = "lg",
  defaultValue = "",
  autoFocus,
  placeholder,
  className,
}: {
  size?: "sm" | "lg"
  defaultValue?: string
  autoFocus?: boolean
  placeholder?: string
  className?: string
}) {
  const [value, setValue] = useState(defaultValue)
  const navigate = useNavigate()
  const parsed = parseTorrentInput(value)

  function submit(e: FormEvent) {
    e.preventDefault()
    const v = value.trim()
    if (!v) return
    const p = parseTorrentInput(v)
    if (p) {
      navigate("magnet" in p ? watchHref({ uri: p.magnet }) : watchHref({ ih: p.infohash }))
      if (size === "sm") setValue("")
    } else navigate(`/search?q=${encodeURIComponent(v)}`)
  }

  const Icon = parsed ? Magnet : Search
  const lg = size === "lg"
  const wide = useMediaQuery("(min-width: 640px)")
  const label = parsed ? "立即播放" : "搜索"
  return (
    <form role="search" onSubmit={submit} className={cn("relative w-full", className)}>
      <input
        value={value}
        onChange={(e) => setValue(e.target.value)}
        autoFocus={autoFocus}
        type="search"
        enterKeyHint={parsed ? "go" : "search"}
        spellCheck={false}
        autoComplete="off"
        aria-label="搜索种子，或粘贴磁力链接 / infohash 直接播放"
        placeholder={placeholder ?? (lg ? (wide ? "粘贴磁力链接 / infohash 立即播放，或输入关键词搜索" : "搜索或粘贴磁力链接") : "搜索种子或粘贴磁力链接")}
        className={cn(
          "w-full border border-input bg-card text-foreground shadow-sm transition-colors placeholder:text-muted-foreground/80 focus:border-primary focus:outline-none focus:ring-2 focus:ring-primary/25 [&::-webkit-search-cancel-button]:hidden",
          lg ? "h-12 rounded-xl pl-4 pr-[7.25rem] text-[15px] sm:h-14 sm:pl-5 sm:pr-32 sm:text-base" : "h-9 rounded-lg bg-muted/60 pl-3 pr-10 text-sm focus:bg-card",
        )}
      />
      {lg ? (
        <button
          type="submit"
          className={cn(
            "absolute right-1.5 top-1/2 inline-flex h-9 -translate-y-1/2 items-center gap-1.5 rounded-lg px-3.5 text-sm font-medium text-white shadow-sm transition-colors sm:h-11 sm:px-5",
            parsed ? "bg-primary hover:bg-primary-hover" : "bg-accent hover:bg-accent-hover",
          )}
        >
          <Icon className="h-4 w-4" />
          {label}
        </button>
      ) : (
        <button
          type="submit"
          aria-label={label}
          title={label}
          className={cn(
            "absolute right-0.5 top-1/2 inline-flex h-8 w-9 -translate-y-1/2 items-center justify-center rounded-md transition-colors hover:bg-muted",
            parsed ? "text-primary-text" : "text-muted-foreground hover:text-foreground",
          )}
        >
          <Icon className="h-4 w-4" />
        </button>
      )}
    </form>
  )
}
