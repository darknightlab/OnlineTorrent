// 选集：正片 + 特别篇（SP / NCOP / NCED / PV / CM）。单集页右栏（side）与番剧页（page）共用。
import { useEffect, useMemo, useRef, useState } from "react"
import { ChevronDown, ChevronLeft, ChevronRight } from "lucide-react"
import { Link } from "react-router-dom"
import { useEpisodes } from "@/lib/queries"
import {
  EPISODE_KINDS,
  compareEpisodeKeys,
  episodeLabel,
  episodeShort,
  formatEpisodeKey,
  type EpisodeKey,
  type EpisodeKind,
} from "@/lib/episode"
import type { Subject } from "@/lib/types"
import { cn, episodeHref, subjectPath } from "@/lib/utils"

/** 特别篇分组说明 */
const KIND_DESC: Record<EpisodeKind, string> = { main: "正片", sp: "特别篇", ncop: "无字幕片头", nced: "无字幕片尾", pv: "宣传片", cm: "广告" }

export interface EpisodeChip extends EpisodeKey {
  key: string
  label: string
  /** 已映射的文件数；undefined = 未知（选集列表未加载） */
  sources?: number
  /** 仅正片：是否已播出 */
  aired?: boolean
  /** 官方单集标题 / 日文原标题 / 放送日期（YYYY-MM-DD） */
  title?: string
  title_ja?: string
  airdate?: string
}

/** 未播出且暂无资源 */
export const isFuture = (c: EpisodeChip) => c.aired === false && !c.sources

/** 单集的完整说明（title 提示）："第 3 话 · 契约 / 契約 / 2026-10-17 放送 · 6 个资源" */
export function chipTooltip(c: EpisodeChip) {
  const head = [c.kind === "main" ? c.label : `${c.label} · ${KIND_DESC[c.kind]}`, c.title].filter(Boolean).join(" · ")
  const status = isFuture(c) ? "未播出" : c.sources === 0 ? "暂无资源" : c.sources ? `${c.sources} 个资源` : null
  const tail = [c.airdate ? `${c.airdate} 放送` : null, status].filter(Boolean).join(" · ")
  return [head, c.title_ja && c.title_ja !== c.title ? c.title_ja : null, tail].filter(Boolean).join("\n")
}

/** 已播集数（映射库最新一集 / 时间表估算取较大者） */
export function airedCount(s: Subject) {
  return Math.max(s.latest_episode ?? 0, s.episodes_aired ?? 0)
}

const chipOf = (k: EpisodeKey, sources?: number): EpisodeChip => ({ ...k, key: formatEpisodeKey(k), label: episodeLabel(k), sources })

/**
 * 选集数据：GET /subjects/{p}/{id}/episodes；未加载 / 失败时按已播集数给出 1..n（资源数未知）。
 * current 不在列表中时补上（如没有资源的特别篇），currentSources 覆盖当前集的资源数。
 */
export function useEpisodeChips(subject: Subject, current?: EpisodeKey | null, currentSources?: number) {
  const p = subjectPath(subject)
  const q = useEpisodes(p?.provider ?? "", p?.id ?? "")
  const chips = useMemo(() => {
    const list: EpisodeChip[] = q.data
      ? q.data.items.map((it) => ({
          kind: it.kind,
          number: it.number,
          key: it.key,
          label: it.label,
          sources: it.sources,
          aired: it.aired,
          title: it.title || undefined,
          title_ja: it.title_ja || undefined,
          airdate: it.airdate || undefined,
        }))
      : Array.from({ length: Math.min(airedCount(subject), 2000) }, (_, i) => chipOf({ kind: "main", number: i + 1 }))
    if (current) {
      const key = formatEpisodeKey(current)
      const i = list.findIndex((c) => c.key === key)
      if (i < 0) list.push(chipOf(current, currentSources))
      else if (currentSources !== undefined) list[i] = { ...list[i], sources: currentSources }
      list.sort(compareEpisodeKeys)
    }
    return list
  }, [q.data, subject, current, currentSources])
  return { chips, loading: q.isPending && !!p, error: q.error }
}

