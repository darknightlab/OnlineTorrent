// VITE_MOCK=1 时安装：拦截 /api/v1/* 的 fetch，返回 fixtures 数据（只在 dev/mock 构建中被动态 import）。
// 切换身份：?mock_user=guest|member|vip|admin（保存在 sessionStorage）；默认 admin。
import * as fx from "./fixtures"
import { DEFAULT_RANK_RULES } from "@/lib/rank"
import { canonicalEpisodeKey, compareEpisodeKeys, episodeLabel, parseEpisodeKey, type EpisodeKey } from "@/lib/episode"
import { scopeVisible, validateScope } from "@/pages/admin/scope"
import { patternOK } from "./rankcheck"
import type { RankRules, EpisodeFile, EpisodeListItem, MeResponse, PlayResponse, Subject, Torrent, TorrentStatus, User } from "@/lib/types"
import type { TrackerHealth, AdminUser, BgmArchiveStatus, ContentScope, ContentScopeInput, GrantRole, PermissionItem, QBServer, Rule, Source } from "@/pages/admin/types"

type Handler = (ctx: { params: Record<string, string>; query: URLSearchParams; body: any }) => unknown

/** 非 200 的成功响应（如 202） */
class Status {
  constructor(
    public status: number,
    public data: unknown,
  ) {}
}

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

// ---- 内容范围（模拟 internal/store/content_scope.go：标签 = tags ∪ meta_tags；AND 优先于 OR；管理员不受限）----
const SCOPES_KEY = "pk-mock-scopes"
const defaultScope = (role: GrantRole): ContentScope => ({ role, tag_mode: "all", tags: [], tag_operators: [], exclude_tags: [] })
const scopes: Record<GrantRole, ContentScope> = {
  ...Object.fromEntries(ROLES.map((r) => [r, defaultScope(r)])),
  ...(JSON.parse(sessionStorage.getItem(SCOPES_KEY) ?? "{}") as Record<GrantRole, ContentScope>),
} as Record<GrantRole, ContentScope>
const scopesView = () => ({ roles: ROLES, items: ROLES.map((r) => scopes[r]) })
const myScope = () => {
  const role = roleOf(currentUser())
  return role === "admin" ? null : scopes[role as GrantRole]
}
const subjectTagsOf = (a: Subject) => [...(a.tags ?? []), ...(a.meta_tags ?? [])]
const visibleSubject = (a: Subject | undefined) => !a || scopeVisible(myScope(), subjectTagsOf(a))

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

/**
 * 特别篇演示：第一个有资源的条目额外带 12.5（总集篇）、SP1、NCOP1、NCED1 的种子（另有空的 PV1 —— 不出现在列表里）。
 * 这些种子合并到 pastEpisodes，选集 / 单集 / 收录状态都由标题里的「 - 键 」派生。
 */
const specialSubject = subjects.find((a) => a.latest_torrent)
if (specialSubject) {
  const lt = specialSubject.latest_torrent!
  const extra = ["12.5", "SP01", "NCOP1", "NCED1"].map((k, i): Torrent => ({
    ...lt,
    id: `${lt.id.slice(0, 20)}sp${String(i).padStart(2, "0")}`,
    infohash: lt.infohash.slice(0, 36) + "5" + String(i).padStart(3, "0"),
    title: lt.title.replace(/ - \d+ /, ` - ${k} `),
    size_bytes: k.startsWith("NC") ? 48 * 1024 ** 2 : lt.size_bytes,
    published_at: new Date(new Date(lt.published_at).getTime() - (i + 1) * 3 * 86400_000).toISOString(),
  }))
  pastEpisodes.get(specialSubject.key)?.push(...extra)
}

const torrents: Torrent[] = [
  ...subjects.flatMap((a) => (a.latest_torrent ? [a.latest_torrent] : [])),
  ...[...pastEpisodes.values()].flat(),
  ...fx.buildTorrents(),
]
torrents.sort((a, b) => b.published_at.localeCompare(a.published_at))
const plays = fx.buildPlays(torrents)
const settings = fx.settings.map((s) => ({ ...s }))
const secrets: Record<string, string> = {}
const withSwarm = (ts: Torrent[]) => ts.map((t) => ({ ...t, swarm: fx.mockSwarm(t.infohash) }))

