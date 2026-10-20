// 种子里的外挂字幕文件（.ass .ssa .srt .vtt）：哪些属于正在播放的视频、语言、编码、转换成完整的 ASS。
//
// 归属规则（externalSubsFor）：
//  - 种子里只有一个视频文件：所有字幕文件都属于它；
//  - 否则按文件名：字幕的文件名（去掉扩展名）= 视频文件名（去掉扩展名），或者是「视频名.后缀」
//    （.sc / .chs / .简体 / .ja / .chs&jpn / .default …，最多 3 段），或者「视频名_后缀 / 视频名 后缀」且后缀能认出语言；
//    字幕在视频所在文件夹（含子文件夹，如 Subs/、[SC]/）或视频文件夹的兄弟文件夹里（Video/ 与 Subs/ 并列）。
//    其他集的字幕（文件名不同）一律不要。
import { codeLang, trackLangs, type LangKey } from "./lang.ts"
import { srtHeader, srtToAss } from "./source.ts"

export const SUB_EXT = /\.(ass|ssa|srt|vtt)$/i
const VIDEO_EXT = /\.(mkv|mk3d|mp4|m4v|mov|webm|avi|ts|m2ts|flv|wmv|rmvb|ogv)$/i
const FONT_EXT = /\.(ttf|otf|ttc)$/i

export interface TorrentFileLike {
  /** 种子内路径（含种子名文件夹），用 / 分隔 */
  path: string
  length: number
}

export interface ExternalSubInfo {
  fileIndex: number
  path: string
  format: "ass" | "ssa" | "srt" | "vtt"
  /** 用于识别语言与会话偏好的名字：文件名去掉视频名后的后缀（.sc → "sc"），没有时为文件名 */
  name: string
  langs: LangKey[]
}

const norm = (s: string) => s.normalize("NFC").toLowerCase()
const base = (p: string) => p.slice(p.lastIndexOf("/") + 1)
const dir = (p: string) => (p.includes("/") ? p.slice(0, p.lastIndexOf("/")) : "")
const stem = (name: string) => name.replace(/\.[^.]+$/, "")

/** 识别语言的已知代码（单独作为后缀出现时） */
const KNOWN_CODES = new Set(["zh", "zh-Hans", "zh-Hant", "ja", "en", "ko", "fr", "de", "es", "ru", "pt", "it", "vi", "th", "id", "ar"])

/** 名字（后缀 + 文件夹名）里的语言：先按字幕组写法（lang.ts NAME_RULES），再按单独的语言代码段 */
export function langsFromHint(hint: string): LangKey[] {
  const byName = trackLangs({ name: hint, language: "", languageIETF: "" })
  if (byName.length) return byName
  const out: LangKey[] = []
  for (const tok of hint.split(/[.\s_[\]()&+,]+/)) {
    const l = tok && codeLang(tok)
    if (l && KNOWN_CODES.has(l) && !out.includes(l)) out.push(l)
  }
  return out
}

/** 视频文件名后面的部分是否像语言 / 版本后缀（不是另一集） */
function suffixOf(subStem: string, videoStem: string): string | null {
  const s = norm(subStem)
  const v = norm(videoStem)
  if (s === v) return ""
  if (!s.startsWith(v)) return null
  const rest = subStem.slice(videoStem.length)
  if (rest.startsWith(".")) {
    const segs = rest.slice(1).split(".")
    if (segs.length > 3 || segs.some((x) => !x || x.length > 32)) return null
    return segs.join(".")
  }
  // 视频名_sc / 视频名 [简体]：只有能认出语言时才算（否则「Show - 1」会匹配「Show - 10」）
  if (/^[_ -]/.test(rest)) {
    const r = rest.replace(/^[_ -]+/, "")
    if (r.length <= 32 && langsFromHint(r).length) return r
  }
  return null
}

