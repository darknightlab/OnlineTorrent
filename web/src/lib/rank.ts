// 单集页资源推荐：纯函数，按后台「推荐规则」（GET /site 的 rank_rules）给每个文件打分并排序；
// 单集页按此排序资源列表并自动播放第一个。规则说明见 docs/api.md「推荐规则」。
import type { Access, RankRules, SwarmStats } from "./types"

export const DEFAULT_RANK_RULES: RankRules = {
  availability: { server_complete: 30, wt_seeder: 5, wt_cap: 30, bt_seeder: 1, bt_cap: 20, dead: -100 },
  keywords: [
    { label: "HEVC", pattern: String.raw`hevc|[hx]\.?265`, score: 3 },
    { label: "1080p", pattern: "1080|1920", score: 3 },
    { label: "4K", pattern: String.raw`2160|\b4k\b`, score: 1 },
    { label: "简体", pattern: String.raw`简|chs|\bsc\b|\bgb\b`, score: 2 },
    { label: "繁体", pattern: String.raw`繁|cht|\btc\b|big5`, score: 1 },
  ],
  teams: [],
  oversize: -10,
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
  score: number
  /** 评分明细（非 0 项） */
  parts: ScorePart[]
  /** 当前用户可播放（策略 allow）；不可播放的一律排在最后、不会被自动播放 */
  allowed: boolean
  /** 对当前用户而言无人做种（已统计且没有可用来源） */
  dead: boolean
  /** 做种人数已统计过（checked_at 非空） */
  known: boolean
}

const compiled = new WeakMap<RankRules, { re: RegExp | null; label: string; score: number }[]>()

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
      return { re, label: k.label, score: k.score }
    })
    compiled.set(rules, c)
  }
  return c
}

/** 给一个文件打分。maxAutoBytes：自动选种大小上限（site.max_auto_size_bytes，≤ 0 不限） */
export function scoreFile<T extends RankInput>(f: T, rules: RankRules, viewer: Viewer, maxAutoBytes: number): Scored<T> {
  const a = rules.availability
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
  if (wt !== null && wt > 0) add(`WebRTC 做种 ${wt}`, Math.min(wt * a.wt_seeder, a.wt_cap))
  if (viewer.canSeed && bt !== null && bt > 0) add(`BT 做种 ${bt}`, Math.min(bt * a.bt_seeder, a.bt_cap))

  // 无可用来源（未统计的不算）：游客类用户只能靠 WebRTC 节点 / 服务器；能用服务器的用户看全部来源
  let dead = false
  if (!viewer.canSeed && !viewer.canWebseed) {
    dead = wt === 0 && !server
  } else {
    dead = !server && (bt !== null || wt !== null) && (bt ?? 0) === 0 && (wt ?? 0) === 0
  }
  if (dead) add("无人做种", a.dead)

  const text = `${f.torrent_title} ${f.path}`
  for (const k of keywordRegexps(rules)) if (k.re?.test(text)) add(k.label, k.score)
  const team = f.team?.trim()
  if (team) for (const t of rules.teams ?? []) if (t.name === team) add(`字幕组 ${t.name}`, t.score)
  if (maxAutoBytes > 0 && f.size_bytes > maxAutoBytes) add("超过大小上限", rules.oversize)

  const allowed = f.access.action === "allow"
  return { file: f, score: parts.reduce((s, p) => s + p.score, 0), parts, allowed, dead, known }
}

/** 排序：可播放的在前 → 分数高 → 发布时间新 → 原顺序 */
export function rankFiles<T extends RankInput>(items: readonly T[], rules: RankRules, viewer: Viewer, maxAutoBytes: number): Scored<T>[] {
  const scored = items.map((f, i) => ({ s: scoreFile(f, rules, viewer, maxAutoBytes), i }))
  scored.sort((x, y) => {
    if (x.s.allowed !== y.s.allowed) return x.s.allowed ? -1 : 1
    if (x.s.score !== y.s.score) return y.s.score - x.s.score
    const px = x.s.file.published_at || ""
    const py = y.s.file.published_at || ""
    if (px !== py) return px < py ? 1 : -1
    return x.i - y.i
  })
  return scored.map((x) => x.s)
}

/** 评分明细的单行文字（tooltip） */
export function describeScore(s: Scored): string {
  const head = s.allowed ? `推荐分 ${s.score}` : "当前账号不可播放（排在最后）"
  if (!s.parts.length) return `${head}${s.known ? "" : "（做种人数未知）"}`
  return `${head}：${s.parts.map((p) => `${p.label} ${p.score > 0 ? "+" : ""}${p.score}`).join(" · ")}${s.known ? "" : "（做种人数未知）"}`
}
