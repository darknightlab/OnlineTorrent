import { Link } from "react-router-dom"
import { ReleaseRowBody, releaseRowClass } from "./ReleaseRow"
import { useSwarmViewer } from "./Swarm"
import { DEFAULT_RANK_RULES, scoreFile, type RankInput, type Scored } from "@/lib/rank"
import { useSiteOrFallback } from "@/lib/queries"
import type { Torrent } from "@/lib/types"
import { cn, watchHref } from "@/lib/utils"
import { Skeleton, SkeletonLine } from "@/components/ui/skeleton"

/** 去掉标题开头与字幕组重复的 [xxx] */
export function stripTeam(title: string, team?: string | null) {
  if (!team) return title
  const m = title.match(/^\s*[[【]([^\]】]+)[\]】]\s*/)
  if (m && (m[1] === team || m[1].includes(team) || team.includes(m[1]))) return title.slice(m[0].length)
  return title
}

/** 种子行（与单集页资源列表同一套样式，不显示「推荐」） */
export function TorrentRow({ t, now, score }: { t: Torrent; now: number; score: Scored | null }) {
  return (
    <li>
      <Link to={watchHref(t.infohash)} state={{ torrent: t }} className={releaseRowClass(false, t.access.action === "block")}>
        <ReleaseRowBody
          team={t.team?.name}
          access={t.access}
          tagText={t.title}
          title={stripTeam(t.title, t.team?.name)}
          titleAttr={t.title}
          size={t.size_bytes}
          publishedAt={t.published_at}
          files={t.files}
          swarm={t.swarm}
          score={score}
          now={now}
        />
      </Link>
    </li>
  )
}

/** 种子 → 评分输入（与单集页同一套推荐规则） */
const asRankInput = (t: Torrent): RankInput & { t: Torrent } => ({
  t,
  torrent_title: t.title,
  path: "",
  team: t.team?.name ?? "",
  size_bytes: t.size_bytes,
  published_at: t.published_at,
  access: t.access,
  swarm: t.swarm,
})

/** 种子列表：保持传入顺序，只显示分数 */
export function TorrentList({ items, now = Date.now(), className }: { items: Torrent[]; now?: number; className?: string }) {
  const site = useSiteOrFallback()
  const viewer = useSwarmViewer()
  const rules = site.rank_rules ?? DEFAULT_RANK_RULES
  return (
    <ul className={cn("divide-y", className)}>
      {items.map((t) => (
        <TorrentRow key={t.id} t={t} now={now} score={scoreFile(asRankInput(t), rules, viewer)} />
      ))}
    </ul>
  )
}

/** 种子列表骨架（与 ReleaseRowBody 同样的三行 + 右侧 BT / RTC / 分数） */
export function TorrentListSkeleton({ rows = 8 }: { rows?: number }) {
  return (
    <ul className="divide-y" aria-hidden>
      {Array.from({ length: rows }, (_, i) => (
        <li key={i} className="flex gap-3 px-3 py-2.5 sm:px-4">
          <div className="min-w-0 flex-1">
            <Skeleton className="h-[22px] w-48 max-w-full" />
            <SkeletonLine className="mt-1 text-[13px] leading-snug" bar="w-[85%]" />
            <SkeletonLine className="mt-0.5 text-[11px] leading-4" bar="w-28" />
          </div>
          <div className="flex shrink-0 flex-col items-end justify-between text-[11px] leading-5">
            <SkeletonLine bar="w-16" />
            <SkeletonLine bar="w-16" />
            <SkeletonLine bar="w-10" />
          </div>
        </li>
      ))}
    </ul>
  )
}
