import { useState } from "react"
import { Link, useLocation, useParams } from "react-router-dom"
import { ChevronLeft, ChevronRight, Clock, Film, HardDrive, Layers, PlayCircle, Sparkles } from "lucide-react"
import { Container } from "@/components/layout/Container"
import { AccessBadge } from "@/components/badges"
import { EmptyState, ErrorState } from "@/components/states"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Card } from "@/components/ui/card"
import { Skeleton } from "@/components/ui/skeleton"
import { isApiError } from "@/lib/api"
import { useEpisode, useSiteOrFallback } from "@/lib/queries"
import { displayTitle } from "@/lib/schedule"
import type { EpisodeFile, Subject } from "@/lib/types"
import { formatBytes, formatDateTime, timeAgo } from "@/lib/format"
import { useDocumentTitle, useNow } from "@/lib/hooks"
import { cn, episodeHref, subjectHref } from "@/lib/utils"
import { StatusPage } from "./NotFound"
import { usePlayAttempt, usePlayResolve } from "./watch/usePlayResolve"
import { PlayGate } from "./watch/PlayGate"
import { Player } from "./watch/Player"

/** /subject/:provider/:id/episode/:n：自动选资源（items[0]）播放，可切换其他资源；URL 保持不变 */
export default function EpisodePage() {
  const { provider = "", id = "", n = "" } = useParams()
  const ep = /^\d+$/.test(n) ? Number(n) : NaN
  const { data, isPending, error, refetch } = useEpisode(provider, id, ep)
  useDocumentTitle(data ? `${displayTitle(data.subject)} 第 ${data.episode} 话` : "单集")

  if (!(ep > 0)) return <StatusPage code="404" title="集数无效" description="链接中的集数格式有误。" />
  if (isPending) return <EpisodeSkeleton />
  if (error) {
    if (isApiError(error) && error.status === 404)
      return <StatusPage code="404" title="番剧不存在" description="该番剧可能尚未收录，或链接有误。" />
    return (
      <Container className="py-8">
        <Card>
          <ErrorState error={error} onRetry={() => refetch()} />
        </Card>
      </Container>
    )
  }

  const s = data.subject
  return (
    <Container className="py-0 sm:py-5">
      <EpisodeHeader subject={s} episode={data.episode} />
      {data.items.length === 0 ? (
        <Card>
          <EmptyState
            icon={Film}
            title={`第 ${data.episode} 话暂无资源`}
            description="字幕组发布后会自动出现在这里，也可以在番剧页查看全部种子。"
            action={
              <Button asChild>
                <Link to={subjectHref(s)}>查看番剧详情</Link>
              </Button>
            }
          />
        </Card>
      ) : (
        // 换集时重置选中的资源
        <EpisodePlayer key={`${provider}/${id}/${data.episode}`} subject={s} episode={data.episode} items={data.items} />
      )}
    </Container>
  )
}

function EpisodeHeader({ subject: s, episode }: { subject: Subject; episode: number }) {
  const title = displayTitle(s)
  const total = Math.max(s.latest_episode ?? 0, s.episodes_aired ?? 0)
  return (
    <div className="flex items-center gap-2 py-2.5 sm:pb-4 sm:pt-0">
      <Link
        to={subjectHref(s)}
        className="group flex min-w-0 flex-1 items-center gap-1 rounded-md text-left focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
        title="返回番剧详情"
      >
        <ChevronLeft className="h-5 w-5 shrink-0 text-muted-foreground group-hover:text-primary-text" />
        <span className="min-w-0">
          <span className="block truncate text-[15px] font-semibold leading-snug group-hover:text-primary-text sm:text-lg">{title}</span>
          <span className="block text-xs text-muted-foreground">第 {episode} 话{total > 0 ? ` · 已播 ${total} 话` : ""}</span>
        </span>
      </Link>
      <div className="flex shrink-0 gap-1.5">
        {episode > 1 && (
          <Button asChild variant="outline" size="sm" title="上一话">
            <Link to={episodeHref(s, episode - 1)}>
              <ChevronLeft />
              <span className="hidden sm:inline">上一话</span>
            </Link>
          </Button>
        )}
        {(total === 0 || episode < total) && (
          <Button asChild variant="outline" size="sm" title="下一话">
            <Link to={episodeHref(s, episode + 1)}>
              <span className="hidden sm:inline">下一话</span>
              <ChevronRight />
            </Link>
          </Button>
        )}
      </div>
    </div>
  )
}

