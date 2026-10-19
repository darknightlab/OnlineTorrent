// 类型化 API 客户端（契约见 docs/api.md）。同源 Cookie 会话；写操作带 X-Requested-With 头。
import type {
  AdminStats,
  AdminUser,
  AdminUserUpdate,
  AuditEntry,
  BgmArchiveStatus,
  InviteCode,
  InviteCreate,
  ListLike,
  LoginRequest,
  MeResponse,
  MetadataJob,
  SearchResponse,
  Source,
  SourceInput,
  SourceProbeRequest,
  SourceProbeResult,
  SourceSchema,
  TorrentStatus,
  PermissionsResponse,
  PermissionsUpdate,
  ContentScope,
  ContentScopeInput,
  ContentScopesResponse,
  SubjectTag,
  SeedResponse,
  PlayRecord,
  PlayResponse,
  PolicyTestRequest,
  PolicyTestResponse,
  QBServer,
  QBServerInput,
  RedeemCode,
  RedeemCreate,
  RegisterRequest,
  ReportRequest,
  Review,
  ReviewDecision,
  Rule,
  RuleInput,
  ScheduleResponse,
  SettingsResponse,
  SettingsUpdate,
  Site,
  SubjectResponse,
  EpisodeResponse,
  EpisodesResponse,
} from "./types"

export const API_BASE = "/api/v1"

export class ApiError extends Error {
  readonly status: number
  readonly code: string
  constructor(status: number, code: string, message: string) {
    super(message)
    this.name = "ApiError"
    this.status = status
    this.code = code
  }
  /** 网络错误或 5xx：可视为后端不可用 */
  get unavailable() {
    return this.status === 0 || this.status >= 500
  }
}

export function isApiError(e: unknown): e is ApiError {
  return e instanceof ApiError
}

type Method = "GET" | "POST" | "PUT" | "DELETE"

function qs(params?: Record<string, string | number | undefined | null>) {
  if (!params) return ""
  const s = new URLSearchParams()
  for (const [k, v] of Object.entries(params)) if (v !== undefined && v !== null && v !== "") s.set(k, String(v))
  const str = s.toString()
  return str ? `?${str}` : ""
}

export async function request<T>(method: Method, path: string, body?: unknown, signal?: AbortSignal): Promise<T> {
  const headers: Record<string, string> = { Accept: "application/json" }
  if (method !== "GET") headers["X-Requested-With"] = "fetch"
  if (body !== undefined) headers["Content-Type"] = "application/json"
  let res: Response
  try {
    res = await fetch(API_BASE + path, {
      method,
      headers,
      credentials: "include",
      body: body === undefined ? undefined : JSON.stringify(body),
      signal,
    })
  } catch (e) {
    if ((e as Error)?.name === "AbortError") throw e
    throw new ApiError(0, "network_error", "无法连接到服务器")
  }
  const text = await res.text()
  let data: unknown = null
  if (text) {
    try {
      data = JSON.parse(text)
    } catch {
      data = null
    }
  }
  if (!res.ok) {
    const err = (data as { error?: { code?: string; message?: string } } | null)?.error
    throw new ApiError(res.status, err?.code || `http_${res.status}`, err?.message || defaultMessage(res.status))
  }
  return data as T
}

function defaultMessage(status: number) {
  if (status === 401) return "请先登录"
  if (status === 402) return "需要大会员"
  if (status === 403) return "没有权限"
  if (status === 404) return "未找到"
  if (status === 429) return "请求过于频繁"
  if (status === 451) return "该内容已被屏蔽"
  if (status >= 500) return "服务器错误"
  return `请求失败（${status}）`
}

/** 后端列表可能是数组或分页对象，统一为数组 + 页数 */
export function toList<T>(v: ListLike<T> | null | undefined): { items: T[]; page: number; page_count: number } {
  if (!v) return { items: [], page: 1, page_count: 1 }
  if (Array.isArray(v)) return { items: v, page: 1, page_count: 1 }
  return { items: v.items ?? [], page: v.page ?? 1, page_count: v.page_count ?? 1 }
}

const seg = encodeURIComponent

