// VITE_MOCK=1 时安装：拦截 /api/v1/* 的 fetch，返回 fixtures 数据（只在 dev/mock 构建中被动态 import）。
// 切换身份：?mock_user=guest|member|vip|admin（保存在 sessionStorage）；默认 admin。
import * as fx from "./fixtures"
import type { AdminUser, EpisodeFile, GrantRole, MeResponse, PermissionItem, PlayResponse, QBServer, Rule, Subject, Torrent, User } from "@/lib/types"

type Handler = (ctx: { params: Record<string, string>; query: URLSearchParams; body: any }) => unknown

class HttpError {
  constructor(
    public status: number,
    public code: string,
    public message = "",
  ) {}
}

const USER_KEY = "pk-mock-user"
const q = new URLSearchParams(location.search).get("mock_user")
if (q) sessionStorage.setItem(USER_KEY, q)

function currentUser(): User | null {
  const role = sessionStorage.getItem(USER_KEY) ?? "admin"
  if (role === "guest") return null
  const r = (["member", "vip", "admin"].includes(role) ? role : "admin") as User["role"]
  return {
    id: 1,
    username: r === "admin" ? "canoziia" : `demo_${r}`,
    email: "demo@projectk.org",
    role: r,
    vip_until: r === "vip" ? new Date(Date.now() + 42 * 86400_000).toISOString() : null,
    created_at: "2024-03-01T08:00:00Z",
  }
}
// ---- 接口权限（模拟后端 perms.go 的语义：guest < member < vip < admin，低等级授权向上继承）----
const ROLES: GrantRole[] = ["guest", "member", "vip"]
const RANK: Record<string, number> = { guest: 0, member: 1, vip: 2, admin: 3 }
const GRANTS_KEY = "pk-mock-grants"
const savedGrants = JSON.parse(sessionStorage.getItem(GRANTS_KEY) ?? "{}") as Record<string, GrantRole[]>
const grants: Record<string, Set<GrantRole>> = Object.fromEntries(
  fx.permissionSpecs
    .filter((s) => s.access === "configurable" || s.access === "user")
    .map((s) => [s.key, new Set(savedGrants[s.key] ?? ((s.default_roles ?? []) as GrantRole[]))]),
)
const persistGrants = () => sessionStorage.setItem(GRANTS_KEY, JSON.stringify(Object.fromEntries(Object.entries(grants).map(([k, v]) => [k, [...v]]))))

function roleOf(u: User | null): string {
  if (!u) return "guest"
  if (u.role === "vip" && u.vip_until && new Date(u.vip_until).getTime() < Date.now()) return "member"
  return u.role
}

function allowed(role: string, key: string): boolean {
  const s = fx.permissionSpecs.find((x) => x.key === key)
  if (!s) return false
  if (role === "admin" || s.access === "public") return true
  if (s.access === "admin") return false
  if (s.access === "user" && role === "guest") return false
  return [...(grants[key] ?? [])].some((g) => RANK[g] <= RANK[role] && !(s.access === "user" && g === "guest"))
}

function requirePerm(key: string) {
  const u = currentUser()
  if (allowed(roleOf(u), key)) return
  if (!u) throw new HttpError(401, "login_required", "请先登录")
  throw new HttpError(403, "forbidden", "当前账号无权使用该功能")
}

const permissionsView = () => ({
  roles: ROLES,
  items: fx.permissionSpecs.map((s): PermissionItem => ({ ...s, granted: { guest: !!grants[s.key]?.has("guest"), member: !!grants[s.key]?.has("member"), vip: !!grants[s.key]?.has("vip") } })),
})

const me = (): MeResponse => {
  const u = currentUser()
  const role = roleOf(u)
  const permissions = fx.permissionSpecs.filter((s) => s.access !== "public" && s.access !== "admin" && allowed(role, s.key)).map((s) => s.key).sort()
  return { user: u, permissions }
}

const subjects = fx.buildSubjects()

/** 每个条目往期各集的种子（由最新一集派生） */
const pastEpisodes = new Map<string, Torrent[]>(
  subjects.map((a) => {
    const lt = a.latest_torrent
    const latestEp = a.latest_episode ?? 0
    const eps = lt
      ? Array.from({ length: Math.max(0, latestEp - 1) }, (_, i): Torrent => ({
          ...lt,
          id: `${lt.id.slice(0, 20)}${String(i).padStart(4, "0")}`,
          infohash: lt.infohash.slice(0, 38) + String(i).padStart(2, "0"),
          title: lt.title.replace(/ - \d+ /, ` - ${String(latestEp - 1 - i).padStart(2, "0")} `),
          published_at: new Date(new Date(lt.published_at).getTime() - (i + 1) * 7 * 86400_000).toISOString(),
        }))
      : []
    return [a.key, eps]
  }),
)

