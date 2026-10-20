// 做种统计显示：「BT ↑23 ↓7」「RTC ↑1 ↓0」（做种 / 下载中）两行对齐。本站服务器是否缓存不对用户显示。
import type { SwarmStats } from "@/lib/types"
import { formatDateTime } from "@/lib/format"
import { PERM, can, useMe } from "@/lib/queries"
import { type Viewer } from "@/lib/rank"

/** 当前用户与做种统计相关的权限（无人做种的判断依赖它） */
export function useSwarmViewer(): Viewer {
  const me = useMe()
  return { canSeed: can(me.data, PERM.seed), canWebseed: can(me.data, PERM.webseed) }
}

/**
 * 做种统计两行（BT / RTC）：三列网格 —— 名称左对齐、↑ 与 ↑ 对齐、↓ 与 ↓ 对齐（位数不同也对齐）。
 * 未统计 / tracker 无响应时该行显示「—」。
 */
export function SwarmGrid({ swarm }: { swarm?: SwarmStats | null }) {
  const s = swarm ?? null
  const known = !!s?.checked_at
  const rows = [
    { name: "BT", full: "BT", up: known ? s!.bt_seeders : null, down: known ? s!.bt_leechers : null },
    { name: "RTC", full: "WebRTC", up: known ? s!.wt_seeders : null, down: known ? s!.wt_leechers : null },
  ]
  return (
    <span className="grid grid-cols-[auto_auto_auto] gap-x-1.5 text-left tabular-nums">
      {rows.map((r) => {
        const title = !known
          ? "做种人数尚未统计"
          : r.up === null
            ? `${r.full}：tracker 无响应`
            : `${r.full} 做种 ${r.up} / 下载中 ${r.down ?? 0}（统计于 ${formatDateTime(s!.checked_at!)}）`
        return (
          <span key={r.name} className="contents" title={title}>
            <span title={title}>{r.name}</span>
            {r.up === null ? (
              <span className="col-span-2" title={title} aria-label="未知">
                —
              </span>
            ) : (
              <>
                <span title={title} aria-label="做种">
                  ↑{r.up}
                </span>
                <span title={title} aria-label="下载中">
                  ↓{r.down ?? 0}
                </span>
              </>
            )}
          </span>
        )
      })}
    </span>
  )
}
