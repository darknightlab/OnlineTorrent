import { memo, useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from "react"
import { Navigate, useLocation } from "react-router-dom"
import { CalendarDays, Megaphone } from "lucide-react"
import { Container } from "@/components/layout/Container"
import { AnimeCard, AnimeCardSkeleton } from "@/components/AnimeCard"
import { EmptyState, ErrorState, SectionHeader } from "@/components/states"
import { Markdown } from "@/components/Markdown"
import { Card } from "@/components/ui/card"
import { useSchedule, useSite } from "@/lib/queries"
import { byWeekday } from "@/lib/schedule"
import type { ScheduleItem } from "@/lib/types"
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
  const scroller = useDaySwipe(day, setDay)
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
              onClick={() => scroller.select(w)}
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
        // 7 天横向排成一行：原生横向滚动 + 吸附，跟手滑动时能看到相邻那天
        <div
          ref={scroller.ref}
          onScroll={scroller.onScroll}
          className="-mx-1.5 flex items-start overflow-x-auto overflow-y-hidden overscroll-x-contain snap-x snap-mandatory transition-[height] duration-200 [scrollbar-width:none] [&::-webkit-scrollbar]:hidden"
        >
          {/* 首尾各放一个克隆（周日 | 周一…周日 | 周一），滑到克隆上停稳后瞬间跳回真身，实现循环 */}
          <DayPanel clone weekday={WEEK_ORDER[6]} items={byDay[6]} pending={isPending} now={now} active={false} />
          {WEEK_ORDER.map((w, i) => (
            <DayPanel key={w} weekday={w} items={byDay[i]} pending={isPending} now={now} active={w === day} />
          ))}
          <DayPanel clone weekday={WEEK_ORDER[0]} items={byDay[0]} pending={isPending} now={now} active={false} />
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
  clone,
}: {
  weekday: number
  items: ScheduleItem[]
  pending: boolean
  now: number
  active: boolean
  clone?: boolean
}) {
  return (
    <div
      id={clone ? undefined : `schedule-panel-${weekday}`}
      role={clone ? undefined : "tabpanel"}
      aria-hidden={clone || !active}
      inert={clone || undefined}
      data-panel
      className="w-full shrink-0 snap-start snap-always px-1.5"
    >
      {pending ? (
        <div className="grid grid-cols-2 gap-x-2.5 gap-y-4 sm:grid-cols-3 sm:gap-x-3 lg:grid-cols-4 xl:grid-cols-5">
          {Array.from({ length: 10 }, (_, i) => <AnimeCardSkeleton key={i} />)}
        </div>
      ) : items.length ? (
        <div className="grid grid-cols-2 gap-x-2.5 gap-y-4 sm:grid-cols-3 sm:gap-x-3 lg:grid-cols-4 xl:grid-cols-5">
          {items.map((i) => <AnimeCard key={i.key} item={i} now={now} />)}
        </div>
      ) : (
        <Card>
          <EmptyState icon={CalendarDays} title={`${WEEKDAYS[weekday]}暂无新番`} description="换一天看看吧" />
        </Card>
      )}
    </div>
  )
})

/**
 * 星期面板的横向滚动：滚动/滑动时同步选中的星期；点标签时滚到对应面板；
 * 容器高度跟随当前（及滑动中相邻）面板，避免短的那天下面留大片空白。
 */
