// 后台 API 客户端（/api/v1/admin/*）。只由 pages/admin 引用：构建在 assets/admin/ 下，服务器只发给管理员。
import { request, qs } from "@/lib/api"
import type { ListLike, Paged, RankRules } from "@/lib/types"
import type {
  AdminStats,
  AdminUser,
  AdminUserUpdate,
  AuditEntry,
  BangumiDataStatus,
  ManamiStatus,
  BgmArchiveStatus,
  ClefAnomalies,
  ClefDecision,
  ClefStatus,
  ClefTestResponse,
  ContentScope,
  ContentScopeInput,
  ContentScopesResponse,
  InviteCode,
  InviteCreate,
  InvitePatch,
  MetadataJob,
  PermissionsResponse,
  PermissionsUpdate,
  PlayRecord,
  PolicyTestRequest,
  PolicyTestResponse,
  QBServer,
  QBServerInput,
  RankRulesResponse,
  RedeemCode,
  RedeemCreate,
  Review,
  ReviewDecision,
  Rule,
  RuleInput,
  SettingsResponse,
  SettingsUpdate,
  Source,
  SourceInput,
  SourceProbeRequest,
  SourceProbeResult,
  SourceSchema,
  SubjectTag,
  TrackersResponse,
} from "./types"

const seg = encodeURIComponent
const get = <T>(p: string) => request<T>("GET", p)
const post = <T>(p: string, b?: unknown) => request<T>("POST", p, b ?? {})
const put = <T>(p: string, b?: unknown) => request<T>("PUT", p, b ?? {})
const del = <T>(p: string) => request<T>("DELETE", p)

export const adminApi = {
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
  edgeToken: (id: number) => post<{ token: string; connect_url: string }>(`/admin/qb-servers/id/${id}/edge-token`),
  edgeUnbind: (id: number) => del<ListLike<QBServer>>(`/admin/qb-servers/id/${id}/edge-token`),
  rotateEdgeKey: () => post<{ keys: number }>("/admin/edge/rotate-key"),

  users: (q: string, page: number) => get<ListLike<AdminUser>>(`/admin/users${qs({ q, page })}`),
  updateUser: (id: number, b: AdminUserUpdate) => put<AdminUser>(`/admin/users/id/${id}`, b),

  invites: () => get<ListLike<InviteCode>>("/admin/invites"),
  createInvite: (b: InviteCreate) => post<InviteCode>("/admin/invites", b),
  updateInvite: (id: number, b: InvitePatch) => put<InviteCode>(`/admin/invites/id/${id}`, b),
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
  bangumiData: () => get<BangumiDataStatus>("/admin/bangumi-data"),
  runBangumiData: () => post<BangumiDataStatus>("/admin/bangumi-data/run"),
  manami: () => get<ManamiStatus>("/admin/manami"),
  runManami: () => post<ManamiStatus>("/admin/manami/run"),

  trackers: () => get<TrackersResponse>("/admin/trackers"),
  rankRules: () => get<RankRulesResponse>("/admin/rank-rules"),
  saveRankRules: (b: RankRules) => put<RankRulesResponse>("/admin/rank-rules", b),

  clef: () => get<ClefStatus>("/admin/clef"),
  /** 试运行：不写库，计入每日请求上限 */
  clefTest: (infohash: string) => post<ClefTestResponse>("/admin/clef/test", { infohash }),
  clefDecisions: (status: string, page: number, ih?: string) => get<Paged<ClefDecision> & { total: number }>(`/admin/clef/decisions${qs({ status, page, ih })}`),
  clefAccept: (id: number) => post<ClefDecision>(`/admin/clef/decisions/id/${id}/accept`),
  /** pending → rejected；applied → 撤销映射（文件改为未映射、已人工确认） */
  clefReject: (id: number) => post<ClefDecision>(`/admin/clef/decisions/id/${id}/reject`),
  clefRequeue: (ih: string) => post<ClefStatus>(`/admin/clef/jobs/hash/${seg(ih)}/run`),
  clefAnomalies: () => get<ClefAnomalies>("/admin/clef/anomalies"),
  /** 重新判断异常映射：把这些种子（默认全部）的异常文件交给决策模型 */
  clefAnomaliesRun: (infohashes?: string[]) => post<{ queued: number }>("/admin/clef/anomalies/run", { infohashes }),

  plays: (page: number) => get<ListLike<PlayRecord>>(`/admin/plays${qs({ page })}`),
  audit: (page: number) => get<ListLike<AuditEntry>>(`/admin/audit${qs({ page })}`),
}