/** 字幕所在文件夹是否属于该视频：视频文件夹（含子文件夹）或视频文件夹的兄弟文件夹 */
function nearby(subDir: string, videoDir: string): boolean {
  const under = (d: string, root: string) => root === "" || d === root || d.startsWith(root + "/")
  if (under(subDir, videoDir)) return true
  const parent = dir(videoDir)
  return videoDir !== "" && parent !== "" && under(subDir, parent)
}

/** 属于第 videoIndex 个文件的外挂字幕 */
export function externalSubsFor(files: TorrentFileLike[], videoIndex: number): ExternalSubInfo[] {
  const video = files[videoIndex]
  if (!video || !VIDEO_EXT.test(video.path)) return []
  const single = files.filter((f) => VIDEO_EXT.test(f.path)).length === 1
  const vDir = dir(video.path)
  const vStem = stem(base(video.path))
  const out: ExternalSubInfo[] = []
  files.forEach((f, i) => {
    const m = SUB_EXT.exec(f.path)
    if (!m || !f.length) return
    const name = base(f.path)
    const sDir = dir(f.path)
    let suffix: string | null
    if (single) suffix = suffixOf(stem(name), vStem) ?? stem(name)
    else {
      if (!nearby(sDir, vDir)) return
      suffix = suffixOf(stem(name), vStem)
      if (suffix === null) return
    }
    // 语言线索：后缀 + 字幕所在的、视频路径里没有的文件夹名（[SC]/、简体/ …）
    const extraDirs = sDir
      .split("/")
      .filter((d, k) => d && vDir.split("/")[k] !== d)
      .join(" ")
    const hint = [suffix, extraDirs].filter(Boolean).join(" ")
    out.push({ fileIndex: i, path: f.path, format: m[1].toLowerCase() as ExternalSubInfo["format"], name: suffix || stem(name), langs: langsFromHint(hint) })
  })
  return out
}

/** 种子里的字体文件（Fonts/ 字体/ 文件夹里的 ttf/otf/ttc）：字幕组附带的，ASS 需要 */
export function fontFiles(files: TorrentFileLike[]): number[] {
  const out: number[] = []
  files.forEach((f, i) => {
    if (FONT_EXT.test(f.path) && /(^|\/)(fonts?|字体|字體)(\/|$)/i.test(dir(f.path))) out.push(i)
  })
  return out
}

const LABELS: Record<string, string> = { "zh-Hans": "简体", "zh-Hant": "繁体", zh: "中文", ja: "日文", en: "English", ko: "韩文" }

/** 菜单里的名字：认出的语言 +「外挂」；认不出时用文件名。重名时附上后缀 */
export function externalLabels(subs: ExternalSubInfo[]): string[] {
  const raw = subs.map((s) => (s.langs.length ? `${s.langs.map((l) => LABELS[l] ?? l).join(" / ")}（外挂）` : base(s.path)))
  return raw.map((l, i) => (raw.indexOf(l) !== raw.lastIndexOf(l) && subs[i].langs.length ? `${l} ${subs[i].name}` : l))
}

// ---------------------------------------------------------------- 编码

// 常用字：简体 / 繁体各一组（用来区分 GB18030 与 Big5 解码的结果）
const COMMON_HANS = "的是了我不在人们有这个上来到时大地为子中你说生国年着就那和要她出也得里后自以会家可下而过天去能对小多然于心学么之都好看起发当没成只如事把还用第样道想作种开美总从无情己面最女但现前些所同日手又行意动方期它头经长儿回位分爱老因很给名法间知世什两次使身者被高已亲其进此话常与活正感"
const COMMON_HANT = "的是了我不在人們有這個上來到時大地為子中你說生國年著就那和要她出也得裡後自以會家可下而過天去能對小多然於心學麼之都好看起發當沒成只如事把還用第樣道想作種開美總從無情己面最女但現前些所同日手又行意動方期它頭經長兒回位分愛老因很給名法間知世什兩次使身者被高已親其進此話常與活正感"
const COMMON = new Set([...COMMON_HANS, ...COMMON_HANT])