/** 选集列表中的某一集（官方标题 / 放送日期）；与 useEpisodeChips 共用同一查询 */
export function useEpisodeItem(subject: Subject | undefined, key: string | null) {
  const p = subject ? subjectPath(subject) : null
  const q = useEpisodes(p?.provider ?? "", p?.id ?? "")
  return key ? q.data?.items.find((it) => it.key === key) : undefined
}

/** 单集完整名称："第 3 话 · 契约"（无标题时只有 "第 3 话"） */
export function episodeFullLabel(k: EpisodeKey, title?: string | null) {
  return title ? `${episodeLabel(k)} · ${title}` : episodeLabel(k)
}

/** 按类型分组（正片在前） */
function groups(chips: EpisodeChip[]) {
  return EPISODE_KINDS.map((kind) => ({ kind, items: chips.filter((c) => c.kind === kind) })).filter((g) => g.items.length > 0)
}

/** 选集每页最多显示的集数（正片在前、特别篇在后，统一分页）：上千集的番剧（名侦探柯南）也能用 */
export const EPISODE_PAGE = 30

/**
 * 选集分页：默认显示 anchor（当前集 / 最新一集）所在的那一页；anchor 变化时跳到它所在的页。
 * pager 是左右箭头（放在「选集」标题行最右侧），不超过一页时为 null。items 交给 EpisodeGrid / EpisodeRows。
 */
export function useEpisodePage(chips: EpisodeChip[], anchor?: string | null) {
  const ordered = useMemo(() => groups(chips).flatMap((g) => g.items), [chips])
  const pages = Math.max(1, Math.ceil(ordered.length / EPISODE_PAGE))
  const at = anchor ? ordered.findIndex((c) => c.key === anchor) : -1
  const anchorPage = at >= 0 ? Math.floor(at / EPISODE_PAGE) : 0
  const [page, setPage] = useState(anchorPage)
  const [lastAnchor, setLastAnchor] = useState(anchorPage)
  if (lastAnchor !== anchorPage) {
    // 渲染期间同步（随 props 调整 state），切到新集时跟着翻页
    setLastAnchor(anchorPage)
    setPage(anchorPage)
  }
  const cur = Math.min(page, pages - 1)
  const items = ordered.slice(cur * EPISODE_PAGE, (cur + 1) * EPISODE_PAGE)
  const range = (i: number) => {
    const part = ordered.slice(i * EPISODE_PAGE, (i + 1) * EPISODE_PAGE)
    return part.length ? `${episodeShort(part[0])}–${episodeShort(part[part.length - 1])}` : ""
  }
  const arrow = "inline-flex h-7 w-7 items-center justify-center rounded-md border bg-card text-muted-foreground hover:text-primary-text disabled:pointer-events-none disabled:opacity-40"
  const pager =
    pages > 1 ? (
      <span role="group" aria-label={`选集翻页（当前 ${range(cur)}）`} className="inline-flex shrink-0 items-center gap-1">
        <button type="button" aria-label={cur > 0 ? `上一页：${range(cur - 1)}` : "上一页"} disabled={cur === 0} onClick={() => setPage(cur - 1)} className={arrow}>
          <ChevronLeft className="h-4 w-4" />
        </button>
        <button
          type="button"
          aria-label={cur < pages - 1 ? `下一页：${range(cur + 1)}` : "下一页"}
          disabled={cur >= pages - 1}
          onClick={() => setPage(cur + 1)}
          className={arrow}
        >
          <ChevronRight className="h-4 w-4" />
        </button>
      </span>
    ) : null
  return { items, pager }
}

/** 正片集数 / 特别篇个数的摘要："12 话 · 4 个特别篇" */
export function episodeSummary(chips: EpisodeChip[]) {
  const main = chips.filter((c) => c.kind === "main").length
  const sp = chips.length - main
  return [main > 0 ? `${main} 话` : null, sp > 0 ? `${sp} 个特别篇` : null].filter(Boolean).join(" · ")
}

/**
 * 选集按钮。variant：
 * - side：单集页右栏。移动端单行横向滚动，桌面端每行 6 个；当前集高亮并滚动到可见区域。
 * - page：番剧页，网格换行。
 * 两种都把特别篇（NCOP1、SP1…）直接接在正片之后，同一个列表，不分组、不加分隔线。
 * 资源数为 0 的集弱化显示；未播出（且无资源）的集用虚线边框、更浅的文字（仍可点击）。
 */
