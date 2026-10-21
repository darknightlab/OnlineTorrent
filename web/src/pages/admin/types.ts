// 后台专用的类型（只由 pages/admin 引用；构建为 assets/admin/ 下的 chunk，仅管理员可下载）。

import type { Access, AccessAction, RankRules, User } from "@/lib/types"

// ---------- admin ----------

export type SettingValue = string | number | boolean | string[] | null
export type SettingType = "string" | "text" | "markdown" | "number" | "bool" | "enum" | "string_list" | "secret"

/** GET /admin/settings 的单个设置项；type 决定编辑控件 */
export interface SettingItem {
  key: string
  group: string
  type: SettingType
  label: string
  description?: string
  value: SettingValue
  /** 仅 enum */
  options?: string[]
  /** 仅 secret：value 恒为 ""，is_set 表示是否已设置 */
  is_set?: boolean
}

export interface SettingsResponse {
  items: SettingItem[]
}

/** PUT /admin/settings；secret 传 "" 表示不修改 */
export interface SettingsUpdate {
  values: Record<string, SettingValue>
}

export interface QBServer {
  id: number
  name: string
  url: string
  username: string
  /** 仅写入时提交 */
  password?: string
  /** 仅写入时提交 */
  api_key?: string
  category: string
  enabled: boolean
  seeding_time_limit_min: number
  ratio_limit: number
  max_active: number
  /** 添加种子后补 wss tracker（默认开） */
  add_wss_trackers: boolean
  /** 边缘节点的公开地址（浏览器直接从这里取 WebSeed），如 https://edge-ams.projectk.org */
  edge_url: string
  /** qB WebUI API 经边缘节点的隧道访问（不再直连 url） */
  edge_api: boolean
  online: boolean
  version: string
  last_error: string
  last_check_at: string | null
  /** 已生成边缘节点令牌 */
  edge_bound?: boolean
  edge?: EdgeStatus
}

export interface EdgeStatus {
  connected: boolean
  since?: string
  version?: string
  host?: string
  remote?: string
  rtt_ms?: number
  bytes_served: number
  requests: number
  listen?: string
  stats_error?: string
}

export type QBServerInput = Omit<QBServer, "id" | "online" | "version" | "last_error" | "last_check_at" | "edge_bound" | "edge">

export interface AdminUser extends User {
  disabled: boolean
  last_login_at?: string | null
}

export interface AdminUserUpdate {
  role?: User["role"]
  vip_until?: string | null
  disabled?: boolean
}

export interface InviteCode {
  id: number
  code: string
  /** 完整邀请链接 */
  url: string
  /** 0 = 不限次数 */
  max_uses: number
  uses: number
  role: "member" | "vip"
  vip_days: number
  expires_at: string | null
  note: string
  disabled: boolean
  created_at: string
}

/** 修改邀请码：省略的字段不变；expires_at 为 "" 表示永不过期，否则 YYYY-MM-DD（当天结束） */
export interface InvitePatch {
  max_uses?: number
  role?: "member" | "vip"
  vip_days?: number
  expires_at?: string
  note?: string
  disabled?: boolean
}

export interface InviteCreate {
  max_uses: number
  role: "member" | "vip"
  vip_days: number
  expires_in_days: number
  note: string
}

export interface RedeemCode {
  id: number
  code: string
  vip_days: number
  note: string
  created_at: string
  used_by_name: string | null
  used_at: string | null
}

export interface RedeemCreate {
  count: number
  vip_days: number
  note: string
}

export type RuleField = "title" | "file" | "infohash" | "team" | "tag" | "any"
export type RuleOp = "contains" | "regex" | "equals"
export type RuleLabel = "political" | "porn" | "copyright" | "other" | ""

export interface Rule {
  id: number
  name: string
  enabled: boolean
  priority: number
  match: { field: RuleField; op: RuleOp; value: string }
  action: AccessAction
  label: RuleLabel
  note: string
}

export type RuleInput = Omit<Rule, "id">

