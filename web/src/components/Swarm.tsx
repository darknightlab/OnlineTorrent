// 做种统计显示：「BT ↑23 ↓7 · WebRTC ↑1 ↓0」（做种 / 下载中），未统计时显示「做种人数未知」；
// 对当前用户无人做种时显示「无人做种」。本站服务器是否缓存不对用户显示。
import type { SwarmStats } from "@/lib/types"
import { formatDateTime } from "@/lib/format"
import { PERM, can, useMe } from "@/lib/queries"
import { swarmDead, type Viewer } from "@/lib/rank"
import { cn } from "@/lib/utils"
import { Badge } from "./ui/badge"

/** 当前用户与做种统计相关的权限（无人做种的判断依赖它） */
export function useSwarmViewer(): Viewer {
  const me = useMe()
  return { canSeed: can(me.data, PERM.seed), canWebseed: can(me.data, PERM.webseed) }
}

function Counts({ label, short, up, down, title }: { label: string; short?: string; up: number | null; down: number | null; title: string }) {
  const unknown = up === null
  return (
    <span className="inline-flex items-center gap-1 whitespace-nowrap tabular-nums" title={unknown ? `${title}：tracker 无响应` : `${title}：${up} / ${down ?? 0}`}>
      {short ? (
        <>
          <span className="sm:hidden">{short}</span>
          <span className="hidden sm:inline">{label}</span>
        </>
      ) : (
        label
      )}
      {unknown ? (
        <span aria-label="未知">—</span>
      ) : (
        <span>
          <span aria-label="做种">↑{up}</span> <span aria-label="下载中">↓{down ?? 0}</span>
        </span>
      )}
    </span>
  )
}

/**
 * dead：由调用方给出（单集页用推荐评分的结果）；省略时按当前用户权限判断。
 */
export function SwarmInfo({ swarm, dead, className }: { swarm?: SwarmStats | null; dead?: boolean; className?: string }) {
  const viewer = useSwarmViewer()
  const s = swarm ?? null
  const known = !!s?.checked_at
  const isDead = dead ?? swarmDead(s, viewer)
  return (
    <span
      className={cn("inline-flex min-w-0 flex-wrap items-center gap-x-1.5 gap-y-0.5 text-[11px] leading-4 text-muted-foreground", className)}
      title={known ? `统计于 ${formatDateTime(s!.checked_at!)}` : "做种人数尚未统计"}
    >
      {isDead && <Badge variant="destructive">无人做种</Badge>}
      {known ? (
        <>
          <Counts label="BT" up={s!.bt_seeders} down={s!.bt_leechers} title="BT 做种 / 下载中" />
          <span aria-hidden>·</span>
          <Counts label="WebRTC" short="RTC" up={s!.wt_seeders} down={s!.wt_leechers} title="WebRTC 做种 / 下载中" />
        </>
      ) : (
        <span className="text-muted-foreground/70">做种人数未知</span>
      )}
    </span>
  )
}
