// 与 docs/api.md（API 契约 v1）一一对应的类型定义。

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
  /** bangumi.moe 的 _id，或 ih:{infohash} */
  id: string
  infohash: string
  title: string
  size_bytes: number
  files: number
  team: NamedRef | null
  tags: NamedRef[]
  published_at: string
  source: "bangumi.moe" | "user"
  access: Access
}

export interface ScheduleItem {
  key: string
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
  latest_torrent: Torrent | null
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

export interface AnimeDetail extends ScheduleItem {
  summary: string
  tags: string[]
  external: ExternalIds
}

export interface AnimeResponse {
  anime: AnimeDetail
  torrents: Torrent[]
}

export interface Paged<T> {
  page: number
  page_count: number
  items: T[]
}

export type PlayRequest = { magnet: string } | { infohash: string } | { torrent_id: string }

export interface PlayResponse {
  access: Access
  infohash: string
  name: string
  magnet: string
  seeded_by: string[]
  webseeds: string[]
  /** 播放开始后多少秒再加入 webseed */
  webseed_delay_sec?: number
  meta: { anime_key: string; episode: number } | null
}

export interface ReportRequest {
  infohash: string
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

export interface AdminStats {
  users: number
  plays_24h: number
  qb_online: number
  qb_total: number
  pending_reviews: number
}

/** 列表接口统一处理：后端可能返回数组或分页对象 */
export type ListLike<T> = T[] | Paged<T>