export interface PolicyTestRequest {
  title: string
  infohash?: string
  files?: string[]
  tags?: string[]
  team?: string
}

export interface PolicyTestResponse {
  access: Access
  matched_rule: Rule | null
}

export type ReviewAction = AccessAction | "dismiss"

export interface Review {
  id: number
  infohash: string
  title: string
  source: "report" | "auto"
  reason: string
  reporter_name: string | null
  status: string
  created_at: string
  /** 已处理记录可能带上裁决结果 */
  action?: ReviewAction | null
  label?: RuleLabel
  note?: string
}

export interface ReviewDecision {
  action: ReviewAction
  label: RuleLabel
  note: string
}

export interface PlayRecord {
  id: number
  infohash: string
  name: string
  user: string | null
  ip?: string
  action: AccessAction
  seeded_by: string[]
  created_at: string
}

export interface AuditEntry {
  id: number
  actor: string | null
  action: string
  target: string
  detail: string
  created_at: string
}

// ---------- 抓取源 ----------

export type SourceKind = "rss" | "bangumi_moe" | (string & {})

export interface Source {
  id: number
  name: string
  kind: SourceKind
  url: string
  interval_min: number
  enabled: boolean
  /** true = 不走上游代理，直连 */
  proxy_disabled: boolean
  last_run_at: string | null
  last_ok_at: string | null
  /** 上次成功抓取时 Feed 中的条目数 */
  last_items: number
  /** 上次新收录的种子数 */
  last_new: number
  last_error: string
  /** 连续失败次数（间隔 × 2^n 退避） */
  fail_count: number
  running: boolean
  next_run_at: string | null
  created_at: string
  updated_at: string
}

export interface SourceInput {
  name?: string
  kind?: SourceKind
  url: string
  interval_min?: number
  enabled?: boolean
  proxy_disabled?: boolean
}

export interface SourceSchema {
  kinds: { value: string; label: string; description: string; example_url: string }[]
  min_interval_min: number
  default_interval_min: number
}

export interface SourceProbeRequest {
  kind?: SourceKind
  url: string
  proxy_disabled?: boolean
}

export interface SourceProbeResult {
  title: string
  link: string
  format: string
  item_count: number
  samples: { title: string; url?: string; published_at?: string; infohash?: string; torrent_url?: string; magnet: boolean }[]
}

export interface MetadataJob {
  infohash: string
  source_id: number
  status: "pending" | "running" | "done" | "failed"
  attempts: number
  next_at: string | null
  server_id: number
  added: boolean
  started_at: string | null
  last_error: string
  created_at: string
  updated_at: string
}

/** GET /admin/bgm-archive：bangumi Archive（bgm.tv 官方每周数据导出）同步状态 */
/** GET /admin/bangumi-data：bangumi-data 同步状态（每 12 小时一次，服务启动时也同步） */
export interface BangumiDataStatus {
  url: string
  running: boolean
  synced_at: string | null
  /** 上次成功同步导入的番剧数 */
  works: number
  duration_sec: number
  error?: string
  error_at?: string | null
  next_at: string | null
}

/** GET /admin/manami：anime-offline-database（manami-project，ODbL）导入状态（每周一次） */
export interface ManamiStatus {
  url: string
  /** 数据库版本（lastUpdate） */
  release: string
  synced_at: string
  /** 数据库条目数 */
  entries: number
  /** 按 MAL / AniDB / AniList ID 对应上的条目数 */
  linked: number
  /** 存下的名称数 */
  names: number
  duration_sec: number
  error?: string
  error_at?: string
  running: boolean
  next_at?: string
}

export interface BgmArchiveStatus {
  /** 当前已导入的导出文件名 */
  dump: string
  dump_at: string | null
  digest: string
  synced_at: string | null
  subjects: number
  episodes: number
  relations: number
  duration_sec: number
  checked_at: string | null
  error?: string
  error_at?: string | null
  running: boolean
  phase?: "subjects" | "episodes" | "relations" | (string & {})
}

