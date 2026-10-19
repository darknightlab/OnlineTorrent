// 单集页（/subject/:provider/:id/episode/:key[/hash/:ih[/file/:index]]）的界面部件；页面逻辑见 watch/Playback.tsx
import { Link } from "react-router-dom"
import { ChevronRight, Film, HardDrive, Layers, ListVideo, Pin, Sparkles } from "lucide-react"
import { AccessBadge } from "@/components/badges"
import { SwarmInfo } from "@/components/Swarm"
import { describeScore, type Scored } from "@/lib/rank"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Skeleton } from "@/components/ui/skeleton"
import { airedCount, EpisodeGrid, episodeFullLabel, useEpisodeChips, useEpisodeItem } from "@/components/EpisodeGrid"
import { displayTitle } from "@/lib/schedule"
import { episodeLabel, formatEpisodeKey, type EpisodeKey } from "@/lib/episode"
import type { EpisodeFile, EpisodeResponse, Subject } from "@/lib/types"
import { formatBytes, formatDateTime, timeAgo, WEEKDAYS } from "@/lib/format"
import { useNow } from "@/lib/hooks"
import { cn, subjectHref } from "@/lib/utils"
import type { PlayerParts } from "./watch/Player"

/** 与 Player 相同的两栏布局（移动端单栏） */
export const GRID = "grid gap-4 lg:grid-cols-[minmax(0,1fr)_360px] lg:gap-5 xl:grid-cols-[minmax(0,1fr)_400px]"
/** 移动端 / 平板全宽（抵消 Container 的左右内边距） */
export const BLEED = "-mx-3 sm:-mx-4 lg:mx-0"