function EpisodePlayer({ subject, episode, items }: { subject: Subject; episode: number; items: EpisodeFile[] }) {
  const loc = useLocation()
  const here = `${loc.pathname}${loc.search}`
  const site = useSiteOrFallback()
  const attempt = usePlayAttempt()
  const [sel, setSel] = useState(0)
  const item = items[Math.min(sel, items.length - 1)]
  const hint = { infohash: item.infohash, title: item.torrent_title, team: item.team || null }
  const { state, skipWait, retry } = usePlayResolve(
    { kind: "hash", ih: item.infohash },
    { hint, trackers: site.trackers, attempt, known: item.access },
  )

  const select = (i: number) => {
    setSel(i)
    window.scrollTo({ top: 0, behavior: "smooth" })
  }
  const sources = <SourceList items={items} selected={sel} onSelect={select} />
  const shareURL = () => `${location.origin}${episodeHref(subject, episode)}`

  if (state.status === "ready")
    return (
      <Player
        key={state.magnet}
        magnet={state.magnet}
        play={state.play}
        direct={state.direct}
        hint={hint}
        shareURL={shareURL}
        fileIndex={item.file_index}
        poster={subject.cover ?? ""}
        showMeta={false}
        extra={sources}
      />
    )
  return (
    <div className="grid gap-4 lg:grid-cols-[minmax(0,1fr)_360px] lg:gap-5 xl:grid-cols-[minmax(0,1fr)_400px]">
      <div className="min-w-0">
        {state.status !== "idle" && <PlayGate state={state} title={item.torrent_title} here={here} skipWait={skipWait} retry={retry} docTitle={false} />}
      </div>
      <div className="min-w-0">{sources}</div>
    </div>
  )
}

function baseName(path: string) {
  const i = path.lastIndexOf("/")
  return i >= 0 ? path.slice(i + 1) : path
}

function SourceList({ items, selected, onSelect }: { items: EpisodeFile[]; selected: number; onSelect: (i: number) => void }) {
  const now = useNow()
  return (
    <section aria-labelledby="sources-title" className="mt-4 lg:mt-5">
      <h2 id="sources-title" className="mb-2 flex items-center gap-1.5 text-sm font-semibold">
        <Layers className="h-4 w-4 text-primary-text" />
        其他资源
        <span className="text-xs font-normal text-muted-foreground">{items.length} 个</span>
      </h2>
      <Card className="overflow-hidden">
        <ul className="divide-y">
          {items.map((f, i) => {
            const active = i === selected
            const blocked = f.access.action === "block"
            const name = baseName(f.path)
            return (
              <li key={`${f.infohash}:${f.file_index}`} className="relative">
                {active && <span aria-hidden className="absolute inset-y-0 left-0 w-0.5 bg-primary" />}
                <button
                  type="button"
                  onClick={() => onSelect(i)}
                  aria-current={active ? "true" : undefined}
                  className={cn(
                    "flex w-full items-start gap-3 px-3 py-2.5 text-left transition-colors hover:bg-muted/60 focus-visible:bg-muted/60 focus-visible:outline-none sm:px-4",
                    active && "bg-primary-soft hover:bg-primary-soft",
                    blocked && "opacity-70",
                  )}
                >
                  <PlayCircle className={cn("mt-0.5 h-5 w-5 shrink-0", active ? "text-primary-text" : "text-muted-foreground")} />
                  <span className="min-w-0 flex-1">
                    <span className="flex flex-wrap items-center gap-1.5">
                      {f.team && <Badge variant="accent-soft">{f.team}</Badge>}
                      {i === 0 && (
                        <Badge variant="soft" title="服务器自动推荐">
                          <Sparkles />
                          推荐
                        </Badge>
                      )}
                      {active && <Badge variant="default">播放中</Badge>}
                      <AccessBadge access={f.access} />
                    </span>
                    <span className={cn("mt-1 line-clamp-2 wrap-anywhere text-[13px] leading-snug", active && "font-medium text-primary-text")} title={f.path}>
                      {name}
                    </span>
                    <span className="mt-1 flex flex-wrap items-center gap-x-3 gap-y-1 text-[11px] tabular-nums text-muted-foreground">
                      <span className="inline-flex items-center gap-1">
                        <HardDrive className="h-3 w-3" />
                        {formatBytes(f.size_bytes)}
                      </span>
                      {f.published_at && (
                        <span className="inline-flex items-center gap-1" title={formatDateTime(f.published_at)}>
                          <Clock className="h-3 w-3" />
                          {timeAgo(f.published_at, now)}
                        </span>
                      )}
                    </span>
                  </span>
                </button>
              </li>
            )
          })}
        </ul>
      </Card>
    </section>
  )
}

function EpisodeSkeleton() {
  return (
    <Container className="py-3 sm:py-5">
      <Skeleton className="h-6 w-2/3" />
      <Skeleton className="mt-1.5 h-3.5 w-24" />
      <Skeleton className="mt-4 aspect-video w-full max-w-4xl rounded-xl" />
    </Container>
  )
}