export function EpisodeGrid({
  subject,
  chips,
  current,
  highlight,
  variant,
}: {
  subject: Subject
  chips: EpisodeChip[]
  /** 当前集（高亮、aria-current） */
  current?: string | null
  /** 弱高亮（番剧页的最新一集） */
  highlight?: string | null
  variant: "side" | "page"
}) {
  const list = useRef<HTMLDivElement>(null)
  const cur = useRef<HTMLAnchorElement>(null)
  const side = variant === "side"

  // 当前集滚动到可见区域中间（只滚动列表本身，不滚动页面）
  useEffect(() => {
    const box = list.current
    const el = cur.current
    if (!side || !box || !el) return
    if (box.scrollWidth > box.clientWidth) box.scrollLeft = el.offsetLeft - (box.clientWidth - el.offsetWidth) / 2
    if (box.scrollHeight > box.clientHeight) box.scrollTop = el.offsetTop - (box.clientHeight - el.offsetHeight) / 2
  }, [side, current, chips.length])

  const chip = (c: EpisodeChip) => {
    const active = c.key === current
    const future = !active && isFuture(c)
    const muted = !active && c.sources === 0
    return (
      <Link
        key={c.key}
        ref={active ? cur : undefined}
        to={episodeHref(subject, c.key)}
        aria-current={active ? "page" : undefined}
        title={chipTooltip(c)}
        className={cn(
          "inline-flex h-10 shrink-0 items-center justify-center border font-medium tabular-nums transition-colors",
          c.kind === "main" ? "text-sm" : "text-[13px]",
          side
            ? cn(
                "min-w-[3.25rem] rounded-lg bg-card px-2 lg:min-w-0",
                active
                  ? "border-primary bg-primary-soft font-semibold text-primary-text"
                  : "hover:border-primary/60 hover:text-primary-text lg:border-transparent lg:bg-muted/50 lg:hover:bg-card",
              )
            : cn(
                "rounded-md bg-card px-1 hover:border-primary hover:text-primary-text",
                active && "border-primary bg-primary-soft font-semibold text-primary-text",
                !active && c.key === highlight && "border-primary/50 text-primary-text",
              ),
          muted && "text-muted-foreground/60",
          future && "border-dashed border-border bg-transparent text-muted-foreground/45 lg:border-border lg:bg-transparent",
        )}
      >
        {episodeShort(c)}
      </Link>
    )
  }

  // 正片与特别篇（NCOP1、SP1…）排在同一个列表里，不分组、不加分隔线；特别篇按种类接在正片之后（分页见 useEpisodePage）
  const ordered = groups(chips).flatMap((g) => g.items)
  if (side)
    return (
      <>
        <div
          ref={list}
          className={cn(
            // 手机：单行横向滑动；sm 以上（含单栏的桌面宽度）换行，鼠标不能横向拖动
            "relative -mx-3 flex gap-2 overflow-x-auto px-3 pb-1 scrollbar-none",
            "sm:mx-0 sm:grid sm:grid-cols-[repeat(auto-fill,minmax(3.5rem,1fr))] sm:overflow-visible sm:px-0 sm:pb-0 lg:grid-cols-6",
          )}
        >
          {ordered.map(chip)}
        </div>
      </>
    )

  return <div className="grid grid-cols-5 gap-2 sm:grid-cols-8 lg:grid-cols-12">{ordered.map(chip)}</div>
}

/** 列表视图折叠：显示的集数 / 超过多少集才折叠 / 最新一集之后多显示几集（移动端与 sm 以上分别计算） */
const FOLD = { mobile: { size: 6, min: 8, lead: 3 }, wide: { size: 12, min: 15, lead: 4 } }

/** 折叠窗口 [start, end)；不需要折叠时为整个列表 */
function foldWindow(len: number, hi: number, f: { size: number; min: number; lead: number }): [number, number] {
  if (len <= f.min) return [0, len]
  const start = Math.max(0, Math.min(hi - f.size + f.lead, len - f.size))
  return [start, start + f.size]
}

/**
 * 番剧页的列表视图：每集一行（编号 · 标题 · 放送日期 · 资源数），移动端 1 列、平板 2 列、桌面 3 列。
 * 正片较多时折叠（移动端 6 集、sm 以上 12 集；连载中时窗口包含 highlight）；特别篇分组列在正片之后。
 */
