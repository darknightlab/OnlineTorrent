// 单集页（/subject/:provider/:id/episode/:n[/hash/:ih[/file/:index]]）的界面部件；页面逻辑见 watch/Playback.tsx
import { useEffect, useMemo, useRef } from "react"
import { Link } from "react-router-dom"
import { useQueryClient } from "@tanstack/react-query"
import { ChevronRight, Film, HardDrive, Layers, ListVideo, Pin, Sparkles } from "lucide-react"
import { AccessBadge } from "@/components/badges"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Skeleton } from "@/components/ui/skeleton"
import { qk } from "@/lib/queries"
import { displayTitle } from "@/lib/schedule"
import type { EpisodeFile, EpisodeResponse, Subject } from "@/lib/types"
import { formatBytes, formatDateTime, timeAgo, WEEKDAYS } from "@/lib/format"
import { useNow } from "@/lib/hooks"
import { cn, episodeHref, subjectHref } from "@/lib/utils"
import type { PlayerParts } from "./watch/Player"

/** 与 Player 相同的两栏布局（移动端单栏） */
export const GRID = "grid gap-4 lg:grid-cols-[minmax(0,1fr)_360px] lg:gap-5 xl:grid-cols-[minmax(0,1fr)_400px]"
/** 移动端 / 平板全宽（抵消 Container 的左右内边距） */
export const BLEED = "-mx-3 sm:-mx-4 lg:mx-0"

