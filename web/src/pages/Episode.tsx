// 单集页（/subject/:provider/:id/episode/:key[/hash/:ih[/file/:index]]）的界面部件；页面逻辑见 watch/Playback.tsx
import { Link } from "react-router-dom"
import { ChevronRight, Film, Layers, ListVideo, Sparkles } from "lucide-react"
import { PlayerSpinner } from "@/components/PlayerSpinner"
import { ReleaseRowBody, releaseRowClass } from "@/components/ReleaseRow"
import { describeScore, type Scored } from "@/lib/rank"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Skeleton, SkeletonLine } from "@/components/ui/skeleton"
import { airedCount, EpisodeGrid, episodeFullLabel, useEpisodeChips, useEpisodeItem, useEpisodePage } from "@/components/EpisodeGrid"
import { displayTitle } from "@/lib/schedule"
import { episodeLabel, formatEpisodeKey, type EpisodeKey } from "@/lib/episode"
import type { EpisodeFile, Subject } from "@/lib/types"
import { WEEKDAYS } from "@/lib/format"
import { useNow } from "@/lib/hooks"
import { cn, subjectHref } from "@/lib/utils"
import { PlayerShell, type PlayerParts } from "./watch/Player"

/** 与 Player 相同的两栏布局（移动端单栏） */
export const GRID = "grid gap-4 lg:grid-cols-[minmax(0,1fr)_360px] lg:gap-5 xl:grid-cols-[minmax(0,1fr)_400px]"
/** 移动端 / 平板全宽（抵消 Container 的左右内边距） */
export const BLEED = "-mx-3 sm:-mx-4 lg:mx-0"

/** 播放器画面内的无资源提示（放在 PlayerShell 里） */
export function EmptyEpisodeOverlay({ subject: s, ep }: { subject: Subject; ep: EpisodeKey }) {
  const item = useEpisodeItem(s, formatEpisodeKey(ep))
  const future = item?.aired === false
  return (
    <div className="flex flex-col items-center gap-2 px-6 text-center text-white">
      <Film className="h-8 w-8 text-white/80" />
      <p className="font-medium">
        {episodeLabel(ep)}
        {ep.kind === "main" ? "" : " "}
        {future ? "尚未播出" : "暂无可播放的资源"}
      </p>
      <p className="text-xs text-white/70">
        {future
          ? `${item?.airdate ? `预计 ${item.airdate} 播出，` : ""}播出后字幕组发布的资源会自动出现`
          : "如已有种子可直接在搜索框输入并观看"}
      </p>
    </div>
  )
}

/** 播放器下方：标题（链接到番剧页）、信息行、操作与统计 */
export function EpisodeInfo({ subject: s, ep, parts }: { subject: Subject; ep: EpisodeKey; parts?: PlayerParts }) {
  const title = displayTitle(s)
  const total = airedCount(s)
  const weekly = s.weekday >= 0 && s.weekday <= 6 && s.air_time ? `每${WEEKDAYS[s.weekday]} ${s.air_time} 更新` : null
  const item = useEpisodeItem(s, formatEpisodeKey(ep))
  const epTitle = item?.title
  const epJa = item?.title_ja && item.title_ja !== epTitle ? item.title_ja : null
  const future = item?.aired === false
  // 正片 "第 12.5 话"；特别篇只写 "NCOP1"（不写成 "第 NCOP1 话"）。有官方标题时集数放在标题行
  const meta = [
    epTitle ? null : episodeLabel(ep),
    item?.airdate ? `${item.airdate} ${future ? "播出" : "放送"}` : null,
    total > 0 ? `已更新至第 ${total} 话` : null,
    weekly,
  ]
    .filter(Boolean)
    .join(" · ")
  return (
    <div className="pt-3 lg:pt-4">
      {/* 只有标题文字与「详情」可点（中间空白不是链接，避免误触） */}
      <div className="flex items-center gap-3">
        <h1 className="min-w-0 flex-1 truncate text-base font-semibold leading-snug lg:text-xl">
          <Link to={subjectHref(s)} className="rounded-sm hover:text-primary-text focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring" title="番剧详情">
            {title}
          </Link>
        </h1>
        <Link
          to={subjectHref(s)}
          className="inline-flex shrink-0 items-center rounded-sm text-xs text-muted-foreground hover:text-primary-text focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
        >
          详情
          <ChevronRight className="h-3.5 w-3.5" />
        </Link>
      </div>
      {epTitle && (
        <p className="mt-1 flex min-w-0 items-baseline gap-2 text-sm lg:text-[15px]" title={[episodeFullLabel(ep, epTitle), epJa].filter(Boolean).join("\n")}>
          {item?.bgm_ep_id ? (
            // 单集标题 → bangumi 的单集页（吐槽、简介）
            <a
              href={`https://bgm.tv/ep/${item.bgm_ep_id}`}
              target="_blank"
              rel="noreferrer noopener"
              className="min-w-0 shrink truncate font-medium text-foreground/90 hover:text-primary-text hover:underline"
            >
              {episodeFullLabel(ep, epTitle)}
            </a>
          ) : (
            <span className="min-w-0 shrink truncate font-medium text-foreground/90">{episodeFullLabel(ep, epTitle)}</span>
          )}
          {epJa && (
            <span lang="ja" className="min-w-0 shrink-[2] truncate text-xs text-muted-foreground lg:text-[13px]">
              {epJa}
            </span>
          )}
        </p>
      )}
      <p className="mt-1 text-xs text-muted-foreground lg:text-[13px]">
        {future && (
          <Badge variant="outline" className="mr-1.5 align-[1px]">
            未播出
          </Badge>
        )}
        {meta}
      </p>
      {parts && (
        // 同一行：左边下载 / 上传 / 节点 / 进度，右边分享 / 下载 / 举报
        <div className="mt-2 flex items-center justify-between gap-2 border-b pb-2 lg:mt-3 lg:pb-3">
          {parts.stats}
          <div className="-mr-2">{parts.actions}</div>
        </div>
      )}
    </div>
  )
}

