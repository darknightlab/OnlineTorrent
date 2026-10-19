// Mock 数据：条目（番剧）来自 bgm.tv /calendar 的真实条目（seed.json），种子标题来自 bangumi.moe 最新列表。
// 放送时间相对“现在”动态计算，保证“已更新/即将播出”徽标在任何时候都有样例。
import seed from "./seed.json"
import type {
  AccessAction,
  AdminStats,
  AdminUser,
  AuditEntry,
  InviteCode,
  PermissionItem,
  PlayRecord,
  MetadataJob,
  QBServer,
  RedeemCode,
  Source,
  SourceSchema,
  Review,
  Rule,
  ScheduleItem,
  SettingItem,
  Site,
  Subject,
  Torrent,
} from "@/lib/types"

const HOUR = 3600_000
const DAY = 24 * HOUR
const TZ_OFFSET = 8 * HOUR // Asia/Shanghai

function rand(seedN: number) {
  // 确定性伪随机，保证每次刷新数据一致
  let x = Math.sin(seedN * 9301 + 49297) * 233280
  x = x - Math.floor(x)
  return x
}

function hexHash(n: number) {
  let s = ""
  for (let i = 0; i < 40; i++) s += Math.floor(rand(n * 41 + i) * 16).toString(16)
  return s
}

function parseSize(s: string) {
  const m = s.match(/([\d.]+)\s*(KB|MB|GB|TB)/i)
  if (!m) return 300 * 1024 ** 2
  const mul = { KB: 1024, MB: 1024 ** 2, GB: 1024 ** 3, TB: 1024 ** 4 }[m[2].toUpperCase() as "KB"]
  return Math.round(parseFloat(m[1]) * mul)
}

const TEAMS = ["ANi", "桜都字幕组", "猎户发布组", "LoliHouse", "喵萌奶茶屋", "北宇治字幕组", "沸班亚马制作组", "Kirara Fantasia"]

function accessFor(i: number): { action: AccessAction; reason: string } {
  if (i % 11 === 4) return { action: "login", reason: "该资源仅限会员观看" }
  if (i % 13 === 7) return { action: "vip", reason: "超过 2GB 的资源仅限大会员" }
  if (i % 17 === 9) return { action: "block", reason: "版权方要求屏蔽" }
  return { action: "allow", reason: "" }
}

/** 本周某星期 + HH:mm（上海时间）对应的 UTC 时间戳；相对 now 返回最近一次过去的和下一次的 */
function airTimes(weekday: number, hhmm: string, now: number) {
  const [h, m] = hhmm.split(":").map(Number)
  const local = new Date(now + TZ_OFFSET)
  const todayW = local.getUTCDay()
  const base = Date.UTC(local.getUTCFullYear(), local.getUTCMonth(), local.getUTCDate(), h, m) - TZ_OFFSET
  let t = base + ((weekday - todayW) % 7) * DAY
  while (t > now) t -= 7 * DAY
  while (t + 7 * DAY <= now) t += 7 * DAY
  return { last: t, next: t + 7 * DAY }
}

const SUMMARIES = [
  "在魔王被讨伐后的世界，精灵魔法使踏上了重新认识人类的旅程。漫长的岁月里，她一点点理解了曾经同伴们留下的意义。",
  "平凡的高中生在某天意外卷入了异世界的纷争，凭借独特的能力与伙伴们一起对抗接踵而来的危机。",
  "以青春与成长为主题的群像剧。少年少女们在社团、比赛与日常之间，寻找属于自己的答案。",
  "古老的王国即将迎来变革。被流放的少女军师带领一支弱小的军队，在乱世中开辟出一条生路。",
]

/** mock 内部用：每部番的最新种子（生成种子列表 / 单集资源用；接口不返回） */
export type MockSubject = Subject & { latest_torrent: Torrent | null }