// ---------- Tracker（后台） ----------

/** 某个已配置 tracker 最后一次实际使用（按需统计做种人数）时的状态；不做主动探测 */
export interface TrackerHealth {
  url: string
  kind: "udp" | "http" | "wss" | (string & {})
  list: "bt" | "webtorrent"
  /** unused = 启动以来尚未使用 */
  status: "ok" | "error" | "unused"
  result: "" | "ok" | "timeout" | "dns" | "network" | "protocol" | "error"
  latency_ms: number
  last_error: string
  checked_at: string | null
  last_ok_at: string | null
  last_error_at: string | null
  ok_count: number
  fail_count: number
}

export interface SwarmInfo {
  enabled: boolean
  running: number
  runs: number
  torrents: number
  last_at: string | null
  last_ms: number
  last_torrents: number
  last_answered: number
  skipped_busy: number
}

/** GET /admin/trackers */
export interface TrackersResponse {
  items: TrackerHealth[]
  swarm: SwarmInfo
  stats: { torrents: number; scraped: number; known: number; dead: number } | null
  lists: { webtorrent: string[]; bt: string[] }
}

/** GET/PUT /admin/rank-rules */
export interface RankRulesResponse {
  rules: RankRules
  defaults: RankRules
}

export interface AdminStats {
  users: number
  plays_24h: number
  qb_online: number
  qb_total: number
  pending_reviews: number
}

// ---------- 接口权限 ----------

/** 可授权的等级（admin 始终放行） */
export type GrantRole = "guest" | "member" | "vip"
/** public=所有人固定；configurable=游客/会员/大会员可配；user=需登录，会员/大会员可配；admin=仅管理员固定 */
export type RouteAccess = "public" | "configurable" | "user" | "admin"

export interface PermissionItem {
  /** "METHOD /path" */
  key: string
  method: string
  path: string
  group: string
  label: string
  access: RouteAccess
  default_roles: string[] | null
  /** 显式授权（不含继承） */
  granted: Record<GrantRole, boolean>
  /** 联动组：同组接口的权限必须一致（后台切换其一，其余自动跟随；保存时后端校验） */
  bundle?: string
  bundle_label?: string
}

export interface PermissionsResponse {
  roles: GrantRole[]
  items: PermissionItem[]
}

export interface PermissionsUpdate {
  grants: Record<string, GrantRole[]>
}

// ---------- 内容范围（按等级限制可见的番剧条目） ----------

export type TagMode = "all" | "selected"
export type TagOperator = "and" | "or"

/**
 * 某等级的内容范围。条目的标签 = works.tags ∪ works.meta_tags。
 * tag_mode=selected 时只显示匹配表达式的条目：tags[i] 之间用 tag_operators[i]（长度 = tags.length - 1）连接，AND 优先于 OR。
 * exclude_tags：带有其中任意一个标签的条目一律隐藏。管理员不受限制。
 */
export interface ContentScope {
  role: GrantRole
  tag_mode: TagMode
  tags: string[]
  tag_operators: TagOperator[]
  exclude_tags: string[]
}

export interface ContentScopesResponse {
  roles: GrantRole[]
  items: ContentScope[]
}

export type ContentScopeInput = Omit<ContentScope, "role">

export interface SubjectTag {
  name: string
  /** 带有该标签的条目数 */
  count: number
}

// ---- 决策模型（Cloudflare Clef，后台 /admin/clef） ----

export type ClefStatusName = "applied" | "pending" | "rejected" | "discarded"

export interface ClefThresholds {
  metric: "prob" | "confidence"
  apply: number
  margin: number
  review: number
}

export interface ClefJob {
  infohash: string
  title: string
  status: "pending" | "done" | "failed" | "expired"
  attempts: number
  requests: number
  next_at: string
  last_error: string
  created_at: string
  updated_at: string
}