/** 移动端为无边框的分区，桌面端为右栏卡片 */
export function SideSection({
  id,
  icon: Icon,
  title,
  extra,
  children,
}: {
  id: string
  icon: typeof Layers
  title: string
  extra?: React.ReactNode
  children: React.ReactNode
}) {
  return (
    <section aria-labelledby={id} className="lg:overflow-hidden lg:rounded-xl lg:border lg:bg-card lg:p-4">
      <div className="mb-2.5 flex items-center justify-between gap-3">
        <h2 id={id} className="flex items-center gap-1.5 text-[15px] font-semibold">
          <Icon className="h-4 w-4 text-primary-text" />
          {title}
        </h2>
        {extra && <div className="flex min-w-0 items-center gap-2 truncate text-xs text-muted-foreground">{extra}</div>}
      </div>
      {children}
    </section>
  )
}

/** 选集：移动端单行横向滚动（特别篇接在正片后），桌面端网格；hasItems：当前集是否有资源 */
export function EpisodeList({ subject: s, ep, hasItems }: { subject: Subject; ep: EpisodeKey; hasItems: boolean }) {
  const total = airedCount(s)
  const { chips } = useEpisodeChips(s, ep, hasItems ? undefined : 0)
  const { items, pager } = useEpisodePage(chips, formatEpisodeKey(ep))
  return (
    <SideSection
      id="episodes-title"
      icon={ListVideo}
      title="选集"
      extra={
        total > 0 || pager ? (
          <>
            {total > 0 && <span className="min-w-0 truncate">已更新至第 {total} 话</span>}
            {pager}
          </>
        ) : undefined
      }
    >
      <EpisodeGrid subject={s} chips={items} current={formatEpisodeKey(ep)} variant="side" />
    </SideSection>
  )
}

function baseName(path: string) {
  const i = path.lastIndexOf("/")
  return i >= 0 ? path.slice(i + 1) : path
}

/** 资源的唯一键 */
export const sourceKey = (f: Pick<EpisodeFile, "infohash" | "file_index">) => `${f.infohash}:${f.file_index}`

/**
 * 资源列表（播放器「资源」弹窗内渲染，已按推荐规则排序，见 lib/rank.ts）。
 * `selected`：正在播放的资源键（sourceKey）。无资源时显示空状态提示与跳转番剧页按钮。
 */
export function SourceList({
  subject: s,
  ep,
  ranked,
  selected,
  onSelect,
}: {
  subject: Subject
  ep: EpisodeKey
  ranked: Scored<EpisodeFile>[]
  selected: string | null
  onSelect: (f: EpisodeFile) => void
}) {
  const now = useNow()
  const item = useEpisodeItem(s, formatEpisodeKey(ep))
  const future = item?.aired === false
  if (ranked.length === 0) {
    return (
      <div className="flex flex-col items-center gap-3 px-4 py-8 text-center">
        <p className="text-[13px] text-muted-foreground">
          {future ? "本集尚未播出，播出后会自动收录资源。" : "本集暂时还没有收录资源，可以在番剧页查看全部种子。"}
        </p>
        <Button asChild variant="soft" size="sm">
          <Link to={subjectHref(s)}>
            查看全部种子
            <ChevronRight />
          </Link>
        </Button>
      </div>
    )
  }
  return (
    <ul className="divide-y">
      {ranked.map((sc, i) => {
        const f = sc.file
        const active = sourceKey(f) === selected
        const blocked = f.access.action === "block"
        const name = baseName(f.path)
        return (
          <li key={sourceKey(f)} className="relative">
            {active && <span aria-hidden className="absolute inset-y-0 left-0 z-10 w-0.5 bg-primary" />}
            <button type="button" onClick={() => onSelect(f)} aria-current={active ? "true" : undefined} className={releaseRowClass(active, blocked)}>
              <ReleaseRowBody
                lead={
                  <>
                    {active && <Badge variant="default">播放中</Badge>}
                    {i === 0 && (
                      <Badge variant="soft" title={`自动推荐（${describeScore(sc)}）`}>
                        <Sparkles />
                        推荐
                      </Badge>
                    )}
                  </>
                }
                team={f.team}
                access={f.access}
                tagText={`${f.path} ${f.torrent_title}`}
                tagPath={f.path}
                title={name}
                titleAttr={f.path}
                size={f.size_bytes}
                publishedAt={f.published_at}
                swarm={f.swarm}
                score={sc}
                active={active}
                now={now}
              />
            </button>
          </li>
        )
      })}
    </ul>
  )
}

