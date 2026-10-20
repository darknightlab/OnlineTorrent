// 字幕选择的会话偏好：用户在「字幕」菜单里明确选过（某个轨道或关闭）后，本次会话里换文件 / 换集也沿用；
// 没选过时按浏览器语言自动选（lang.ts pickDefault）。
import { pickDefault, trackLangs, type TrackLangInput } from "./lang.ts"

export type SubPref = { mode: "off" } | { mode: "track"; name: string; langs: string[] }

const KEY = "ot.subtitle.pref"

export function loadPref(storage: Pick<Storage, "getItem"> | null = safeSession()): SubPref | null {
  try {
    const v = JSON.parse(storage?.getItem(KEY) ?? "null")
    if (v?.mode === "off") return { mode: "off" }
    if (v?.mode === "track" && typeof v.name === "string" && Array.isArray(v.langs)) return { mode: "track", name: v.name, langs: v.langs.map(String) }
  } catch {
    /* ignore */
  }
  return null
}

export function savePref(p: SubPref, storage: Pick<Storage, "setItem"> | null = safeSession()) {
  try {
    storage?.setItem(KEY, JSON.stringify(p))
  } catch {
    /* ignore */
  }
}

/** 用户选了轨道 index（-1 = 关闭）时要记下的偏好 */
export function prefFor(tracks: TrackLangInput[], index: number): SubPref {
  if (index < 0 || !tracks[index]) return { mode: "off" }
  return { mode: "track", name: tracks[index].name.trim(), langs: trackLangs(tracks[index]) }
}

/** 选哪个轨道（-1 = 不显示）：有偏好时同名轨道 → 同语言的轨道 → 按浏览器语言；关闭偏好 → 不显示 */
export function chooseTrack(tracks: TrackLangInput[], pref: SubPref | null, languages: readonly string[]): number {
  if (pref?.mode === "off") return -1
  if (pref?.mode === "track") {
    if (pref.name) {
      const i = tracks.findIndex((t) => t.name.trim() === pref.name)
      if (i >= 0) return i
    }
    if (pref.langs.length) {
      const key = pref.langs.join("+")
      const i = tracks.findIndex((t) => trackLangs(t).join("+") === key)
      if (i >= 0) return i
    }
  }
  return pickDefault(tracks, languages)
}

function safeSession(): Storage | null {
  try {
    return typeof sessionStorage === "undefined" ? null : sessionStorage
  } catch {
    return null
  }
}