/** GET /admin/clef */
export interface ClefStatus {
  enabled: boolean
  configured: boolean
  /** openai = LLM（结构化输出）；clef = Cloudflare Clef */
  provider: "openai" | "clef"
  model: string
  base_url: string
  thresholds: ClefThresholds
  llm_thresholds: { apply: number; review: number; require_agreement: boolean }
  concurrency: number
  rate_per_min: number
  daily_cap: number
  /** 今日（UTC）已发请求数 */
  today: number
  jobs: Record<string, number>
  decisions: Record<string, number>
  recent_jobs: ClefJob[]
  current?: string
  last_error?: string
  last_error_at?: string
  last_ok_at?: string
  session: { requests: number; input_tokens: number }
}

export interface ClefOptionP {
  id: string
  p: number
}

export interface ClefAnswerSummary {
  choice: string
  confidence: number
  top: ClefOptionP[]
  /** 条目问题：去掉没有该集 / 季数不符的候选后重新归一化的概率 */
  conditioned?: ClefOptionP[]
  /** LLM 的简短理由 */
  note?: string
}

/** 一条决策（GET /admin/clef/decisions） */
export interface ClefDecision {
  id: number
  infohash: string
  file_index: number
  path: string
  torrent_title: string
  work_key?: string
  subject_id?: number
  title?: string
  title_cn?: string
  kind: string
  episode: number | null
  episode_key?: string
  score: number
  margin: number
  confidence: number
  answers: Record<string, ClefAnswerSummary>
  model: string
  /** model = 决策模型；rule = 规则映射（缺少发布时间，待人工确认） */
  source: "model" | "rule"
  status: ClefStatusName
  /** 原因代码；date_mismatch 后跟「: 说明」 */
  reason: string
  created_at: string
  decided_at?: string
  decided_by?: string
  /** 用户提交的种子：提交时间与提交者 */
  submitted_at?: string
  submitted_by?: number
  submitter?: string
}

export interface ClefCandidate {
  id: string
  work_key: string
  subject_id: number
  title: string
  cn?: string
  aliases?: string[]
  air_date?: string
  eps?: number
  type?: string
  season?: number
  why: "anchor" | "hint" | "search" | "related"
}

export interface ClefNumber {
  id: string
  value: number
  context: string
}

export interface ClefFile {
  index: number
  path: string
  kind: string
  numbers: ClefNumber[]
  hint?: string
  season?: number
}

export interface ClefFileDecision {
  file: ClefFile
  candidate?: ClefCandidate
  kind: string
  episode?: number
  episode_key?: string
  score: number
  margin: number
  confidence: number
  answers: Record<string, ClefAnswerSummary>
  status: ClefStatusName
  reason?: string
}

export interface ClefRuleFile {
  index: number
  path: string
  kind: string
  work_key?: string
  episode?: number
  queue: boolean
  hint?: string
}

/** POST /admin/clef/test */
export interface ClefTestResponse {
  rules: ClefRuleFile[]
  /** 发布时间（by: published = 抓取源发布时间；submitted = 用户提交时间）；空 = 缺少 */
  release?: { time?: string; by?: "published" | "submitted"; submitted_by?: number; submitter?: string }
  outcome: {
    provider?: "openai" | "clef"
    llm?: { model: string; rounds: number; requests: number; prompt_tokens: number; completion_tokens: number; latency_ms: number; suggested_title?: string; added_candidates?: string[] }
    task: { infohash: string; title: string; published?: string; files: ClefFile[]; candidates: ClefCandidate[]; per_file: boolean }
    model: string
    requests: { Files: number[]; EstTokens: number }[] | null
    decisions: ClefFileDecision[]
    usage: { input_tokens: number; output_tokens: number }
    sent: number
  } | null
  error?: string
}

/** GET /admin/clef/anomalies：映射的集数不在官方章节列表中（已考虑分割放送换算） */
export interface ClefAnomalies {
  items: { infohash: string; title: string; files: { index: number; path: string; work_key: string; title: string; episode: number; official: string }[] }[]
  torrents: number
  files: number
}
