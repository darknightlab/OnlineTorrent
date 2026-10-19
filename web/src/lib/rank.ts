// 单集页资源推荐：纯函数，按后台「推荐规则」（GET /site 的 rank_rules）给每个文件打分并排序；
// 单集页按此排序资源列表并自动播放第一个（当前用户无权播放的资源后端已过滤）。规则与取值依据见 docs/research/rank-rules.md。
import type { Access, RankRules, SwarmStats } from "./types"

export const DEFAULT_RANK_RULES: RankRules = {
  availability: { server_complete: 2, wt_seeder: 20, wt_cap: 20, wt_scale: 20, bt_seeder: 10, bt_cap: 10, bt_scale: 20, curve: "tanh", dead: -100 },
  keywords: [
    { label: "简体中文", pattern: String.raw`简|chs|\bsc\b|\bgb\b|zh-?hans|jpsc`, score: 25, group: "字幕" },
    { label: "繁体中文", pattern: String.raw`繁|cht|\btc\b|big5|zh-?hant|jptc`, score: 20, group: "字幕" },
    { label: "日文", pattern: String.raw`\bjpn?\b|\bjapanese\b|日字|日文|日[语語]|[简繁中]日|ch[st][_&.-]?jp|jp[_&.-]?ch[st]`, score: 10, group: "字幕" },
    { label: "4K 及以上", pattern: String.raw`2160p?\b|3840x2160|4320p?\b|7680x4320|\b[48]k\b`, score: 30, group: "分辨率" },
    { label: "1080p", pattern: String.raw`1080[pi]?\b|1920x1080`, score: 20, group: "分辨率" },
    { label: "720p", pattern: String.raw`720p\b|1280x720`, score: 10, group: "分辨率" },
    { label: "HEVC", pattern: String.raw`hevc|[hx]\.?265`, score: 2, group: "" },
  ],
  teams: [],
  size: { from_mb: 200, from_score: 0, to_mb: 1000, to_score: -16, extend: true },
}

/** 当前用户的相关权限 */
export interface Viewer {
  /** 能让服务器下载做种（PERM.seed）：BT 做种者对他有意义 */
  canSeed: boolean
  /** 能使用 HTTP WebSeed（PERM.webseed） */
  canWebseed: boolean
}

/** 参与评分的文件字段（EpisodeFile 的子集） */
export interface RankInput {
  torrent_title: string
  path: string
  team: string
  size_bytes: number
  published_at: string
  access: Access
  swarm?: SwarmStats | null
}

export interface ScorePart {
  label: string
  score: number
}

export interface Scored<T extends RankInput = RankInput> {
  file: T
  /** 总分（可为小数） */
  score: number
  /** 评分明细（非 0 项） */
  parts: ScorePart[]
  /** 对当前用户而言无人做种（已统计且没有可用来源） */
  dead: boolean
  /** 做种人数已统计过（checked_at 非空） */
  known: boolean
}

interface CompiledKeyword {
  re: RegExp | null
  label: string
  score: number
  group: string
}

const compiled = new WeakMap<RankRules, CompiledKeyword[]>()

function keywordRegexps(rules: RankRules) {
  let c = compiled.get(rules)
  if (!c) {
    c = (rules.keywords ?? []).map((k) => {
      let re: RegExp | null = null
      try {
        re = new RegExp(k.pattern, "i")
      } catch {
        re = null // 服务端已校验；万一不兼容就忽略该条
      }
      return { re, label: k.label, score: k.score, group: k.group?.trim() ?? "" }
    })
    compiled.set(rules, c)
  }
  return c
}

const round1 = (x: number) => Math.round(x * 10) / 10

/**
 * 做种人数加分（都不超过上限）：linear = 每人 k 分；log = k·log2(1+n)；
 * tanh = k·tanh(n / scale)（人少时约 k·n/scale，人多时趋近 k）
 */
export function seederScore(n: number, k: number, cap: number, curve: RankRules["availability"]["curve"] | undefined, scale = 0) {
  if (n <= 0 || k === 0) return 0
  let raw: number
  if (curve === "tanh") raw = scale > 0 ? k * Math.tanh(n / scale) : 0
  else if (curve === "log") raw = k * Math.log2(1 + n)
  else raw = k * n
  return round1(Math.min(raw, cap))
}

/**
 * 文件大小分：过 (from_mb, from_score) 与 (to_mb, to_score) 的直线；from_mb 以下取 from_score，
 * to_mb 以上取 to_score（extend 时直线继续延伸、不设下限）。大小未知为 0。
 * 兼容旧写法 {max, full_below_mb, zero_above_mb}（小文件加分）。
 */
