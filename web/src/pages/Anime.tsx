import { useMemo, useState } from "react"
import { Link, useParams } from "react-router-dom"
import { CalendarClock, CalendarDays, ExternalLink, Film, ListVideo, Play, Tv } from "lucide-react"
import { Container } from "@/components/layout/Container"
import { Cover } from "@/components/Cover"
import { TorrentList, TorrentTableHead } from "@/components/TorrentList"
import { EmptyState, ErrorState, SectionHeader } from "@/components/states"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Card } from "@/components/ui/card"
import { Skeleton } from "@/components/ui/skeleton"
import { useAnime } from "@/lib/queries"
import { airStatus, displayTitle } from "@/lib/schedule"
import { formatDate, timeUntil, WEEKDAYS } from "@/lib/format"
import { useDocumentTitle, useNow } from "@/lib/hooks"
import { isApiError } from "@/lib/api"
import type { ExternalIds } from "@/lib/types"
import { cn, watchHref } from "@/lib/utils"
import { StatusPage } from "./NotFound"

const EXTERNAL: { key: keyof ExternalIds; label: string; url: (id: string | number) => string }[] = [
  { key: "bgm", label: "Bangumi", url: (id) => `https://bgm.tv/subject/${id}` },
  { key: "mal", label: "MyAnimeList", url: (id) => `https://myanimelist.net/anime/${id}` },
  { key: "anidb", label: "AniDB", url: (id) => `https://anidb.net/anime/${id}` },
  { key: "tmdb", label: "TMDB", url: (id) => `https://www.themoviedb.org/tv/${id}` },
  { key: "imdb", label: "IMDb", url: (id) => `https://www.imdb.com/title/${id}` },
]