export function buildSubjects(now = Date.now()) {
  return seed.subjects.map((a, i): MockSubject => {
    const { last, next } = airTimes(a.weekday, a.air_time, now)
    const begin = new Date(`${a.begin}T00:00:00+08:00`).getTime()
    const episodes = Math.max(0, Math.floor((last - begin) / (7 * DAY)) + 1)
    const notStarted = begin > now
    const key = `bgm:${a.bgm_id}`
    const team = TEAMS[i % TEAMS.length]
    const ep = String(Math.max(1, episodes)).padStart(2, "0")
    const name = a.title_cn || a.title
    const latest: Torrent | null =
      notStarted || i % 6 === 5
        ? null
        : {
            id: `moe${String(i).padStart(4, "0")}${hexHash(i).slice(0, 16)}`,
            infohash: hexHash(i),
            title: `[${team}] ${name} / ${a.title} - ${ep} [WebRip 1080p ${i % 3 === 0 ? "HEVC-10bit" : "AVC"} AAC][简繁内封字幕]`,
            size_bytes: Math.round((260 + rand(i) * 700) * 1024 ** 2),
            files: 1,
            team: { id: `team-${i % TEAMS.length}`, name: team },
            tags: [{ id: "tag-cn", name: "简繁内封" }],
            published_at: new Date(Math.min(now - (3 + i) * 60_000, last + (20 + rand(i + 3) * 120) * 60_000)).toISOString(),
            source: "bangumi.moe",
            access: accessFor(i),
          }
    return {
      key,
      subject_id: 1000 + i,
      title: a.title,
      title_cn: a.title_cn,
      cover: a.cover,
      begin: new Date(begin).toISOString(),
      weekday: a.weekday,
      air_time: a.air_time,
      last_air_at: notStarted ? null : new Date(last).toISOString(),
      next_air_at: notStarted ? new Date(`${a.begin}T${a.air_time}:00+08:00`).toISOString() : new Date(next).toISOString(),
      episodes_aired: notStarted ? 0 : episodes,
      bgm_id: a.bgm_id,
      moe_tag_id: `5ff${hexHash(i + 100).slice(0, 21)}`,
      latest_torrent: latest,
      latest_episode: latest ? Math.max(1, episodes) : null,
      summary: a.summary || SUMMARIES[i % SUMMARIES.length],
      tags: ["TV", ["奇幻", "校园", "战斗", "日常", "恋爱"][i % 5], ["原创", "漫画改", "小说改"][i % 3], "2026年10月", ["治愈", "热血", "搞笑"][i % 3], "日本"],
      meta_tags: ["TV", "日本", ["原创", "漫画改", "小说改"][i % 3]],
      nsfw: false,
      score: i % 7 === 6 ? 0 : Math.round((5.5 + rand(i + 9) * 3.4) * 10) / 10,
      rank: i % 7 === 6 ? 0 : 50 + Math.round(rand(i + 11) * 4000),
      eps: i % 4 === 3 ? 0 : [12, 13, 24, 12][i % 4],
      external: { bgm: a.bgm_id, mal: 50000 + i * 37, anidb: 18000 + i * 11, tmdb: i % 2 ? 230000 + i : null, imdb: null },
    }
  })
}

export function toScheduleItem(a: MockSubject): ScheduleItem {
  const { summary: _s, tags: _t, external: _e, meta_tags: _m, nsfw: _n, score: _sc, rank: _r, eps: _ep, latest_torrent: _lt, ...rest } = a
  void [_s, _t, _e, _m, _n, _sc, _r, _ep, _lt]
  return rest
}

/** 种子来源轮换（抓取源名称；"user" 为用户提交） */
const SOURCES_ROT = ["bangumi.moe", "蜜柑计划", "Nyaa", "bangumi.moe", "user"]

