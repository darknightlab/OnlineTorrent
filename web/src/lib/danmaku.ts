// 弹幕：服务器格式 → artplayer-plugin-danmuku 的格式（pages/watch/danmaku.ts）、显示设置（每个浏览器保存）、
// 发送前的校验（与服务端 internal/api/danmaku.go 一致）
import type { DanmakuItem } from "./types"

/** 插件的弹幕（与 artplayer-plugin-danmuku 的 Danmu 相同的字段） */
export interface PluginDanmu {
  text: string
  time: number
  /** 0 滚动、1 顶部、2 底部 */
  mode: 0 | 1 | 2
  color: string
  /** 自己发送的弹幕加边框 */
  border?: boolean
}

/** 服务器的模式（bilibili 写法：1 滚动、4 底部、5 顶部）→ 插件的模式 */
const MODES: Record<number, 0 | 1 | 2> = { 1: 0, 5: 1, 4: 2 }

export function toDanmu(items: DanmakuItem[]): PluginDanmu[] {
  const out: PluginDanmu[] = []
  for (const [time, mode, color, text, own] of items) {
    if (!text || !(time >= 0)) continue
    const d: PluginDanmu = { text, time, mode: MODES[mode] ?? 0, color: `#${(color & 0xffffff).toString(16).padStart(6, "0")}` }
    if (own === 1) d.border = true
    out.push(d)
  }
  return out
}

// ---- 显示设置 ----

export type DanmakuSize = "s" | "m" | "l"
export type DanmakuSpeed = "slow" | "normal" | "fast"
/** 显示区域：从顶部起占画面的比例 */
export type DanmakuArea = 0.25 | 0.5 | 0.75 | 1

export interface DanmakuSettings {
  visible: boolean
  /** 0.1 – 1 */
  opacity: number
  size: DanmakuSize
  speed: DanmakuSpeed
  area: DanmakuArea
}

export const DEFAULT_DANMAKU_SETTINGS: DanmakuSettings = { visible: true, opacity: 1, size: "m", speed: "normal", area: 0.75 }

export const SIZE_OPTIONS: { value: DanmakuSize; label: string }[] = [
  { value: "s", label: "小" },
  { value: "m", label: "中" },
  { value: "l", label: "大" },
]
export const SPEED_OPTIONS: { value: DanmakuSpeed; label: string }[] = [
  { value: "slow", label: "慢" },
  { value: "normal", label: "中" },
  { value: "fast", label: "快" },
]
export const AREA_OPTIONS: { value: DanmakuArea; label: string }[] = [
  { value: 0.25, label: "1/4" },
  { value: 0.5, label: "1/2" },
  { value: 0.75, label: "3/4" },
  { value: 1, label: "全屏" },
]

const KEY = "ot.danmaku.settings"

/** 读取并规范化（无效值回到默认） */
export function parseDanmakuSettings(raw: unknown): DanmakuSettings {
  const d = DEFAULT_DANMAKU_SETTINGS
  if (!raw || typeof raw !== "object") return { ...d }
  const r = raw as Record<string, unknown>
  const opacity = typeof r.opacity === "number" && Number.isFinite(r.opacity) ? Math.min(1, Math.max(0.1, r.opacity)) : d.opacity
  return {
    visible: typeof r.visible === "boolean" ? r.visible : d.visible,
    opacity: Math.round(opacity * 100) / 100,
    size: SIZE_OPTIONS.some((o) => o.value === r.size) ? (r.size as DanmakuSize) : d.size,
    speed: SPEED_OPTIONS.some((o) => o.value === r.speed) ? (r.speed as DanmakuSpeed) : d.speed,
    area: AREA_OPTIONS.some((o) => o.value === r.area) ? (r.area as DanmakuArea) : d.area,
  }
}

export function loadDanmakuSettings(): DanmakuSettings {
  try {
    const s = globalThis.localStorage?.getItem(KEY)
    return parseDanmakuSettings(s ? JSON.parse(s) : null)
  } catch {
    return { ...DEFAULT_DANMAKU_SETTINGS }
  }
}

export function saveDanmakuSettings(s: DanmakuSettings) {
  try {
    globalThis.localStorage?.setItem(KEY, JSON.stringify(s))
  } catch {
    /* 隐私模式等：本次有效 */
  }
}

/** 设置 → 插件选项（字号按手机 / 桌面；速度为弹幕滚过画面的秒数，插件范围 1–10；区域 = 底部留白） */
export function pluginOptions(s: DanmakuSettings, mobile: boolean) {
  const sizes = mobile ? { s: 13, m: 15, l: 18 } : { s: 20, m: 24, l: 30 }
  const speeds = { slow: 8, normal: 5, fast: 3 }
  return {
    visible: s.visible,
    opacity: s.opacity,
    fontSize: sizes[s.size],
    speed: speeds[s.speed],
    margin: [10, s.area >= 1 ? 10 : `${Math.round((1 - s.area) * 100)}%`] as [number, number | `${number}%`],
  }
}

// ---- 发送 ----

export const DANMAKU_MAX_TEXT = 100

// 控制字符（含换行）与行 / 段分隔符
// eslint-disable-next-line no-control-regex
const CONTROL = /[\u0000-\u001f\u007f-\u009f\u2028\u2029]/

/** 发送前的校验：null = 可发送，否则为原因（空文本返回 ""：只禁用按钮，不提示） */
export function danmakuTextProblem(text: string): string | null {
  const t = text.trim()
  if (!t) return ""
  if ([...t].length > DANMAKU_MAX_TEXT) return `最多 ${DANMAKU_MAX_TEXT} 个字`
  if (CONTROL.test(t)) return "不能包含换行或控制字符"
  return null
}