/** 后台 Tracker：按当前设置列出，状态为假定的「最后一次使用」结果 */
function trackersView() {
  const list = (key: string) => (settings.find((s) => s.key === key)?.value as string[] | undefined) ?? []
  const ago = (min: number) => new Date(Date.now() - min * 60_000).toISOString()
  const health = (url: string, i: number, kind: "bt" | "webtorrent"): TrackerHealth => {
    const k = url.startsWith("udp") ? "udp" : url.startsWith("ws") ? "wss" : "http"
    if (i === 3) return { url, kind: k, list: kind, status: "unused", result: "", latency_ms: 0, last_error: "", checked_at: null, last_ok_at: null, last_error_at: null, ok_count: 0, fail_count: 0 }
    if (i === 2 || (kind === "webtorrent" && i === 1))
      return { url, kind: k, list: kind, status: "error", result: i === 2 ? "timeout" : "protocol", latency_ms: 0, last_error: i === 2 ? "scrape: timeout" : "websocket.Dial: bad status", checked_at: ago(4), last_ok_at: null, last_error_at: ago(4), ok_count: 0, fail_count: 7 }
    return { url, kind: k, list: kind, status: "ok", result: "ok", latency_ms: 180 + i * 140, last_error: "", checked_at: ago(4 + i), last_ok_at: ago(4 + i), last_error_at: null, ok_count: 42 - i * 5, fail_count: i }
  }
  return {
    items: [...list("webtorrent.trackers").map((u, i) => health(u, i, "webtorrent")), ...list("bt.trackers").map((u, i) => health(u, i, "bt"))],
    swarm: { enabled: settings.find((s) => s.key === "swarm.enabled")?.value !== false, running: 0, runs: 37, torrents: 412, last_at: ago(4), last_ms: 640, last_torrents: 3, last_answered: 3, skipped_busy: 0 },
    stats: { torrents: torrents.length, scraped: Math.round(torrents.length * 0.6), known: Math.round(torrents.length * 0.7), dead: Math.round(torrents.length * 0.1) },
    lists: { webtorrent: list("webtorrent.trackers"), bt: list("bt.trackers") },
  }
}

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
  if (!a || !visibleSubject(a)) throw new HttpError(404, "not_found", "番剧不存在")
  return a
}

/** 标题中「 - 12 」「 - 12.5 」「 - SP01 」「 - NCOP1 」→ 集的键（规范写法）；没有时 null */
const keyOf = (title: string) => canonicalEpisodeKey(title.match(/ - (\d+(?:\.\d+)?|(?:SP|NCOP|NCED|PV|CM)\d*)(?:v\d)? /i)?.[1])

/** 选集：1..max(已播, 已知最大整数集) ∪ 有资源的键（同后端 episodeList） */
function episodeList(a: Subject): EpisodeListItem[] {
  const counts = new Map<string, number>()
  for (const t of subjectTorrents(a)) {
    const k = keyOf(t.title)
    if (k) counts.set(k, (counts.get(k) ?? 0) + 1)
  }
  const keys = new Map<string, EpisodeKey>()
  let top = a.episodes_aired
  for (const k of counts.keys()) {
    const p = parseEpisodeKey(k)!
    keys.set(k, p)
    if (p.kind === "main" && Number.isInteger(p.number)) top = Math.max(top, p.number)
  }
  // 官方章节：1..eps（含未播出的集）+ 一个官方 SP
  const official = (a.eps ?? 0) > 0
  top = Math.max(top, a.eps ?? 0)
  for (let n = 1; n <= top; n++) keys.set(String(n), { kind: "main", number: n })
  if (official) keys.set("sp1", { kind: "sp", number: 1 })
  const begin = new Date(a.begin).getTime() + 8 * 3600_000
  const day = (n: number) => new Date(begin + (n - 1) * 7 * 86400_000).toISOString().slice(0, 10)
  return [...keys.entries()]
    .sort((x, y) => compareEpisodeKeys(x[1], y[1]))
    .map(([key, k]) => {
      const t = k.kind === "main" && Number.isInteger(k.number) && official ? fx.EPISODE_TITLES[(k.number - 1 + a.subject_id) % fx.EPISODE_TITLES.length] : null
      return {
        key,
        ...k,
        label: episodeLabel(k),
        sources: counts.get(key) ?? 0,
        ...(k.kind === "main" ? { aired: k.number >= 1 && k.number <= a.episodes_aired } : {}),
        ...(official && (k.kind === "main" ? k.number <= (a.eps ?? 0) : key === "sp1")
          ? {
              official: true,
              ...(k.kind === "main" ? { airdate: day(k.number) } : {}),
              // 偶尔缺少标题（与真实数据一致）
              ...(t && k.number % 5 !== 3 ? { title: t[0], title_ja: t[1] } : key === "sp1" ? { title: "总集篇" } : {}),
            }
          : {}),
      }
    })
}