export function buildTorrents(now = Date.now()): Torrent[] {
  const real = seed.torrents.map((t, i): Torrent => {
    const size = parseSize(t.size)
    const acc = accessFor(i + 3)
    const src = SOURCES_ROT[i % SOURCES_ROT.length]
    return {
      id: src === "bangumi.moe" ? t.id : `ih:${t.infohash}`,
      infohash: t.infohash,
      title: t.title,
      size_bytes: size,
      files: size > 3 * 1024 ** 3 ? 4 : i % 9 === 2 ? 3 : 1,
      team: t.team,
      tags: [],
      published_at: new Date(now - (i * 23 + 4) * 60_000).toISOString(),
      source: SOURCES_ROT[i % SOURCES_ROT.length],
      access: size > 2 * 1024 ** 3 && acc.action === "allow" ? { action: "vip", reason: "超过 2GB 的资源仅限大会员" } : acc,
    }
  })
  const extra: Torrent[] = Array.from({ length: 60 }, (_, i) => {
    const t = real[i % real.length]
    const infohash = hexHash(500 + i)
    return { ...t, id: t.id.startsWith("ih:") ? `ih:${infohash}` : `old${i}${t.id.slice(5)}`, infohash, published_at: new Date(now - (12 + i * 1.7) * HOUR).toISOString() }
  })
  return [...real, ...extra]
}

export const site: Site = {
  name: "Project Kawaii",
  announcement_md:
    "**onlinetorrent v2 预览版** 上线啦。\n\n- 支持新番时间表、会员登录与大会员资源\n- 推荐使用最新版 **Chrome / Edge** 观看；HEVC、AC3 等编码可能无法播放\n- 问题反馈请到 [GitHub](https://github.com/darknightlab)",
  registration: "invite",
  turnstile_site_key: "",
  invite_required: true,
  trackers: ["wss://tracker.projectk.org", "wss://tracker.dnlab.net:16443", "wss://tracker.openwebtorrent.com"],
  ice_servers: [{ urls: ["stun:stun.miwifi.com:3478", "stun:stun.l.google.com:19302"] }],
  max_auto_size_bytes: 2 * 1024 ** 3,
  webseed: { enabled: true },
}

export const settings: SettingItem[] = [
  { key: "site.name", group: "站点", type: "string", label: "站点名称", value: "Project Kawaii" },
  { key: "site.description", group: "站点", type: "text", label: "站点简介", description: "用于 SEO 描述", value: "在浏览器中直接观看番剧与种子" },
  { key: "site.announcement_md", group: "站点", type: "markdown", label: "站点公告", description: "Markdown，显示在首页侧栏", value: site.announcement_md },
  { key: "auth.registration", group: "用户", type: "enum", label: "注册方式", options: ["open", "invite", "closed"], value: "invite" },
  { key: "auth.turnstile_site_key", group: "用户", type: "string", label: "Turnstile Site Key", description: "留空则不启用人机验证", value: "" },
  { key: "auth.turnstile_secret", group: "用户", type: "secret", label: "Turnstile Secret", value: "", is_set: true },
  { key: "auth.session_days", group: "用户", type: "number", label: "会话有效期（天）", value: 30 },
  { key: "play.trackers", group: "播放", type: "string_list", label: "Tracker 列表", description: "每行一个，前端与 qB 都会使用", value: site.trackers },
  { key: "play.ice_servers", group: "播放", type: "string_list", label: "STUN/TURN 服务器", value: ["stun:stun.miwifi.com:3478", "stun:stun.l.google.com:19302"] },
  { key: "play.max_auto_size_bytes", group: "播放", type: "number", label: "自动推送大小上限", description: "超过此大小的种子不会自动推送到 qB", value: 2147483648 },
  { key: "policy.default_action", group: "策略", type: "enum", label: "默认策略动作", options: ["allow", "login", "vip", "block"], value: "allow" },
  { key: "policy.guest_can_play", group: "策略", type: "bool", label: "允许游客播放", value: true },
  { key: "qb.cleanup_after_hours", group: "qB 池", type: "number", label: "做种清理时间（小时）", value: 72 },
  { key: "qb.watchdog_enabled", group: "qB 池", type: "bool", label: "启用 wss tracker 看门狗", description: "wss tracker 卡在 Updating 时自动 reannounce", value: true },
  { key: "upstream.proxy", group: "上游", type: "string", label: "上游代理", description: "抓取与 bgm.tv / TMDB 请求共用；为空时使用环境变量 HTTPS_PROXY 等", value: "" },
  { key: "upstream.tmdb_api_key", group: "上游", type: "secret", label: "TMDB API Key", description: "TMDB v3 key 或 v4 读取令牌；设置后搜索时本地无结果会查询 TMDB", value: "", is_set: false },
  { key: "crawl.enabled", group: "抓取", type: "bool", label: "启用后台抓取", description: "关闭后各抓取源不再定时运行（仍可在「抓取源」页手动抓取）", value: true },
  { key: "bgm_archive.enabled", group: "抓取", type: "bool", label: "同步 bangumi 数据", description: "每 6 小时检查 bangumi Archive（bgm.tv 官方每周数据导出），有新版本时导入全部动画条目、章节与条目关联；关闭后「立即同步」仍可用", value: true },
  { key: "crawl.workers", group: "抓取", type: "number", label: "抓取并发数", description: "同时抓取的源数", value: 2 },
]

