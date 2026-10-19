import { useState, type FormEvent } from "react"
import { useNavigate } from "react-router-dom"
import { Magnet, Search } from "lucide-react"
import { cn, parseTorrentInput, searchHref, watchHref } from "@/lib/utils"
import { useMediaQuery } from "@/lib/hooks"

/**
 * 统一输入框：磁力链接/infohash → 直接播放；其他文本 → 种子搜索。
 * entry：只读入口（手机首页顶栏）——与搜索页同一个 <input>、同样式，点一下进入搜索页；
 * 两边是同一个元素，任何浏览器 / 字体下文字和图标位置都一致。
 */
export function TorrentSearchBox({
  size = "lg",
  defaultValue = "",
  autoFocus,
  placeholder,
  className,
  entry,
}: {
  size?: "sm" | "lg"
  defaultValue?: string
  autoFocus?: boolean
  placeholder?: string
  className?: string
  /** 只读入口：点击 / 获得焦点时跳到这个地址（不弹键盘、不提交） */
  entry?: string
}) {
  const [value, setValue] = useState(defaultValue)
  const navigate = useNavigate()
  const parsed = parseTorrentInput(value) !== null

  function submit(e: FormEvent) {
    e.preventDefault()
    const v = value.trim()
    if (!v) return
    // 磁力 / infohash → /watch/hash/{ih}（dn 与 tracker 丢弃，服务端只用站点 tracker）
    const ih = parseTorrentInput(v)
    if (ih) {
      navigate(watchHref(ih))
      if (size === "sm") setValue("")
    } else navigate(searchHref(v))
  }

  const Icon = parsed ? Magnet : Search
  const lg = size === "lg"
  const wide = useMediaQuery("(min-width: 640px)")
  const label = parsed ? "立即播放" : "搜索"
  return (
    <form role="search" onSubmit={entry ? (e) => (e.preventDefault(), navigate(entry)) : submit} className={cn("relative w-full", className)}>
      <input
        value={value}
        onChange={(e) => setValue(e.target.value)}
        autoFocus={autoFocus}
        readOnly={!!entry}
        // 入口：按下即跳转（preventDefault 防止获得焦点弹出键盘）；键盘 Tab 聚焦后按 Enter 走 onSubmit
        onPointerDown={entry ? (e) => (e.preventDefault(), navigate(entry)) : undefined}
        type="search"
        enterKeyHint={parsed ? "go" : "search"}
        spellCheck={false}
        autoComplete="off"
        aria-label="搜索种子，或粘贴磁力链接 / infohash 直接播放"
        placeholder={placeholder ?? (lg ? (wide ? "粘贴磁力链接 / infohash 立即播放，或输入关键词搜索" : "搜索或粘贴磁力链接") : "搜索种子或粘贴磁力链接")}
        className={cn(
          "w-full border border-input bg-card text-foreground transition-colors placeholder:text-muted-foreground/80 focus:border-primary focus:outline-none [&::-webkit-search-cancel-button]:hidden",
          entry && "cursor-pointer",
          lg ? "h-12 rounded-xl pl-4 pr-[7.25rem] text-[15px] focus:ring-2 focus:ring-primary/25 sm:h-14 sm:pl-5 sm:pr-32 sm:text-base" : "h-9 rounded-lg bg-muted/60 pl-3 pr-10 text-base md:text-sm",
        )}
      />
      {lg ? (
        <button
          type="submit"
          className={cn(
            "absolute right-1.5 top-1/2 inline-flex h-9 -translate-y-1/2 items-center gap-1.5 rounded-lg px-3.5 text-sm font-medium text-white transition-colors sm:h-11 sm:px-5",
            parsed ? "bg-primary hover:bg-primary-hover" : "bg-accent hover:bg-accent-hover",
          )}
        >
          <Icon className="h-4 w-4" />
          {label}
        </button>
      ) : (
        <button
          type="submit"
          tabIndex={entry ? -1 : undefined}
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