const torrents: Torrent[] = [
  ...subjects.flatMap((a) => (a.latest_torrent ? [a.latest_torrent] : [])),
  ...[...pastEpisodes.values()].flat(),
  ...fx.buildTorrents(),
]
torrents.sort((a, b) => b.published_at.localeCompare(a.published_at))
const plays = fx.buildPlays(torrents)
const settings = fx.settings.map((s) => ({ ...s }))
const secrets: Record<string, string> = {}
const settingsView = () => ({ items: settings.map((s) => (s.type === "secret" ? { ...s, value: "", is_set: s.is_set || !!secrets[s.key] } : s)) })
const qb = [...fx.qbServers]
const rules = [...fx.rules]
const users = [...fx.users]
const PAGE = 24

function page<T>(list: T[], p: number, size = PAGE) {
  const page_count = Math.max(1, Math.ceil(list.length / size))
  return { page: p, page_count, items: list.slice((p - 1) * size, p * size) }
}

function findSubject(provider: string, id: string): Subject {
  const a = subjects.find((x) => (provider === "bgm" ? String(x.bgm_id) === id : provider === "id" ? String(x.subject_id) === id : false))
  if (!a) throw new HttpError(404, "not_found", "番剧不存在")
  return a
}

const epOf = (title: string) => Number(title.match(/ - (\d+)(?:v\d)? /)?.[1] ?? 0)

/** 条目的全部种子：最新一集 + 往期 + 标题相关 */
function subjectTorrents(a: Subject): Torrent[] {
  const name = (a.title_cn || a.title).slice(0, 4)
  const own = [...(a.latest_torrent ? [a.latest_torrent] : []), ...(pastEpisodes.get(a.key) ?? [])]
  const related = torrents.filter((t) => t.title.includes(name) && !own.includes(t))
  return [...own, ...related]
}

function subjectOfTorrent(t: Torrent): Subject | undefined {
  return subjects.find((a) => a.latest_torrent?.id === t.id || pastEpisodes.get(a.key)?.includes(t) || t.title.includes((a.title_cn || a.title).slice(0, 4)))
}

function playOf(t: Torrent | undefined, ih: string): PlayResponse {
  requirePerm("POST /api/v1/torrents/hash/{ih}/play")
  const u = currentUser()
  const action = t?.access.action ?? "allow"
  if (action === "block") throw new HttpError(451, "blocked", t?.access.reason || "该资源已被屏蔽")
  if (action === "login" && !u) throw new HttpError(401, "login_required", "该资源需要登录后观看")
  if (action === "vip" && (!u || (u.role !== "vip" && u.role !== "admin"))) throw new HttpError(402, "vip_required", t?.access.reason || "该资源仅限大会员")
  const a = t ? subjectOfTorrent(t) : undefined
  const name = t?.title ?? "Unknown torrent"
  const ep = t ? epOf(t.title) : 0
  return {
    access: { action: "allow", reason: "" },
    infohash: ih,
    name,
    magnet: `magnet:?xt=urn:btih:${ih}&dn=${encodeURIComponent(name)}&tr=${encodeURIComponent("wss://tracker.projectk.org")}`,
    seeded_by: [],
    webseeds: [],
    meta: a ? { subject_id: a.subject_id, subject_key: a.key, bgm_id: a.bgm_id, title: a.title_cn || a.title, episode: ep || a.episodes_aired, file_index: 0 } : null,
  }
}

function requireAdmin() {
  const u = currentUser()
  if (!u) throw new HttpError(401, "login_required", "请先登录")
  if (u.role !== "admin") throw new HttpError(403, "forbidden", "需要管理员权限")
}