const iso = (msAgo: number) => new Date(Date.now() - msAgo).toISOString()

export const qbServers: QBServer[] = [
  { id: 1, name: "ams", url: "https://qbittorrent-ams.kawaii.cam", username: "admin", category: "动画/临时", enabled: true, seeding_time_limit_min: 4320, ratio_limit: 5, max_active: 40, add_wss_trackers: true, online: true, version: "v5.3.0beta1", last_error: "", last_check_at: iso(40_000) },
  { id: 2, name: "pku", url: "https://pku.dnlab.net:8888", username: "admin", category: "动画/临时", enabled: true, seeding_time_limit_min: 2880, ratio_limit: 3, max_active: 20, add_wss_trackers: true, online: false, version: "v5.2.4", last_error: "libtorrent 2.0 不支持 WebTorrent；dial tcp: i/o timeout", last_check_at: iso(120_000) },
  { id: 3, name: "zj", url: "https://zj.dnlab.net:8888", username: "admin", category: "临时", enabled: false, seeding_time_limit_min: 1440, ratio_limit: 2, max_active: 10, add_wss_trackers: false, online: false, version: "", last_error: "", last_check_at: null },
]

const NAMES = ["canoziia", "yotsuba", "kawaii", "miku", "nanako", "frieren", "himmel", "fern", "stark", "lain", "rei", "asuka", "shinji", "mikasa", "eren", "anya", "loid", "yor", "bocchi", "nijika", "ryo", "kita", "marin", "gojo", "nobara"]

export const users: AdminUser[] = NAMES.map((n, i) => ({
  id: i + 1,
  username: n,
  email: `${n}@example.com`,
  role: i === 0 ? "admin" : i % 5 === 1 ? "vip" : "member",
  vip_until: i % 5 === 1 ? new Date(Date.now() + (30 + i) * DAY).toISOString() : null,
  created_at: iso((400 - i * 12) * DAY),
  disabled: i === 13,
  last_login_at: iso(i * 5 * HOUR),
}))

export function makeInvite(id: number, code: string, p: Partial<InviteCode> = {}): InviteCode {
  return {
    id,
    code,
    url: `${location.origin}/register/invite/${code}`,
    max_uses: 1,
    uses: 0,
    role: "member",
    vip_days: 0,
    expires_at: null,
    note: "",
    disabled: false,
    created_at: new Date().toISOString(),
    ...p,
  }
}