/** 条目的全部种子：最新一集 + 往期 + 标题相关 */
function subjectTorrents(a: Subject): Torrent[] {
  const name = (a.title_cn || a.title).slice(0, 4)
  const lt = (a as Partial<fx.MockSubject>).latest_torrent
  const own = [...(lt ? [lt] : []), ...(pastEpisodes.get(a.key) ?? [])]
  const related = torrents.filter((t) => t.title.includes(name) && !own.includes(t))
  return [...own, ...related]
}

/** 与后端一致：当前用户无权播放的种子不出现在任何列表中（管理员看到全部） */
function playableFor(access: { action: string }): boolean {
  const role = roleOf(currentUser())
  if (role === "admin") return true
  const need = { allow: 0, login: 1, vip: 2, block: 99 }[access.action] ?? 99
  const have = { guest: 0, member: 1, vip: 2 }[role as "guest"] ?? 0
  return have >= need
}
const listable = <T extends { access: { action: string } }>(ts: T[]) => ts.filter((t) => playableFor(t.access))

function subjectOfTorrent(t: Torrent): Subject | undefined {
  return subjects.find((a) => a.latest_torrent?.id === t.id || pastEpisodes.get(a.key)?.includes(t) || t.title.includes((a.title_cn || a.title).slice(0, 4)))
}

// ---- 新种子（本地库没有元数据）：play 返回 pending，resolve/events 推送结果 ----
// ?mock_resolve=redirect|ready|denied|timeout（默认 ready）
const MOCK_RESOLVE = new URLSearchParams(location.search).get("mock_resolve") || "ready"
const resolved = new Map<string, string>()

function pendingPlay(ih: string): PlayResponse {
  return { status: "pending", access: { action: "pending", reason: "" }, infohash: ih, name: "", magnet: "", webseeds: [], meta: null }
}

function resolvedPlay(ih: string): PlayResponse | null {
  const r = resolved.get(ih)
  if (!r) return pendingPlay(ih)
  if (r === "denied") throw new HttpError(451, "blocked", "该资源含有色情内容，已被屏蔽")
  if (r === "redirect") {
    const a = subjects[0]
    return {
      status: "ready", access: { action: "allow", reason: "" }, infohash: ih, name: "[Mock] 新种子 - 01", magnet: `magnet:?xt=urn:btih:${ih}`, webseeds: [],
      meta: { subject_id: a.subject_id, subject_key: a.key, bgm_id: a.bgm_id, title: a.title_cn || a.title, episode: 1, episode_key: "1", episode_label: "第 1 话", file_index: 0 },
    }
  }
  return null // ready: an ordinary unmapped torrent
}

function playOf(t: Torrent | undefined, ih: string): PlayResponse {
  requirePerm("POST /api/v1/torrents/hash/{ih}/play")
  if (!t) {
    const p = resolvedPlay(ih)
    if (p) return p
  }
  const u = currentUser()
  const action = t?.access.action ?? "allow"
  if (action === "block") throw new HttpError(451, "blocked", t?.access.reason || "该资源已被屏蔽")
  if (action === "login" && !u) throw new HttpError(401, "login_required", "该资源需要登录后观看")
  if (action === "vip" && (!u || (u.role !== "vip" && u.role !== "admin"))) throw new HttpError(402, "vip_required", t?.access.reason || "该资源仅限大会员")
  const found = t ? subjectOfTorrent(t) : undefined
  const a = visibleSubject(found) ? found : undefined
  const name = t?.title ?? "Unknown torrent"
  const key = (t ? keyOf(t.title) : null) ?? (a ? String(a.episodes_aired) : null)
  const k = parseEpisodeKey(key)
  return {
    status: "ready",
    access: { action: "allow", reason: "" },
    infohash: ih,
    name,
    magnet: `magnet:?xt=urn:btih:${ih}&dn=${encodeURIComponent(name)}&tr=${encodeURIComponent("wss://tracker.projectk.org")}`,
    webseeds: [],
    meta:
      a && k
        ? {
            subject_id: a.subject_id,
            subject_key: a.key,
            bgm_id: a.bgm_id,
            title: a.title_cn || a.title,
            episode: k.kind === "main" ? k.number : null,
            episode_key: key!,
            episode_label: episodeLabel(k),
            file_index: 0,
          }
        : null,
  }
}

// ---- 收录状态（GET /torrents/hash/{ih}）----
/** 请求服务器做种的时间：做种后 ~5 秒「刮削完成」 */
const seededAt = new Map<string, number>()
const MAP_DELAY_MS = 5000
/** ?mock_map=none：用户磁力刮削不到条目（演示留在播放页） */
const MOCK_MAP = new URLSearchParams(location.search).get("mock_map")

