import { memo, startTransition, useEffect, useMemo, useState } from "react"
import useEmblaCarousel from "embla-carousel-react"
import AutoHeight from "embla-carousel-auto-height"
import { WheelGesturesPlugin } from "embla-carousel-wheel-gestures"
import { Navigate, useLocation } from "react-router-dom"
import { CalendarDays, Megaphone } from "lucide-react"
import { Container } from "@/components/layout/Container"
import { SubjectCard, SubjectCardSkeleton } from "@/components/SubjectCard"
import { EmptyState, ErrorState, SectionHeader } from "@/components/states"
import { Markdown } from "@/components/Markdown"
import { Card } from "@/components/ui/card"
import { useSchedule, useSite } from "@/lib/queries"
import { byWeekday } from "@/lib/schedule"
import type { ScheduleItem } from "@/lib/types"
import { todayWeekday, WEEKDAYS, weekdayDate } from "@/lib/format"
import { useDocumentTitle, useNow } from "@/lib/hooks"
import { cn, parseTorrentInput, watchHref } from "@/lib/utils"

const WEEK_ORDER = [1, 2, 3, 4, 5, 6, 0]

export default function Home() {
  const loc = useLocation()
  useDocumentTitle(null)
  // 兼容旧版分享链接 /?uri=magnet... → /watch/hash/{ih}
  const uri = new URLSearchParams(loc.search).get("uri")
  const ih = uri ? parseTorrentInput(uri) : null
  if (ih) return <Navigate replace to={watchHref(ih)} />
  return (
    <Container className="space-y-6 pb-4 pt-3 sm:space-y-8 sm:py-6">
      <Announcement />
      <ScheduleSection />
    </Container>
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
  const byDay = useMemo(() => WEEK_ORDER.map((w) => byWeekday(visible, w)), [visible])
  const [emblaRef, embla] = useEmblaCarousel(
    { loop: true, align: "start", startIndex: WEEK_ORDER.indexOf(today), duration: 22 },
    [AutoHeight(), WheelGesturesPlugin()],
  )
  // 轮播停在哪天 → 选中哪天
  useEffect(() => {
    if (!embla) return
    // 低优先级更新，不和拖动动画抢主线程
    const onSelect = () => startTransition(() => setDay(WEEK_ORDER[embla.selectedScrollSnap()]))
    embla.on("select", onSelect)
    return () => {
      embla.off("select", onSelect)
    }
  }, [embla])
  // 数据加载完成 / 显示全部切换后，面板高度变了，让轮播重新测量
  useEffect(() => {
    embla?.reInit()
  }, [embla, byDay, isPending])
  // 轮播区域宽度变化（窗口缩放、滚动条出现/消失、字体加载）时重新测量，否则面板会错位
  useEffect(() => {
    if (!embla) return
    const vp = embla.rootNode()
    let last = vp.clientWidth
    let raf = 0
    const ro = new ResizeObserver(() => {
      if (vp.clientWidth === last) return
      last = vp.clientWidth
      cancelAnimationFrame(raf)
      raf = requestAnimationFrame(() => embla.reInit())
    })
    ro.observe(vp)
    return () => {
      ro.disconnect()
      cancelAnimationFrame(raf)
    }
  }, [embla])
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
              aria-controls={`schedule-panel-${w}`}
              onClick={() => {
                setDay(w)
                embla?.scrollTo(WEEK_ORDER.indexOf(w))
              }}
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
      {error ? (
        <Card>
          <ErrorState error={error} onRetry={() => refetch()} />
        </Card>
      ) : (
        // Embla 轮播（loop）：跟手拖动、无缝首尾循环、快速连续滑动不卡；高度跟随当前这天
        <div ref={emblaRef} className="relative -mx-1.5 overflow-hidden">
          <div className="flex items-start will-change-transform [touch-action:pan-y_pinch-zoom]">
            {WEEK_ORDER.map((w, i) => (
              <DayPanel key={w} weekday={w} items={byDay[i]} pending={isPending} now={now} active={w === day} />
            ))}
          </div>
        </div>
      )}
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


const DayPanel = memo(function DayPanel({
  weekday,
  items,
  pending,
  now,
  active,
}: {
  weekday: number
  items: ScheduleItem[]
  pending: boolean
  now: number
  active: boolean
}) {
  return (
    <div
      id={`schedule-panel-${weekday}`}
      role="tabpanel"
      aria-hidden={!active}
      className="min-w-0 shrink-0 grow-0 basis-full px-1.5"
    >
      {pending ? (
        <div className="grid grid-cols-2 gap-x-2.5 gap-y-4 sm:grid-cols-3 sm:gap-x-3 lg:grid-cols-4 xl:grid-cols-5">
          {Array.from({ length: 10 }, (_, i) => <SubjectCardSkeleton key={i} />)}
        </div>
      ) : items.length ? (
        <div className="grid grid-cols-2 gap-x-2.5 gap-y-4 sm:grid-cols-3 sm:gap-x-3 lg:grid-cols-4 xl:grid-cols-5">
          {items.map((i) => <SubjectCard key={i.key} item={i} now={now} />)}
        </div>
      ) : (
        <Card>
          <EmptyState icon={CalendarDays} title={`${WEEKDAYS[weekday]}暂无新番`} description="换一天看看吧" />
        </Card>
      )}
    </div>
  )
})