export function sizeScore(bytes: number, size: RankRules["size"] | LegacySize | undefined) {
  if (!size || bytes <= 0) return 0
  const s = normalizeSize(size)
  if ((s.from_score === 0 && s.to_score === 0) || s.to_mb <= s.from_mb) return 0
  const mb = bytes / 1024 ** 2
  if (mb <= s.from_mb) return s.from_score
  if (mb >= s.to_mb && !s.extend) return s.to_score
  return round1(s.from_score + ((s.to_score - s.from_score) * (mb - s.from_mb)) / (s.to_mb - s.from_mb))
}

interface LegacySize {
  max: number
  full_below_mb: number
  zero_above_mb: number
}

function normalizeSize(s: RankRules["size"] | LegacySize): RankRules["size"] {
  if ("max" in s) return { from_mb: s.full_below_mb, from_score: s.max, to_mb: s.zero_above_mb, to_score: 0, extend: false }
  return s
}

/** 给一个文件打分（策略不允许的资源后端不会返回，这里不处理） */
export function scoreFile<T extends RankInput>(f: T, rules: RankRules, viewer: Viewer): Scored<T> {
  const a = rules.availability
  const curve = a.curve ?? "linear"
  const parts: ScorePart[] = []
  const add = (label: string, score: number) => {
    if (score !== 0) parts.push({ label, score })
  }
  const sw = f.swarm ?? null
  const server = !!sw?.server_complete
  const known = !!sw?.checked_at
  const bt = known ? (sw!.bt_seeders ?? null) : null
  const wt = known ? (sw!.wt_seeders ?? null) : null

  if (server) add("服务器已缓存", a.server_complete)
  if (wt !== null && wt > 0) add(`WebRTC 做种 ${wt}`, seederScore(wt, a.wt_seeder, a.wt_cap, curve, a.wt_scale))
  if (viewer.canSeed && bt !== null && bt > 0) add(`BT 做种 ${bt}`, seederScore(bt, a.bt_seeder, a.bt_cap, curve, a.bt_scale))

  // 无可用来源（未统计的不算）：游客类用户只能靠 WebRTC 节点 / 服务器；能用服务器的用户看全部来源
  let dead = false
  if (!viewer.canSeed && !viewer.canWebseed) {
    dead = wt === 0 && !server
  } else {
    dead = !server && (bt !== null || wt !== null) && (bt ?? 0) === 0 && (wt ?? 0) === 0
  }
  if (dead) add("无人做种", a.dead)

  // 关键词：同一分组只取命中的最高分
  const text = `${f.torrent_title} ${f.path}`
  const best = new Map<string, { label: string; score: number }>()
  for (const k of keywordRegexps(rules)) {
    if (!k.re?.test(text)) continue
    if (!k.group) {
      add(k.label, k.score)
      continue
    }
    const cur = best.get(k.group)
    if (!cur || k.score > cur.score) best.set(k.group, { label: k.label, score: k.score })
  }
  for (const v of best.values()) add(v.label, v.score)

  const team = f.team?.trim()
  if (team) for (const t of rules.teams ?? []) if (t.name === team) add(`字幕组 ${t.name}`, t.score)
  add("文件大小", sizeScore(f.size_bytes, rules.size))

  return { file: f, score: round1(parts.reduce((s, p) => s + p.score, 0)), parts, dead, known }
}

/** 排序：分数高 → 发布时间新 → 原顺序 */
export function rankFiles<T extends RankInput>(items: readonly T[], rules: RankRules, viewer: Viewer): Scored<T>[] {
  const scored = items.map((f, i) => ({ s: scoreFile(f, rules, viewer), i }))
  scored.sort((x, y) => {
    if (x.s.score !== y.s.score) return y.s.score - x.s.score
    const px = x.s.file.published_at || ""
    const py = y.s.file.published_at || ""
    if (px !== py) return px < py ? 1 : -1
    return x.i - y.i
  })
  return scored.map((x) => x.s)
}

const fmt = (n: number) => `${n > 0 ? "+" : ""}${n}`

/** 评分明细的单行文字（tooltip） */
export function describeScore(s: Scored): string {
  const head = `推荐分 ${s.score}`
  if (!s.parts.length) return `${head}${s.known ? "" : "（做种人数未知）"}`
  return `${head}：${s.parts.map((p) => `${p.label} ${fmt(p.score)}`).join(" · ")}${s.known ? "" : "（做种人数未知）"}`
}
