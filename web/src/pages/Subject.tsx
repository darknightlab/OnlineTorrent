import { useLayoutEffect, useMemo, useRef, useState } from "react"
import { Link, Navigate, useParams } from "react-router-dom"
import { CalendarClock, CalendarDays, Clapperboard, ExternalLink, Film, LayoutGrid, List, ListVideo, Play, Star, Tv } from "lucide-react"
import { Container } from "@/components/layout/Container"
import { Cover } from "@/components/Cover"
import { airedCount, EpisodeGrid, EpisodeRows, episodeSummary, useEpisodeChips } from "@/components/EpisodeGrid"
import { TorrentList } from "@/components/TorrentList"
import { EmptyState, ErrorState, SectionHeader } from "@/components/states"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Card } from "@/components/ui/card"
import { Skeleton } from "@/components/ui/skeleton"
import { useSubject, useEpisodes } from "@/lib/queries"
import { airStatus, displayTitle } from "@/lib/schedule"
import { formatDate, timeUntil, WEEKDAYS } from "@/lib/format"
import { useDocumentTitle, useNow } from "@/lib/hooks"
import { isApiError } from "@/lib/api"
import type { ExternalIds, Subject } from "@/lib/types"
import { cn, episodeHref, watchHref } from "@/lib/utils"
import { StatusPage } from "./NotFound"
import { releaseTagRank, releaseTags } from "@/lib/release-tags"

const EXTERNAL: { key: keyof ExternalIds; label: string; url: (id: string | number) => string }[] = [
  { key: "bgm", label: "Bangumi", url: (id) => `https://bgm.tv/subject/${id}` },
  { key: "mal", label: "MyAnimeList", url: (id) => `https://myanimelist.net/anime/${id}` },
  { key: "anidb", label: "AniDB", url: (id) => `https://anidb.net/anime/${id}` },
  // 映射库存 "tv/123" / "movie/123"；旧数据只有数字时按 TV
  { key: "tmdb", label: "TMDB", url: (id) => (String(id).includes("/") ? `https://www.themoviedb.org/${id}` : `https://www.themoviedb.org/tv/${id}`) },
  { key: "imdb", label: "IMDb", url: (id) => `https://www.imdb.com/title/${id}` },
]

/** 旧链接 /anime/{key}：bgm:N → /subject/bgm/N，其他回首页 */
export function LegacyAnimeRedirect() {
  const { key = "" } = useParams()
  const m = key.match(/^bgm:(\d+)$/)
  return <Navigate replace to={m ? `/subject/bgm/${m[1]}` : "/"} />
}

