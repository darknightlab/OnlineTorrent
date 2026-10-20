// 种子 / 资源行（番剧页、搜索页的种子列表与单集页的资源列表共用）：
// 左侧：标签行 · 标题 · 大小与发布时间，超长时整块可左右滑动；右侧固定：BT / RTC / 分数。
import type { ReactNode } from "react"
import { HardDrive } from "lucide-react"
import { AccessBadge } from "./badges"
import { SwarmGrid } from "./Swarm"
import { Badge } from "./ui/badge"
import { describeScore, type Scored } from "@/lib/rank"
import { releaseTags } from "@/lib/release-tags"
import { formatBytes, formatDateTime, timeAgo } from "@/lib/format"
import type { Access, SwarmStats } from "@/lib/types"
import { cn } from "@/lib/utils"

export const releaseRowClass = (active?: boolean, blocked?: boolean) =>
  cn(
    "group flex w-full items-stretch gap-3 px-3 py-2.5 text-left transition-colors hover:bg-muted/60 focus-visible:bg-muted/60 focus-visible:outline-none sm:px-4",
    active && "bg-primary-soft hover:bg-primary-soft",
    blocked && "opacity-70",
  )

export function ReleaseRowBody({
  lead,
  team,
  access,
  tagText,
  tagPath,
  title,
  titleAttr,
  size,
  publishedAt,
  files,
  swarm,
  score,
  active,
  now,
}: {
  /** 标签行最前面的徽章（播放中 / 推荐 / 指定） */
  lead?: ReactNode
  team?: string | null
  access: Access
  /** 解析分辨率等标签的文本（标题 + 文件路径） */
  tagText: string
  tagPath?: string
  title: string
  titleAttr?: string
  size: number
  publishedAt?: string | null
  files?: number
  swarm?: SwarmStats | null
  score?: Scored | null
  active?: boolean
  now: number
}) {
  return (
    <>
      {/* 左：超长时整块左右滑动 */}
      <span className="min-w-0 flex-1 overflow-x-auto overscroll-x-contain scrollbar-none">
        <span className="flex w-max items-center gap-1">
          {lead}
          {team && <Badge variant="accent-soft">{team}</Badge>}
          <AccessBadge access={access} />
          {releaseTags(tagText, tagPath).map((t) => (
            <Badge key={t} variant="outline" className="border-foreground/15 px-1 font-normal">
              {t}
            </Badge>
          ))}
        </span>
        <span
          className={cn("mt-1 block w-max whitespace-nowrap text-[13px] leading-snug", active ? "font-medium text-primary-text" : "text-foreground/85 group-hover:text-primary-text")}
          title={titleAttr ?? title}
        >
          {title}
        </span>
        <span className="mt-0.5 flex w-max items-center gap-1.5 whitespace-nowrap text-[11px] leading-4 tabular-nums text-muted-foreground">
          {size > 0 && (
            <span className="inline-flex items-center gap-1">
              <HardDrive className="h-3 w-3" />
              {formatBytes(size)}
            </span>
          )}
          {size > 0 && publishedAt && <span aria-hidden>·</span>}
          {publishedAt && <span title={formatDateTime(publishedAt)}>{timeAgo(publishedAt, now)}</span>}
          {files !== undefined && files > 1 && (
            <>
              <span aria-hidden>·</span>
              <span>{files} 个文件</span>
            </>
          )}
        </span>
      </span>
      {/* 右：固定不动 —— BT / RTC / 分数 */}
      <span className="flex shrink-0 flex-col items-end justify-between text-right text-[11px] leading-5 tabular-nums text-muted-foreground">
        <SwarmGrid swarm={swarm} />
        <span className={cn("font-medium", score && score.score < 0 ? "text-destructive-text" : "text-foreground/80")} title={score ? describeScore(score) : undefined}>
          {score ? `${score.score > 0 ? "+" : ""}${Math.round(score.score)} 分` : "\u00a0"}
        </span>
      </span>
    </>
  )
}