export const invites: InviteCode[] = Array.from({ length: 8 }, (_, i) =>
  makeInvite(i + 1, `PK${hexHash(900 + i).slice(0, 8).toUpperCase()}`, {
    max_uses: [1, 1, 10, 0, 1, 5, 1, 1][i],
    uses: [1, 1, 4, 23, 0, 0, 0, 0][i],
    role: i === 4 ? "vip" : "member",
    vip_days: i === 4 ? 30 : i === 2 ? 7 : 0,
    expires_at: i === 7 ? iso(5 * DAY) : i === 6 ? iso(-2 * DAY) : i === 5 ? iso(-20 * DAY + 30 * DAY) : null,
    note: ["", "", "QQ 群", "论坛长期链接", "赞助者", "", "", ""][i],
    disabled: i === 5,
    created_at: iso((10 - i) * DAY),
  }),
).reverse()

export const redeemCodes: RedeemCode[] = Array.from({ length: 6 }, (_, i) => ({
  id: i + 1,
  code: `VIP-${hexHash(950 + i).slice(0, 12).toUpperCase()}`,
  vip_days: [30, 30, 90, 365, 30, 7][i],
  created_at: iso((20 - i) * DAY),
  used_by_name: i < 2 ? NAMES[i + 1] : null,
  used_at: i < 2 ? iso((15 - i) * DAY) : null,
  note: i === 3 ? "年度赞助" : "",
})).reverse()

export const rules: Rule[] = [
  { id: 1, name: "政治敏感关键词", enabled: true, priority: 100, match: { field: "title", op: "regex", value: "(?i)(某敏感词|另一个敏感词)" }, action: "block", label: "political", note: "一律屏蔽" },
  { id: 2, name: "成人内容分类", enabled: true, priority: 90, match: { field: "tag", op: "equals", value: "里番" }, action: "block", label: "porn", note: "" },
  { id: 3, name: "版权方投诉", enabled: true, priority: 80, match: { field: "infohash", op: "equals", value: hexHash(17 * 9) }, action: "block", label: "copyright", note: "2026-10-12 邮件投诉" },
  { id: 4, name: "4K 资源仅大会员", enabled: true, priority: 50, match: { field: "title", op: "regex", value: "(2160p|4K)" }, action: "vip", label: "", note: "节省带宽" },
  { id: 5, name: "国漫合集需登录", enabled: true, priority: 40, match: { field: "team", op: "contains", value: "GM-Team" }, action: "login", label: "", note: "" },
  { id: 6, name: "测试规则（停用）", enabled: false, priority: 10, match: { field: "file", op: "contains", value: ".exe" }, action: "block", label: "other", note: "可执行文件" },
]

export const reviews: Review[] = [
  { id: 1, infohash: hexHash(301), title: "[某字幕组] 某作品 - 01 [1080p]", source: "report", reason: "内容与标题不符：实际是另一部作品", status: "pending", reporter_name: "miku", created_at: iso(2 * HOUR) },
  { id: 2, infohash: hexHash(302), title: "[Raws] Unknown Title - 12 (BD 1920x1080 x265 FLAC)", source: "auto", reason: "未匹配任何作品，自动进入审核", status: "pending", reporter_name: null, created_at: iso(5 * HOUR) },
  { id: 3, infohash: hexHash(303), title: "合集 2026 全集打包 [4K]", source: "auto", reason: "体积 38.2 GB 超过自动阈值", status: "pending", reporter_name: null, created_at: iso(9 * HOUR) },
  { id: 4, infohash: hexHash(304), title: "[喵萌奶茶屋] 某番 - 03 [简体]", source: "report", reason: "侵犯版权：疑似未授权搬运", status: "pending", reporter_name: "anya", created_at: iso(26 * HOUR) },
]

export function buildPlays(torrents: Torrent[]): PlayRecord[] {
  return torrents.slice(0, 40).map((t, i) => ({
    id: 1000 - i,
    infohash: t.infohash,
    name: t.title,
    user: i % 4 === 0 ? null : NAMES[i % NAMES.length],
    ip: `203.0.113.${(i * 7) % 255}`,
    action: t.access.action,
    seeded_by: t.access.action === "allow" ? ["ams"] : [],
    created_at: iso(i * 17 * 60_000),
  }))
}