/** /subject/:provider/:id */
export default function SubjectPage() {
  const { provider = "", id = "" } = useParams()
  const { data, isPending, error, refetch } = useSubject(provider, id)
  const episodes = useEpisodes(provider, id) // 同一请求被选集组件复用（同一查询键）
  const now = useNow()
  const [team, setTeam] = useState<string | null>(null)
  const [expanded, setExpanded] = useState(false)
  useDocumentTitle(data ? displayTitle(data.subject) : "番剧详情")

  const teams = useMemo(() => {
    const m = new Map<string, number>()
    for (const t of data?.torrents ?? []) if (t.team?.name) m.set(t.team.name, (m.get(t.team.name) ?? 0) + 1)
    return [...m.entries()].sort((a, b) => b[1] - a[1])
  }, [data])
  // 字幕组单选；标签（1080P、WEB-DL、AVC、MKV…）多选，同时满足（AND）
  const [picked, setPicked] = useState<string[]>([])
  const byTeam = useMemo(() => (data?.torrents ?? []).filter((t) => !team || t.team?.name === team), [data, team])
  const tagsOf = useMemo(() => new Map((data?.torrents ?? []).map((t) => [t.id, releaseTags(t.title)])), [data])
  const releaseTagCounts = useMemo(() => {
    const m = new Map<string, number>()
    for (const t of byTeam) for (const g of tagsOf.get(t.id) ?? []) m.set(g, (m.get(g) ?? 0) + 1)
    for (const g of picked) if (!m.has(g)) m.set(g, 0)
    return [...m.entries()].sort((a, b) => releaseTagRank(a[0]) - releaseTagRank(b[0]) || b[1] - a[1] || a[0].localeCompare(b[0]))
  }, [byTeam, tagsOf, picked])
  const torrents = useMemo(
    () => byTeam.filter((t) => picked.every((g) => tagsOf.get(t.id)?.includes(g))),
    [byTeam, tagsOf, picked],
  )

  if (isPending) return <SubjectSkeleton />
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

  const a = data.subject
  const title = displayTitle(a)
  const status = airStatus(a, now)
  const latest = data.torrents[0]
  // 已播集数（映射库最新一集 / 时间表估算取较大者）
  const epCount = airedCount(a)
  // 播放按钮：有资源的最新正片；都没有资源时从第 1 话开始（已播出的话）
  const withSources = (episodes.data?.items ?? []).filter((it) => it.kind === "main" && it.sources > 0)
  const latestEp =
    withSources.length > 0
      ? withSources[withSources.length - 1].number
      : a.latest_episode && a.latest_episode > 0
        ? a.latest_episode
        : epCount > 0
          ? 1
          : null
  // bgm 简介常带大段空行/行尾空格，压缩成最多一个空行
  const summary = (a.summary ?? "").replace(/\r\n?/g, "\n").replace(/[ \t\u3000]+\n/g, "\n").replace(/\n{3,}/g, "\n\n").trim()
  const tags = subjectTags(a)
  const ext = EXTERNAL.filter((e) => a.external?.[e.key] !== null && a.external?.[e.key] !== undefined)

  return (
    <div>
      <SubjectHero
        subject={a}
        title={title}
        status={status}
        meta={
          <dl className="grid grid-cols-2 gap-x-6 gap-y-1.5 text-[13px] sm:gap-y-1 sm:text-sm lg:max-w-xl">
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
            {(a.eps ?? 0) > 0 && (
              <Meta icon={Clapperboard} label="集数">
                全 {a.eps} 话
              </Meta>
            )}
            <Meta icon={ListVideo} label="资源">
              {data.torrents.length} 个种子
            </Meta>
          </dl>
        }
        tags={tags.length > 0 ? <TagList tags={tags} className="flex" /> : null}
        play={
          latestEp ? (
            <Button asChild>
              <Link to={episodeHref(a, String(latestEp))}>
                <Play className="fill-current" />
                播放第 {latestEp} 话
              </Link>
            </Button>
          ) : latest ? (
            <Button asChild>
              <Link to={watchHref(latest.infohash)} state={{ torrent: latest }}>
                <Play className="fill-current" />
                播放最新资源
              </Link>
            </Button>
          ) : null
        }
        links={
          ext.length > 0
            ? ext.map((e) => (
                <Button key={e.key} asChild variant="outline">
                  <a href={e.url(a.external[e.key]!)} target="_blank" rel="noreferrer noopener">
                    {e.label}
                    <ExternalLink className="!size-3.5 text-muted-foreground" />
                  </a>
                </Button>
              ))
            : null
        }
      />

      <Container className="space-y-6 py-5 sm:py-6">
        {/* 选集在简介前 */}
        <SubjectEpisodes subject={a} latest={latestEp !== null ? String(latestEp) : null} />
        {summary && (
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
          </Card>
        )}


        <section aria-labelledby="torrents-title">
          <SectionHeader id="torrents-title" icon={Film} title="种子资源" extra={<span className="text-xs text-muted-foreground">{torrents.length} 个</span>} />
          {(teams.length > 1 || releaseTagCounts.length > 0) && (
            // 第一部分：字幕组（单选）｜第二部分：标签（多选，同时满足）。手机：两行各自横滑；sm 以上：一行换行，竖线分隔
            <div className="mb-3 space-y-2 sm:flex sm:flex-wrap sm:items-center sm:gap-1.5 sm:space-y-0">
              {teams.length > 1 && (
                <div className="-mx-3 flex items-center gap-1.5 overflow-x-auto px-3 scrollbar-none sm:contents">
                  <FilterChip kind="team" active={!team} onClick={() => setTeam(null)}>
                    全部
                  </FilterChip>
                  {teams.map(([name, n]) => (
                    <FilterChip key={name} kind="team" active={team === name} count={n} onClick={() => setTeam(team === name ? null : name)}>
                      {name}
                    </FilterChip>
                  ))}
                </div>
              )}
              {teams.length > 1 && releaseTagCounts.length > 0 && <span aria-hidden className="mx-1 hidden h-4 w-px shrink-0 bg-border sm:block" />}
              {releaseTagCounts.length > 0 && (
                <div className="-mx-3 flex items-center gap-1.5 overflow-x-auto px-3 scrollbar-none sm:contents">
                  {releaseTagCounts.map(([g, n]) => (
                    <FilterChip
                      key={g}
                      active={picked.includes(g)}
                      count={n}
                      onClick={() => setPicked((p) => (p.includes(g) ? p.filter((x) => x !== g) : [...p, g]))}
                    >
                      {g}
                    </FilterChip>
                  ))}
                </div>
              )}
            </div>
          )}
          <Card className="overflow-hidden">
            {torrents.length ? (
              <TorrentList items={torrents} now={now} />
            ) : picked.length > 0 ? (
              <EmptyState icon={Film} title="没有同时符合这些标签的种子" description="取消一些标签试试。" />
            ) : (
              <EmptyState icon={Film} title="暂无种子" description="字幕组发布后会自动出现在这里。" />
            )}
          </Card>
        </section>
      </Container>
    </div>
  )
}