/**
 * 整页骨架：与播放器同样的 PlayerShell + 信息 + 右栏。
 * files：种子播放页（右栏为文件，信息为标题两行 + 统计行）；否则为单集页（右栏为选集）。
 */
export function EpisodeSkeleton({ files }: { files?: boolean }) {
  return (
    <div className={GRID} aria-hidden>
      <section className="min-w-0">
        <PlayerShell>
          <PlayerSpinner />
        </PlayerShell>
        {files ? <WatchInfoSkeleton /> : <EpisodeInfoSkeleton />}
      </section>
      <aside className="min-w-0">{files ? <FilesSkeleton /> : <EpisodeSideSkeleton />}</aside>
    </div>
  )
}

/** 单集信息：标题 · 单集标题 · 信息行（与 EpisodeInfo 同样的字号 / 行高） */
export function EpisodeInfoSkeleton() {
  return (
    <div className="pt-3 lg:pt-4" aria-hidden>
      <SkeletonLine className="text-base leading-snug lg:text-xl" bar="w-1/2" />
      <SkeletonLine className="mt-1 text-sm lg:text-[15px]" bar="w-2/3" />
      <SkeletonLine className="mt-1 text-xs lg:text-[13px]" bar="w-3/5" />
      <StatsRowSkeleton />
    </div>
  )
}

/** 统计与操作行的占位（EpisodeInfo 的 parts；等待播放会话时也用它占住这一行） */
export const STATS_SKELETON: PlayerParts = {
  stats: <Skeleton className="h-3.5 w-36" />,
  actions: (
    <div aria-hidden className="flex h-9 items-center pr-2">
      <Skeleton className="h-5 w-28 sm:w-56" />
    </div>
  ),
}

function StatsRowSkeleton() {
  return (
    <div className="mt-2 flex items-center justify-between gap-2 border-b pb-2 lg:mt-3 lg:pb-3">
      {STATS_SKELETON.stats}
      <div className="-mr-2">{STATS_SKELETON.actions}</div>
    </div>
  )
}

/** 种子播放页信息：标题（两行）· 统计与操作行 */
export function WatchInfoSkeleton() {
  return (
    <div className="pt-3 lg:pt-4" aria-hidden>
      <SkeletonLine className="text-base leading-snug lg:text-xl" bar="w-full" />
      <SkeletonLine className="text-base leading-snug lg:text-xl" bar="w-3/5" />
      <div className="mt-1 flex" />
      <StatsRowSkeleton />
    </div>
  )
}

/** 右栏卡片（移动端无边框）：标题行 + 内容 */
function SideSkeleton({ children }: { children: React.ReactNode }) {
  return (
    <div aria-hidden className="lg:overflow-hidden lg:rounded-xl lg:border lg:bg-card lg:p-4">
      <SkeletonLine className="mb-2.5 text-[15px]" bar="w-16" />
      {children}
    </div>
  )
}

/** 选集卡片（与 EpisodeGrid side 同样的单行横滑 / 网格） */
export function EpisodeSideSkeleton() {
  return (
    <SideSkeleton>
      <div className="-mx-3 flex gap-2 overflow-hidden px-3 pb-1 sm:mx-0 sm:grid sm:grid-cols-[repeat(auto-fill,minmax(3.5rem,1fr))] sm:px-0 sm:pb-0 lg:grid-cols-6">
        {Array.from({ length: 12 }, (_, i) => (
          <Skeleton key={i} className="h-10 w-[3.25rem] shrink-0 rounded-lg sm:w-auto" />
        ))}
      </div>
    </SideSkeleton>
  )
}

/** 文件列表中的一行（与 Player 的 FileRow 同样的尺寸） */
export function FileRowSkeleton() {
  return (
    <div aria-hidden className="flex min-h-12 items-center gap-3 py-2 pl-3 pr-[3.375rem]">
      <Skeleton className="h-5 w-5 shrink-0" />
      <span className="min-w-0 flex-1">
        <SkeletonLine className="text-[13px] leading-snug" bar="w-4/5" />
        <SkeletonLine className="mt-1 text-[11px]" bar="w-1/3" />
      </span>
    </div>
  )
}

/** 文件卡片（种子播放页右栏） */
export function FilesSkeleton() {
  return (
    <SideSkeleton>
      <div className={cn(BLEED, "border-y lg:-mx-4 lg:-mb-4 lg:border-b-0")}>
        <FileRowSkeleton />
      </div>
    </SideSkeleton>
  )
}
