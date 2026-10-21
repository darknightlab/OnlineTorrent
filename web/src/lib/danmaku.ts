// 服务器弹幕 → artplayer-plugin-danmuku 的格式（pages/watch/danmaku.ts）
import type { DanmakuItem } from "./types"

/** 插件的弹幕（与 artplayer-plugin-danmuku 的 Danmu 相同的字段） */
export interface PluginDanmu {
  text: string
  time: number
  /** 0 滚动、1 顶部、2 底部 */
  mode: 0 | 1 | 2
  color: string
}

/** 服务器的模式（bilibili 写法：1 滚动、4 底部、5 顶部）→ 插件的模式 */
const MODES: Record<number, 0 | 1 | 2> = { 1: 0, 5: 1, 4: 2 }

export function toDanmu(items: DanmakuItem[]): PluginDanmu[] {
  const out: PluginDanmu[] = []
  for (const [time, mode, color, text] of items) {
    if (!text || !(time >= 0)) continue
    out.push({ text, time, mode: MODES[mode] ?? 0, color: `#${(color & 0xffffff).toString(16).padStart(6, "0")}` })
  }
  return out
}