/** 无资源：保留选集，资源区给出提示 */
export function EmptyEpisode({ data }: { data: EpisodeResponse }) {
  const s = data.subject
  return (
    <div className={GRID}>
      <section className="min-w-0">
        <div className={cn(BLEED, "relative aspect-video overflow-hidden bg-black lg:rounded-xl lg:shadow-lg")}>
          {s.cover && (
            <div aria-hidden className="absolute inset-0 scale-110 bg-cover bg-center opacity-40 blur-2xl" style={{ backgroundImage: `url("${s.cover}")` }} />
          )}
          <div className="absolute inset-0 flex flex-col items-center justify-center gap-2 bg-black/50 px-6 text-center text-white">
            <Film className="h-8 w-8 text-white/80" />
            <p className="font-medium">第 {data.episode} 话暂无可播放的资源</p>
            <p className="text-xs text-white/70">字幕组发布后会自动出现，可先看看其他集</p>
          </div>
        </div>
        <EpisodeInfo subject={s} episode={data.episode} />
      </section>
      <aside className="min-w-0 space-y-4 lg:space-y-5">
        <EpisodeList subject={s} episode={data.episode} hasItems={false} />
        <SideSection id="sources-title" icon={Layers} title="资源">
          <div className="flex flex-col items-center gap-3 rounded-lg border border-dashed px-4 py-6 text-center lg:border-0 lg:py-4">
            <p className="text-[13px] text-muted-foreground">本集暂时还没有收录资源，可以在番剧页查看全部种子。</p>
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

/** 已播集数（映射库最新一集 / 时间表估算取较大者） */
function airedCount(s: Subject) {
  return Math.max(s.latest_episode ?? 0, s.episodes_aired ?? 0)
}

/** 播放器下方：标题（链接到番剧页）、信息行、操作与统计 */
export function EpisodeInfo({ subject: s, episode, parts }: { subject: Subject; episode: number; parts?: PlayerParts }) {
  const title = displayTitle(s)
  const total = airedCount(s)
  const weekly = s.weekday >= 0 && s.weekday <= 6 && s.air_time ? `每${WEEKDAYS[s.weekday]} ${s.air_time} 更新` : null
  const meta = [`第 ${episode} 话`, total > 0 ? `已更新至第 ${total} 话` : null, weekly].filter(Boolean).join(" · ")
  return (
    <div className="pt-3 lg:pt-4">
      <Link to={subjectHref(s)} className="group flex items-center gap-3 rounded-md focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring" title="番剧详情">
        <h1 className="min-w-0 flex-1 truncate text-base font-semibold leading-snug group-hover:text-primary-text lg:text-xl">{title}</h1>
        <span className="inline-flex shrink-0 items-center text-xs text-muted-foreground group-hover:text-primary-text">
          详情
          <ChevronRight className="h-3.5 w-3.5" />
        </span>
      </Link>
      <p className="mt-1 text-xs text-muted-foreground lg:text-[13px]">{meta}</p>
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
    <section aria-labelledby={id} className="lg:overflow-hidden lg:rounded-xl lg:border lg:bg-card lg:p-4 lg:shadow-card">
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

/** 选集：移动端单行横向滚动，桌面端每行 6 个 */
export function EpisodeList({ subject: s, episode, hasItems }: { subject: Subject; episode: number; hasItems: boolean }) {
  const qc = useQueryClient()
  const total = airedCount(s)
  const count = Math.max(total, episode)
  const list = useRef<HTMLDivElement>(null)
  const cur = useRef<HTMLAnchorElement>(null)
  const path = useMemo(() => {
    const m = episodeHref(s, 1).match(/^\/subject\/([^/]+)\/([^/]+)\//)
    return m ? { provider: m[1], id: decodeURIComponent(m[2]) } : null
  }, [s])

  // 当前集滚动到可见区域中间（只滚动列表本身，不滚动页面）
  useEffect(() => {
    const box = list.current
    const el = cur.current
    if (!box || !el) return
    if (box.scrollWidth > box.clientWidth) box.scrollLeft = el.offsetLeft - (box.clientWidth - el.offsetWidth) / 2
    if (box.scrollHeight > box.clientHeight) box.scrollTop = el.offsetTop - (box.clientHeight - el.offsetHeight) / 2
  }, [episode])

  // 已加载过且确认没有资源的集显示为弱化
  const empty = (k: number) => {
    if (k === episode) return !hasItems
    if (!path) return false
    const d = qc.getQueryData<EpisodeResponse>(qk.episode(path.provider, path.id, k))
    return !!d && d.items.length === 0
  }

  return (
    <SideSection id="episodes-title" icon={ListVideo} title="选集" extra={total > 0 ? `已更新至第 ${total} 话` : undefined}>
      <div
        ref={list}
        className={cn(
          BLEED,
          "relative flex gap-2 overflow-x-auto px-3 pb-1 scrollbar-none sm:px-4",
          "lg:grid lg:max-h-[13.5rem] lg:grid-cols-6 lg:overflow-y-auto lg:overflow-x-visible lg:px-0 lg:pb-0",
        )}
      >
        {episodeNumbers(count, episode).map((k) => {
          const active = k === episode
          return (
            <Link
              key={k}
              ref={active ? cur : undefined}
              to={episodeHref(s, k)}
              aria-current={active ? "page" : undefined}
              title={`第 ${k} 话`}
              className={cn(
                "inline-flex h-10 w-[3.25rem] shrink-0 items-center justify-center rounded-lg border bg-card text-sm font-medium tabular-nums transition-colors lg:w-auto",
                active
                  ? "border-primary bg-primary-soft font-semibold text-primary-text"
                  : "hover:border-primary/60 hover:text-primary-text lg:bg-muted/50 lg:border-transparent lg:hover:bg-card",
                !active && empty(k) && "text-muted-foreground/60",
              )}
            >
              {k}
            </Link>
          )
        })}
      </div>
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
 * 资源列表。`extra`：URL 指定、但不在本集已知文件中的资源（显示在最上方）。
 * `selected`：正在播放的资源键（sourceKey）。
 */
export function SourceList({
  items,
  extra,
  selected,
  onSelect,
}: {
  items: EpisodeFile[]
  extra?: EpisodeFile | null
  selected: string | null
  onSelect: (f: EpisodeFile) => void
}) {
  const now = useNow()
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
                    {i === 0 && (
                      <Badge variant="soft" title="服务器自动推荐">
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

/** 选集列表：1..count，再加上当前集（若为小数集如 12.5 或第 0 集），按顺序排列 */
function episodeNumbers(count: number, current: number) {
  const list = Array.from({ length: Math.floor(count) }, (_, i) => i + 1)
  if (!list.includes(current)) list.push(current)
  return list.sort((a, b) => a - b)
}
