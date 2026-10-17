import { useMemo, useState } from "react"
import { Link, Navigate, useLocation } from "react-router-dom"
import { CalendarDays, ChevronRight, Flame, Lightbulb, Megaphone, MonitorPlay, Share2, Zap } from "lucide-react"
import { Container } from "@/components/layout/Container"
import { TorrentSearchBox } from "@/components/TorrentSearchBox"
import { AnimeCard, AnimeCardSkeleton } from "@/components/AnimeCard"
import { TorrentList, TorrentListSkeleton, TorrentTableHead } from "@/components/TorrentList"
import { EmptyState, ErrorState, SectionHeader } from "@/components/states"
import { Markdown } from "@/components/Markdown"
import { Card } from "@/components/ui/card"
import { useLatest, useSchedule, useSite } from "@/lib/queries"
import { byWeekday } from "@/lib/schedule"
import { todayWeekday, WEEKDAYS, weekdayDate } from "@/lib/format"
import { useDocumentTitle, useNow } from "@/lib/hooks"
import { cn } from "@/lib/utils"

const WEEK_ORDER = [1, 2, 3, 4, 5, 6, 0]

export default function Home() {
  const loc = useLocation()
  useDocumentTitle(null)
  // 兼容旧版分享链接 /?uri=magnet...
  if (new URLSearchParams(loc.search).has("uri")) return <Navigate replace to={`/watch${loc.search}`} />
  return (
    <Container className="space-y-6 py-4 sm:space-y-8 sm:py-6">
      <Hero />
      {/* 窄屏：公告放在时间表之前，避免被埋在长列表下方 */}
      <Announcement className="xl:hidden" />
      <ScheduleSection />
      <div className="grid gap-6 xl:grid-cols-[minmax(0,1fr)_340px]">
        <LatestSection />
        <aside className="grid content-start gap-4">
          <Announcement className="hidden xl:block" />
          <Tips />
        </aside>
      </div>
    </Container>
  )
}

function Hero() {
  return (
    <section
      aria-labelledby="hero-title"
      className="relative overflow-hidden rounded-xl border bg-card px-3.5 py-4 shadow-card sm:px-8 sm:py-7"
    >
      <div
        aria-hidden
        className="pointer-events-none absolute inset-0 bg-[radial-gradient(55%_130%_at_100%_0%,var(--primary-soft),transparent_70%),radial-gradient(45%_120%_at_0%_100%,var(--accent-soft),transparent_70%)]"
      />
      <div className="relative mx-auto max-w-3xl text-center">
        <h1 id="hero-title" className="text-lg font-bold tracking-tight sm:text-2xl">
          在浏览器里，<span className="text-primary-text">直接看番</span>
        </h1>
        <p className="mt-1 text-xs text-muted-foreground sm:mt-1.5 sm:text-sm">
          <span className="sm:hidden">粘贴磁力链接，基于 WebTorrent 边下边播</span>
          <span className="hidden sm:inline">粘贴磁力链接或 infohash，基于 WebTorrent 边下边播，无需安装任何客户端</span>
        </p>
        <TorrentSearchBox className="mt-3.5 sm:mt-5" />
        <ul className="mt-3 hidden flex-wrap sm:flex items-center justify-center gap-x-4 gap-y-1 text-[12px] text-muted-foreground">
          <li className="inline-flex items-center gap-1">
            <Zap className="h-3.5 w-3.5 text-primary-text" />
            首尾块优先，秒开时长
          </li>
          <li className="inline-flex items-center gap-1">
            <MonitorPlay className="h-3.5 w-3.5 text-primary-text" />
            支持拖动进度
          </li>
          <li className="inline-flex items-center gap-1">
            <Share2 className="h-3.5 w-3.5 text-primary-text" />
            一键分享播放链接
          </li>
        </ul>
      </div>
    </section>
  )
}

