// 一个视频文件的全部字幕：MKV 内封轨道 + 种子里的外挂字幕文件 + 字体（MKV 附件、种子的 Fonts/ 文件夹）。
// 「字幕」菜单与默认选择都基于这里的统一列表。
import type { SubtitleSource } from "./source.ts"
import type { SubTrack } from "./mkv.ts"
import { externalLabels, type ExternalSubInfo } from "./external.ts"
import { trackLabel, type TrackLangInput } from "./lang.ts"
import { chooseTrack, type SubPref } from "./prefs.ts"

export interface ExternalTrack extends ExternalSubInfo {
  /** 下载并转换成完整的 ASS 文本（第一次显示时调用；低优先级下载） */
  load(): Promise<string>
}

export interface SubtitleSet {
  /** MKV 内封字幕（不是 MKV 时为 null） */
  embedded: SubtitleSource | null
  external: ExternalTrack[]
  /** 字体：MKV 附件 + 种子里 Fonts/ 文件夹的字体（第一次显示 ASS 时读取，合计过大时跳过） */
  fonts(): Promise<{ fonts: Uint8Array[]; attachments: number; files: number }>
}

export type MenuTrack =
  | { kind: "embedded"; label: string; input: TrackLangInput; track: SubTrack }
  | { kind: "external"; label: string; input: TrackLangInput; ext: ExternalTrack }

/** 菜单顺序：内封轨道在前，外挂文件在后 */
export function buildMenu(embedded: SubTrack[], external: ExternalTrack[]): MenuTrack[] {
  const labels = externalLabels(external)
  return [
    ...embedded.map((t, i): MenuTrack => ({ kind: "embedded", label: trackLabel(t, i), input: t, track: t })),
    // 外挂文件按「默认轨道」计分（与 MKV 的默认标记同权）：发布者特意附带的
    ...external.map((e, i): MenuTrack => ({ kind: "external", label: labels[i], input: { name: e.name, language: "", languageIETF: "", langs: e.langs, isDefault: true } as TrackLangInput, ext: e })),
  ]
}

/**
 * 选哪个（-1 = 不显示）。会话偏好（同名 → 同语言）与浏览器语言评分（lang.ts pickDefault）都在
 * 「外挂在前、内封在后」的顺序上进行，同分取先出现的；外挂文件计分时视同 MKV 的默认轨道。
 * 即外挂字幕与内封轨道对浏览器语言的匹配程度相同时选外挂（发布者特意附带的，通常更新 / 更全），
 * 内封轨道匹配得更好（例如外挂是双语、内封是单语）时选内封。
 */
export function chooseMenu(menu: MenuTrack[], pref: SubPref | null, languages: readonly string[]): number {
  const order = [...menu.keys()].sort((a, b) => Number(menu[a].kind === "embedded") - Number(menu[b].kind === "embedded") || a - b)
  const i = chooseTrack(
    order.map((k) => menu[k].input),
    pref,
    languages,
  )
  return i < 0 ? -1 : order[i]
}
