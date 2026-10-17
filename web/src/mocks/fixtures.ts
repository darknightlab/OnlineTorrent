// Mock 数据：番剧来自 bgm.tv /calendar 的真实条目（seed.json），种子标题来自 bangumi.moe 最新列表。
// 放送时间相对“现在”动态计算，保证“已更新/即将播出”徽标在任何时候都有样例。
import seed from "./seed.json"
import type {
  AccessAction,
  AdminStats,
  AdminUser,
  AnimeDetail,
  AuditEntry,
  InviteCode,
  PlayRecord,
  QBServer,
  RedeemCode,
  Review,
  Rule,
  ScheduleItem,
  SettingItem,
  Site,
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

export function buildAnime(now = Date.now()) {
  return seed.anime.map((a, i): AnimeDetail => {
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
      summary: a.summary || SUMMARIES[i % SUMMARIES.length],
      tags: ["TV", ["奇幻", "校园", "战斗", "日常", "恋爱"][i % 5], ["原创", "漫画改", "小说改"][i % 3], "2026年10月"],
      external: { bgm: a.bgm_id, mal: 50000 + i * 37, anidb: 18000 + i * 11, tmdb: i % 2 ? 230000 + i : null, imdb: null },
    }
  })
}

export function toScheduleItem(a: AnimeDetail): ScheduleItem {
  const { summary: _s, tags: _t, external: _e, ...rest } = a
  void _s
  void _t
  void _e
  return rest
}

export function buildTorrents(now = Date.now()): Torrent[] {
  const real = seed.torrents.map((t, i): Torrent => {
    const size = parseSize(t.size)
    const acc = accessFor(i + 3)
    return {
      id: t.id,
      infohash: t.infohash,
      title: t.title,
      size_bytes: size,
      files: size > 3 * 1024 ** 3 ? 4 : i % 9 === 2 ? 3 : 1,
      team: t.team,
      tags: [],
      published_at: new Date(now - (i * 23 + 4) * 60_000).toISOString(),
      source: "bangumi.moe",
      access: size > 2 * 1024 ** 3 && acc.action === "allow" ? { action: "vip", reason: "超过 2GB 的资源仅限大会员" } : acc,
    }
  })
  const extra: Torrent[] = Array.from({ length: 60 }, (_, i) => {
    const t = real[i % real.length]
    return { ...t, id: `old${i}${t.id.slice(5)}`, infohash: hexHash(500 + i), published_at: new Date(now - (12 + i * 1.7) * HOUR).toISOString() }
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
  { key: "source.bangumi_moe_proxy", group: "数据源", type: "string", label: "bangumi.moe 代理", description: "例如 socks5://127.0.0.1:1080", value: "" },
  { key: "source.bgm_token", group: "数据源", type: "secret", label: "bgm.tv Access Token", value: "", is_set: false },
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
    url: `${location.origin}/register?invite=${code}`,
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