const AUDIT_ACTIONS = ["settings.update", "rule.create", "qb.test", "user.update", "review.decide", "invite.create", "auth.login", "play.block"]

export const audit: AuditEntry[] = Array.from({ length: 36 }, (_, i) => ({
  id: 5000 - i,
  actor: i % 5 === 4 ? null : NAMES[i % 3],
  action: AUDIT_ACTIONS[i % AUDIT_ACTIONS.length],
  target: ["site", "rule#4", "qb#1", "user#7", "review#2", "invite×5", "canoziia", hexHash(i).slice(0, 12)][i % 8],
  detail: ["registration: open → invite", "4K 资源仅大会员", "online v5.3.0beta1", "role: member → vip", "action=block label=copyright", "批量生成 5 个", "login ok", "命中规则 #1"][i % 8],
  created_at: iso(i * 47 * 60_000),
}))

export function stats(): AdminStats {
  return { users: users.length, plays_24h: 1284, qb_online: qbServers.filter((q) => q.online).length, qb_total: qbServers.length, pending_reviews: reviews.filter((r) => r.status === "pending").length }
}

// ---------- 接口权限（与后端 internal/api 中 s.route(...) 注册保持一致） ----------

type Spec = Omit<PermissionItem, "granted">
const spec = (method: string, path: string, access: PermissionItem["access"], group: string, label: string, default_roles: string[] | null = null): Spec => ({
  key: `${method} ${path}`,
  method,
  path,
  group,
  label,
  access,
  default_roles,
})
const adm = (method: string, path: string, label: string) => spec(method, `/api/v1/admin${path}`, "admin", "后台", label)

