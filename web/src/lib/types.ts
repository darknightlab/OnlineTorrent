// 与 docs/api.md（API 契约 v1）一一对应的类型定义。

import type { EpisodeKind } from "./episode"
export type { EpisodeKind }

export type Role = "guest" | "member" | "vip" | "admin"
export type AccessAction = "allow" | "login" | "vip" | "block"
export type RegistrationMode = "open" | "invite" | "closed"

export interface ApiErrorBody {
  error: { code: string; message?: string }
}

export interface IceServer {
  urls: string | string[]
  username?: string
  credential?: string
}

export interface Site {
  name: string
  announcement_md: string
  registration: RegistrationMode
  turnstile_site_key: string
  /** 注册是否必须填写邀请码 */
  invite_required: boolean
  trackers: string[]
  ice_servers: IceServer[]
  max_auto_size_bytes: number
  /** HTTP WebSeed：地址固定 /webseed/hash/{ih}/（Cookie 鉴权，权限 PERM.webseed）；播放即订阅就绪推送 */
  webseed?: { enabled: boolean }
  /** 可播放种子大小上限（字节）；浏览器缓存预算 = min(此值, 存储配额的 25%) */
  max_push_size_bytes?: number
  /** 单集页推荐规则（后台「推荐规则」；评分见 lib/rank.ts）。缺省时用 DEFAULT_RANK_RULES */
  rank_rules?: RankRules
}

/**
 * 做种统计（tracker scrape，按需刷新、缓存 30 分钟）。计数为 null = 未知（该类 tracker 没有响应过）；
 * checked_at 为 null = 从未统计（只有 server_complete 可能已知）。
 */
export interface SwarmStats {
  bt_seeders: number | null
  bt_leechers: number | null
  /** WebTorrent（wss tracker）做种 / 下载人数 */
  wt_seeders: number | null
  wt_leechers: number | null
  /** 某台 qB 服务器已完整下载（可做种 / WebSeed） */
  server_complete: boolean
  checked_at: string | null
}

/** 推荐规则（GET /site 的 rank_rules，后台 GET/PUT /admin/rank-rules）；说明见 docs/research/rank-rules.md */
export interface RankRules {
  availability: {
    /** 服务器已缓存加分 */
    server_complete: number
    /** WebTorrent 做种者：系数与上限 */
    wt_seeder: number
    wt_cap: number
    /** BT 做种者：系数与上限（仅对能让服务器做种的用户） */
    bt_seeder: number
    bt_cap: number
    /** linear：每人 系数 分；log：系数 × log2(1 + 人数) */
    curve: "linear" | "log"
    /** 对当前用户无可用来源时的扣分 */
    dead: number
  }
  /** 正则（不区分大小写）匹配 种子标题 + 文件路径；同一 group（非空）只取命中的最高分 */
  keywords: { label: string; pattern: string; score: number; group?: string }[]
  /** 字幕组名称精确匹配 */
  teams: { name: string; score: number }[]
  /** 小文件加分：≤ full_below_mb 得 max，≥ zero_above_mb 得 0，中间线性 */
  size: { max: number; full_below_mb: number; zero_above_mb: number }
  /** 文件超过「自动选种大小上限」时的分值（负数） */
  oversize: number
}

export interface User {
  id: number
  username: string
  email: string
  role: Exclude<Role, "guest">
  vip_until: string | null
  created_at: string
}

export interface MeResponse {
  user: User | null
  /** 当前用户（未登录为游客）可调用的接口权限键，如 "POST /api/v1/torrents/hash/{ih}/play"；公开与后台接口不在其中 */
  permissions: string[]
}

export interface RegisterRequest {
  username: string
  email: string
  password: string
  invite_code?: string
  turnstile_token?: string
}

export interface LoginRequest {
  identifier: string
  password: string
}

export interface Access {
  action: AccessAction
  reason: string
}

export interface NamedRef {
  id: string
  name: string
}

export interface Torrent {
  /** 不透明 ID（某些来源的条目 ID，或 ih:{infohash}）；页面链接一律用 infohash */
  id: string
  infohash: string
  title: string
  size_bytes: number
  files: number
  team: NamedRef | null
  tags: NamedRef[]
  published_at: string
  /** 抓取源名称（后台可改名）或 "user"（用户提交的磁力） */
  source: string
  access: Access
  /** 做种统计；null = 未知 */
  swarm?: SwarmStats | null
}

export interface ScheduleItem {
  /** 映射库内部键，如 "bgm:123" */
  key: string
  /** 本站条目 ID → /subject/id/{subject_id} */
  subject_id: number
  title: string
  title_cn: string
  cover: string | null
  begin: string
  /** 0=周日 … 6=周六（Asia/Shanghai） */
  weekday: number
  /** HH:mm（Asia/Shanghai） */
  air_time: string
  last_air_at: string | null
  next_air_at: string | null
  episodes_aired: number
  bgm_id: number | null
  moe_tag_id: string | null
  /** 最新一集的集数（映射库已知时） */
  latest_episode?: number | null
}

export interface ScheduleResponse {
  generated_at: string
  season: string
  items: ScheduleItem[]
}

export interface ExternalIds {
  bgm?: number | null
  mal?: number | null
  anidb?: number | null
  tmdb?: number | string | null
  imdb?: string | null
  [k: string]: number | string | null | undefined
}

