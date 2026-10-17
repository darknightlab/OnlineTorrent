import { Link } from "react-router-dom"
import { Clock, Files, HardDrive, Play } from "lucide-react"
import { AccessBadge } from "./badges"
import type { Torrent } from "@/lib/types"
import { formatBytes, formatDateTime, timeAgo } from "@/lib/format"
import { cn, watchHref } from "@/lib/utils"
import { Skeleton } from "@/components/ui/skeleton"

/** 去掉标题开头与字幕组重复的 [xxx] */
export function stripTeam(title: string, team?: string | null) {
  if (!team) return title
  const m = title.match(/^\s*[[【]([^\]】]+)[\]】]\s*/)
  if (m && (m[1] === team || m[1].includes(team) || team.includes(m[1]))) return title.slice(m[0].length)
  return title
}

export function TorrentRow({ t, now }: { t: Torrent; now: number }) {
  const blocked = t.access.action === "block"
  const title = stripTeam(t.title, t.team?.name)
  return (
    <li>
      <Link
        to={watchHref({ t: t.id })}
        state={{ torrent: t }}
        className={cn(
          "group flex items-start gap-3 px-3 py-2.5 transition-colors hover:bg-muted/60 focus-visible:bg-muted/60 focus-visible:outline-none sm:px-4 md:items-center",
          blocked && "opacity-70",
        )}
      >
        <span
          aria-hidden
          className={cn(
            "mt-0.5 hidden h-8 w-8 shrink-0 items-center justify-center rounded-full transition-colors sm:inline-flex md:mt-0",
            blocked ? "bg-muted text-muted-foreground" : "bg-primary-soft text-primary-text group-hover:bg-primary group-hover:text-white",
          )}
        >
          <Play className="ml-px h-3.5 w-3.5 fill-current" />
        </span>
        <div className="min-w-0 flex-1">
          <p className="line-clamp-2 wrap-anywhere text-[13px] leading-snug sm:text-sm md:line-clamp-1" title={t.title}>
            {t.team?.name && (
              <span className="mr-1.5 inline-block translate-y-[-1px] rounded bg-accent-soft px-1.5 align-middle text-[11px] font-medium leading-[18px] text-accent-text">
                {t.team.name}
              </span>
            )}
            <span className="group-hover:text-primary-text">{title}</span>
          </p>
          {/* 移动端：元信息行 */}
          <div className="mt-1 flex flex-wrap items-center gap-x-3 gap-y-1 text-[11px] text-muted-foreground md:hidden">
            <span className="inline-flex items-center gap-1 tabular-nums">
              <HardDrive className="h-3 w-3" />
              {formatBytes(t.size_bytes)}
            </span>
            <span className="inline-flex items-center gap-1 tabular-nums">
              <Files className="h-3 w-3" />
              {t.files} 个文件
            </span>
            <span className="inline-flex items-center gap-1">
              <Clock className="h-3 w-3" />
              {timeAgo(t.published_at, now)}
            </span>
            <AccessBadge access={t.access} />
          </div>
        </div>
        {/* 桌面：列 */}
        <div className="hidden shrink-0 items-center gap-4 text-xs tabular-nums text-muted-foreground md:flex">
          <span className="w-16 text-right">
            <AccessBadge access={t.access} />
          </span>
          <span className="w-[4.5rem] text-right">{formatBytes(t.size_bytes)}</span>
          <span className="w-12 text-right">{t.files} 文件</span>
          <span className="w-20 text-right" title={formatDateTime(t.published_at)}>
            {timeAgo(t.published_at, now)}
          </span>
        </div>
      </Link>
    </li>
  )
}

export function TorrentList({ items, now = Date.now(), className }: { items: Torrent[]; now?: number; className?: string }) {
  return <ul className={cn("divide-y", className)}>{items.map((t) => <TorrentRow key={t.id} t={t} now={now} />)}</ul>
}

export function TorrentListSkeleton({ rows = 8 }: { rows?: number }) {
  return (
    <ul className="divide-y" aria-hidden>
      {Array.from({ length: rows }, (_, i) => (
        <li key={i} className="flex items-center gap-3 px-4 py-3">
          <Skeleton className="hidden h-8 w-8 rounded-full sm:block" />
          <div className="flex-1 space-y-1.5">
            <Skeleton className="h-3.5 w-[85%]" />
            <Skeleton className="h-3 w-1/3 md:hidden" />
          </div>
          <Skeleton className="hidden h-3 w-48 md:block" />
        </li>
      ))}
    </ul>
  )
}

export function TorrentTableHead() {
  return (
    <div className="hidden items-center gap-4 border-b bg-muted/50 px-4 py-2 text-xs text-muted-foreground md:flex">
      <span className="ml-11 flex-1">标题</span>
      <span className="w-16 text-right">权限</span>
      <span className="w-[4.5rem] text-right">大小</span>
      <span className="w-12 text-right">文件</span>
      <span className="w-20 text-right">发布</span>
    </div>
  )
}