export const permissionSpecs: Spec[] = [
  spec("GET", "/api/v1/site", "public", "账号", "站点信息"),
  spec("GET", "/api/v1/me", "public", "账号", "当前用户与权限"),
  spec("POST", "/api/v1/auth/register", "public", "账号", "注册"),
  spec("POST", "/api/v1/auth/login", "public", "账号", "登录"),
  spec("POST", "/api/v1/auth/logout", "public", "账号", "退出登录"),
  spec("POST", "/api/v1/me/password", "user", "账号", "修改密码", ["member"]),
  spec("POST", "/api/v1/me/redeem", "user", "账号", "使用兑换码", ["member"]),
  spec("GET", "/api/v1/schedule", "configurable", "浏览", "新番时间表", ["guest"]),
  spec("GET", "/api/v1/subjects/{provider}/{id}", "configurable", "浏览", "番剧详情", ["guest"]),
  spec("GET", "/api/v1/subjects/{provider}/{id}/episodes", "configurable", "浏览", "选集列表（正片 + SP / NCOP / NCED / PV / CM）", ["guest"]),
  spec("GET", "/api/v1/subjects/{provider}/{id}/episodes/{n}", "configurable", "浏览", "单集资源列表", ["guest"]),
  spec("GET", "/api/v1/search/{keyword}", "configurable", "浏览", "搜索（番剧 + 种子，本地库；本地无番剧时按需查询 bgm.tv / TMDB）", ["guest"]),
  spec("GET", "/api/v1/torrents/hash/{ih}", "configurable", "浏览", "种子收录状态与文件刮削结果", ["guest"]),
  spec("GET", "/api/v1/torrents/hash/{ih}/torrent", "configurable", "播放", "获取种子文件（HTTP 元数据，无需 P2P 节点即可显示文件列表）", ["guest"]),
  spec("POST", "/api/v1/torrents/hash/{ih}/play", "configurable", "播放", "打开种子（按 infohash）", ["guest"]),
  spec("POST", "/api/v1/torrents/moe/{id}/play", "configurable", "播放", "打开种子（按 bangumi.moe ID）", ["guest"]),
  spec("POST", "/api/v1/torrents/hash/{ih}/seed", "configurable", "播放", "让服务器下载做种（消耗服务器资源）", ["member"]),
  spec("GET", "/webseed/hash/{ih}/{path...}", "configurable", "播放", "WebSeed 文件下载（HTTP 加速，消耗服务器流量）", ["member"]),
  spec("GET", "/api/v1/torrents/hash/{ih}/webseed/events", "configurable", "播放", "等待 WebSeed 就绪（服务器推送）", ["member"]),
  spec("GET", "/api/v1/torrents/hash/{ih}/webseed/events/file/{idx}", "configurable", "播放", "等待指定文件的 WebSeed 就绪（服务器推送）", ["member"]),
  spec("POST", "/api/v1/torrents/hash/{ih}/report", "user", "播放", "举报种子", ["member"]),
  adm("GET", "/stats", "概览 · 查看"),
  adm("GET", "/settings", "站点设置 · 查看"),
  adm("PUT", "/settings", "站点设置 · 修改"),
  adm("GET", "/qb-servers", "qB 服务器 · 查看"),
  adm("POST", "/qb-servers", "qB 服务器 · 新建/操作"),
  adm("PUT", "/qb-servers/id/{id}", "qB 服务器 · 修改"),
  adm("DELETE", "/qb-servers/id/{id}", "qB 服务器 · 删除"),
  adm("POST", "/qb-servers/id/{id}/test", "qB 服务器 · 新建/操作"),
  adm("GET", "/sources", "抓取源 · 查看"),
  adm("GET", "/sources/schema", "抓取源 · 查看"),
  adm("POST", "/sources", "抓取源 · 新建/操作"),
  adm("POST", "/sources/probe", "抓取源 · 新建/操作"),
  adm("PUT", "/sources/id/{id}", "抓取源 · 修改"),
  adm("DELETE", "/sources/id/{id}", "抓取源 · 删除"),
  adm("POST", "/sources/id/{id}/run", "抓取源 · 新建/操作"),
  adm("GET", "/metadata-jobs", "元数据队列 · 查看"),
  adm("GET", "/users", "用户 · 查看"),
  adm("PUT", "/users/id/{id}", "用户 · 修改"),
  adm("GET", "/invites", "邀请码 · 查看"),
  adm("POST", "/invites", "邀请码 · 新建/操作"),
  adm("PUT", "/invites/id/{id}", "邀请码 · 修改"),
  adm("GET", "/redeem-codes", "兑换码 · 查看"),
  adm("POST", "/redeem-codes", "兑换码 · 新建/操作"),
  adm("GET", "/rules", "策略规则 · 查看"),
  adm("POST", "/rules", "策略规则 · 新建/操作"),
  adm("PUT", "/rules/id/{id}", "策略规则 · 修改"),
  adm("DELETE", "/rules/id/{id}", "策略规则 · 删除"),
  adm("POST", "/policy/test", "策略测试 · 新建/操作"),
  adm("GET", "/reviews", "审核队列 · 查看"),
  adm("PUT", "/reviews/id/{id}", "审核队列 · 修改"),
  adm("GET", "/plays", "播放记录 · 查看"),
  adm("GET", "/audit", "审计日志 · 查看"),
  adm("GET", "/permissions", "接口权限 · 查看"),
  adm("PUT", "/permissions", "接口权限 · 修改"),
  adm("GET", "/content-scopes", "内容范围 · 查看"),
  adm("PUT", "/content-scopes/role/{role}", "内容范围 · 修改"),
  adm("GET", "/subject-tags", "内容范围 · 查看"),
  { key: "GET /meta/v1/", method: "GET", path: "/meta/v1/…", group: "映射库", label: "读取映射库（作品 / 种子文件映射）", access: "configurable", default_roles: ["guest"] },
  { key: "PUT /meta/v1/", method: "PUT", path: "/meta/v1/torrents/hash/{ih}/files/{idx}", group: "映射库", label: "纠正映射", access: "admin", default_roles: null },
]

// ---------- 抓取源 ----------

