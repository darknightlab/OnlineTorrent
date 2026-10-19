// node --test src/lib/rank.scenarios.test.ts（npm test）
// 推荐规则默认值的场景测试：每个场景 2–5 个候选、一个观看者（游客 = 无做种 / WebSeed 权限，会员 = 两者都有），
// expect = 人工判断应自动播放的资源，alt = 也可以接受的。依据见 docs/research/rank-rules.md。
import { test } from "node:test"
import assert from "node:assert/strict"
import { DEFAULT_RANK_RULES, describeScore, rankFiles, type RankInput, type Viewer } from "./rank.ts"
import type { AccessAction } from "./types.ts"

const NOW = Date.parse("2026-10-20T12:00:00Z")

export const VIEWERS: Record<"guest" | "member", Viewer> = {
  guest: { canSeed: false, canWebseed: false },
  member: { canSeed: true, canWebseed: true },
}

export interface Cand {
  id: string
  /** 种子标题（文件名由标题推出） */
  title: string
  mb: number
  bt?: number | null
  wt?: number | null
  server?: boolean
  /** 发布于几小时前 */
  age: number
  /** false = 从未统计（swarm 为 null，除非 server） */
  known?: boolean
  access?: AccessAction
  /** 文件路径（默认 标题 + .mkv） */
  path?: string
}

export interface Scenario {
  name: string
  viewer: keyof typeof VIEWERS
  cands: Cand[]
  expect: string
  alt?: string[]
  why: string
}

export function toInput(c: Cand): RankInput & { id: string } {
  const known = c.known !== false
  return {
    id: c.id,
    torrent_title: c.title,
    path: c.path ?? `${c.title}.mkv`,
    team: c.title.match(/^\[([^\]]+)\]/)?.[1] ?? "",
    size_bytes: Math.round(c.mb * 1024 ** 2),
    published_at: new Date(NOW - c.age * 3600_000).toISOString(),
    access: { action: c.access ?? "allow", reason: "" },
    swarm:
      known || c.server
        ? {
            bt_seeders: known ? (c.bt ?? 0) : null,
            bt_leechers: 0,
            wt_seeders: known ? (c.wt ?? 0) : null,
            wt_leechers: 0,
            server_complete: !!c.server,
            checked_at: known ? new Date(NOW - 600_000).toISOString() : null,
          }
        : null,
  }
}

const S = "Kusuriya no Hitorigoto"