function commonScore(s: string) {
  let n = 0
  for (const c of s) if (COMMON.has(c)) n++
  let bad = 0
  for (const c of s) if (c === "\uFFFD") bad++
  return n - 4 * bad
}

/** 字幕文件的文本：BOM（UTF-8 / UTF-16）→ 严格 UTF-8 → GB18030 与 Big5 中常用字多的那个 */
export function decodeSubtitle(b: Uint8Array): string {
  if (b[0] === 0xef && b[1] === 0xbb && b[2] === 0xbf) return new TextDecoder("utf-8").decode(b.subarray(3))
  if (b[0] === 0xff && b[1] === 0xfe) return new TextDecoder("utf-16le").decode(b.subarray(2))
  if (b[0] === 0xfe && b[1] === 0xff) return new TextDecoder("utf-16be").decode(b.subarray(2))
  try {
    return new TextDecoder("utf-8", { fatal: true }).decode(b)
  } catch {
    /* 旧式编码 */
  }
  const gb = new TextDecoder("gb18030").decode(b)
  const big5 = new TextDecoder("big5").decode(b)
  return commonScore(big5) > commonScore(gb) ? big5 : gb
}

// ---------------------------------------------------------------- 转换

const assTime = (ms: number) => {
  const cs = Math.max(0, Math.round(ms / 10))
  const h = Math.floor(cs / 360000)
  const m = Math.floor((cs % 360000) / 6000)
  const s = Math.floor((cs % 6000) / 100)
  return `${h}:${String(m).padStart(2, "0")}:${String(s).padStart(2, "0")}.${String(cs % 100).padStart(2, "0")}`
}

const TIME = String.raw`(?:(\d+):)?(\d{1,2}):(\d{1,2})[,.](\d{1,3})`
const CUE_LINE = new RegExp(String.raw`^\s*${TIME}\s*-->\s*${TIME}`)

const toMs = (h: string | undefined, m: string, s: string, f: string) => ((Number(h ?? 0) * 60 + Number(m)) * 60 + Number(s)) * 1000 + Number(f.padEnd(3, "0"))

/** SRT / WebVTT → 完整的 ASS 文件（与内封 SRT 轨道同一套样式与标签转换） */
export function timedTextToAss(text: string): string {
  const lines = text.replace(/\r\n?/g, "\n").split("\n")
  const events: string[] = []
  for (let i = 0; i < lines.length; i++) {
    const m = CUE_LINE.exec(lines[i])
    if (!m) continue
    const start = toMs(m[1], m[2], m[3], m[4])
    const end = toMs(m[5], m[6], m[7], m[8])
    const body: string[] = []
    for (i++; i < lines.length && lines[i].trim() !== ""; i++) body.push(lines[i])
    i--
    // WebVTT 的 <v 名字> <c.class> <00:00:01.000> 等标签：去掉
    const t = body.join("\n").replace(/<\/?(?:v|c|lang|ruby|rt)(?:[.\s][^>]*)?>|<\d[\d:.]*>/g, "")
    const chunk = srtToAss(t, events.length + 1) // ReadOrder,Layer,Style,Name,MarginL,MarginR,MarginV,Effect,Text
    const [, layer, style, name, ml, mr, mv, effect] = chunk.split(",", 8)
    const textPart = chunk.split(",").slice(8).join(",")
    events.push(`Dialogue: ${layer},${assTime(start)},${assTime(end)},${style},${name},${ml},${mr},${mv},${effect},${textPart}`)
  }
  return srtHeader() + events.join("\n") + "\n"
}

/** 外挂字幕文件 → JASSUB 可直接加载的 ASS 文本 */
export function externalToAss(format: ExternalSubInfo["format"], bytes: Uint8Array): string {
  const text = decodeSubtitle(bytes)
  return format === "ass" || format === "ssa" ? text.replace(/^\uFEFF/, "") : timedTextToAss(text)
}