function useDaySwipe(day: number, setDay: (w: number) => void) {
  // 滚动槽位：0 = 周日克隆，1..7 = 周一..周日，8 = 周一克隆
  const slotOf = (w: number) => WEEK_ORDER.indexOf(w) + 1
  const dayOfSlot = (slot: number) => WEEK_ORDER[(slot + 6) % 7]
  const ref = useRef<HTMLDivElement>(null)
  const heights = useRef<number[]>([])
  const fromScroll = useRef(false)
  const first = useRef(true)
  // 点击标签触发的平滑滚动目标槽位：滚动途中不回写星期，避免高亮来回跳
  const target = useRef<number | null>(null)

  const applyHeight = useCallback(() => {
    const el = ref.current
    if (!el || !el.clientWidth) return
    const f = el.scrollLeft / el.clientWidth
    const h = Math.max(heights.current[Math.floor(f)] ?? 0, heights.current[Math.ceil(f)] ?? 0)
    if (h) el.style.height = `${h}px`
  }, [])

  // 观察每个面板（含克隆）的高度
  useEffect(() => {
    const el = ref.current
    if (!el) return
    const panels = Array.from(el.querySelectorAll<HTMLElement>("[data-panel]"))
    const ro = new ResizeObserver(() => {
      heights.current = panels.map((p) => p.offsetHeight)
      applyHeight()
    })
    panels.forEach((p) => ro.observe(p))
    return () => ro.disconnect()
  }, [applyHeight])

  // 选中的星期变化（非滑动引起）时滚过去；首次进入直接定位到今天
  useLayoutEffect(() => {
    const el = ref.current
    if (!el) return
    if (fromScroll.current) {
      fromScroll.current = false
      return
    }
    const slot = slotOf(day)
    if (Math.round(el.scrollLeft / (el.clientWidth || 1)) !== slot) target.current = first.current ? null : slot
    el.scrollTo({ left: slot * el.clientWidth, behavior: first.current ? "auto" : "smooth" })
    first.current = false
    applyHeight()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [day, applyHeight])

  // 窗口尺寸变化后保持停在当前这天
  useEffect(() => {
    const onResize = () => {
      const el = ref.current
      if (el) el.scrollTo({ left: slotOf(day) * el.clientWidth })
    }
    window.addEventListener("resize", onResize)
    return () => window.removeEventListener("resize", onResize)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [day])

  // 停在首尾克隆上时，无动画跳回对应的真身
  const settle = useCallback(() => {
    const el = ref.current
    if (!el || !el.clientWidth) return
    const w = el.clientWidth
    const pos = el.scrollLeft / w
    if (Math.abs(pos - Math.round(pos)) > 0.01) return // 还没停稳
    const slot = Math.round(pos)
    if (slot === 0 || slot === 8) {
      el.scrollTo({ left: (slot === 0 ? 7 : 1) * w, behavior: "auto" })
      applyHeight()
    }
  }, [applyHeight])

  useEffect(() => {
    const el = ref.current
    if (!el) return
    el.addEventListener("scrollend", settle)
    return () => el.removeEventListener("scrollend", settle)
  }, [settle])

  const frame = useRef(0)
  const idle = useRef<ReturnType<typeof setTimeout>>(undefined)
  const onScroll = useCallback(() => {
    cancelAnimationFrame(frame.current)
    // 不支持 scrollend 的浏览器（旧 Safari）：滚动静止 150ms 后再检查
    clearTimeout(idle.current)
    idle.current = setTimeout(settle, 150)
    frame.current = requestAnimationFrame(() => {
      const el = ref.current
      if (!el || !el.clientWidth) return
      applyHeight()
      const slot = Math.min(8, Math.max(0, Math.round(el.scrollLeft / el.clientWidth)))
      if (target.current !== null) {
        if (slot !== target.current) return
        target.current = null
      }
      const w = dayOfSlot(slot)
      if (w !== day) {
        fromScroll.current = true
        setDay(w)
      }
    })
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [day, setDay, applyHeight, settle])

  const select = useCallback(
    (w: number) => {
      fromScroll.current = false
      setDay(w)
    },
    [setDay],
  )
  // 用户手动滑动/滚轮时放弃程序滚动目标
  useEffect(() => {
    const el = ref.current
    if (!el) return
    const cancel = () => (target.current = null)
    el.addEventListener("touchstart", cancel, { passive: true })
    el.addEventListener("wheel", cancel, { passive: true })
    return () => {
      el.removeEventListener("touchstart", cancel)
      el.removeEventListener("wheel", cancel)
    }
  }, [])
  return { ref, onScroll, select }
}