export const sourceSchema: SourceSchema = {
  kinds: [
    { value: "rss", label: "RSS / Atom", description: "通用 RSS/Atom：enclosure/link 中的 .torrent、磁力链接或 nyaa:infoHash", example_url: "https://nyaa.si/?page=rss&c=1_3&f=0" },
    { value: "bangumi_moe", label: "bangumi.moe", description: "bangumi.moe JSON 最新列表第 1 页（含标签与文件列表，作为强刮削线索）", example_url: "https://bangumi.moe/api/torrent/page/1" },
  ],
  min_interval_min: 5,
  default_interval_min: 15,
}

function src(id: number, name: string, kind: string, url: string, p: Partial<Source> = {}): Source {
  const ran = p.last_run_at !== undefined ? p.last_run_at : iso((3 + id * 2) * 60_000)
  return {
    id,
    name,
    kind,
    url,
    interval_min: 15,
    enabled: true,
    proxy_disabled: false,
    last_run_at: ran,
    last_ok_at: ran,
    last_items: 75,
    last_new: 12,
    last_error: "",
    fail_count: 0,
    running: false,
    next_run_at: ran ? new Date(new Date(ran).getTime() + 15 * 60_000).toISOString() : null,
    created_at: iso(3 * DAY),
    updated_at: iso(3 * DAY),
    ...p,
  }
}

export const sources: Source[] = [
  src(1, "bangumi.moe", "bangumi_moe", "https://bangumi.moe/api/torrent/page/1", { last_items: 30, last_new: 0 }),
  src(2, "蜜柑计划", "rss", "https://mikanani.me/RSS/Classic", { last_items: 100, last_new: 29 }),
  src(3, "Nyaa", "rss", "https://nyaa.si/?page=rss&c=1_3&f=0", { last_new: 41 }),
  src(4, "动漫花园", "rss", "https://share.dmhy.org/topics/rss/sort_id/2/rss.xml", {
    last_error: "GET https://share.dmhy.org/topics/rss/sort_id/2/rss.xml: dial tcp: i/o timeout",
    fail_count: 3,
    last_ok_at: iso(2 * DAY),
    interval_min: 30,
  }),
  src(5, "ACG.RIP", "rss", "https://acg.rip/.xml", { enabled: false, last_run_at: null, last_ok_at: null, next_run_at: null, last_items: 0, last_new: 0 }),
]

export const metadataJobs: MetadataJob[] = Array.from({ length: 14 }, (_, i): MetadataJob => {
  const status: MetadataJob["status"] = i === 0 ? "running" : i < 3 ? "pending" : i === 5 ? "failed" : "done"
  return {
    infohash: hexHash(700 + i),
    source_id: [3, 2, 3, 2, 3][i % 5],
    status,
    attempts: status === "failed" ? 3 : status === "pending" ? 0 : 1,
    next_at: iso(-(i + 1) * 30_000),
    server_id: 1,
    added: status !== "pending",
    started_at: status === "pending" ? null : iso(i * 90_000),
    last_error: status === "failed" ? "获取元数据超时（2 分钟）" : "",
    created_at: iso(i * 120_000 + 60_000),
    updated_at: iso(i * 80_000),
  }
})

/** 单集标题（mock） */
export const EPISODE_TITLES: [string, string][] = [
  ["旅程的终点", "冒険の終わり"],
  ["不必是魔法", "別に魔法じゃなくたって"],
  ["杀人魔法", "人を殺す魔法"],
  ["魂之安眠地", "魂の眠る地"],
  ["亡者的幻影", "死者の幻影"],
  ["村庄的英雄", "村の英雄"],
  ["宛如童话", "おとぎ話のようなもの"],
  ["芙莉莲", "フリーレン"],
  ["断头台阿乌拉", "断頭台のアウラ"],
  ["强大的魔法使", "強い魔法使い"],
  ["北方的村庄", "北側諸国の村"],
  ["真正的勇者", "本物の勇者"],
]
