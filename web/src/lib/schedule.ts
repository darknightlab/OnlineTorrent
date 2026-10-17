import type { ScheduleItem } from "./types"

const DAY = 86400_000

export type AirStatus = "updated" | "soon" | null

/** 已更新：24 小时内播出；即将播出：24 小时内将播出 */
export function airStatus(item: ScheduleItem, now = Date.now()): AirStatus {
  const last = item.last_air_at ? new Date(item.last_air_at).getTime() : NaN
  if (Number.isFinite(last) && now - last >= 0 && now - last <= DAY) return "updated"
  const next = item.next_air_at ? new Date(item.next_air_at).getTime() : NaN
  if (Number.isFinite(next) && next - now > 0 && next - now <= DAY) return "soon"
  return null
}

export function byWeekday(items: ScheduleItem[], weekday: number) {
  return items.filter((i) => i.weekday === weekday).sort((a, b) => a.air_time.localeCompare(b.air_time))
}

export function displayTitle(i: { title: string; title_cn?: string | null }) {
  return i.title_cn || i.title
}

export function episodeLabel(item: ScheduleItem) {
  if (item.episodes_aired > 0) return `第 ${item.episodes_aired} 话`
  return "未开播"
}
