// 选集：正片 + 特别篇（SP / NCOP / NCED / PV / CM）。单集页右栏（side）与番剧页（page）共用。
import { useEffect, useMemo, useRef } from "react"
import { Link } from "react-router-dom"
import { useEpisodes } from "@/lib/queries"
import {
  EPISODE_KIND_LABEL,
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
      ? q.data.items.map((it) => ({ kind: it.kind, number: it.number, key: it.key, label: it.label, sources: it.sources }))
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
  return { chips, loading: q.isPending && !!p }
}

/** 按类型分组（正片在前） */
function groups(chips: EpisodeChip[]) {
  return EPISODE_KINDS.map((kind) => ({ kind, items: chips.filter((c) => c.kind === kind) })).filter((g) => g.items.length > 0)
}

/** 正片集数 / 特别篇个数的摘要："12 话 · 4 个特别篇" */
export function episodeSummary(chips: EpisodeChip[]) {
  const main = chips.filter((c) => c.kind === "main").length
  const sp = chips.length - main
  return [main > 0 ? `${main} 话` : null, sp > 0 ? `${sp} 个特别篇` : null].filter(Boolean).join(" · ")
}

/**
 * 选集按钮。variant：
 * - side：单集页右栏。移动端单行横向滚动（特别篇接在正片后，以细分隔线 + 分组名隔开），桌面端正片每行 6 个、
 *   特别篇分组每行 4 个；当前集高亮并滚动到可见区域。
 * - page：番剧页，网格换行，特别篇分组带小标题。
 * 资源数为 0 的集弱化显示。
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
    const muted = !active && c.sources === 0
    return (
      <Link
        key={c.key}
        ref={active ? cur : undefined}
        to={episodeHref(subject, c.key)}
        aria-current={active ? "page" : undefined}
        title={`${c.kind === "main" ? c.label : `${c.label} · ${KIND_DESC[c.kind]}`}${c.sources === 0 ? "（暂无资源）" : ""}`}
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
        )}
      >
        {episodeShort(c)}
      </Link>
    )
  }

  const gs = groups(chips)
  const main = gs.find((g) => g.kind === "main")
  const specials = gs.filter((g) => g.kind !== "main")
  if (side)
    // 移动端：所有按钮在同一行（分组容器为 display: contents），特别篇各组前加细分隔线 + 组名；
    // 桌面端：正片网格每行 6 个，特别篇在分隔线下方每组一行（左侧组名）
    return (
      <div
        ref={list}
        className={cn(
          "relative -mx-3 flex gap-2 overflow-x-auto px-3 pb-1 scrollbar-none sm:-mx-4 sm:px-4",
          "lg:mx-0 lg:block lg:max-h-[15rem] lg:overflow-y-auto lg:overflow-x-visible lg:px-0 lg:pb-0",
        )}
      >
        {main && <div className="contents lg:grid lg:grid-cols-6 lg:gap-2">{main.items.map(chip)}</div>}
        {specials.length > 0 && (
          <div className={cn("contents lg:block lg:space-y-2", main && "lg:mt-3 lg:border-t lg:pt-3")}>
            {specials.map((g, i) => (
              <div key={g.kind} role="group" aria-label={KIND_DESC[g.kind]} className="contents lg:flex lg:items-start lg:gap-2">
                <span className="flex shrink-0 items-center gap-2 lg:w-10 lg:pt-3" title={KIND_DESC[g.kind]}>
                  {(main || i > 0) && <span aria-hidden className="ml-1 h-6 w-px bg-border lg:hidden" />}
                  <span className="text-[11px] font-medium leading-4 text-muted-foreground">{EPISODE_KIND_LABEL[g.kind]}</span>
                </span>
                <div className="contents lg:grid lg:min-w-0 lg:flex-1 lg:grid-cols-4 lg:gap-2">{g.items.map(chip)}</div>
              </div>
            ))}
          </div>
        )}
      </div>
    )

  // 番剧页：正片与特别篇共用同一套列宽；特别篇每组从新的一行开始，第一格为组名
  const grid = "grid grid-cols-5 gap-2 sm:grid-cols-8 lg:grid-cols-12"
  return (
    <div className="space-y-3">
      {main && <div className={grid}>{main.items.map(chip)}</div>}
      {specials.length > 0 && (
        <div className={cn(grid, main && "border-t pt-3")}>
          {specials.map((g) => (
            <div key={g.kind} role="group" aria-label={KIND_DESC[g.kind]} className="contents">
              <span className="col-start-1 flex h-10 min-w-0 flex-col justify-center leading-tight">
                <span className="text-xs font-semibold text-foreground/80">{EPISODE_KIND_LABEL[g.kind]}</span>
                <span className="truncate text-[11px] text-muted-foreground">{KIND_DESC[g.kind]}</span>
              </span>
              {g.items.map(chip)}
            </div>
          ))}
        </div>
      )}
    </div>
  )
}
