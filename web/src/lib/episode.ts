// 集的键（URL 与 API 共用；与后端 internal/meta/episode.go 的语法一致）：
//   正片：数字原样 "12"、"12.5"、"0"
//   特别篇：小写类型 + 编号 "sp1"、"ncop1"、"nced2"、"pv1"、"cm1"
// 没有编号的特别篇记为 1（"ncop" 是 "ncop1" 的别名）；解析不区分大小写、忽略前导零（"SP01" → "sp1"），
// formatEpisodeKey 给出规范写法。本文件不依赖其他模块（node --test 直接运行 episode.test.ts）。

export type EpisodeKind = "main" | "sp" | "ncop" | "nced" | "pv" | "cm"

export interface EpisodeKey {
  kind: EpisodeKind
  /** ≥ 0，可为小数（12.5） */
  number: number
}

/** 选集中的显示顺序：正片，然后 SP、NCOP、NCED、PV、CM */
export const EPISODE_KINDS: readonly EpisodeKind[] = ["main", "sp", "ncop", "nced", "pv", "cm"]

/** 特别篇分组标题 */
export const EPISODE_KIND_LABEL: Record<EpisodeKind, string> = { main: "正片", sp: "SP", ncop: "NCOP", nced: "NCED", pv: "PV", cm: "CM" }

const MAX_MAIN = 5000
const MAX_SPECIAL = 999
const RE = /^(sp|ncop|nced|pv|cm)?(\d{1,4}(?:\.\d{1,3})?)?$/i

/** 解析集的键；无效时返回 null */
export function parseEpisodeKey(raw: string | null | undefined): EpisodeKey | null {
  const m = RE.exec((raw ?? "").trim())
  if (!m || (!m[1] && !m[2])) return null
  const kind = (m[1]?.toLowerCase() ?? "main") as EpisodeKind
  const number = m[2] ? Number(m[2]) : 1
  if (!Number.isFinite(number) || number < 0 || number > (kind === "main" ? MAX_MAIN : MAX_SPECIAL)) return null
  return { kind, number }
}

/** 规范写法："12.5"、"ncop1" */
export function formatEpisodeKey(k: EpisodeKey): string {
  return k.kind === "main" ? String(k.number) : `${k.kind}${k.number}`
}

/** 选集按钮文字："12.5"、"SP1"、"NCOP1" */
export function episodeShort(k: EpisodeKey): string {
  return k.kind === "main" ? String(k.number) : `${k.kind.toUpperCase()}${k.number}`
}

/** 显示名："第 12 话"、"SP1"、"NCOP1" */
export function episodeLabel(k: EpisodeKey): string {
  return k.kind === "main" ? `第 ${k.number} 话` : episodeShort(k)
}

/** 规范化任意写法的键（无效返回 null）："SP01" → "sp1"、"012" → "12" */
export function canonicalEpisodeKey(raw: string | null | undefined): string | null {
  const k = parseEpisodeKey(raw)
  return k ? formatEpisodeKey(k) : null
}

/** 排序：正片按编号，然后 sp、ncop、nced、pv、cm 各自按编号 */
export function compareEpisodeKeys(a: EpisodeKey, b: EpisodeKey): number {
  return EPISODE_KINDS.indexOf(a.kind) - EPISODE_KINDS.indexOf(b.kind) || a.number - b.number
}