/** 无资源：保留选集，资源区给出提示 */
export function EmptyEpisode({ data, ep }: { data: EpisodeResponse; ep: EpisodeKey }) {
  const s = data.subject
  const item = useEpisodeItem(s, formatEpisodeKey(ep))
  const future = item?.aired === false
  return (
    <div className={GRID}>
      <section className="min-w-0">
        <div className={cn(BLEED, "relative aspect-video overflow-hidden bg-black lg:rounded-xl")}>
          {s.cover && (
            <div aria-hidden className="absolute inset-0 scale-110 bg-cover bg-center opacity-40 blur-2xl" style={{ backgroundImage: `url("${s.cover}")` }} />
          )}
          <div className="absolute inset-0 flex flex-col items-center justify-center gap-2 bg-black/50 px-6 text-center text-white">
            <Film className="h-8 w-8 text-white/80" />
            <p className="font-medium">
              {episodeLabel(ep)}
              {ep.kind === "main" ? "" : " "}
              {future ? "尚未播出" : "暂无可播放的资源"}
            </p>
            <p className="text-xs text-white/70">
              {future
                ? `${item?.airdate ? `预计 ${item.airdate} 播出，` : ""}播出后字幕组发布的资源会自动出现`
                : "字幕组发布后会自动出现，可先看看其他集"}
            </p>
          </div>
        </div>
        <EpisodeInfo subject={s} ep={ep} />
      </section>
      <aside className="min-w-0 space-y-4 lg:space-y-5">
        <EpisodeList subject={s} ep={ep} hasItems={false} />
        <SideSection id="sources-title" icon={Layers} title="资源">
          <div className="flex flex-col items-center gap-3 rounded-lg border border-dashed px-4 py-6 text-center lg:border-0 lg:py-4">
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
        </SideSection>
      </aside>
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
      <Link to={subjectHref(s)} className="group flex items-center gap-3 rounded-md focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring" title="番剧详情">
        <h1 className="min-w-0 flex-1 truncate text-base font-semibold leading-snug group-hover:text-primary-text lg:text-xl">{title}</h1>
        <span className="inline-flex shrink-0 items-center text-xs text-muted-foreground group-hover:text-primary-text">
          详情
          <ChevronRight className="h-3.5 w-3.5" />
        </span>
      </Link>
      {epTitle && (
        <p className="mt-1 flex min-w-0 items-baseline gap-2 text-sm lg:text-[15px]" title={[episodeFullLabel(ep, epTitle), epJa].filter(Boolean).join("\n")}>
          <span className="min-w-0 shrink truncate font-medium text-foreground/90">{episodeFullLabel(ep, epTitle)}</span>
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
        <div className="mt-2 flex flex-col gap-2 lg:mt-3 lg:flex-row-reverse lg:items-center lg:justify-between lg:border-b lg:pb-3">
          {parts.stats}
          <div className="-mx-2 border-b pb-1 lg:mx-0 lg:-ml-2.5 lg:border-0 lg:pb-0">{parts.actions}</div>
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
        {extra && <div className="min-w-0 truncate text-xs text-muted-foreground">{extra}</div>}
      </div>
      {children}
    </section>
  )
}

/** 选集：移动端单行横向滚动（特别篇接在正片后），桌面端网格；hasItems：当前集是否有资源 */
export function EpisodeList({ subject: s, ep, hasItems }: { subject: Subject; ep: EpisodeKey; hasItems: boolean }) {
  const total = airedCount(s)
  const { chips } = useEpisodeChips(s, ep, hasItems ? undefined : 0)
  return (
    <SideSection id="episodes-title" icon={ListVideo} title="选集" extra={total > 0 ? `已更新至第 ${total} 话` : undefined}>
      <EpisodeGrid subject={s} chips={chips} current={formatEpisodeKey(ep)} variant="side" />
    </SideSection>
  )
}

function baseName(path: string) {
  const i = path.lastIndexOf("/")
  return i >= 0 ? path.slice(i + 1) : path
}

/** 从文件名 / 种子标题粗略解析 分辨率 · 来源 · 编码 · 容器 */
function sourceTags(f: EpisodeFile) {
  const t = `${f.path} ${f.torrent_title}`
  const tags: string[] = []
  const res = t.match(/\b(2160|1080|720|480)[pP]\b/)?.[1] ?? t.match(/\d{3,4}\s*[x×]\s*(2160|1080|720|480)\b/)?.[1]
  if (res) tags.push(res === "2160" ? "4K" : `${res}P`)
  const src = t.match(/\b(Baha|B-Global|CR|ABEMA|Netflix|AMZN|Bilibili|BDRip|BD|WEB-?DL|WebRip)\b/i)?.[1]
  if (src) tags.push(src)
  if (/\b(HEVC|[xh]\.?265)\b/i.test(t)) tags.push("HEVC")
  else if (/\b(AVC|[xh]\.?264)\b/i.test(t)) tags.push("AVC")
  const ext = f.path.match(/\.(mkv|mp4|webm|avi)$/i)?.[1]
  if (ext) tags.push(ext.toUpperCase())
  return tags
}

/** 资源的唯一键 */
export const sourceKey = (f: Pick<EpisodeFile, "infohash" | "file_index">) => `${f.infohash}:${f.file_index}`

/**
 * 资源列表（已按推荐规则排序，见 lib/rank.ts）。`extra`：URL 指定、但不在本集已知文件中的资源（显示在最上方）。
 * `selected`：正在播放的资源键（sourceKey）。
 */
export function SourceList({
  ranked,
  extra,
  selected,
  onSelect,
}: {
  ranked: Scored<EpisodeFile>[]
  extra?: EpisodeFile | null
  selected: string | null
  onSelect: (f: EpisodeFile) => void
}) {
  const now = useNow()
  const items = ranked.map((r) => r.file)
  const list = extra ? [extra, ...items] : items
  return (
    <SideSection id="sources-title" icon={Layers} title="资源" extra={`${list.length} 个`}>
      <ul className={cn(BLEED, "divide-y border-y lg:-mx-4 lg:-mb-4 lg:border-b-0")}>
        {list.map((f, n) => {
          const i = extra ? n - 1 : n
          const pinned = !!extra && n === 0
          const active = sourceKey(f) === selected
          const blocked = f.access.action === "block"
          const name = baseName(f.path)
          const sc = i >= 0 ? ranked[i] : undefined
          return (
            <li key={pinned ? "extra" : sourceKey(f)} className="relative">
              {active && <span aria-hidden className="absolute inset-y-0 left-0 w-0.5 bg-primary" />}
              <button
                type="button"
                onClick={() => onSelect(f)}
                aria-current={active ? "true" : undefined}
                className={cn(
                  "flex w-full items-center gap-3 px-3 py-2.5 text-left transition-colors hover:bg-muted/60 focus-visible:bg-muted/60 focus-visible:outline-none sm:px-4",
                  active && "bg-primary-soft hover:bg-primary-soft",
                  blocked && "opacity-70",
                )}
              >
                <span className="min-w-0 flex-1">
                  <span className="flex items-center gap-1 overflow-hidden">
                    {active && <Badge variant="default">播放中</Badge>}
                    {pinned && (
                      <Badge variant="accent-soft" title="通过链接指定的资源（尚未收录到本集）">
                        <Pin />
                        指定
                      </Badge>
                    )}
                    {i === 0 && sc && (
                      <Badge variant="soft" title={`自动推荐（${describeScore(sc)}）`}>
                        <Sparkles />
                        推荐
                      </Badge>
                    )}
                    {f.team && (
                      <Badge variant="accent-soft" className="max-w-[9rem] truncate">
                        {f.team}
                      </Badge>
                    )}
                    <AccessBadge access={f.access} />
                    {sourceTags(f).map((t) => (
                      <Badge key={t} variant="outline" className="border-foreground/15 px-1 font-normal">
                        {t}
                      </Badge>
                    ))}
                  </span>
                  <span className={cn("mt-1 block truncate text-[13px] leading-snug", active ? "font-medium text-primary-text" : "text-foreground/85")} title={f.path}>
                    {name}
                  </span>
                  {!pinned && <SwarmInfo swarm={f.swarm} dead={sc?.dead} className="mt-0.5 flex" />}
                </span>
                <span className="shrink-0 text-right text-[11px] leading-5 tabular-nums text-muted-foreground">
                  {f.size_bytes > 0 && (
                    <span className="flex items-center justify-end gap-1">
                      <HardDrive className="h-3 w-3" />
                      {formatBytes(f.size_bytes)}
                    </span>
                  )}
                  {f.published_at && (
                    <span className="block" title={formatDateTime(f.published_at)}>
                      {timeAgo(f.published_at, now)}
                    </span>
                  )}
                  {sc && (
                    <span className={cn("block", sc.score < 0 && "text-destructive-text")} title={describeScore(sc)}>
                      {`${sc.score > 0 ? "+" : ""}${Math.round(sc.score)} 分`}
                    </span>
                  )}
                </span>
              </button>
            </li>
          )
        })}
      </ul>
    </SideSection>
  )
}

/** 整页骨架（单集数据加载中，尚未开始播放） */
export function EpisodeSkeleton() {
  return (
    <div className={GRID}>
      <div className="min-w-0">
        <Skeleton className={cn(BLEED, "aspect-video rounded-none lg:rounded-xl")} />
        <EpisodeInfoSkeleton />
      </div>
      <EpisodeSideSkeleton />
    </div>
  )
}

export function EpisodeInfoSkeleton() {
  return (
    <div className="pt-3 lg:pt-4" aria-hidden>
      <Skeleton className="h-5 w-2/3" />
      <Skeleton className="mt-2 h-3.5 w-1/2" />
    </div>
  )
}

export function EpisodeSideSkeleton() {
  return (
    <div className="space-y-4" aria-hidden>
      <Skeleton className="h-24 w-full rounded-xl" />
      <Skeleton className="h-48 w-full rounded-xl" />
    </div>
  )
}