const get = <T>(p: string, signal?: AbortSignal) => request<T>("GET", p, undefined, signal)
const post = <T>(p: string, b?: unknown) => request<T>("POST", p, b ?? {})
const put = <T>(p: string, b?: unknown) => request<T>("PUT", p, b ?? {})
const del = <T>(p: string) => request<T>("DELETE", p)

export const api = {
  // 公共
  site: (s?: AbortSignal) => get<Site>("/site", s),
  me: (s?: AbortSignal) => get<MeResponse>("/me", s),
  register: (b: RegisterRequest) => post<MeResponse>("/auth/register", b),
  login: (b: LoginRequest) => post<MeResponse>("/auth/login", b),
  logout: () => post<Record<string, never>>("/auth/logout"),
  changePassword: (b: { old_password: string; new_password: string }) => post<Record<string, never>>("/me/password", b),
  redeem: (code: string) => post<MeResponse>("/me/redeem", { code }),
  schedule: (s?: AbortSignal) => get<ScheduleResponse>("/schedule", s),
  /** 条目详情：provider = id（本站 ID）/ bgm / mal / anidb … */
  subject: (provider: string, id: string, s?: AbortSignal) => get<SubjectResponse>(`/subjects/${seg(provider)}/${seg(id)}`, s),
  /** 单集所有已知文件，items[0] 为自动推荐 */
  /** 选集列表（正片 + 特别篇） */
  episodes: (provider: string, id: string, s?: AbortSignal) => get<EpisodesResponse>(`/subjects/${seg(provider)}/${seg(id)}/episodes`, s),
  /** key：集的键（"12"、"12.5"、"sp1"、"ncop1"…） */
  episode: (provider: string, id: string, key: string, s?: AbortSignal) =>
    get<EpisodeResponse>(`/subjects/${seg(provider)}/${seg(id)}/episodes/${seg(key)}`, s),
  /** 搜索：番剧（仅第 1 页）+ 种子 */
  search: (keyword: string, page = 1, s?: AbortSignal) =>
    get<SearchResponse>(`/search/${seg(keyword)}${qs({ page: page > 1 ? page : undefined })}`, s),
  /** 种子收录状态与逐文件刮削结果（做种后轮询） */
  torrentStatus: (ih: string, s?: AbortSignal) => get<TorrentStatus>(`/torrents/hash/${seg(ih)}`, s),
  /** 种子文件（.torrent，HTTP 元数据；权限 PERM.torrentFile）。无可用文件时 404 */
  torrentFile: async (ih: string, s?: AbortSignal) => {
    let res: Response
    try {
      res = await fetch(`${API_BASE}/torrents/hash/${seg(ih)}/torrent`, { credentials: "include", signal: s })
    } catch (e) {
      if ((e as Error)?.name === "AbortError") throw e
      throw new ApiError(0, "network_error", "无法连接到服务器")
    }
    if (!res.ok) throw new ApiError(res.status, `http_${res.status}`, defaultMessage(res.status))
    return new Uint8Array(await res.arrayBuffer())
  },
  /** 按 infohash 打开种子（策略判定 + 磁力） */
  /** file：正在播放的文件序号（多番剧合集时 meta 按该文件给出番剧与集数） */
  playHash: (ih: string, s?: AbortSignal, file?: number | null) =>
    request<PlayResponse>("POST", `/torrents/hash/${seg(ih)}/play${file != null ? `?file=${file}` : ""}`, undefined, s),
  /** 按 bangumi.moe ID 打开种子（本地库中由该抓取源收录的种子） */
  playMoe: (id: string, s?: AbortSignal) => request<PlayResponse>("POST", `/torrents/moe/${seg(id)}/play`, undefined, s),
  report: (ih: string, b: ReportRequest) => post<Record<string, never>>(`/torrents/hash/${seg(ih)}/report`, b),
  /** 请求服务器下载做种（权限 PERM.seed） */
  seed: (ih: string) => post<SeedResponse>(`/torrents/hash/${seg(ih)}/seed`),
  /** WebSeed 可用性推送（SSE）地址；fileIndex 为正在播放的文件，省略表示任意 / 单文件 */
  webseedEventsURL: (ih: string, fileIndex?: number | null) =>
    `${API_BASE}/torrents/hash/${seg(ih)}/webseed/events${fileIndex != null ? `/file/${fileIndex}` : ""}`,

  // 后台
  admin: {
    stats: () => get<AdminStats>("/admin/stats"),
    settings: () => get<SettingsResponse>("/admin/settings"),
    saveSettings: (b: SettingsUpdate) => put<SettingsResponse>("/admin/settings", b),
    permissions: () => get<PermissionsResponse>("/admin/permissions"),
    savePermissions: (b: PermissionsUpdate) => put<PermissionsResponse>("/admin/permissions", b),
    contentScopes: () => get<ContentScopesResponse>("/admin/content-scopes"),
    saveContentScope: (role: string, b: ContentScopeInput) => put<ContentScope>(`/admin/content-scopes/role/${role}`, b),
    subjectTags: (q: string) => get<{ items: SubjectTag[] }>(`/admin/subject-tags${qs({ q })}`),

    qbServers: () => get<ListLike<QBServer>>("/admin/qb-servers"),
    createQB: (b: QBServerInput) => post<QBServer>("/admin/qb-servers", b),
    updateQB: (id: number, b: Partial<QBServerInput>) => put<QBServer>(`/admin/qb-servers/id/${id}`, b),
    deleteQB: (id: number) => del<Record<string, never>>(`/admin/qb-servers/id/${id}`),
    testQB: (id: number) => post<QBServer>(`/admin/qb-servers/id/${id}/test`),

    users: (q: string, page: number) => get<ListLike<AdminUser>>(`/admin/users${qs({ q, page })}`),
    updateUser: (id: number, b: AdminUserUpdate) => put<AdminUser>(`/admin/users/id/${id}`, b),

    invites: () => get<ListLike<InviteCode>>("/admin/invites"),
    createInvite: (b: InviteCreate) => post<InviteCode>("/admin/invites", b),
    updateInvite: (id: number, b: { disabled: boolean }) => put<InviteCode>(`/admin/invites/id/${id}`, b),
    redeemCodes: () => get<ListLike<RedeemCode>>("/admin/redeem-codes"),
    createRedeemCodes: (b: RedeemCreate) => post<ListLike<RedeemCode>>("/admin/redeem-codes", b),

    rules: () => get<ListLike<Rule>>("/admin/rules"),
    createRule: (b: RuleInput) => post<Rule>("/admin/rules", b),
    updateRule: (id: number, b: Partial<RuleInput>) => put<Rule>(`/admin/rules/id/${id}`, b),
    deleteRule: (id: number) => del<Record<string, never>>(`/admin/rules/id/${id}`),
    testPolicy: (b: PolicyTestRequest) => post<PolicyTestResponse>("/admin/policy/test", b),

    reviews: (status = "pending") => get<ListLike<Review>>(`/admin/reviews${qs({ status })}`),
    decideReview: (id: number, b: ReviewDecision) => put<Review>(`/admin/reviews/id/${id}`, b),

    sources: () => get<{ items: Source[] }>("/admin/sources"),
    sourceSchema: () => get<SourceSchema>("/admin/sources/schema"),
    probeSource: (b: SourceProbeRequest) => post<SourceProbeResult>("/admin/sources/probe", b),
    createSource: (b: SourceInput) => post<Source>("/admin/sources", b),
    updateSource: (id: number, b: Partial<SourceInput>) => put<Source>(`/admin/sources/id/${id}`, b),
    deleteSource: (id: number) => del<{ items: Source[] }>(`/admin/sources/id/${id}`),
    runSource: (id: number) => post<Source>(`/admin/sources/id/${id}/run`),
    metadataJobs: () => get<{ items: MetadataJob[] }>("/admin/metadata-jobs"),
    bgmArchive: () => get<BgmArchiveStatus>("/admin/bgm-archive"),
    runBgmArchive: () => post<BgmArchiveStatus>("/admin/bgm-archive/run"),

    plays: (page: number) => get<ListLike<PlayRecord>>(`/admin/plays${qs({ page })}`),
    audit: (page: number) => get<ListLike<AuditEntry>>(`/admin/audit${qs({ page })}`),
  },
}