export const SCENARIOS: Scenario[] = [
  {
    name: "01 服务器已缓存的生肉 vs 新发布的简繁内封（会员）",
    viewer: "member",
    cands: [
      { id: "raw", title: `[Ohys-Raws] ${S} - 05 (AT-X 1280x720 x264 AAC)`, mb: 400, bt: 30, wt: 1, server: true, age: 30 },
      { id: "sub", title: `[喵萌奶茶屋&LoliHouse] ${S} - 05 [WebRip 1080p HEVC-10bit AAC][简繁日内封字幕]`, mb: 320, bt: 80, wt: 0, age: 3 },
    ],
    expect: "sub",
    why: "会员可以让服务器拉 BT，字幕优先；服务器缓存不应锁死推荐",
  },
  {
    name: "02 同上（游客）：字幕版没有 WebRTC 节点",
    viewer: "guest",
    cands: [
      { id: "raw", title: `[Ohys-Raws] ${S} - 05 (AT-X 1280x720 x264 AAC)`, mb: 400, bt: 30, wt: 1, server: true, age: 30 },
      { id: "sub", title: `[喵萌奶茶屋&LoliHouse] ${S} - 05 [WebRip 1080p HEVC-10bit AAC][简繁日内封字幕]`, mb: 320, bt: 80, wt: 0, age: 3 },
    ],
    expect: "raw",
    why: "游客只能从 WebRTC 节点 / 服务器拿数据，字幕版现在放不了",
  },
  {
    name: "03 同上（游客）：字幕版已有 WebRTC 节点",
    viewer: "guest",
    cands: [
      { id: "raw", title: `[Ohys-Raws] ${S} - 05 (AT-X 1280x720 x264 AAC)`, mb: 400, bt: 30, wt: 1, server: true, age: 30 },
      { id: "sub", title: `[喵萌奶茶屋&LoliHouse] ${S} - 05 [WebRip 1080p HEVC-10bit AAC][简繁日内封字幕]`, mb: 320, bt: 80, wt: 2, age: 3 },
    ],
    expect: "sub",
    why: "两者都能播，字幕优先",
  },
  {
    name: "04 服务器已缓存的简体 AVC vs 新出的简体 HEVC 小文件（会员）",
    viewer: "member",
    cands: [
      { id: "old", title: `[桜都字幕组] ${S} [05][1080p][简体内嵌]`, mb: 700, bt: 5, wt: 1, server: true, age: 40, path: `[桜都字幕组] ${S} [05][1080p][简体内嵌].mp4` },
      { id: "new", title: `[LoliHouse] ${S} - 05 [WebRip 1080p HEVC-10bit AAC][简体内封]`, mb: 300, bt: 25, wt: 0, age: 4 },
    ],
    expect: "new",
    alt: ["old"],
    why: "同为简体 1080p，新版本更小、BT 做种多；服务器缓存只是小加分",
  },
  {
    name: "05 同上（游客）",
    viewer: "guest",
    cands: [
      { id: "old", title: `[桜都字幕组] ${S} [05][1080p][简体内嵌]`, mb: 700, bt: 5, wt: 1, server: true, age: 40 },
      { id: "new", title: `[LoliHouse] ${S} - 05 [WebRip 1080p HEVC-10bit AAC][简体内封]`, mb: 300, bt: 25, wt: 0, age: 4 },
    ],
    expect: "old",
    why: "新版本对游客无可用来源",
  },
  {
    name: "06 繁体 ANi vs 简体（游客，都有 WebRTC 节点）",
    viewer: "guest",
    cands: [
      { id: "cht", title: `[ANi] 藥師少女的獨語 - 05 [1080P][Baha][WEB-DL][AAC AVC][CHT]`, mb: 600, bt: 40, wt: 1, age: 6, path: "[ANi] 藥師少女的獨語 - 05 [1080P][Baha][WEB-DL][AAC AVC][CHT].mp4" },
      { id: "chs", title: `[桜都字幕组] ${S} [05][1080p][简体内嵌]`, mb: 700, bt: 20, wt: 1, age: 10 },
    ],
    expect: "chs",
    why: "简体优先于繁体",
  },
  {
    name: "07 繁体 1080p vs 简体 720p（会员）",
    viewer: "member",
    cands: [
      { id: "cht1080", title: `[ANi] 藥師少女的獨語 - 05 [1080P][Baha][WEB-DL][AAC AVC][CHT]`, mb: 600, bt: 40, wt: 1, age: 6 },
      { id: "chs720", title: `[某字幕组] ${S} [05][720p][简体]`, mb: 280, bt: 10, wt: 1, age: 12 },
    ],
    expect: "cht1080",
    alt: ["chs720"],
    why: "（第三版改）繁体只比简体少 5 分，1080p 比 720p 多 10 分；简体 720p 也可接受",
  },
  {
    name: "08 生肉 1080p vs 简体 720p",
    viewer: "member",
    cands: [
      { id: "raw1080", title: `[Ohys-Raws] ${S} - 05 (TBS 1920x1080 x264 AAC)`, mb: 900, bt: 60, wt: 1, server: true, age: 20 },
      { id: "chs720", title: `[某字幕组] ${S} [05][720p][简体]`, mb: 280, bt: 10, wt: 1, age: 12 },
    ],
    expect: "chs720",
    why: "中文站：有中文字幕比清晰度重要",
  },
  {
    name: "09 英文字幕（SubsPlease，大量 BT）vs 繁体（游客）",
    viewer: "guest",
    cands: [
      { id: "eng", title: `[SubsPlease] ${S} - 05 (1080p) [A1B2C3D4]`, mb: 1400, bt: 500, wt: 1, age: 8 },
      { id: "cht", title: `[ANi] 藥師少女的獨語 - 05 [1080P][Baha][WEB-DL][AAC AVC][CHT]`, mb: 600, bt: 50, wt: 1, age: 6 },
    ],
    expect: "cht",
    why: "英文字幕对中文用户等同生肉",
  },
  {
    name: "10 同上（会员）",
    viewer: "member",
    cands: [
      { id: "eng", title: `[SubsPlease] ${S} - 05 (1080p) [A1B2C3D4]`, mb: 1400, bt: 500, wt: 3, server: true, age: 8 },
      { id: "cht", title: `[ANi] 藥師少女的獨語 - 05 [1080P][Baha][WEB-DL][AAC AVC][CHT]`, mb: 600, bt: 50, wt: 0, age: 6 },
    ],
    expect: "cht",
    why: "字幕优先；英文版做种再多也只是可用性余量",
  },
  {
    name: "11 很小的 480p vs 1080p（同为简体）",
    viewer: "member",
    cands: [
      { id: "480", title: `[某字幕组] ${S} [05][480P][简体]`, mb: 120, bt: 5, wt: 1, age: 12, path: `[某字幕组] ${S} [05][480P][简体].mp4` },
      { id: "1080", title: `[桜都字幕组] ${S} [05][1080p][简体内嵌]`, mb: 650, bt: 5, wt: 1, age: 12 },
    ],
    expect: "1080",
    why: "小文件加分不能盖过清晰度",
  },
  {
    name: "12 4K 大文件（超过自动选种上限）vs 1080p HEVC",
    viewer: "member",
    cands: [
      { id: "4k", title: `[Nekomoe kissaten&VCB-Studio] ${S} [05][Ma10p_2160p][x265_flac][CHS]`, mb: 3900, bt: 60, wt: 0, age: 200 },
      { id: "1080", title: `[LoliHouse] ${S} - 05 [WebRip 1080p HEVC-10bit AAC][简繁内封字幕]`, mb: 450, bt: 20, wt: 1, age: 150 },
    ],
    expect: "1080",
    why: "4K 单集近 4 GB，浏览器 P2P 播放代价太大",
  },
  {
    name: "13 4K HEVC 1.6 GB vs 1080p AVC 800 MB（同为简体）",
    viewer: "member",
    cands: [
      { id: "4k", title: `[某字幕组] ${S} [05][2160p][HEVC][简体]`, mb: 1600, bt: 20, wt: 0, age: 30 },
      { id: "1080", title: `[桜都字幕组] ${S} [05][1080p][简体内嵌]`, mb: 800, bt: 20, wt: 0, age: 30 },
    ],
    expect: "1080",
    alt: ["4k"],
    why: "1080p 已足够，文件小一半",
  },
  {
    name: "14 同组 HEVC vs AVC",
    viewer: "guest",
    cands: [
      { id: "avc", title: `[LoliHouse] ${S} - 05 [WebRip 1080p AVC AAC][简繁内封字幕]`, mb: 750, bt: 30, wt: 1, age: 5 },
      { id: "hevc", title: `[LoliHouse] ${S} - 05 [WebRip 1080p HEVC-10bit AAC][简繁内封字幕]`, mb: 350, bt: 30, wt: 1, age: 5 },
    ],
    expect: "hevc",
    why: "同组同字幕，HEVC 小一半",
  },
  {
    name: "15 会员：简体但完全无人做种 vs 繁体 720p 可用",
    viewer: "member",
    cands: [
      { id: "dead", title: `[LoliHouse] ${S} - 05 [WebRip 1080p HEVC-10bit AAC][简体内封]`, mb: 300, bt: 0, wt: 0, age: 400 },
      { id: "cht720", title: `[Lilith-Raws] ${S} - 05 [Baha][WEB-DL][720p][AVC AAC][CHT][MP4]`, mb: 350, bt: 3, wt: 0, age: 400 },
    ],
    expect: "cht720",
    why: "放不了的资源必须排在后面",
  },
  {
    name: "16 游客：全部都没有 WebRTC 节点",
    viewer: "guest",
    cands: [
      { id: "chs", title: `[LoliHouse] ${S} - 05 [WebRip 1080p HEVC-10bit AAC][简体内封]`, mb: 300, bt: 80, wt: 0, age: 30 },
      { id: "cht", title: `[ANi] 藥師少女的獨語 - 05 [1080P][Baha][WEB-DL][AAC AVC][CHT]`, mb: 600, bt: 200, wt: 0, age: 30 },
      { id: "raw", title: `[Ohys-Raws] ${S} - 05 (AT-X 1280x720 x264 AAC)`, mb: 400, bt: 30, wt: 0, age: 30 },
    ],
    expect: "chs",
    why: "都放不了时仍按内容排序（打开后可能有节点上线）",
  },
  {
    name: "17 游客：简体只有 BT 做种 vs 生肉有 WebRTC 节点",
    viewer: "guest",
    cands: [
      { id: "chs", title: `[桜都字幕组] ${S} [05][1080p][简体内嵌]`, mb: 700, bt: 200, wt: 0, age: 8 },
      { id: "raw", title: `[Ohys-Raws] ${S} - 05 (TBS 1920x1080 x264 AAC)`, mb: 900, bt: 10, wt: 1, age: 20 },
    ],
    expect: "raw",
    why: "游客用不了 BT，能播放优先",
  },
  {
    name: "18 合集（整季 BD）vs 每周单集（会员）",
    viewer: "member",
    cands: [
      { id: "batch", title: `[喵萌奶茶屋&VCB-Studio] ${S} [01-12][Ma10p_1080p][x265_flac][简繁内封]`, mb: 1100, bt: 120, wt: 0, age: 2000, path: `[喵萌奶茶屋&VCB-Studio] ${S} [01-12][Ma10p_1080p][x265_flac][简繁内封]/${S} [05][Ma10p_1080p][x265_flac].mkv` },
      { id: "weekly", title: `[桜都字幕组] ${S} [05][1080p][简体内嵌]`, mb: 600, bt: 15, wt: 2, server: true, age: 2100 },
    ],
    expect: "weekly",
    alt: ["batch"],
    why: "都是简体 1080p；单集更小且已缓存，BD 合集画质好但大，两者皆可",
  },
  {
    name: "19 合集 vs 每周单集（游客）",
    viewer: "guest",
    cands: [
      { id: "batch", title: `[喵萌奶茶屋&VCB-Studio] ${S} [01-12][Ma10p_1080p][x265_flac][简繁内封]`, mb: 1100, bt: 120, wt: 0, age: 2000 },
      { id: "weekly", title: `[桜都字幕组] ${S} [05][1080p][简体内嵌]`, mb: 600, bt: 15, wt: 2, server: true, age: 2100 },
    ],
    expect: "weekly",
    why: "合集对游客无可用来源",
  },
  {
    name: "20 完全相同的两个资源：较新的在前",
    viewer: "guest",
    cands: [
      { id: "older", title: `[某字幕组] ${S} [05][1080p][简体]`, mb: 500, bt: 10, wt: 1, age: 26 },
      { id: "newer", title: `[某字幕组] ${S} [05][1080p][简体]`, mb: 500, bt: 10, wt: 1, age: 2 },
    ],
    expect: "newer",
    why: "同分按发布时间（修正版通常更新）",
  },
  {
    name: "21 刚发布、尚未统计的简体 vs 已缓存的繁体（游客）",
    viewer: "guest",
    cands: [
      { id: "new", title: `[LoliHouse] ${S} - 05 [WebRip 1080p HEVC-10bit AAC][简体内封]`, mb: 300, age: 0.2, known: false },
      { id: "cht", title: `[ANi] 藥師少女的獨語 - 05 [1080P][Baha][WEB-DL][AAC AVC][CHT]`, mb: 600, bt: 50, wt: 2, server: true, age: 6 },
    ],
    expect: "new",
    alt: ["cht"],
    why: "未知不扣分；字幕更好（统计通常 1.5 秒内就会有）",
  },
  {
    name: "22 刚发布、尚未统计 vs 已知可用的同字幕（游客）",
    viewer: "guest",
    cands: [
      { id: "new", title: `[LoliHouse] ${S} - 05 [WebRip 1080p HEVC-10bit AAC][简体内封]`, mb: 300, age: 0.2, known: false },
      { id: "old", title: `[桜都字幕组] ${S} [05][1080p][简体内嵌]`, mb: 700, bt: 20, wt: 2, server: true, age: 30 },
    ],
    expect: "new",
    alt: ["old"],
    why: "（第三版改）新版本小 400 MB（约 +8），未知不扣分；已知可播的旧版也可接受",
  },

  {
    name: "24 游客：已缓存的繁体 vs 有 WebRTC 节点的简体",
    viewer: "guest",
    cands: [
      { id: "cht", title: `[ANi] 藥師少女的獨語 - 05 [1080P][Baha][WEB-DL][AAC AVC][CHT]`, mb: 600, bt: 50, wt: 3, server: true, age: 10 },
      { id: "chs", title: `[桜都字幕组] ${S} [05][1080p][简体内嵌]`, mb: 700, bt: 8, wt: 1, age: 8 },
    ],
    expect: "cht",
    alt: ["chs"],
    why: "（第三版改）繁体只比简体少 5 分：繁体更小、已缓存、节点更多，可以压过简体；选简体也可接受",
  },
  {
    name: "25 会员：简体只有 1 个 BT 做种 vs 繁体大量做种且已缓存",
    viewer: "member",
    cands: [
      { id: "chs", title: `[某字幕组] ${S} [05][1080p][简体]`, mb: 550, bt: 1, wt: 0, age: 50 },
      { id: "cht", title: `[ANi] 藥師少女的獨語 - 05 [1080P][Baha][WEB-DL][AAC AVC][CHT]`, mb: 600, bt: 100, wt: 3, server: true, age: 50 },
    ],
    expect: "cht",
    alt: ["chs"],
    why: "（第三版改）1 个做种者 vs 100 个且已缓存：可用性差足以压过简/繁差；简体也可接受",
  },
  {
    name: "26 游客：同为简体，WebRTC 节点 5 vs 1",
    viewer: "guest",
    cands: [
      { id: "one", title: `[某字幕组] ${S} [05][1080p][简体]`, mb: 500, bt: 10, wt: 1, age: 4 },
      { id: "five", title: `[另一字幕组] ${S} [05][1080p][简体]`, mb: 500, bt: 10, wt: 5, age: 10 },
    ],
    expect: "five",
    why: "其他相同时可用性余量决定",
  },
  {
    name: "27 会员：AVC 1.4 GB（BT 40）vs HEVC 380 MB（BT 3）",
    viewer: "member",
    cands: [
      { id: "avc", title: `[某字幕组] ${S} [05][WEB-DL][1080p][AVC][简体]`, mb: 1400, bt: 40, wt: 0, age: 20 },
      { id: "hevc", title: `[LoliHouse] ${S} - 05 [WebRip 1080p HEVC-10bit AAC][简体内封]`, mb: 380, bt: 3, wt: 0, age: 20 },
    ],
    expect: "hevc",
    alt: ["avc"],
    why: "小文件对 P2P 与服务器流量都更友好",
  },
  {
    name: "28 简体 2.5 GB（超过自动选种上限）vs 繁体 600 MB",
    viewer: "member",
    cands: [
      { id: "big", title: `[某字幕组] ${S} [05][1080p][简体][BDRip]`, mb: 2560, bt: 30, wt: 0, age: 100 },
      { id: "cht", title: `[ANi] 藥師少女的獨語 - 05 [1080P][Baha][WEB-DL][AAC AVC][CHT]`, mb: 600, bt: 30, wt: 1, age: 100 },
    ],
    expect: "cht",
    alt: ["big"],
    why: "超过站点设定的自动选种大小上限",
  },
  {
    name: "29 同组 720p vs 1080p",
    viewer: "guest",
    cands: [
      { id: "720", title: `[某字幕组] ${S} [05][720p][简体]`, mb: 300, bt: 10, wt: 1, age: 5 },
      { id: "1080", title: `[某字幕组] ${S} [05][1080p][简体]`, mb: 600, bt: 10, wt: 1, age: 5 },
    ],
    expect: "1080",
    why: "清晰度优先于文件大小",
  },
  {
    name: "30 CHS vs CHT（同组）",
    viewer: "guest",
    cands: [
      { id: "cht", title: `[Nekomoe kissaten][${S}][05][1080p][CHT]`, mb: 500, bt: 10, wt: 2, age: 5 },
      { id: "chs", title: `[Nekomoe kissaten][${S}][05][1080p][CHS]`, mb: 500, bt: 10, wt: 1, age: 5 },
    ],
    expect: "chs",
    why: "简体优先，WebRTC 节点差一点不影响",
  },
  {
    name: "31 简日内嵌 AVC vs 简繁内封 HEVC",
    viewer: "member",
    cands: [
      { id: "hardsub", title: `[北宇治字幕组] ${S} [05][WebRip][1080p][AVC_AAC][简日内嵌]`, mb: 550, bt: 20, wt: 1, age: 10 },
      { id: "softsub", title: `[LoliHouse] ${S} - 05 [WebRip 1080p HEVC-10bit AAC][简繁内封字幕]`, mb: 300, bt: 20, wt: 1, age: 10 },
    ],
    expect: "softsub",
    alt: ["hardsub"],
    why: "都是简体，「简繁」不应比「简」多拿分；HEVC 更小",
  },
  {
    name: "32 只有生肉（会员）：已缓存 AVC 1.3 GB vs HEVC 400 MB（BT 50）",
    viewer: "member",
    cands: [
      { id: "avc", title: `[Ohys-Raws] ${S} - 05 (TBS 1920x1080 x264 AAC)`, mb: 1300, bt: 5, wt: 1, server: true, age: 20 },
      { id: "hevc", title: `[NC-Raws] ${S} - 05 (B-Global 1920x1080 HEVC AAC MKV)`, mb: 400, bt: 50, wt: 0, age: 20 },
    ],
    expect: "hevc",
    alt: ["avc"],
    why: "会员可以让服务器拉更小的版本",
  },
  {
    name: "33 只有生肉（游客）",
    viewer: "guest",
    cands: [
      { id: "avc", title: `[Ohys-Raws] ${S} - 05 (TBS 1920x1080 x264 AAC)`, mb: 1300, bt: 5, wt: 1, server: true, age: 20 },
      { id: "hevc", title: `[NC-Raws] ${S} - 05 (B-Global 1920x1080 HEVC AAC MKV)`, mb: 400, bt: 50, wt: 0, age: 20 },
    ],
    expect: "avc",
    why: "HEVC 版本对游客无可用来源",
  },
  {
    name: "34 会员：简体完全无人做种 vs 繁体尚未统计",
    viewer: "member",
    cands: [
      { id: "dead", title: `[某字幕组] ${S} [05][1080p][简体]`, mb: 500, bt: 0, wt: 0, age: 300 },
      { id: "unknown", title: `[ANi] 藥師少女的獨語 - 05 [1080P][Baha][WEB-DL][AAC AVC][CHT]`, mb: 600, age: 300, known: false },
    ],
    expect: "unknown",
    why: "确定放不了的排在未知之后",
  },

  {
    name: "36 游客：只有服务器缓存的生肉 vs 有 WebRTC 节点的简体",
    viewer: "guest",
    cands: [
      { id: "raw", title: `[Ohys-Raws] ${S} - 05 (AT-X 1280x720 x264 AAC)`, mb: 400, bt: 0, wt: 0, server: true, age: 30 },
      { id: "chs", title: `[桜都字幕组] ${S} [05][1080p][简体内嵌]`, mb: 700, bt: 3, wt: 1, age: 8 },
    ],
    expect: "chs",
    why: "都能播，字幕优先",
  },
  {
    name: "37 整季简体合集已缓存 vs 每周繁体（游客）",
    viewer: "guest",
    cands: [
      { id: "batch", title: `[喵萌奶茶屋] ${S} [01-12][1080p][简日双语]`, mb: 1300, bt: 40, wt: 1, server: true, age: 900, path: `[喵萌奶茶屋] ${S} [01-12][1080p][简日双语]/[喵萌奶茶屋] ${S} [05][1080p][简日双语].mp4` },
      { id: "cht", title: `[ANi] 藥師少女的獨語 - 05 [1080P][Baha][WEB-DL][AAC AVC][CHT]`, mb: 600, bt: 30, wt: 2, age: 1000 },
    ],
    expect: "cht",
    alt: ["batch"],
    why: "（第三版改）合集文件 1.3 GB vs 600 MB，大小差足以压过简/繁差；合集也可接受",
  },
  {
    name: "38 刚发布的简体（会员）：BT 3、尚无 WebRTC vs 已缓存的繁体",
    viewer: "member",
    cands: [
      { id: "chs", title: `[桜都字幕组] ${S} [05][1080p][简体内嵌]`, mb: 700, bt: 3, wt: 0, age: 1 },
      { id: "cht", title: `[ANi] 藥師少女的獨語 - 05 [1080P][Baha][WEB-DL][AAC AVC][CHT]`, mb: 600, bt: 60, wt: 3, server: true, age: 7 },
    ],
    expect: "cht",
    alt: ["chs"],
    why: "（第三版改）BT 3 vs 60 且已缓存：差距大时繁体可以压过简体；简体也可接受",
  },
  {
    name: "39 同上（游客）",
    viewer: "guest",
    cands: [
      { id: "chs", title: `[桜都字幕组] ${S} [05][1080p][简体内嵌]`, mb: 700, bt: 3, wt: 0, age: 1 },
      { id: "cht", title: `[ANi] 藥師少女的獨語 - 05 [1080P][Baha][WEB-DL][AAC AVC][CHT]`, mb: 600, bt: 60, wt: 3, server: true, age: 7 },
    ],
    expect: "cht",
    why: "游客只能播有 WebRTC 节点 / 服务器缓存的",
  },
  {
    name: "42 日文字幕 vs 生肉（第四版新增）",
    viewer: "member",
    cands: [
      { id: "raw", title: `[Ohys-Raws] ${S} - 05 (TBS 1920x1080 x264 AAC)`, mb: 900, bt: 60, wt: 1, server: true, age: 20 },
      { id: "jp", title: `[某组] ${S} - 05 [1080p][HEVC][日字]`, mb: 400, bt: 20, wt: 1, age: 10 },
    ],
    expect: "jp",
    why: "有日文字幕也比生肉好（+10）",
  },
  {
    name: "43 4K HEVC 900 MB vs 1080p AVC 600 MB（同为简体，第四版新增）",
    viewer: "member",
    cands: [
      { id: "4k", title: `[某字幕组] ${S} [05][2160p][HEVC][简体]`, mb: 900, bt: 20, wt: 1, age: 10 },
      { id: "1080", title: `[桜都字幕组] ${S} [05][1080p][简体内嵌]`, mb: 600, bt: 20, wt: 1, age: 10 },
    ],
    expect: "4k",
    alt: ["1080"],
    why: "4K 只大 300 MB，画质优势值得；1080p 也可接受",
  },
  {
    name: "44 游客：繁体 20 个 WebRTC 节点 vs 简体 1 个（第四版新增）",
    viewer: "guest",
    cands: [
      { id: "cht", title: `[ANi] 藥師少女的獨語 - 05 [1080P][Baha][WEB-DL][AAC AVC][CHT]`, mb: 600, bt: 300, wt: 20, server: true, age: 30 },
      { id: "chs", title: `[桜都字幕组] ${S} [05][1080p][简体内嵌]`, mb: 600, bt: 5, wt: 1, age: 30 },
    ],
    expect: "cht",
    alt: ["chs"],
    why: "游客的播放速度几乎全靠 WebRTC 节点，20 个 vs 1 个差距大；简体也可接受",
  },
  {
    name: "45 标题含「日常」「日本」不算日文字幕（第四版新增）",
    viewer: "member",
    cands: [
      { id: "nichijou", title: "[Ohys-Raws] Nichijou 日常 日本語 - 05 (TBS 1920x1080 x264 AAC)", mb: 500, bt: 10, wt: 1, age: 10 },
      { id: "jp", title: "[某组] Nichijou - 05 [1080p][日文字幕]", mb: 520, bt: 10, wt: 1, age: 10 },
    ],
    expect: "jp",
    why: "生肉的「日常」「日本」不能拿到日文字幕分",
  },
  {
    name: "40 真实：学生会也有洞！03（会员，生产环境统计）",
    viewer: "member",
    cands: [
      { id: "tv", title: "[TV版&无修版] 学生会也有洞！ - EP03 [简／繁] (1080p H.264 AAC SRTx2) {生徒会にも穴はある！ | 脑洞学生会 | Even the Student Council Has Its Holes!}", path: "[TV版&无修版] 学生会也有洞！ - EP03 [简／繁] (1080p H.264 AAC SRTx2)/【10月】学生会也有洞！ 03【TV放送版】.mp4", mb: 448857591 / 1024 ** 2, bt: 98, wt: 1, server: true, age: 30 },
      { id: "nekomoe", title: "【喵萌奶茶屋】★10月新番★[学生会也有洞 / Seitokai ni mo Ana wa Aru!][03][1080p][简日双语]", path: "[Nekomoe kissaten][Seitokai ni mo Ana wa Aru!][03][1080p][JPSC].mp4", mb: 365901113 / 1024 ** 2, bt: 226, wt: 0, age: 20 },
      { id: "ani", title: "[ANi] Seitokai ni mo Ana wa Aru /  學生會也有洞！ - 03 [1080P][Baha][WEB-DL][AAC AVC][CHT][MP4]", path: "[ANi] 學生會也有洞！ - 03 [1080P][Baha][WEB-DL][AAC AVC][CHT].mp4", mb: 369444782 / 1024 ** 2, bt: 476, wt: 1, server: true, age: 40 },
    ],
    expect: "tv",
    alt: ["nekomoe", "ani"],
    why: "三者接近：简繁版已缓存且有节点；喵萌简体小 80 MB、做种更多；ANi 繁体做种最多，都可接受",
  },
  {
    name: "41 真实：学生会也有洞！03（游客）",
    viewer: "guest",
    cands: [
      { id: "tv", title: "[TV版&无修版] 学生会也有洞！ - EP03 [简／繁] (1080p H.264 AAC SRTx2)", path: "【10月】学生会也有洞！ 03【TV放送版】.mp4", mb: 448857591 / 1024 ** 2, bt: 98, wt: 1, server: true, age: 30 },
      { id: "nekomoe", title: "【喵萌奶茶屋】★10月新番★[学生会也有洞 / Seitokai ni mo Ana wa Aru!][03][1080p][简日双语]", path: "[Nekomoe kissaten][Seitokai ni mo Ana wa Aru!][03][1080p][JPSC].mp4", mb: 365901113 / 1024 ** 2, bt: 226, wt: 0, age: 20 },
      { id: "ani", title: "[ANi] Seitokai ni mo Ana wa Aru /  學生會也有洞！ - 03 [1080P][Baha][WEB-DL][AAC AVC][CHT][MP4]", mb: 369444782 / 1024 ** 2, bt: 476, wt: 1, server: true, age: 40 },
    ],
    expect: "tv",
    alt: ["ani"],
    why: "喵萌版没有 WebRTC 节点，游客放不了；简体优先",
  },
]

/** 跑一个场景：返回排序结果（id、分数、明细） */
export function runScenario(sc: Scenario, rules = DEFAULT_RANK_RULES) {
  const items = sc.cands.map(toInput)
  return rankFiles(items, rules, VIEWERS[sc.viewer]).map((s) => ({ id: (s.file as RankInput & { id: string }).id, score: s.score, why: describeScore(s) }))
}

for (const sc of SCENARIOS) {
  test(sc.name, () => {
    const ranked = runScenario(sc)
    const ok = [sc.expect, ...(sc.alt ?? [])]
    assert.ok(ok.includes(ranked[0].id), `${sc.name}: picked ${ranked[0].id}, expected ${ok.join(" / ")}\n${ranked.map((r) => `  ${r.id}: ${r.why}`).join("\n")}`)
    // 默认规则应选中首选（alt 只是可接受，不应被默认规则选中——见 docs/research/rank-rules.md 的「刻意例外」）
    assert.equal(ranked[0].id, sc.expect, `${sc.name}: picked acceptable alternative ${ranked[0].id}\n${ranked.map((r) => `  ${r.id}: ${r.why}`).join("\n")}`)
  })
}