/** 与 mock 播放引擎生成的文件列表一致：0 为视频，其余为字幕 / 图片 / 文本 */
function mockFiles(name: string) {
  const base = name.replace(/\.(mkv|mp4)$/i, "")
  const video = `${base}${/HEVC|MKV/i.test(base) ? ".mkv" : ".mp4"}`
  return [video, `${base}.sc.ass`, `${base}.tc.ass`, "cover.jpg", "readme.txt"].map((n, index) => ({
    index,
    path: `${base}/${n}`,
    size_bytes: index === 0 ? 368 * 1024 * 1024 : 46 * 1024,
    kind: index === 0 ? ("episode" as const) : ("other" as const),
  }))
}

function torrentStatus(ih: string): TorrentStatus {
  const t = torrents.find((x) => x.infohash === ih)
  const at = seededAt.get(ih)
  if (!t) {
    // 用户磁力：做种前 unknown；做种后 pending，约 5 秒后 ready 并映射到某番最新一集
    if (at === undefined) return { status: "unknown", title: "", files: [] }
    if (Date.now() - at < MAP_DELAY_MS) return { status: "pending", title: "", files: [] }
    const name = "Unknown torrent"
    const a = MOCK_MAP === "none" ? undefined : subjects.find((x) => x.latest_episode)
    return {
      status: "ready",
      title: name,
      files: mockFiles(name).map((f) =>
        f.index === 0 && a
          ? { ...f, subject: { subject_id: a.subject_id, bgm_id: a.bgm_id, title: a.title, title_cn: a.title_cn }, episode: a.latest_episode ?? 1, episode_key: String(a.latest_episode ?? 1) }
          : f,
      ),
    }
  }
  const found = subjectOfTorrent(t)
  const a = visibleSubject(found) ? found : undefined
  const key = keyOf(t.title)
  const k = parseEpisodeKey(key)
  const kind = !k ? "episode" : ({ main: "episode", sp: "sp", ncop: "op", nced: "ed", pv: "pv", cm: "cm" } as const)[k.kind]
  return {
    status: "ready",
    title: t.title,
    files: mockFiles(t.title).map((f) =>
      f.index === 0 && a && k
        ? {
            ...f,
            kind,
            subject: { subject_id: a.subject_id, bgm_id: a.bgm_id, title: a.title, title_cn: a.title_cn },
            ...(k.kind === "main" ? { episode: k.number } : {}),
            episode_key: key!,
          }
        : f,
    ),
  }
}

// ---- 抓取源 ----
const sources = fx.sources
const manami = {
  url: "https://github.com/manami-project/anime-offline-database/releases/latest/download/anime-offline-database.jsonl",
  release: "2026-07-04", synced_at: new Date(Date.now() - 86400_000).toISOString(),
  entries: 41537, linked: 8228, names: 117423, duration_sec: 3.5, running: false,
  next_at: new Date(Date.now() + 6 * 86400_000).toISOString(),
}
const bgmArchive: BgmArchiveStatus = {
  dump: "dump-2026-10-13.210353Z.zip",
  dump_at: "2026-10-13T21:03:54Z",
  digest: "sha256:19d12d42502fefeb4a2b14561cc978986a554320b06ca4bbf454d1830b101f90",
  synced_at: new Date(Date.now() - 5 * 3600_000).toISOString(),
  subjects: 31174,
  episodes: 347689,
  relations: 29893,
  duration_sec: 85,
  checked_at: new Date(Date.now() - 40 * 60_000).toISOString(),
  error: "GET https://api.github.com/repos/bangumi/Archive/releases/latest: dial tcp: i/o timeout",
  error_at: new Date(Date.now() - 40 * 60_000).toISOString(),
  running: false,
}

