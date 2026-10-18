import { memo } from "react"
import { Link } from "react-router-dom"
import { Info, Play } from "lucide-react"
import { Cover } from "./Cover"
import type { ScheduleItem } from "@/lib/types"
import { airStatus, displayTitle, episodeLabel } from "@/lib/schedule"
import { formatDate } from "@/lib/format"
import { cn, episodeHref, subjectHref } from "@/lib/utils"

/** 新番时间表卡片（16:9；竖版封面宽度铺满、偏上裁切，以后换成本集截图 / 横版剧照） */
export const SubjectCard = memo(function SubjectCard({ item, now }: { item: ScheduleItem; now: number }) {
  const status = airStatus(item, now)
  const title = displayTitle(item)
  // 已知最新一集 → 单集页（自动选资源播放）；否则 → 条目页
  const ep = item.latest_episode && item.latest_episode > 0 ? item.latest_episode : null
  const href = ep ? episodeHref(item, ep) : subjectHref(item)
  return (
    <div className="group relative">
      <Link
        to={href}
        className="block rounded-lg focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2 focus-visible:ring-offset-background"
        aria-label={`${title}，${episodeLabel(item)}，${item.air_time} 播出${ep ? `，点击播放第 ${ep} 话` : ""}`}
      >
        <div className="relative overflow-hidden rounded-lg shadow-card ring-1 ring-black/5 dark:ring-white/5">
          <Cover src={item.cover} title={title} position="object-[50%_30%]" className="aspect-video w-full transition-transform duration-300 group-hover:scale-[1.03]" />
          {/* 顶部：时间 + 状态 */}
          <div className="absolute inset-x-0 top-0 flex items-start justify-between gap-1 whitespace-nowrap p-1 sm:p-1.5">
            <span className="rounded bg-black/65 px-1 py-px sm:px-1.5 text-[11px] font-semibold tabular-nums leading-[18px] text-white">
              {item.air_time}
            </span>
            {status === "updated" && (
              <span className="rounded bg-primary px-1 py-px text-[11px] sm:px-1.5 font-medium leading-[18px] text-white shadow-sm">已更新</span>
            )}
            {status === "soon" && (
              <span className="rounded bg-accent px-1 py-px text-[11px] sm:px-1.5 font-medium leading-[18px] text-white shadow-sm">即将播出</span>
            )}
          </div>
          {/* 底部：集数 */}
          <div className="absolute inset-x-0 bottom-0 flex items-end justify-between bg-gradient-to-t from-black/75 via-black/30 to-transparent px-2 pb-1.5 pt-8">
            <span className="text-[12px] font-semibold text-white drop-shadow sm:text-[13px]">
              {item.episodes_aired > 0 ? episodeLabel(item) : `${formatDate(item.begin).slice(5)} 开播`}
            </span>
            {ep && (
              <span className="inline-flex h-6 w-6 items-center justify-center rounded-full bg-primary text-white opacity-90 shadow transition-transform group-hover:scale-110 sm:h-7 sm:w-7">
                <Play className="ml-px h-3 w-3 fill-current sm:h-3.5 sm:w-3.5" />
              </span>
            )}
          </div>
        </div>
        <div className="px-0.5 pt-1.5">
          <h3 className="line-clamp-2 text-[13px] font-medium leading-snug text-foreground group-hover:text-primary-text sm:text-sm" title={title}>
            {title}
          </h3>
          {item.title_cn && item.title !== item.title_cn && (
            <p className="mt-0.5 hidden truncate text-[11px] text-muted-foreground sm:block" title={item.title}>
              {item.title}
            </p>
          )}
        </div>
      </Link>
      {ep && (
        <Link
          to={subjectHref(item)}
          aria-label={`${title} 番剧详情`}
          title="番剧详情"
          className={cn(
            "absolute right-1.5 top-8 hidden h-7 w-7 items-center justify-center rounded-full bg-black/65 text-white opacity-0 transition-opacity hover:bg-black/75 focus-visible:opacity-100 group-hover:opacity-100 md:inline-flex",
          )}
        >
          <Info className="h-3.5 w-3.5" />
        </Link>
      )}
    </div>
  )
})

export function SubjectCardSkeleton() {
  return (
    <div>
      <div className="aspect-video w-full animate-pulse rounded-lg bg-muted" />
      <div className="mt-2 h-3.5 w-4/5 animate-pulse rounded bg-muted" />
      <div className="mt-1.5 h-3 w-1/2 animate-pulse rounded bg-muted" />
    </div>
  )
}
