// 字幕轨道的语言识别与默认选择。
//
// 字幕组的 MKV 语言代码常常不准（简繁都标 chi，甚至标 und / jpn），所以同时看轨道名：
// 名字里能认出语言时以名字为准（可能是多语，如「简日双语」），否则用 LanguageIETF / Language 代码。

/** 语言键：简体 / 繁体 / 中文（未知简繁）/ 其他语言的 ISO 639-1 主标签 */
export type LangKey = "zh-Hans" | "zh-Hant" | "zh" | string

export interface TrackLangInput {
  language: string
  languageIETF: string
  name: string
}

// 字幕组常把语言代号连写：JPSC / JPTC / JPCHS / SCJP / CHS_JP …
const JP = String.raw`(?:jp|jpn)`
const SC = String.raw`(?:sc|chs|gb)`
const TC = String.raw`(?:tc|cht|big5)`

const NAME_RULES: [LangKey, RegExp][] = [
  ["zh-Hans", new RegExp(String.raw`(?<![a-z])${JP}[-_&+ ]?${SC}(?![a-z])|(?<![a-z])${SC}[-_&+ ]?${JP}(?![a-z])`, "i")],
  ["zh-Hant", new RegExp(String.raw`(?<![a-z])${JP}[-_&+ ]?${TC}(?![a-z])|(?<![a-z])${TC}[-_&+ ]?${JP}(?![a-z])`, "i")],
  ["ja", new RegExp(String.raw`(?<![a-z])${JP}[-_&+ ]?(?:${SC}|${TC})(?![a-z])|(?<![a-z])(?:${SC}|${TC})[-_&+ ]?${JP}(?![a-z])`, "i")],
  // 简体：简 / 簡体 / 简中 / CHS / SC / GB / zh-CN / Hans / Simplified
  ["zh-Hans", /简|簡体|簡中|chs|(?<![a-z])sc(?![a-z])|(?<![a-z])gb(?:k|2312)?(?![a-z])|zh[-_ ]?(?:cn|sg|hans)|hans(?![a-z])|simplified/i],
  // 繁体：繁 / 正體 / CHT / TC / BIG5 / zh-TW / zh-HK / Hant / Traditional
  ["zh-Hant", /繁|正體|cht|(?<![a-z])tc(?![a-z])|big5|zh[-_ ]?(?:tw|hk|mo|hant)|hant(?![a-z])|traditional/i],
  ["ja", /日本語|日语|日語|日文|(?<![a-z])jpn?(?![a-z])|japanese|(?<=[简繁中])日|日(?=[简繁中英])/i],
  ["en", /english|英文|英语|英語|(?<![a-z])eng?(?![a-z])|(?<=[简繁中日])英|英(?=[简繁中日])/i],
  ["ko", /한국어|korean|韩|韓|(?<![a-z])kor?(?![a-z])/i],
]

const CODE_MAP: Record<string, LangKey> = {
  chi: "zh",
  zho: "zh",
  zh: "zh",
  chs: "zh-Hans",
  cht: "zh-Hant",
  jpn: "ja",
  ja: "ja",
  eng: "en",
  en: "en",
  kor: "ko",
  ko: "ko",
  fre: "fr",
  fra: "fr",
  ger: "de",
  deu: "de",
  spa: "es",
  rus: "ru",
  por: "pt",
  ita: "it",
  vie: "vi",
  tha: "th",
  ind: "id",
  ara: "ar",
}

/** BCP-47 / ISO 639-2 代码 → 语言键（und / 空 → null） */
export function codeLang(code: string): LangKey | null {
  const c = code.trim().toLowerCase().replace("_", "-")
  if (!c || c === "und" || c === "mul" || c === "zxx") return null
  const [primary, ...rest] = c.split("-")
  const sub = rest.join("-")
  if (primary === "zh" || primary === "chi" || primary === "zho" || primary === "cmn" || primary === "yue") {
    if (/hans|cn|sg|my/.test(sub)) return "zh-Hans"
    if (/hant|tw|hk|mo/.test(sub)) return "zh-Hant"
    return primary === "yue" ? "zh-Hant" : "zh"
  }
  return CODE_MAP[primary] ?? (primary.length === 2 ? primary : null)
}

/** 轨道的语言（可能多个，按名字中出现的为准）；认不出时为空 */
export function trackLangs(t: TrackLangInput): LangKey[] {
  const fromName = [...new Set(NAME_RULES.filter(([, re]) => re.test(t.name)).map(([k]) => k))]
  if (fromName.length) return fromName
  const ietf = codeLang(t.languageIETF)
  if (ietf) return [ietf]
  const l = codeLang(t.language)
  return l ? [l] : []
}

/** 浏览器语言（navigator.languages 的一项）→ 语言键 */
export function browserLang(tag: string): LangKey | null {
  return codeLang(tag)
}

/**
 * 按浏览器语言选默认轨道：依次看每个浏览器语言，取语言匹配的轨道
 * （同一语言里：单语优先于多语、名字/代码明确简繁的优先于只知道是中文的、MKV 默认标记优先）。
 * 浏览器的简体 / 繁体只匹配对应字幕或未知简繁的中文字幕。都不匹配返回 -1（不显示字幕）。
 */
export function pickDefault(tracks: TrackLangInput[], languages: readonly string[]): number {
  const langs = tracks.map(trackLangs)
  for (const tag of languages) {
    const want = browserLang(tag)
    if (!want) continue
    let best = -1
    let bestScore = -1
    tracks.forEach((t, i) => {
      const ls = langs[i]
      let score = -1
      if (ls.includes(want)) score = 4
      else if (want.startsWith("zh") && ls.includes("zh")) score = 2 // 中文、简繁未知
      else if (want === "zh" && ls.some((l) => l.startsWith("zh-"))) score = 3 // 浏览器只说 zh
      if (score < 0) return
      if (ls.length === 1) score += 0.5 // 单语优先
      if ((t as { isDefault?: boolean }).isDefault) score += 0.1
      if (score > bestScore) {
        bestScore = score
        best = i
      }
    })
    if (best >= 0) return best
  }
  return -1
}

const LABELS: Record<string, string> = { "zh-Hans": "简体", "zh-Hant": "繁体", zh: "中文", ja: "日文", en: "English", ko: "韩文" }

/** 选择列表里显示的名字：轨道名，没有时用语言 */
export function trackLabel(t: TrackLangInput, index: number): string {
  if (t.name.trim()) return t.name.trim()
  const ls = trackLangs(t)
  return ls.length ? ls.map((l) => LABELS[l] ?? l).join(" / ") : `字幕 ${index + 1}`
}
