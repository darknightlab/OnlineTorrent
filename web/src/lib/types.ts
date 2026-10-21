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
  /** HTTP WebSeed（只由边缘节点提供，权限 PERM.webseed）：播放即订阅就绪推送，地址与访问令牌由 SSE 下发 */
  webseed?: { enabled: boolean }
  /** 可播放种子大小上限（字节）；浏览器缓存预算 = min(此值, 存储配额的 25%) */
  max_push_size_bytes?: number
  /** 单集页推荐规则（后台「推荐规则」；评分见 lib/rank.ts）。缺省时用 DEFAULT_RANK_RULES */
  rank_rules?: RankRules
}

/**
 * 做种统计（tracker scrape，按需刷新、缓存 30 分钟）。计数为 null = 未知（该类 tracker 没有响应过）；
 * checked_at 为 null = 从未统计。本站服务器是否已缓存不对用户公开。
 */
export interface SwarmStats {
  bt_seeders: number | null
  bt_leechers: number | null
  /** WebTorrent（wss tracker）做种 / 下载人数（本站 qB 做种时也计入） */
  wt_seeders: number | null
  wt_leechers: number | null
  checked_at: string | null
}

/** 推荐规则（GET /site 的 rank_rules，后台 GET/PUT /admin/rank-rules）；说明见 docs/research/rank-rules.md */
export interface RankRules {
  availability: {
    /** WebTorrent 做种者：系数、上限、尺度（tanh） */
    wt_seeder: number
    wt_cap: number
    wt_scale?: number
    /** BT 做种者：系数、上限、尺度（仅对能让服务器做种的用户） */
    bt_seeder: number
    bt_cap: number
    bt_scale?: number
    /** linear：每人 系数 分；log：系数 × log2(1 + 人数)；tanh：系数 × tanh(人数 / 尺度) */
    curve: "linear" | "log" | "tanh"
    /** 对当前用户无可用来源时的扣分 */
    dead: number
  }
  /** 正则（不区分大小写）匹配 种子标题 + 文件路径；同一 group（非空）只取命中的最高分 */
  keywords: { label: string; pattern: string; score: number; group?: string }[]
  /** 字幕组名称精确匹配 */
  teams: { name: string; score: number }[]
  /** 文件大小：过 (from_mb, from_score)、(to_mb, to_score) 的直线；from 以下取 from_score，to 以上取 to_score（extend 时继续延伸） */
  size: { from_mb: number; from_score: number; to_mb: number; to_score: number; extend: boolean }
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
  /** 已放送完（全部正片已播出）：番剧页从第 1 话开始播，选集不标记最新一集 */
  finished?: boolean
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

/** 一条弹幕：[时间（秒）, 模式（1 滚动 / 4 底部 / 5 顶部）, 颜色 0xRRGGBB, 文字] */
export type DanmakuItem = [number, number, number, string]

/** GET /subjects/{provider}/{id}/episodes/{key}/danmaku；没有弹幕时 items 为空 */
export interface DanmakuResponse {
  items: DanmakuItem[]
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
  /** bangumi 单集 ID（https://bgm.tv/ep/{id}） */
  bgm_ep_id?: number
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
  /** 只在第 1 页返回（其余页为 []）；数量为后台设置「搜索：每次显示的番剧数」 */
  subjects: Subject[]
  /** 匹配的番剧总数（更多的用 GET /search/{keyword}/subjects?offset= 分批取） */
  subjects_total: number
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
  /** ready：已判定（allow 时带磁力）；pending：新种子，服务器正在获取元数据（无磁力，订阅 resolve/events） */
  status?: "ready" | "pending"
  /** pending 时 action 为 "pending" */
  access: Omit<Access, "action"> & { action: AccessAction | "pending" }
  infohash: string
  name: string
  magnet: string
  /** 恒为 []：WebSeed（边缘节点）地址就绪后由 SSE 推送 */
  webseeds: string[]
  meta: PlayMeta | null
}

export interface SeedResponse {
  /** 至少一台服务器接受了做种（不透露是哪台） */
  seeded: boolean
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

/** 列表接口统一处理：后端可能返回数组或分页对象 */
export type ListLike<T> = T[] | Paged<T>