function ScheduleSection() {
  const now = useNow()
  const today = todayWeekday(new Date(now))
  const [day, setDay] = useState(today)
  const { data, isPending, error, refetch } = useSchedule()
  const [showAll, setShowAll] = useState(() => localStorage.getItem("schedule.showAll") === "1")
  const toggleShowAll = () =>
    setShowAll((v) => {
      localStorage.setItem("schedule.showAll", v ? "0" : "1")
      return !v
    })
  // 默认只显示 bangumi.moe 上有资源的番（无资源的多为国产/少儿动画，点进去也看不了）
  const visible = useMemo(
    () => (data ? (showAll ? data.items : data.items.filter((i) => i.moe_tag_id || i.latest_torrent)) : []),
    [data, showAll],
  )
  const hiddenCount = (data?.items.length ?? 0) - visible.length
  const items = useMemo(() => byWeekday(visible, day), [visible, day])
  const counts = useMemo(() => {
    const c = new Map<number, number>()
    for (const i of visible) c.set(i.weekday, (c.get(i.weekday) ?? 0) + 1)
    return c
  }, [visible])
  const season = data?.season?.match(/^(\d{4})-(\d{2})/)

  return (
    <section aria-labelledby="schedule-title" id="schedule">
      <SectionHeader
        id="schedule-title"
        icon={CalendarDays}
        title={
          <span className="flex items-baseline gap-2 whitespace-nowrap">
            新番时间表
            {season && (
              <span className="hidden text-xs font-normal text-muted-foreground min-[400px]:inline">
                {season[1]} 年 {Number(season[2])} 月新番
              </span>
            )}
          </span>
        }
        extra={
          <span className="flex shrink-0 items-center gap-3 whitespace-nowrap text-xs text-muted-foreground">
            <span className="hidden sm:inline">北京时间 · 按播出时间排序</span>
            {(hiddenCount > 0 || showAll) && (
              <button
                type="button"
                onClick={toggleShowAll}
                aria-pressed={showAll}
                className="inline-flex h-8 items-center rounded-md px-2 font-medium text-primary-text hover:bg-primary-soft"
              >
                {showAll ? "只看有资源" : `显示全部（+${hiddenCount}）`}
              </button>
            )}
          </span>
        }
      />
      <div role="tablist" aria-label="选择星期" className="mb-3 grid grid-cols-7 gap-1 rounded-xl border bg-card p-1 shadow-card sm:mb-4">
        {WEEK_ORDER.map((w) => {
          const active = w === day
          const isToday = w === today
          return (
            <button
              key={w}
              type="button"
              role="tab"
              aria-selected={active}
              aria-controls="schedule-panel"
              onClick={() => setDay(w)}
              className={cn(
                "relative flex h-12 flex-col items-center justify-center rounded-lg text-sm font-medium transition-colors sm:h-14",
                active ? "bg-primary text-white shadow-sm" : "text-muted-foreground hover:bg-muted hover:text-foreground",
                !active && isToday && "text-primary-text",
              )}
            >
              <span className="leading-tight">{isToday ? "今天" : WEEKDAYS[w]}</span>
              <span className={cn("text-[10px] tabular-nums leading-tight sm:text-[11px]", active ? "text-white/85" : "text-muted-foreground/80")}>
                {isToday ? WEEKDAYS[w] : weekdayDate(w, new Date(now))}
                <span className="hidden sm:inline">{counts.get(w) ? ` · ${counts.get(w)} 部` : ""}</span>
              </span>
              {isToday && !active && <span aria-hidden className="absolute right-1.5 top-1.5 h-1.5 w-1.5 rounded-full bg-primary" />}
            </button>
          )
        })}
      </div>
      <div
        id="schedule-panel"
        role="tabpanel"
        className="grid grid-cols-3 gap-x-2.5 gap-y-4 sm:grid-cols-4 sm:gap-x-3 md:grid-cols-5 lg:grid-cols-6 xl:grid-cols-8"
      >
        {isPending && Array.from({ length: 7 }, (_, i) => <AnimeCardSkeleton key={i} />)}
        {!isPending && !error && items.map((i) => <AnimeCard key={i.key} item={i} now={now} />)}
      </div>
      {error && (
        <Card>
          <ErrorState error={error} onRetry={() => refetch()} />
        </Card>
      )}
      {data && items.length === 0 && (
        <Card>
          <EmptyState icon={CalendarDays} title={`${WEEKDAYS[day]}暂无新番`} description="换一天看看吧" />
        </Card>
      )}
    </section>
  )
}

function LatestSection() {
  const now = useNow()
  const { data, isPending, error, refetch } = useLatest(1)
  return (
    <section aria-labelledby="latest-title" className="min-w-0">
      <SectionHeader
        id="latest-title"
        icon={Flame}
        title="最新种子"
        extra={
          <Link to="/latest" className="inline-flex h-9 items-center gap-0.5 rounded-md px-2 text-[13px] text-muted-foreground hover:text-primary-text">
            查看更多
            <ChevronRight className="h-4 w-4" />
          </Link>
        }
      />
      <Card className="overflow-hidden">
        <TorrentTableHead />
        {isPending ? (
          <TorrentListSkeleton rows={10} />
        ) : error ? (
          <ErrorState error={error} onRetry={() => refetch()} />
        ) : data.items.length ? (
          <TorrentList items={data.items.slice(0, 16)} now={now} />
        ) : (
          <EmptyState title="暂无种子" />
        )}
        <Link
          to="/latest"
          className="flex h-11 items-center justify-center gap-1 border-t text-[13px] font-medium text-primary-text hover:bg-muted/60"
        >
          浏览全部最新种子
          <ChevronRight className="h-4 w-4" />
        </Link>
      </Card>
    </section>
  )
}

function Announcement({ className }: { className?: string }) {
  const { data } = useSite()
  if (!data?.announcement_md) return null
  return (
    <Card className={cn("p-4", className)}>
      <h2 className="mb-2 flex items-center gap-2 text-sm font-semibold">
        <Megaphone className="h-4 w-4 text-accent-text" />
        站点公告
      </h2>
      <Markdown className="text-[13px] text-foreground/85">{data.announcement_md}</Markdown>
    </Card>
  )
}

function Tips() {
  return (
    <Card className="p-4">
      <h2 className="mb-2 flex items-center gap-2 text-sm font-semibold">
        <Lightbulb className="h-4 w-4 text-warning-text" />
        观看提示
      </h2>
      <ol className="space-y-2 text-[13px] leading-relaxed text-foreground/85">
        {[
          "推荐使用最新版 Chrome 或 Edge 浏览器，兼容性最好。",
          "HEVC (H.265)、10bit、AC3/DTS 音轨在部分浏览器可能无法播放，可下载后用本地播放器观看。",
          "播放时浏览器会同时为其他观众上传数据，请保持页面打开以帮助分享。",
          "会员 / 大会员资源需要登录，大会员可通过兑换码开通。",
        ].map((t, i) => (
          <li key={i} className="flex gap-2">
            <span className="mt-[3px] inline-flex h-4 w-4 shrink-0 items-center justify-center rounded-full bg-primary-soft text-[10px] font-semibold text-primary-text">
              {i + 1}
            </span>
            <span>{t}</span>
          </li>
        ))}
      </ol>
    </Card>
  )
}