/** 公共标签在前、用户标签在后，去重，最多 MAX_TAGS 个 */
const MAX_TAGS = 10
function subjectTags(a: Subject) {
  const meta = new Set(a.meta_tags ?? [])
  const all = [...new Set([...(a.meta_tags ?? []), ...(a.tags ?? [])].map((t) => t.trim()).filter(Boolean))].slice(0, MAX_TAGS)
  return all.map((name) => ({ name, meta: meta.has(name) }))
}

function TagList({ tags, className }: { tags: { name: string; meta: boolean }[]; className?: string }) {
  return (
    <ul aria-label="标签" className={cn("flex-wrap gap-1.5", className)}>
      {tags.map((t) => (
        <li key={t.name}>
          <Badge variant={t.meta ? "soft" : "outline"} title={t.meta ? "公共标签" : "用户标签"}>
            {t.name}
          </Badge>
        </li>
      ))}
    </ul>
  )
}

/** bangumi 评分与排名（均为 0 时不显示） */
function Rating({ subject: a }: { subject: Subject }) {
  const score = a.score ?? 0
  const rank = a.rank ?? 0
  if (score <= 0 && rank <= 0) return null
  const bgm = a.external?.bgm ?? a.bgm_id
  const body = (
    <>
      {score > 0 && (
        <span className="inline-flex items-center gap-1 font-bold text-warning-text">
          <Star className="h-4 w-4 fill-current" />
          <span className="text-base tabular-nums sm:text-lg">{score.toFixed(1)}</span>
        </span>
      )}
      {rank > 0 && <span className="text-xs tabular-nums text-muted-foreground">Bangumi 排名 #{rank}</span>}
    </>
  )
  return bgm ? (
    <a
      href={`https://bgm.tv/subject/${bgm}`}
      target="_blank"
      rel="noreferrer noopener"
      title="bangumi 评分"
      className="mt-1.5 inline-flex w-fit items-center gap-2 rounded-md hover:opacity-80"
    >
      {body}
    </a>
  ) : (
    <div className="mt-1.5 flex items-center gap-2">{body}</div>
  )
}

type EpisodeView = "list" | "grid"
const VIEW_KEY = "subject.episodeView"

/** 选集：正片 + 特别篇分组（无任何集时不显示）。有官方标题时默认列表视图，可切换为网格（记住选择） */
function SubjectEpisodes({ subject, latest }: { subject: Subject; latest: string | null }) {
  const { chips, loading } = useEpisodeChips(subject)
  const [pref, setPref] = useState<EpisodeView | null>(() => {
    const v = localStorage.getItem(VIEW_KEY)
    return v === "list" || v === "grid" ? v : null
  })
  // 未选过视图时等选集加载完再决定默认视图（避免网格 → 列表闪烁）
  if (loading && !pref) return <Skeleton className="h-40 w-full rounded-xl" />
  if (chips.length === 0) return null
  const view: EpisodeView = pref ?? (chips.some((c) => c.title) ? "list" : "grid")
  const choose = (v: EpisodeView) => {
    localStorage.setItem(VIEW_KEY, v)
    setPref(v)
  }
  return (
    <section aria-labelledby="episodes-title">
      <SectionHeader
        id="episodes-title"
        icon={ListVideo}
        title="选集"
        extra={
          <span className="flex items-center gap-2">
            <span className="text-xs text-muted-foreground">{episodeSummary(chips)}</span>
            <span role="group" aria-label="选集视图" className="inline-flex rounded-md border bg-card p-0.5">
              {(
                [
                  ["list", List, "列表"],
                  ["grid", LayoutGrid, "网格"],
                ] as const
              ).map(([v, Icon, label]) => (
                <button
                  key={v}
                  type="button"
                  aria-pressed={view === v}
                  aria-label={`${label}视图`}
                  title={`${label}视图`}
                  onClick={() => choose(v)}
                  className={cn(
                    "inline-flex h-7 w-7 items-center justify-center rounded text-muted-foreground transition-colors hover:text-foreground",
                    view === v && "bg-primary-soft text-primary-text hover:text-primary-text",
                  )}
                >
                  <Icon className="h-4 w-4" />
                </button>
              ))}
            </span>
          </span>
        }
      />
      {view === "list" ? <EpisodeRows subject={subject} chips={chips} highlight={latest} /> : <EpisodeGrid subject={subject} chips={chips} highlight={latest} variant="page" />}
    </section>
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

/** 筛选标签：与番剧标签同样的小圆角框；字幕组选中为蓝色，其余选中为绿色 */
function FilterChip({
  active,
  onClick,
  count,
  kind,
  children,
}: {
  active: boolean
  onClick: () => void
  count?: number
  kind?: "team"
  children: React.ReactNode
}) {
  return (
    <button
      type="button"
      aria-pressed={active}
      onClick={onClick}
      className={cn(
        "inline-flex h-7 shrink-0 items-center gap-1 whitespace-nowrap rounded border px-2 text-xs font-medium transition-colors",
        active
          ? kind === "team"
            ? "border-accent bg-accent-soft text-accent-text"
            : "border-primary bg-primary-soft text-primary-text"
          : "border-border bg-card text-foreground/75 hover:border-primary/60 hover:text-primary-text",
      )}
    >
      {children}
      {count !== undefined && <span className="text-[11px] tabular-nums opacity-60">{count}</span>}
    </button>
  )
}

function SubjectSkeleton() {
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

const TITLE_MAX = 24
const TITLE_MIN = 16
const TITLE_LINES = 3 // 标题最多行数

/**
 * 番剧页顶部：封面（原始比例，各断点固定高度）+ 右侧。
 * 右侧固定两端：标题与评分贴封面顶部，播放按钮贴封面底部（宽屏时外部链接接在按钮右侧，同高）。
 * 中间依次尝试放「放送信息」「标签」，放不下的（以及窄屏放不下的外部链接）整体挪到封面下方。
 */
function SubjectHero({
  subject: a,
  title,
  status,
  meta,
  tags,
  play,
  links,
}: {
  subject: Subject
  title: string
  status: ReturnType<typeof airStatus>
  meta: React.ReactNode
  tags: React.ReactNode
  play: React.ReactNode
  links: React.ReactNode
}) {
  const [ratio, setRatio] = useState(1 / Math.SQRT2) // bgm 封面多为 1:√2；加载后用真实比例
  const mid = useRef<HTMLDivElement>(null) // 右侧中间可用区域
  const probe = useRef<HTMLDivElement>(null) // 不可见的测量副本（与中间区域同宽）
  const linksRow = useRef<HTMLDivElement>(null)
  const [fit, setFit] = useState({ meta: true, tags: true, links: true })
  // 标题字号：默认最大；只有「标题 + 评分 + 底部按钮」放不进封面高度时才逐级缩小（最小 16px）
  const col = useRef<HTMLDivElement>(null) // 右侧整列（高度 = 封面高度）
  const head = useRef<HTMLDivElement>(null) // 顶部：状态 + 标题 + 评分
  const titleProbe = useRef<HTMLHeadingElement>(null) // 不可见的标题副本（同宽），用来试字号
  const titleEl = useRef<HTMLHeadingElement>(null)
  const [titlePx, setTitlePx] = useState(TITLE_MAX)
  const [titleLines, setTitleLines] = useState<number | null>(null) // 最小字号仍放不下时才截断
  const [heroH, setHeroH] = useState<number | null>(null) // 封面 / 右侧高度（null = 断点基准高度）

  useLayoutEffect(() => {
    const m = mid.current
    const p = probe.current
    if (!m || !p) return
    const GAP = 12
    const calc = () => {
      const avail = m.clientHeight
      const [mh, th] = Array.from(p.children).map((c) => (c as HTMLElement).offsetHeight)
      const metaFits = mh > 0 && mh <= avail
      const tagsFits = metaFits && th > 0 && mh + GAP + th <= avail
      // 外部链接与播放按钮同一行：整行不换行才放在右侧
      const row = linksRow.current
      const linksFit = !!row && row.scrollWidth <= row.clientWidth + 1
      setFit((f) => (f.meta === metaFits && f.tags === tagsFits && f.links === linksFit ? f : { meta: metaFits, tags: tagsFits, links: linksFit }))
    }
    const ro = new ResizeObserver(calc)
    ro.observe(m)
    ro.observe(p)
    if (linksRow.current) ro.observe(linksRow.current)
    calc()
    return () => ro.disconnect()
  }, [])

  useLayoutEffect(() => {
    const c = col.current
    const h = head.current
    const tp = titleProbe.current
    const row = linksRow.current
    if (!c || !h || !tp) return
    const fitTitle = () => {
      const title = titleEl.current
      if (!title) return
      // 基准高度 / 上限随断点（与 className 里的 h-44 / sm:h-60 / lg:h-72 一致）
      const vw = window.innerWidth
      const [base, cap] = vw >= 1024 ? [288, 320] : vw >= 640 ? [240, 288] : [176, 216]
      // 右侧除标题外的固定部分（状态、评分）+ 中间最小间距（my-3）+ 底部按钮行；用小数避免取整抖动
      const fixed = h.getBoundingClientRect().height - title.getBoundingClientRect().height + 24 + (row?.getBoundingClientRect().height ?? 0)
      const measure = (px: number) => {
        tp.style.fontSize = `${px}px`
        const r = tp.getBoundingClientRect().height
        const lh = parseFloat(getComputedStyle(tp).lineHeight) || px * 1.375
        return { lh, lines: Math.round(r / lh), h: r }
      }
      // 1. 最大字号、最多 TITLE_LINES 行：右侧需要多高，封面就长多高（不超过上限）
      let px = TITLE_MAX
      let m = measure(px)
      let height = Math.max(base, Math.ceil(fixed + m.h))
      if (m.lines > TITLE_LINES || height > cap) {
        // 2. 放不下：封面取上限，按「TITLE_LINES 行」定字号（只取决于可用高度，不来回跳），
        //    仍超过 TITLE_LINES 行再继续缩小（最小 TITLE_MIN），最后才截断
        height = cap
        const avail = cap - fixed
        px = Math.max(TITLE_MIN, Math.min(TITLE_MAX, Math.floor(avail / (TITLE_LINES * (m.lh / TITLE_MAX)))))
        m = measure(px)
        while (m.lines > TITLE_LINES && px > TITLE_MIN) m = measure(--px)
        if (m.h > avail + 0.5) height = cap // 截断时高度保持上限
      }
      const lines = m.lines > TITLE_LINES ? TITLE_LINES : null
      const hv = height === base ? null : height
      setTitlePx((v) => (v === px ? v : px))
      setTitleLines((v) => (v === lines ? v : lines))
      setHeroH((v) => (v === hv ? v : hv))
    }
    window.addEventListener("resize", fitTitle)
    const ro = new ResizeObserver(fitTitle)
    ro.observe(tp) // 同宽副本：宽度变化（含封面变宽）即重算
    fitTitle()
    return () => {
      ro.disconnect()
      window.removeEventListener("resize", fitTitle)
    }
  }, [])

  const below = [!fit.meta && meta, !fit.tags && tags, !fit.links && links && (
      // 下方的外部链接用小号按钮
      <div className="flex flex-wrap gap-2 [&>a]:h-8 [&>a]:px-3 [&>a]:text-[13px]">{links}</div>
    )].filter(Boolean)
  return (
    <section className="relative overflow-hidden border-b">
      {a.cover && (
        <div aria-hidden className="absolute inset-0 scale-110 bg-cover bg-center opacity-30 blur-2xl dark:opacity-25" style={{ backgroundImage: `url("${a.cover}")` }} />
      )}
      <div aria-hidden className="absolute inset-0 bg-gradient-to-b from-background/40 via-background/70 to-background" />
      <Container className="relative py-5 sm:py-8">
        <div className="flex gap-4 sm:gap-6">
          {/* 封面：各断点固定高度、按原始比例定宽 */}
          <div
            className="h-44 shrink-0 overflow-hidden rounded-lg bg-muted ring-1 ring-black/10 sm:h-60 lg:h-72"
            style={{ aspectRatio: String(ratio), ...(heroH ? { height: heroH } : null) }}
          >
            <Cover src={a.cover} title={title} eager className="h-full w-full" onRatio={setRatio} />
          </div>
          {/* 右侧与封面等高：顶部标题 / 中间内容自下而上贴着按钮（留白在标题下方）/ 底部按钮 */}
          <div ref={col} className="flex h-44 min-w-0 flex-1 flex-col sm:h-60 lg:h-72" style={heroH ? { height: heroH } : undefined}>
            <div ref={head} className="relative shrink-0">
              {/* 标题测量副本：同宽、不可见 */}
              <h1 ref={titleProbe} aria-hidden className="invisible absolute inset-x-0 top-0 font-bold leading-snug" style={{ fontSize: TITLE_MAX }}>
                {title}
              </h1>
              {(status === "updated" || status === "soon") && (
                <div className="mb-1 flex flex-wrap items-center gap-1.5">
                  {status === "updated" && <Badge variant="default">已更新</Badge>}
                  {status === "soon" && <Badge variant="accent">即将播出</Badge>}
                </div>
              )}
              <h1
                ref={titleEl}
                className={cn("font-bold leading-snug", titleLines !== null && "overflow-hidden")}
                style={{ fontSize: titlePx, ...(titleLines !== null ? { display: "-webkit-box", WebkitBoxOrient: "vertical", WebkitLineClamp: titleLines } : null) }}
                title={title}
              >
                {title}
              </h1>
              <Rating subject={a} />
            </div>
            <div ref={mid} className="relative my-3 flex min-h-0 flex-1 flex-col justify-end overflow-hidden">
              {/* 测量副本：与中间区域同宽、不可见 */}
              <div ref={probe} aria-hidden className="invisible absolute inset-x-0 top-0 space-y-3">
                <div>{meta}</div>
                <div>{tags}</div>
              </div>
              <div className="space-y-3">
                {fit.meta && meta}
                {fit.tags && tags}
              </div>
            </div>
            <div ref={linksRow} className="flex min-w-0 shrink-0 gap-2 overflow-hidden [&>*]:shrink-0">
              {play}
              {fit.links && links}
              {/* 测量链接是否放得下：放不下时也保留不可见的一份撑开宽度 */}
              {!fit.links && links && <div aria-hidden className="invisible flex gap-2">{links}</div>}
            </div>
          </div>
        </div>
        {/* 右侧放不下的内容：封面下方，与顶部同一块背景 */}
        {below.length > 0 && <div className="mt-5 space-y-3 border-t border-border/60 pt-4">{below}</div>}
      </Container>
    </section>
  )
}