export interface Subject extends ScheduleItem {
  summary: string
  type?: string
  official_site?: string
  /** 用户标签（按人数排序的前若干个） */
  tags: string[]
  /** bangumi 公共标签（维基维护，如 "TV"、"原创"） */
  meta_tags?: string[]
  /** 仅保存，暂不展示 / 过滤 */
  nsfw?: boolean
  /** bangumi 评分；0 = 无 */
  score?: number
  /** bangumi 排名；0 = 无 */
  rank?: number
  /** 计划集数；0 = 未知 */
  eps?: number
  external: ExternalIds
}

/** GET /subjects/{provider}/{id} */
export interface SubjectResponse {
  subject: Subject
  torrents: Torrent[]
}

/** 单集的一个已知文件（种子内某个文件） */
export interface EpisodeFile {
  infohash: string
  file_index: number
  path: string
  size_bytes: number
  torrent_title: string
  team: string
  published_at: string
  /** 来源条目 ID（可空） */
  source_id: string
  /** 抓取源名称或 "user" */
  source?: string
  confidence: number
  access: Access
  /** 做种统计；null = 从未统计且不在服务器上 */
  swarm?: SwarmStats | null
}

/** GET /subjects/{provider}/{id}/episodes/{key}；items 按发布时间倒序，由前端按推荐规则排序（lib/rank.ts） */
export interface EpisodeResponse {
  subject: Subject
  /** 规范写法的集的键："12"、"12.5"、"sp1"、"ncop1"… */
  key: string
  kind: EpisodeKind
  number: number
  /** "第 12 话"、"SP1"、"NCOP1"… */
  label: string
  /** 仅正片 */
  episode?: number
  items: EpisodeFile[]
}

/** 选集的一项 */
export interface EpisodeListItem {
  key: string
  kind: EpisodeKind
  number: number
  label: string
  /** 已映射到该集的文件数（0 = 暂无资源） */
  sources: number
  /** 仅正片：是否已播出（有官方放送日期时按日期，否则按时间表估算） */
  aired?: boolean
  /** 官方单集标题（有中文时为中文） */
  title?: string
  /** 日文原标题（与 title 不同时给出） */
  title_ja?: string
  /** 官方放送日期 "YYYY-MM-DD" */
  airdate?: string
  /** 是否来自 bangumi 官方章节数据 */
  official?: boolean
}

/** GET /subjects/{provider}/{id}/episodes：正片按编号，然后 SP、NCOP、NCED、PV、CM */
export interface EpisodesResponse {
  items: EpisodeListItem[]
}

export interface Paged<T> {
  page: number
  page_count: number
  items: T[]
}

/** GET /search/{keyword}?page= */
export interface SearchResponse {
  /** 只在第 1 页返回（其余页为 []） */
  subjects: Subject[]
  torrents: Torrent[]
  page: number
  page_count: number
  /** 匹配的种子总数 */
  total: number
}

export type TorrentFileKind = "episode" | "sp" | "op" | "ed" | "pv" | "cm" | "extra" | "other"

/** GET /torrents/hash/{ih} 的单个文件 */
export interface TorrentStatusFile {
  index: number
  path: string
  size_bytes: number
  kind: TorrentFileKind
  /** 未刮削到条目时省略 */
  subject?: { subject_id: number; bgm_id?: number | null; title: string; title_cn: string }
  /** 仅正片且有 subject 时给出；可为小数（12.5） */
  episode?: number
  /** 有 subject 时给出：集的键（"12"、"sp1"、"ncop1"…） */
  episode_key?: string
}

/** GET /torrents/hash/{ih}：收录状态与逐文件刮削结果 */
export interface TorrentStatus {
  status: "unknown" | "pending" | "ready"
  title: string
  files: TorrentStatusFile[]
  swarm?: SwarmStats | null
}

export interface PlayMeta {
  subject_id: number
  subject_key: string
  bgm_id?: number | string | null
  title: string
  /** 仅正片（特别篇为 null） */
  episode: number | null
  /** 集的键（"12"、"sp1"、"ncop1"…） */
  episode_key: string
  /** "第 12 话"、"SP1"、"NCOP1"… */
  episode_label: string
  file_index: number
}

export interface PlayResponse {
  access: Access
  infohash: string
  name: string
  magnet: string
  /** 恒为 []：服务器做种改由 POST /torrents/hash/{ih}/seed 单独请求 */
  seeded_by: string[]
  /** 恒为 []：WebSeed 地址固定为 /webseed/hash/{ih}/，就绪后由 SSE 推送 */
  webseeds: string[]
  meta: PlayMeta | null
}

export interface SeedResponse {
  seeded_by: string[]
}


/** GET /torrents/hash/{ih}/webseed/events[/file/{index}]（SSE）各事件的 data */
export interface WebseedReadyEvent {
  /** 新签名的 WebSeed 地址 */
  webseeds: string[]
}
export interface WebseedWaitingEvent {
  progress: number
}
export interface WebseedEndEvent {
  reason?: string
}

export interface ReportRequest {
  reason: string
}

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
  online: boolean
  version: string
  last_error: string
  last_check_at: string | null
}

export type QBServerInput = Omit<QBServer, "id" | "online" | "version" | "last_error" | "last_check_at">

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

/** 列表接口统一处理：后端可能返回数组或分页对象 */
export type ListLike<T> = T[] | Paged<T>

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