function probe(body: { kind?: string; url?: string }) {
  const url = String(body.url ?? "").trim()
  if (!/^https?:\/\//.test(url)) throw new HttpError(400, "bad_request", "地址必须是 http(s) 链接")
  if (/fail|example\.com/.test(url)) throw new HttpError(400, "source_probe_failed", "没有可识别的种子条目（需要 enclosure .torrent、磁力链接或 nyaa:infoHash）")
  const moe = body.kind === "bangumi_moe"
  const host = new URL(url).hostname
  return {
    title: moe ? "bangumi.moe" : `${host} - RSS`,
    link: `https://${host}/`,
    format: moe ? "bangumi.moe JSON" : "rss 2.0",
    item_count: moe ? 30 : 75,
    samples: torrents.slice(0, 3).map((t, i) => ({
      title: t.title,
      url: `https://${host}/view/${1000 + i}`,
      published_at: t.published_at,
      infohash: i === 2 ? undefined : t.infohash,
      torrent_url: i === 1 ? `https://${host}/download/${1000 + i}.torrent` : undefined,
      magnet: i !== 1,
    })),
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
  ["GET", "/schedule", () => (requirePerm("GET /api/v1/schedule"), { generated_at: new Date().toISOString(), items: subjects.filter(visibleSubject).map(fx.toScheduleItem) })],
  ["GET", "/subjects/:provider/:id", ({ params }) => {
    requirePerm("GET /api/v1/subjects/{provider}/{id}")
    const a = findSubject(params.provider, params.id)
    return { subject: a, torrents: withSwarm(listable(subjectTorrents(a))) }
  }],
  ["GET", "/subjects/:provider/:id/episodes", ({ params }) => {
    requirePerm("GET /api/v1/subjects/{provider}/{id}/episodes")
    return { items: episodeList(findSubject(params.provider, params.id)) }
  }],
  ["GET", "/subjects/:provider/:id/episodes/:n", ({ params }) => {
    requirePerm("GET /api/v1/subjects/{provider}/{id}/episodes/{n}")
    const a = findSubject(params.provider, params.id)
    const k = parseEpisodeKey(params.n)
    if (!k) throw new HttpError(400, "bad_request", "无效集数")
    const key = canonicalEpisodeKey(params.n)
    const items = listable(subjectTorrents(a))
      .filter((t) => keyOf(t.title) === key)
      .map((t, i): EpisodeFile => ({
        infohash: t.infohash,
        file_index: 0,
        path: `${t.title}.mkv`,
        size_bytes: t.size_bytes,
        torrent_title: t.title,
        team: t.team?.name ?? "",
        published_at: t.published_at,
        source_id: t.id.startsWith("ih:") ? "" : t.id,
        source: t.source,
        confidence: i === 0 ? 0.95 : 0.8,
        access: t.access,
        swarm: fx.mockSwarm(t.infohash),
      }))
    return { subject: a, key, kind: k.kind, number: k.number, label: episodeLabel(k), ...(k.kind === "main" ? { episode: k.number } : {}), items }
  }],
  ["GET", "/search/:keyword", ({ params, query }) => {
    requirePerm("GET /api/v1/search/{keyword}")
    const kw = params.keyword.toLowerCase().split(/\s+/).filter(Boolean)
    const p = Math.min(50, Math.max(1, Number(query.get("page") || 1)))
    const hit = (s: string) => kw.every((k) => s.toLowerCase().includes(k))
    const subs = p === 1 ? subjects.filter((a) => visibleSubject(a) && hit(`${a.title} ${a.title_cn}`)).slice(0, 24) : []
    const list = torrents.filter((t) => hit(`${t.title} ${t.team?.name ?? ""}`) && visibleSubject(subjectOfTorrent(t)) && playableFor(t.access))
    const pg = page(list, p, 30)
    return { subjects: subs, torrents: withSwarm(pg.items), page: pg.page, page_count: Math.min(50, pg.page_count), total: list.length }
  }],
  ["GET", "/torrents/hash/:ih", ({ params }) => (requirePerm("GET /api/v1/torrents/hash/{ih}"), { ...torrentStatus(params.ih.toLowerCase()), swarm: fx.mockSwarm(params.ih.toLowerCase()) })],
  // mock 引擎自带模拟元数据，不需要 .torrent
  ["GET", "/torrents/hash/:ih/torrent", () => {
    requirePerm("GET /api/v1/torrents/hash/{ih}/torrent")
    throw new HttpError(404, "not_found", "没有该种子的种子文件")
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
  ["POST", "/torrents/hash/:ih/seed", ({ params, body }) => {
    requirePerm("POST /api/v1/torrents/hash/{ih}/seed")
    const t = torrents.find((x) => x.infohash === params.ih.toLowerCase())
    if (t?.access.action === "block") throw new HttpError(451, "blocked", t.access.reason || "该资源已被屏蔽")
    // qB 只下载请求的文件：多文件种子必须指定 file；上限按要下载的文件计（模拟：单个文件 ≈ 种子大小 / 文件数）
    const file = (body as { file?: unknown } | undefined)?.file
    const n = Math.max(1, t?.files ?? 1)
    if (n > 1 && typeof file !== "number") throw new HttpError(400, "bad_request", "请指定要播放的文件（file）")
    if (t && t.size_bytes / n > fx.site.max_auto_size_bytes) throw new HttpError(413, "too_large", `要下载的文件过大（超过 ${fx.site.max_auto_size_bytes >> 20} MB），服务器不参与做种`)
    if (!seededAt.has(params.ih.toLowerCase())) seededAt.set(params.ih.toLowerCase(), Date.now())
    return { seeded: true }
  }],
  ["POST", "/torrents/hash/:ih/report", () => (requirePerm("POST /api/v1/torrents/hash/{ih}/report"), {})],

  // ---- admin ----
  ["GET", "/admin/stats", () => (requireAdmin(), fx.stats())],
  ["GET", "/admin/sources", () => (requireAdmin(), { items: sources })],
  ["GET", "/admin/sources/schema", () => (requireAdmin(), fx.sourceSchema)],
  ["POST", "/admin/sources/probe", ({ body }) => (requireAdmin(), probe(body))],
  ["POST", "/admin/sources", ({ body }) => {
    requireAdmin()
    const url = String(body.url ?? "").trim()
    if (sources.some((x) => x.url === url)) throw new HttpError(409, "source_exists", "该地址已存在")
    const r = probe(body)
    const now = new Date().toISOString()
    const s: Source = {
      id: Math.max(0, ...sources.map((x) => x.id)) + 1,
      name: body.name || r.title,
      kind: body.kind || "rss",
      url,
      interval_min: Math.max(5, Number(body.interval_min) || 15),
      enabled: body.enabled ?? true,
      proxy_disabled: !!body.proxy_disabled,
      last_run_at: null,
      last_ok_at: null,
      last_items: 0,
      last_new: 0,
      last_error: "",
      fail_count: 0,
      running: false,
      next_run_at: body.enabled ?? true ? now : null,
      created_at: now,
      updated_at: now,
    }
    sources.push(s)
    return new Status(201, s)
  }],
  ["PUT", "/admin/sources/id/:id", ({ params, body }) => {
    requireAdmin()
    const s = sources.find((x) => x.id === Number(params.id))
    if (!s) throw new HttpError(404, "not_found", "抓取源不存在")
    if ((body.url !== undefined && body.url !== s.url) || (body.kind !== undefined && body.kind !== s.kind)) probe({ ...s, ...body })
    if (body.interval_min !== undefined && Number(body.interval_min) < 5) throw new HttpError(400, "bad_request", "间隔最小 5 分钟")
    Object.assign(s, body, { updated_at: new Date().toISOString() })
    if (!s.enabled) s.next_run_at = null
    else if (body.enabled || body.url) s.next_run_at = new Date().toISOString()
    return s
  }],
  ["DELETE", "/admin/sources/id/:id", ({ params }) => {
    requireAdmin()
    sources.splice(sources.findIndex((x) => x.id === Number(params.id)), 1)
    return { items: sources }
  }],
  ["POST", "/admin/sources/id/:id/run", ({ params }) => {
    requireAdmin()
    const s = sources.find((x) => x.id === Number(params.id))
    if (!s) throw new HttpError(404, "not_found", "抓取源不存在")
    if (s.running) throw new HttpError(409, "busy", "正在抓取")
    s.running = true
    window.setTimeout(() => {
      const now = new Date().toISOString()
      s.running = false
      s.last_run_at = now
      if (s.id === 4) {
        s.fail_count += 1
        s.last_error = "GET https://share.dmhy.org/topics/rss/sort_id/2/rss.xml: dial tcp: i/o timeout"
      } else {
        s.last_ok_at = now
        s.last_error = ""
        s.fail_count = 0
        s.last_new = Math.floor(Math.random() * 6)
        s.last_items = s.last_items || 50
      }
      s.next_run_at = s.enabled ? new Date(Date.now() + s.interval_min * 60_000 * 2 ** s.fail_count).toISOString() : null
    }, 4000)
    return new Status(202, { ...s })
  }],
  ["GET", "/admin/manami", () => (requireAdmin(), { ...manami })],
  ["POST", "/admin/manami/run", () => {
    requireAdmin()
    if (manami.running) throw new HttpError(409, "busy", "正在导入")
    manami.running = true
    setTimeout(() => { manami.running = false; manami.synced_at = new Date().toISOString() }, 3000)
    return new Status(202, { ...manami })
  }],
  ["GET", "/admin/bgm-archive", () => (requireAdmin(), { ...bgmArchive })],
  ["POST", "/admin/bgm-archive/run", () => {
    requireAdmin()
    if (bgmArchive.running) throw new HttpError(409, "busy", "正在同步")
    const started = Date.now()
    Object.assign(bgmArchive, { running: true, phase: "subjects" })
    const timer = window.setInterval(() => {
      const t = Date.now() - started
      if (t < 4000) bgmArchive.phase = "subjects"
      else if (t < 8000) bgmArchive.phase = "episodes"
      else if (t < 10000) bgmArchive.phase = "relations"
      else {
        window.clearInterval(timer)
        const now = new Date().toISOString()
        Object.assign(bgmArchive, { running: false, phase: undefined, synced_at: now, checked_at: now, duration_sec: Math.round(t / 1000), error: undefined, error_at: undefined })
      }
    }, 500)
    return new Status(202, { ...bgmArchive })
  }],
  ["GET", "/admin/metadata-jobs", () => (requireAdmin(), { items: fx.metadataJobs })],
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
  ["GET", "/admin/content-scopes", () => (requireAdmin(), scopesView())],
  ["PUT", "/admin/content-scopes/role/:role", ({ params, body }) => {
    requireAdmin()
    if (params.role === "admin") throw new HttpError(400, "bad_request", "管理员不受内容范围限制")
    if (!ROLES.includes(params.role as GrantRole)) throw new HttpError(404, "not_found", "角色不存在")
    const known = ["tag_mode", "tags", "tag_operators", "exclude_tags", "role"]
    const unknown = Object.keys(body ?? {}).find((k) => !known.includes(k))
    if (unknown) throw new HttpError(400, "bad_request", `请求格式错误：unknown field "${unknown}"`)
    const input: ContentScopeInput = {
      tag_mode: body.tag_mode,
      tags: (body.tags ?? []).map((t: string) => t.trim()),
      tag_operators: body.tag_operators ?? [],
      exclude_tags: (body.exclude_tags ?? []).map((t: string) => t.trim()),
    }
    const err = validateScope(input)
    if (err) throw new HttpError(400, "bad_request", err)
    const role = params.role as GrantRole
    scopes[role] =
      input.tag_mode === "all"
        ? { ...defaultScope(role), exclude_tags: [...new Set(input.exclude_tags)].sort() }
        : { role, ...input, exclude_tags: [...new Set(input.exclude_tags)].sort() }
    sessionStorage.setItem(SCOPES_KEY, JSON.stringify(scopes))
    return scopes[role]
  }],
  ["GET", "/admin/subject-tags", ({ query }) => {
    requireAdmin()
    const term = (query.get("q") ?? "").trim()
    const counts = new Map<string, number>()
    for (const a of subjects) for (const t of new Set(subjectTagsOf(a))) counts.set(t, (counts.get(t) ?? 0) + 1)
    const items = [...counts.entries()]
      .filter(([n]) => n.includes(term))
      .sort((x, y) => Number(y[0].startsWith(term)) - Number(x[0].startsWith(term)) || y[1] - x[1] || x[0].localeCompare(y[0]))
      .slice(0, 30)
      .map(([name, count]) => ({ name, count }))
    return { items }
  }],
  ["GET", "/admin/trackers", () => (requireAdmin(), trackersView())],
  ["GET", "/admin/rank-rules", () => (requireAdmin(), { rules: fx.site.rank_rules, defaults: DEFAULT_RANK_RULES })],
  ["PUT", "/admin/rank-rules", ({ body }) => {
    requireAdmin()
    const r = body as RankRules
    if (!r?.availability || !Array.isArray(r.keywords) || !Array.isArray(r.teams)) throw new HttpError(400, "bad_request", "规则格式错误")
    for (const [i, k] of r.keywords.entries()) {
      if (!k.label?.trim()) throw new HttpError(400, "bad_request", `第 ${i + 1} 个关键词：名称不能为空`)
      if (!patternOK(k.pattern)) throw new HttpError(400, "bad_request", `第 ${i + 1} 个关键词（${k.label}）：正则无效`)
    }
    fx.site.rank_rules = structuredClone(r)
    return { rules: fx.site.rank_rules, defaults: DEFAULT_RANK_RULES }
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

/**
 * 模拟 WebSeed 可用性推送（SSE）：/api/v1/torrents/hash/{ih}/webseed/events[/file/{index}]
 * 默认 ~1.5s 后 waiting、~5s 后 ready；?mock_webseed=timeout|unavailable|error 可演示其它分支。
 */
class MockEventSource extends EventTarget {
  static readonly CONNECTING = 0
  static readonly OPEN = 1
  static readonly CLOSED = 2
  readonly CONNECTING = 0
  readonly OPEN = 1
  readonly CLOSED = 2
  readyState = 0
  readonly url: string
  readonly withCredentials: boolean
  onopen: ((e: Event) => void) | null = null
  onmessage: ((e: MessageEvent) => void) | null = null
  onerror: ((e: Event) => void) | null = null
  private timers: number[] = []

  constructor(url: string, ih: string, init?: EventSourceInit) {
    super()
    this.url = url
    this.withCredentials = !!init?.withCredentials
    const mode = new URLSearchParams(location.search).get("mock_webseed")
    console.info(`[mock] SSE ${url}`)
    const at = (ms: number, fn: () => void) => this.timers.push(window.setTimeout(() => this.readyState !== 2 && fn(), ms))
    at(200, () => {
      try {
        requirePerm("GET /webseed/hash/{ih}/{path...}")
      } catch {
        this.readyState = 2
        return this.fire(new Event("error"))
      }
      this.readyState = 1
      this.fire(new Event("open"))
      if (mode === "error") {
        // 模拟连接反复断开（浏览器会自动重连）
        for (let i = 1; i <= 8; i++) at(i * 600, () => ((this.readyState = 0), this.fire(new Event("error"))))
        return
      }
      at(1300, () => this.emit("waiting", { progress: 0.42 }))
      at(3000, () => this.emit("waiting", { progress: 0.8 }))
      if (mode === "timeout") at(4800, () => this.emit("timeout", { reason: "mock timeout" }))
      else if (mode === "unavailable") at(4800, () => this.emit("unavailable", { reason: "mock: 服务器无此种子" }))
      else at(4800, () => this.emit("ready", { pieces: null, seeds: [{ url: `https://edge-mock.invalid/webseed/hash/${ih}/`, have: null, complete: true, token: "mock-token", exp: Math.floor(Date.now() / 1000) + 600 }] }))
    })
  }

  private emit(type: string, data: unknown) {
    this.fire(new MessageEvent(type, { data: JSON.stringify(data) }))
  }

  private fire(e: Event) {
    this.dispatchEvent(e)
    if (e.type === "open") this.onopen?.(e)
    else if (e.type === "error") this.onerror?.(e)
    else if (e.type === "message") this.onmessage?.(e as MessageEvent)
  }

  close() {
    this.readyState = 2
    this.timers.forEach((t) => window.clearTimeout(t))
  }
}

/** 模拟 GET /api/v1/torrents/hash/{ih}/resolve/events：~3 秒后按 ?mock_resolve 推送结果 */
class MockResolveSource extends EventTarget {
  readyState = 0
  readonly url: string
  readonly withCredentials = true
  onerror: ((e: Event) => void) | null = null
  private timer = 0

  constructor(url: string, ih: string) {
    super()
    this.url = url
    console.info(`[mock] SSE ${url} (${MOCK_RESOLVE})`)
    this.timer = window.setTimeout(() => {
      if (this.readyState === 2) return
      this.readyState = 1
      const a = subjects[0]
      const data: Record<string, unknown> =
        MOCK_RESOLVE === "redirect"
          ? { path: `/subject/${a.bgm_id ? "bgm" : "id"}/${a.bgm_id || a.subject_id}/episode/1/hash/${ih}/file/0` }
          : MOCK_RESOLVE === "denied"
            ? { action: "block", reason: "该资源含有色情内容，已被屏蔽" }
            : {}
      const type = ["redirect", "denied", "timeout"].includes(MOCK_RESOLVE) ? MOCK_RESOLVE : "ready"
      if (type !== "timeout") resolved.set(ih, type)
      this.dispatchEvent(new MessageEvent(type, { data: JSON.stringify(data) }))
    }, 3000)
  }

  close() {
    this.readyState = 2
    window.clearTimeout(this.timer)
  }
}

export function installMock() {
  const RealEventSource = window.EventSource
  window.EventSource = function (this: unknown, url: string | URL, init?: EventSourceInit) {
    const u = new URL(String(url), location.origin)
    const m = u.origin === location.origin && u.pathname.match(/^\/api\/v1\/torrents\/hash\/([^/]+)\/webseed\/events(?:\/file\/\d+)?$/)
    if (m) return new MockEventSource(u.href, decodeURIComponent(m[1]), init)
    const rm = u.origin === location.origin && u.pathname.match(/^\/api\/v1\/torrents\/hash\/([^/]+)\/resolve\/events$/)
    if (rm) return new MockResolveSource(u.href, decodeURIComponent(rm[1]))
    return new RealEventSource(url, init)
  } as unknown as typeof EventSource
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
      if (data instanceof Status) return json(data.status, data.data)
      return json(200, data ?? {})
    } catch (e) {
      if (e instanceof HttpError) return json(e.status, { error: { code: e.code, message: e.message } })
      return json(500, { error: { code: "mock_error", message: String(e) } })
    }
  }
  console.info("%c[mock] API mock enabled", "color:#4caf50")
}
