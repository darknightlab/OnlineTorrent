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
import { todayWeekday, WEEKDAYS } from "@/lib/format"
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
  const visible = useMemo(() => data?.items ?? [], [data])
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
  // 数据加载完成后，面板高度变了，让轮播重新测量
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
  return (
    <section aria-labelledby="schedule-title" id="schedule">
      <SectionHeader id="schedule-title" icon={CalendarDays} title="新番时间表" />
      <div role="tablist" aria-label="选择星期" className="mb-3 grid grid-cols-7 border-b sm:mb-4 md:flex md:gap-1">
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
              aria-label={isToday ? `${WEEKDAYS[w]}（今天）` : WEEKDAYS[w]}
              onClick={() => {
                setDay(w)
                embla?.scrollTo(WEEK_ORDER.indexOf(w))
              }}
              className={cn(
                "relative -mb-px flex h-10 items-center justify-center border-b-2 text-sm font-medium transition-colors md:px-4",
                active ? "border-primary text-primary-text" : "border-transparent text-muted-foreground hover:text-foreground",
                !active && isToday && "text-primary-text",
              )}
            >
              {WEEKDAYS[w]}
              {isToday && !active && <span aria-hidden className="absolute right-[18%] top-2 h-1.5 w-1.5 rounded-full bg-primary md:right-1.5" />}
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