const routes: [string, string, Handler][] = [
  ["GET", "/site", () => fx.site],
  ["GET", "/me", () => me()],
  ["POST", "/auth/login", ({ body }) => {
    if (!body.identifier || !body.password) throw new HttpError(400, "invalid", "请输入账号和密码")
    if (body.password === "wrong") throw new HttpError(401, "invalid_credentials", "用户名或密码错误")
    sessionStorage.setItem(USER_KEY, body.identifier === "admin" || body.identifier === "canoziia" ? "admin" : "member")
    return me()
  }],
  ["POST", "/auth/register", ({ body }) => {
    if (fx.site.registration === "invite" && !body.invite_code) throw new HttpError(400, "invite_required", "需要邀请码")
    sessionStorage.setItem(USER_KEY, "member")
    return me()
  }],
  ["POST", "/auth/logout", () => {
    sessionStorage.setItem(USER_KEY, "guest")
    return {}
  }],
  ["POST", "/me/password", ({ body }) => {
    requirePerm("POST /api/v1/me/password")
    if (body.old_password === "wrong") throw new HttpError(400, "invalid_password", "原密码错误")
    return {}
  }],
  ["POST", "/me/redeem", ({ body }) => {
    requirePerm("POST /api/v1/me/redeem")
    if (!String(body.code).startsWith("VIP-")) throw new HttpError(400, "invalid_code", "兑换码无效或已使用")
    sessionStorage.setItem(USER_KEY, "vip")
    return me()
  }],
  ["GET", "/schedule", () => (requirePerm("GET /api/v1/schedule"), { generated_at: new Date().toISOString(), season: "2026-10", items: subjects.map(fx.toScheduleItem) })],
  ["GET", "/subjects/:provider/:id", ({ params }) => {
    requirePerm("GET /api/v1/subjects/{provider}/{id}")
    const a = findSubject(params.provider, params.id)
    return { subject: a, torrents: subjectTorrents(a) }
  }],
  ["GET", "/subjects/:provider/:id/episodes/:n", ({ params }) => {
    requirePerm("GET /api/v1/subjects/{provider}/{id}/episodes/{n}")
    const a = findSubject(params.provider, params.id)
    const n = Number(params.n)
    const items = subjectTorrents(a)
      .filter((t) => epOf(t.title) === n)
      .map((t, i): EpisodeFile => ({
        infohash: t.infohash,
        file_index: 0,
        path: `${t.title}.mkv`,
        size_bytes: t.size_bytes,
        torrent_title: t.title,
        team: t.team?.name ?? "",
        published_at: t.published_at,
        source_id: t.source === "bangumi.moe" ? t.id : "",
        confidence: i === 0 ? 0.95 : 0.8,
        access: t.access,
      }))
    return { subject: a, episode: n, items }
  }],
  ["GET", "/torrents/search", ({ query }) => {
    requirePerm("GET /api/v1/torrents/search")
    const kw = (query.get("q") || "").toLowerCase().split(/\s+/).filter(Boolean)
    const list = torrents.filter((t) => kw.every((k) => t.title.toLowerCase().includes(k) || t.team?.name.toLowerCase().includes(k)))
    return page(list, Number(query.get("page") || 1))
  }],
  ["POST", "/torrents/hash/:ih/play", ({ params }) => {
    const ih = params.ih.toLowerCase()
    if (!/^[0-9a-f]{40}$/.test(ih)) throw new HttpError(400, "bad_request", "infohash 格式错误")
    return playOf(torrents.find((t) => t.infohash === ih), ih)
  }],
  ["POST", "/torrents/moe/:id/play", ({ params }) => {
    requirePerm("POST /api/v1/torrents/moe/{id}/play")
    const t = torrents.find((x) => x.id === params.id)
    if (!t) throw new HttpError(404, "not_found", "未找到该种子")
    return playOf(t, t.infohash)
  }],
  ["POST", "/torrents/hash/:ih/seed", ({ params }) => {
    requirePerm("POST /api/v1/torrents/hash/{ih}/seed")
    const t = torrents.find((x) => x.infohash === params.ih.toLowerCase())
    if (t?.access.action === "block") throw new HttpError(451, "blocked", t.access.reason || "该资源已被屏蔽")
    if (t && t.size_bytes > fx.site.max_auto_size_bytes) throw new HttpError(413, "too_large", `该种子过大（超过 ${fx.site.max_auto_size_bytes >> 20} MB），服务器不参与做种`)
    return { seeded_by: ["ams"] }
  }],
  ["POST", "/torrents/hash/:ih/webseed", ({ params }) => {
    requirePerm("POST /api/v1/torrents/hash/{ih}/webseed")
    return { webseeds: [`${location.origin}/webseed/ams/${params.ih}/`], delay_sec: 3 }
  }],
  ["POST", "/torrents/hash/:ih/report", () => (requirePerm("POST /api/v1/torrents/hash/{ih}/report"), {})],

  // ---- admin ----
  ["GET", "/admin/stats", () => (requireAdmin(), fx.stats())],
  ["GET", "/admin/permissions", () => (requireAdmin(), permissionsView())],
  ["PUT", "/admin/permissions", ({ body }) => {
    requireAdmin()
    const input = (body.grants ?? {}) as Record<string, string[]>
    for (const [key, roles] of Object.entries(input)) {
      const s = fx.permissionSpecs.find((x) => x.key === key)
      if (!s) throw new HttpError(400, "bad_request", `未知接口: ${key}`)
      if (s.access !== "configurable" && s.access !== "user") throw new HttpError(400, "bad_request", `该接口权限不可修改: ${key}`)
      if (s.access === "user" && roles.includes("guest")) throw new HttpError(400, "bad_request", `该接口需要登录，不能授权给游客: ${key}`)
      if (roles.some((r) => !ROLES.includes(r as GrantRole))) throw new HttpError(400, "bad_request", `未知等级: ${roles.join(",")}`)
    }
    for (const [key, roles] of Object.entries(input)) grants[key] = new Set(roles as GrantRole[])
    persistGrants()
    return permissionsView()
  }],
  ["GET", "/admin/settings", () => (requireAdmin(), settingsView())],
  ["PUT", "/admin/settings", ({ body }) => {
    for (const [k, v] of Object.entries(body.values ?? {})) {
      const s = settings.find((x) => x.key === k)
      if (!s) throw new HttpError(400, "unknown_setting", `未知设置项 ${k}`)
      if (s.type === "secret") {
        if (v) secrets[k] = String(v)
      } else s.value = v as typeof s.value
    }
    return settingsView()
  }],
  ["GET", "/admin/qb-servers", () => (requireAdmin(), { items: qb, page: 1, page_count: 1 })],
  ["POST", "/admin/qb-servers", ({ body }) => {
    const s: QBServer = { ...body, id: Math.max(0, ...qb.map((x) => x.id)) + 1, online: false, version: "", last_error: "", last_check_at: null }
    delete s.password
    delete s.api_key
    qb.push(s)
    return s
  }],
  ["PUT", "/admin/qb-servers/id/:id", ({ params, body }) => {
    const i = qb.findIndex((x) => x.id === Number(params.id))
    const { password: _p, api_key: _k, ...rest } = body
    void _p
    void _k
    qb[i] = { ...qb[i], ...rest }
    return qb[i]
  }],
  ["DELETE", "/admin/qb-servers/id/:id", ({ params }) => {
    qb.splice(qb.findIndex((x) => x.id === Number(params.id)), 1)
    return {}
  }],
  ["POST", "/admin/qb-servers/id/:id/test", ({ params }) => {
    const s = qb.find((x) => x.id === Number(params.id))!
    s.last_check_at = new Date().toISOString()
    if (s.id === 2) throw new HttpError(502, "qb_unreachable", "连接失败：dial tcp 162.105.0.1:8888: i/o timeout")
    s.online = s.enabled
    s.version = s.version || "v5.3.0rc1"
    return s
  }],
  ["GET", "/admin/users", ({ query }) => {
    const kw = (query.get("q") || "").toLowerCase()
    return page(users.filter((u) => !kw || u.username.includes(kw) || u.email.includes(kw)), Number(query.get("page") || 1), 12)
  }],
  ["PUT", "/admin/users/id/:id", ({ params, body }) => {
    const i = users.findIndex((u) => u.id === Number(params.id))
    users[i] = { ...users[i], ...body } as AdminUser
    return users[i]
  }],
  ["GET", "/admin/invites", () => page(fx.invites, 1, 100)],
  ["POST", "/admin/invites", ({ body }) => {
    const inv = fx.makeInvite(Math.max(0, ...fx.invites.map((i) => i.id)) + 1, `PK${Math.random().toString(36).slice(2, 10).toUpperCase()}`, {
      max_uses: Number(body.max_uses) || 0,
      role: body.role === "vip" ? "vip" : "member",
      vip_days: Number(body.vip_days) || 0,
      expires_at: Number(body.expires_in_days) > 0 ? new Date(Date.now() + Number(body.expires_in_days) * 86400_000).toISOString() : null,
      note: body.note ?? "",
    })
    fx.invites.unshift(inv)
    return inv
  }],
  ["PUT", "/admin/invites/id/:id", ({ params, body }) => {
    const inv = fx.invites.find((i) => i.id === Number(params.id))
    if (!inv) throw new HttpError(404, "not_found", "邀请码不存在")
    inv.disabled = !!body.disabled
    return inv
  }],
  ["GET", "/admin/redeem-codes", () => page(fx.redeemCodes, 1, 100)],
  ["POST", "/admin/redeem-codes", ({ body }) => {
    const n = Math.min(100, Number(body.count) || 1)
    const created = Array.from({ length: n }, (_, i) => ({ id: Math.max(0, ...fx.redeemCodes.map((r) => r.id)) + i + 1, code: `VIP-${Math.random().toString(36).slice(2, 14).toUpperCase()}`, vip_days: Number(body.vip_days) || 30, created_at: new Date().toISOString(), used_by_name: null, used_at: null, note: body.note ?? "" }))
    fx.redeemCodes.unshift(...created)
    return { items: created }
  }],
  ["GET", "/admin/rules", () => ({ items: [...rules].sort((a, b) => b.priority - a.priority), page: 1, page_count: 1 })],
  ["POST", "/admin/rules", ({ body }) => {
    const r: Rule = { ...body, id: Math.max(0, ...rules.map((x) => x.id)) + 1 }
    rules.push(r)
    return r
  }],
  ["PUT", "/admin/rules/id/:id", ({ params, body }) => {
    const i = rules.findIndex((r) => r.id === Number(params.id))
    rules[i] = { ...rules[i], ...body }
    return rules[i]
  }],
  ["DELETE", "/admin/rules/id/:id", ({ params }) => {
    rules.splice(rules.findIndex((r) => r.id === Number(params.id)), 1)
    return {}
  }],
  ["POST", "/admin/policy/test", ({ body }) => {
    const sorted = [...rules].filter((r) => r.enabled).sort((a, b) => b.priority - a.priority)
    for (const r of sorted) {
      const hay: string[] =
        r.match.field === "title" ? [body.title ?? ""] : r.match.field === "file" ? (body.files ?? []) : r.match.field === "infohash" ? [body.infohash ?? ""] : r.match.field === "team" ? [body.team ?? ""] : r.match.field === "tag" ? (body.tags ?? []) : []
      const v = r.match.value
      const hit = hay.some((h) => {
        if (r.match.op === "equals") return h === v
        if (r.match.op === "contains") return h.toLowerCase().includes(v.toLowerCase())
        try {
          return new RegExp(v.replace(/^\(\?i\)/, ""), "i").test(h)
        } catch {
          return false
        }
      })
      if (hit) return { access: { action: r.action, reason: r.name }, matched_rule: r }
    }
    return { access: { action: settings.find((s) => s.key === "policy.default_action")?.value ?? "allow", reason: "默认动作" }, matched_rule: null }
  }],
  ["GET", "/admin/reviews", ({ query }) => page(fx.reviews.filter((r) => ((query.get("status") || "pending") === "pending") === (r.status === "pending")), 1, 100)],
  ["PUT", "/admin/reviews/id/:id", ({ params, body }) => {
    const r = fx.reviews.find((x) => x.id === Number(params.id))!
    Object.assign(r, body, { status: body.action === "dismiss" ? "dismissed" : "done" })
    return r
  }],
  ["GET", "/admin/plays", ({ query }) => page(plays, Number(query.get("page") || 1), 15)],
  ["GET", "/admin/audit", ({ query }) => page(fx.audit, Number(query.get("page") || 1), 15)],
]

