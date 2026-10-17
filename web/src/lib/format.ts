// 展示格式化：大小、速度、相对时间、星期（Asia/Shanghai）。

export function formatBytes(n: number | null | undefined, digits = 1) {
  if (n === null || n === undefined || !Number.isFinite(n)) return "-"
  if (n < 1024) return `${Math.round(n)} B`
  const units = ["KB", "MB", "GB", "TB"]
  let v = n / 1024
  let i = 0
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024
    i++
  }
  return `${v.toFixed(v >= 100 ? 0 : digits)} ${units[i]}`
}

export function formatSpeed(n: number) {
  return `${formatBytes(n)}/s`
}

export function formatPercent(p: number) {
  if (p >= 1) return "100%"
  return `${(p * 100).toFixed(p < 0.1 ? 1 : 0)}%`
}

export function formatDuration(ms: number) {
  if (!Number.isFinite(ms) || ms <= 0) return "-"
  const s = Math.round(ms / 1000)
  if (s < 60) return `${s} 秒`
  const m = Math.floor(s / 60)
  if (m < 60) return `${m} 分 ${s % 60} 秒`
  const h = Math.floor(m / 60)
  if (h < 48) return `${h} 小时 ${m % 60} 分`
  return `${Math.floor(h / 24)} 天`
}

export function timeAgo(iso: string | null | undefined, now = Date.now()) {
  if (!iso) return "-"
  const t = new Date(iso).getTime()
  if (!Number.isFinite(t)) return "-"
  const diff = Math.round((now - t) / 1000)
  if (diff < 0) return timeUntil(iso, now)
  if (diff < 60) return "刚刚"
  if (diff < 3600) return `${Math.floor(diff / 60)} 分钟前`
  if (diff < 86400) return `${Math.floor(diff / 3600)} 小时前`
  if (diff < 86400 * 30) return `${Math.floor(diff / 86400)} 天前`
  return formatDate(iso)
}

export function timeUntil(iso: string, now = Date.now()) {
  const diff = Math.round((new Date(iso).getTime() - now) / 1000)
  if (diff <= 60) return "即将"
  if (diff < 3600) return `${Math.floor(diff / 60)} 分钟后`
  if (diff < 86400) return `${Math.floor(diff / 3600)} 小时后`
  return `${Math.floor(diff / 86400)} 天后`
}

const TZ = "Asia/Shanghai"

export function formatDate(iso: string | null | undefined) {
  if (!iso) return "-"
  const d = new Date(iso)
  if (!Number.isFinite(d.getTime())) return "-"
  return d.toLocaleDateString("zh-CN", { timeZone: TZ, year: "numeric", month: "2-digit", day: "2-digit" })
}

export function formatDateTime(iso: string | null | undefined) {
  if (!iso) return "-"
  const d = new Date(iso)
  if (!Number.isFinite(d.getTime())) return "-"
  return d.toLocaleString("zh-CN", {
    timeZone: TZ,
    year: "numeric",
    month: "2-digit",
    day: "2-digit",
    hour: "2-digit",
    minute: "2-digit",
    hour12: false,
  })
}

export const WEEKDAYS = ["周日", "周一", "周二", "周三", "周四", "周五", "周六"]

/** 当前在 Asia/Shanghai 的星期（0=周日） */
export function todayWeekday(now = new Date()) {
  const s = now.toLocaleDateString("en-US", { timeZone: TZ, weekday: "short" })
  return ["Sun", "Mon", "Tue", "Wed", "Thu", "Fri", "Sat"].indexOf(s)
}

/** 某星期在本周（Asia/Shanghai）对应的日期 "MM/DD" */
export function weekdayDate(weekday: number, now = new Date()) {
  const today = todayWeekday(now)
  // 本周按周一开始
  const offsetFromMon = (d: number) => (d + 6) % 7
  const delta = offsetFromMon(weekday) - offsetFromMon(today)
  const d = new Date(now.getTime() + delta * 86400_000)
  return d.toLocaleDateString("zh-CN", { timeZone: TZ, month: "2-digit", day: "2-digit" })
}
