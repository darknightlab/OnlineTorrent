// 做种统计显示：「BT ↑23 ↓7」「WebRTC ↑1 ↓0」（做种 / 下载中）各一行。本站服务器是否缓存不对用户显示。
import type { SwarmStats } from "@/lib/types"
import { formatDateTime } from "@/lib/format"
import { PERM, can, useMe } from "@/lib/queries"
import { type Viewer } from "@/lib/rank"

/** 当前用户与做种统计相关的权限（无人做种的判断依赖它） */
export function useSwarmViewer(): Viewer {
  const me = useMe()
  return { canSeed: can(me.data, PERM.seed), canWebseed: can(me.data, PERM.webseed) }
}

/** 一行做种统计：「BT ↑23 ↓7」；未统计 / tracker 无响应时「BT —」 */
export function SwarmLine({ swarm, kind }: { swarm?: SwarmStats | null; kind: "bt" | "wt" }) {
  const s = swarm ?? null
  const known = !!s?.checked_at
  const up = known ? (kind === "bt" ? s!.bt_seeders : s!.wt_seeders) : null
  const down = known ? (kind === "bt" ? s!.bt_leechers : s!.wt_leechers) : null
  const name = kind === "bt" ? "BT" : "WebRTC"
  const title = !known
    ? "做种人数尚未统计"
    : up === null
      ? `${name}：tracker 无响应`
      : `${name} 做种 ${up} / 下载中 ${down ?? 0}（统计于 ${formatDateTime(s!.checked_at!)}）`
  return (
    <span className="block whitespace-nowrap tabular-nums" title={title}>
      {kind === "bt" ? (
        "BT"
      ) : (
        <>
          <span className="sm:hidden">RTC</span>
          <span className="hidden sm:inline">WebRTC</span>
        </>
      )}{" "}
      {up === null ? (
        <span aria-label="未知">—</span>
      ) : (
        <>
          <span aria-label="做种">↑{up}</span> <span aria-label="下载中">↓{down ?? 0}</span>
        </>
      )}
    </span>
  )
}