function match(method: string, path: string) {
  for (const [m, pattern, h] of routes) {
    if (m !== method) continue
    const pp = pattern.split("/")
    const sp = path.split("/")
    if (pp.length !== sp.length) continue
    const params: Record<string, string> = {}
    let ok = true
    for (let i = 0; i < pp.length; i++) {
      if (pp[i].startsWith(":")) params[pp[i].slice(1)] = decodeURIComponent(sp[i])
      else if (pp[i] !== sp[i]) {
        ok = false
        break
      }
    }
    if (ok) return { h, params }
  }
  return null
}

const json = (status: number, data: unknown) =>
  new Response(JSON.stringify(data), { status, headers: { "Content-Type": "application/json" } })

export function installMock() {
  const realFetch = window.fetch.bind(window)
  window.fetch = async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = new URL(typeof input === "string" ? input : input instanceof URL ? input.href : input.url, location.origin)
    if (url.origin !== location.origin || !url.pathname.startsWith("/api/v1/")) return realFetch(input, init)
    const method = (init?.method || "GET").toUpperCase()
    const path = url.pathname.slice("/api/v1".length)
    await new Promise((r) => setTimeout(r, 120 + Math.random() * 280))
    const m = match(method, path)
    if (!m) return json(404, { error: { code: "not_found", message: `mock: ${method} ${path}` } })
    try {
      const body = init?.body ? JSON.parse(String(init.body)) : {}
      const data = m.h({ params: m.params, query: url.searchParams, body })
      return json(200, data ?? {})
    } catch (e) {
      if (e instanceof HttpError) return json(e.status, { error: { code: e.code, message: e.message } })
      return json(500, { error: { code: "mock_error", message: String(e) } })
    }
  }
  console.info("%c[mock] API mock enabled", "color:#4caf50")
}
