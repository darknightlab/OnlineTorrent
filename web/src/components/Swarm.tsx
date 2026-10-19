// 做种统计显示：「BT 23 · WebRTC 1 · 服务器已缓存」，未统计时显示「做种人数未知」
import { Server, Users } from "lucide-react"
import type { SwarmStats } from "@/lib/types"
import { formatDateTime } from "@/lib/format"
import { cn } from "@/lib/utils"
import { Badge } from "./ui/badge"

/** 是否所有已知来源都为 0（与用户无关的通用判断，列表页用；单集页用 rank 的 dead） */
export function swarmDead(s: SwarmStats | null | undefined) {
  if (!s?.checked_at || s.server_complete) return false
  if (s.bt_seeders === null && s.wt_seeders === null) return false
  return (s.bt_seeders ?? 0) === 0 && (s.wt_seeders ?? 0) === 0
}

export function SwarmInfo({ swarm, dead, className }: { swarm?: SwarmStats | null; dead?: boolean; className?: string }) {
  const s = swarm ?? null
  const known = !!s?.checked_at
  const parts: string[] = []
  if (known && s!.bt_seeders !== null) parts.push(`BT ${s!.bt_seeders}`)
  if (known && s!.wt_seeders !== null) parts.push(`WebRTC ${s!.wt_seeders}`)
  const title = known
    ? [
        s!.bt_seeders !== null ? `BT 做种 ${s!.bt_seeders} / 下载 ${s!.bt_leechers ?? 0}` : "BT：tracker 无响应",
        s!.wt_seeders !== null ? `WebRTC 做种 ${s!.wt_seeders} / 下载 ${s!.wt_leechers ?? 0}` : "WebRTC：tracker 无响应",
        s!.server_complete ? "服务器已完整缓存" : null,
        `统计于 ${formatDateTime(s!.checked_at!)}`,
      ]
        .filter(Boolean)
        .join("\n")
    : s?.server_complete
      ? "服务器已完整缓存；做种人数尚未统计"
      : "做种人数尚未统计"
  return (
    <span className={cn("inline-flex min-w-0 items-center gap-1.5 text-[11px] leading-4 text-muted-foreground", className)} title={title}>
      {dead && <Badge variant="destructive">无人做种</Badge>}
      {parts.length > 0 && (
        <span className="inline-flex items-center gap-1 tabular-nums">
          <Users className="h-3 w-3 shrink-0" />
          {parts.join(" · ")}
        </span>
      )}
      {s?.server_complete && (
        <span className="inline-flex items-center gap-1 text-primary-text">
          <Server className="h-3 w-3 shrink-0" />
          服务器已缓存
        </span>
      )}
      {!known && !s?.server_complete && <span className="text-muted-foreground/70">做种人数未知</span>}
    </span>
  )
}
