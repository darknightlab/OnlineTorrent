// 类型化 API 客户端（契约见 docs/api.md）。同源 Cookie 会话；写操作带 X-Requested-With 头。
import type {
  AdminStats,
  AdminUser,
  AdminUserUpdate,
  AnimeResponse,
  AuditEntry,
  InviteCode,
  InviteCreate,
  ListLike,
  LoginRequest,
  MeResponse,
  Paged,
  PermissionsResponse,
  PermissionsUpdate,
  SeedResponse,
  WebseedResponse,
  PlayRecord,
  PlayRequest,
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
  Torrent,
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
  anime: (key: string, s?: AbortSignal) => get<AnimeResponse>(`/anime/${encodeURIComponent(key)}`, s),
  search: (q: string, page = 1, s?: AbortSignal) => get<Paged<Torrent>>(`/torrents/search${qs({ q, page })}`, s),
  play: (b: PlayRequest) => post<PlayResponse>("/play", b),
  report: (b: ReportRequest) => post<Record<string, never>>("/report", b),
  /** 请求服务器下载做种（权限 "POST /api/v1/torrents/{ih}/seed"） */
  seed: (ih: string) => post<SeedResponse>(`/torrents/${encodeURIComponent(ih)}/seed`),
  /** 获取 HTTP WebSeed（权限 "POST /api/v1/torrents/{ih}/webseed"） */
  webseed: (ih: string) => post<WebseedResponse>(`/torrents/${encodeURIComponent(ih)}/webseed`),

  // 后台
  admin: {
    stats: () => get<AdminStats>("/admin/stats"),
    settings: () => get<SettingsResponse>("/admin/settings"),
    saveSettings: (b: SettingsUpdate) => put<SettingsResponse>("/admin/settings", b),
    permissions: () => get<PermissionsResponse>("/admin/permissions"),
    savePermissions: (b: PermissionsUpdate) => put<PermissionsResponse>("/admin/permissions", b),

    qbServers: () => get<ListLike<QBServer>>("/admin/qb-servers"),
    createQB: (b: QBServerInput) => post<QBServer>("/admin/qb-servers", b),
    updateQB: (id: number, b: Partial<QBServerInput>) => put<QBServer>(`/admin/qb-servers/${id}`, b),
    deleteQB: (id: number) => del<Record<string, never>>(`/admin/qb-servers/${id}`),
    testQB: (id: number) => post<QBServer>(`/admin/qb-servers/${id}/test`),

    users: (q: string, page: number) => get<ListLike<AdminUser>>(`/admin/users${qs({ q, page })}`),
    updateUser: (id: number, b: AdminUserUpdate) => put<AdminUser>(`/admin/users/${id}`, b),

    invites: () => get<ListLike<InviteCode>>("/admin/invites"),
    createInvite: (b: InviteCreate) => post<InviteCode>("/admin/invites", b),
    updateInvite: (id: number, b: { disabled: boolean }) => put<InviteCode>(`/admin/invites/${id}`, b),
    redeemCodes: () => get<ListLike<RedeemCode>>("/admin/redeem-codes"),
    createRedeemCodes: (b: RedeemCreate) => post<ListLike<RedeemCode>>("/admin/redeem-codes", b),

    rules: () => get<ListLike<Rule>>("/admin/rules"),
    createRule: (b: RuleInput) => post<Rule>("/admin/rules", b),
    updateRule: (id: number, b: Partial<RuleInput>) => put<Rule>(`/admin/rules/${id}`, b),
    deleteRule: (id: number) => del<Record<string, never>>(`/admin/rules/${id}`),
    testPolicy: (b: PolicyTestRequest) => post<PolicyTestResponse>("/admin/policy/test", b),

    reviews: (status = "pending") => get<ListLike<Review>>(`/admin/reviews${qs({ status })}`),
    decideReview: (id: number, b: ReviewDecision) => put<Review>(`/admin/reviews/${id}`, b),

    plays: (page: number) => get<ListLike<PlayRecord>>(`/admin/plays${qs({ page })}`),
    audit: (page: number) => get<ListLike<AuditEntry>>(`/admin/audit${qs({ page })}`),
  },
}