export function EpisodeRows({ subject, chips, highlight }: { subject: Subject; chips: EpisodeChip[]; highlight?: string | null }) {
  const [expanded, setExpanded] = useState(false)
  const gs = groups(chips)
  const main = gs.find((g) => g.kind === "main")?.items ?? []
  const specials = gs.filter((g) => g.kind !== "main").flatMap((g) => g.items)
  // 连载中（还有未播出的集）时折叠窗口包含最新一集，否则从第 1 话开始
  const hi = main.some(isFuture) ? Math.max(0, main.findIndex((c) => c.key === highlight)) : 0
  const [m0, m1] = foldWindow(main.length, hi, FOLD.mobile)
  const [w0, w1] = foldWindow(main.length, hi, FOLD.wide)
  const foldMobile = main.length > FOLD.mobile.min
  const foldWide = main.length > FOLD.wide.min
  const grid = "grid gap-2 sm:grid-cols-2 lg:grid-cols-3"
  const row = (c: EpisodeChip, i?: number) => (
    <EpisodeRow
      key={c.key}
      subject={subject}
      c={c}
      highlight={c.key === highlight}
      className={cn(i !== undefined && !expanded && (i < m0 || i >= m1) && "max-sm:hidden", i !== undefined && !expanded && (i < w0 || i >= w1) && "sm:hidden")}
    />
  )
  return (
    <div className="space-y-3">
      {/* 正片与特别篇同一个列表，不分组、不加分隔线（折叠只作用于正片） */}
      <div className={grid}>
        {main.map((c, i) => row(c, i))}
        {specials.map((c) => row(c))}
      </div>
      {foldMobile && (
        <button
          type="button"
          onClick={() => setExpanded((v) => !v)}
          aria-expanded={expanded}
          className={cn(
            "flex h-9 w-full items-center justify-center gap-1 rounded-lg border bg-card text-[13px] text-muted-foreground hover:border-primary/50 hover:text-primary-text",
            !foldWide && "sm:hidden",
          )}
        >
          {expanded ? "收起" : `展开全部 ${main.length} 话`}
          <ChevronDown className={cn("h-4 w-4 transition-transform", expanded && "rotate-180")} />
        </button>
      )}
    </div>
  )
}

function EpisodeRow({ subject, c, highlight, className }: { subject: Subject; c: EpisodeChip; highlight: boolean; className?: string }) {
  const future = isFuture(c)
  const none = !future && c.sources === 0
  return (
    <Link
      to={episodeHref(subject, c.key)}
      title={chipTooltip(c)}
      className={cn(
        "group flex h-14 min-w-0 items-center gap-3 rounded-lg border bg-card px-3 transition-colors hover:border-primary/60",
        highlight && "border-primary/50 bg-primary-soft/50",
        future && "border-dashed bg-transparent",
        className,
      )}
    >
      <span
        className={cn(
          // 纯数字，无底色 / 边框（卡片本身已有边框，避免框套框）
          "min-w-8 shrink-0 whitespace-nowrap text-center text-base font-semibold tabular-nums",
          future ? "text-muted-foreground/60" : none ? "text-muted-foreground" : "text-primary-text",
        )}
      >
        {episodeShort(c)}
      </span>
      <span className="min-w-0 flex-1">
        <span className={cn("block truncate text-[13px] font-medium leading-5 group-hover:text-primary-text", future ? "text-muted-foreground/70" : none && "text-foreground/70")}>
          {c.title || (c.kind === "main" ? c.label : `${c.label} · ${KIND_DESC[c.kind]}`)}
        </span>
        <span className="flex items-center gap-1.5 text-[11px] leading-4 text-muted-foreground tabular-nums">
          {c.airdate && <span>{c.airdate}</span>}
          {c.airdate && <span aria-hidden>·</span>}
          {future ? (
            <span>未播出</span>
          ) : c.sources === undefined ? null : c.sources > 0 ? (
            <span className="text-primary-text">{c.sources} 个资源</span>
          ) : (
            <span>暂无资源</span>
          )}
        </span>
      </span>
    </Link>
  )
}