export default function AnimePage() {
  const { key = "" } = useParams()
  const { data, isPending, error, refetch } = useAnime(key)
  const now = useNow()
  const [team, setTeam] = useState<string | null>(null)
  const [expanded, setExpanded] = useState(false)
  useDocumentTitle(data ? displayTitle(data.anime) : "番剧详情")

  const teams = useMemo(() => {
    const m = new Map<string, number>()
    for (const t of data?.torrents ?? []) if (t.team?.name) m.set(t.team.name, (m.get(t.team.name) ?? 0) + 1)
    return [...m.entries()].sort((a, b) => b[1] - a[1])
  }, [data])
  const torrents = useMemo(() => (data?.torrents ?? []).filter((t) => !team || t.team?.name === team), [data, team])

  if (isPending) return <AnimeSkeleton />
  if (error) {
    if (isApiError(error) && error.status === 404) return <StatusPage code="404" title="番剧不存在" description="该番剧可能尚未收录，或链接有误。" />
    return (
      <Container className="py-8">
        <Card>
          <ErrorState error={error} onRetry={() => refetch()} />
        </Card>
      </Container>
    )
  }

  const a = data.anime
  const title = displayTitle(a)
  const status = airStatus(a, now)
  const latest = a.latest_torrent ?? data.torrents[0]
  // bgm 简介常带大段空行/行尾空格，压缩成最多一个空行
  const summary = (a.summary ?? "").replace(/\r\n?/g, "\n").replace(/[ \t\u3000]+\n/g, "\n").replace(/\n{3,}/g, "\n\n").trim()
  const ext = EXTERNAL.filter((e) => a.external?.[e.key] !== null && a.external?.[e.key] !== undefined)

  return (
    <div>
      <section className="relative overflow-hidden border-b">
        {a.cover && (
          <div aria-hidden className="absolute inset-0 scale-110 bg-cover bg-center opacity-30 blur-2xl dark:opacity-25" style={{ backgroundImage: `url("${a.cover}")` }} />
        )}
        <div aria-hidden className="absolute inset-0 bg-gradient-to-b from-background/40 via-background/70 to-background" />
        <Container className="relative flex gap-4 py-5 sm:gap-6 sm:py-8">
          <Cover src={a.cover} title={title} eager className="aspect-[3/4] w-28 shrink-0 rounded-lg shadow-lg ring-1 ring-black/10 sm:w-44 lg:w-52" />
          <div className="flex min-w-0 flex-1 flex-col">
            <div className="flex flex-wrap items-center gap-1.5">
              {status === "updated" && <Badge variant="default">已更新</Badge>}
              {status === "soon" && <Badge variant="accent">即将播出</Badge>}
              <Badge variant="muted">{a.episodes_aired > 0 ? `已播 ${a.episodes_aired} 话` : "未开播"}</Badge>
            </div>
            <h1 className="mt-1.5 text-lg font-bold leading-snug sm:text-2xl">{title}</h1>
            {a.title_cn && a.title !== a.title_cn && <p className="mt-0.5 text-[13px] text-muted-foreground sm:text-sm">{a.title}</p>}
            <dl className="mt-3 grid grid-cols-1 gap-x-6 gap-y-1 text-[13px] sm:grid-cols-2 sm:text-sm lg:max-w-xl">
              <Meta icon={Tv} label="放送">
                每{WEEKDAYS[a.weekday]} {a.air_time}
              </Meta>
              <Meta icon={CalendarDays} label="开播">
                {formatDate(a.begin)}
              </Meta>
              {a.next_air_at && (
                <Meta icon={CalendarClock} label="下一话">
                  {timeUntil(a.next_air_at, now)}
                </Meta>
              )}
              <Meta icon={ListVideo} label="资源">
                {data.torrents.length} 个种子
              </Meta>
            </dl>
            {a.tags?.length > 0 && (
              <div className="mt-3 hidden flex-wrap gap-1.5 sm:flex">
                {a.tags.map((t) => (
                  <Badge key={t} variant="outline">
                    {t}
                  </Badge>
                ))}
              </div>
            )}
            <div className="mt-auto flex flex-wrap gap-2 pt-4">
              {latest && (
                <Button asChild>
                  <Link to={watchHref({ t: latest.id })} state={{ torrent: latest }}>
                    <Play className="fill-current" />
                    播放最新一集
                  </Link>
                </Button>
              )}
              {ext.map((e) => (
                <Button key={e.key} asChild variant="outline" size="sm" className="hidden sm:inline-flex">
                  <a href={e.url(a.external[e.key]!)} target="_blank" rel="noreferrer noopener">
                    {e.label}
                    <ExternalLink className="!size-3.5 text-muted-foreground" />
                  </a>
                </Button>
              ))}
            </div>
          </div>
        </Container>
      </section>

      <Container className="space-y-6 py-5 sm:py-6">
        {(summary || ext.length > 0) && (
          <Card className="p-4 sm:p-5">
            <h2 className="mb-2 text-sm font-semibold">简介</h2>
            <p className={cn("max-w-4xl whitespace-pre-line text-[13px] leading-7 text-foreground/85 sm:text-sm", !expanded && "line-clamp-4 sm:line-clamp-none")}>
              {summary || "暂无简介"}
            </p>
            {summary.length > 80 && (
              <button type="button" onClick={() => setExpanded((v) => !v)} className="mt-1 h-8 text-[13px] font-medium text-primary-text sm:hidden">
                {expanded ? "收起" : "展开全部"}
              </button>
            )}
            {ext.length > 0 && (
              <div className="mt-3 flex flex-wrap gap-2 border-t pt-3 sm:hidden">
                {ext.map((e) => (
                  <a
                    key={e.key}
                    href={e.url(a.external[e.key]!)}
                    target="_blank"
                    rel="noreferrer noopener"
                    className="inline-flex h-9 items-center gap-1 rounded-md border px-3 text-[13px]"
                  >
                    {e.label}
                    <ExternalLink className="h-3.5 w-3.5 text-muted-foreground" />
                  </a>
                ))}
              </div>
            )}
          </Card>
        )}

        <section aria-labelledby="torrents-title">
          <SectionHeader id="torrents-title" icon={Film} title="种子资源" extra={<span className="text-xs text-muted-foreground">{torrents.length} 个</span>} />
          {teams.length > 1 && (
            <div className="-mx-3 mb-3 flex gap-2 overflow-x-auto px-3 pb-1 scrollbar-none sm:mx-0 sm:flex-wrap sm:px-0">
              <Chip active={!team} onClick={() => setTeam(null)}>
                全部
              </Chip>
              {teams.map(([name, n]) => (
                <Chip key={name} active={team === name} onClick={() => setTeam(name)}>
                  {name}
                  <span className="ml-1 text-[11px] opacity-70">{n}</span>
                </Chip>
              ))}
            </div>
          )}
          <Card className="overflow-hidden">
            <TorrentTableHead />
            {torrents.length ? <TorrentList items={torrents} now={now} /> : <EmptyState icon={Film} title="暂无种子" description="字幕组发布后会自动出现在这里。" />}
          </Card>
        </section>
      </Container>
    </div>
  )
}

function Meta({ icon: Icon, label, children }: { icon: typeof Tv; label: string; children: React.ReactNode }) {
  return (
    <div className="flex items-center gap-1.5">
      <Icon className="h-3.5 w-3.5 shrink-0 text-muted-foreground" />
      <dt className="text-muted-foreground">{label}</dt>
      <dd className="truncate font-medium">{children}</dd>
    </div>
  )
}

function Chip({ active, onClick, children }: { active: boolean; onClick: () => void; children: React.ReactNode }) {
  return (
    <button
      type="button"
      aria-pressed={active}
      onClick={onClick}
      className={cn(
        "inline-flex h-9 shrink-0 items-center rounded-full border px-3.5 text-[13px] font-medium transition-colors",
        active ? "border-primary bg-primary text-white" : "bg-card text-foreground/80 hover:border-primary hover:text-primary-text",
      )}
    >
      {children}
    </button>
  )
}

function AnimeSkeleton() {
  return (
    <Container className="flex gap-4 py-6 sm:gap-6 sm:py-8">
      <Skeleton className="aspect-[3/4] w-28 shrink-0 sm:w-44 lg:w-52" />
      <div className="flex-1 space-y-3">
        <Skeleton className="h-6 w-2/3" />
        <Skeleton className="h-4 w-1/3" />
        <Skeleton className="h-4 w-1/2" />
        <Skeleton className="mt-6 h-10 w-36" />
      </div>
    </Container>
  )
}
