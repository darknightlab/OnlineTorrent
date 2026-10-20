import { DEFAULT_RANK_RULES } from "./rank"

/** 字幕语言：与推荐规则「字幕」组同一套匹配（简繁日可同时出现） */
const LANG_SHORT: Record<string, string> = { 简体中文: "简体", 繁体中文: "繁体", 日文: "日文" }
const LANGS = DEFAULT_RANK_RULES.keywords
  .filter((k) => k.group === "字幕")
  .map((k) => ({ tag: LANG_SHORT[k.label] ?? k.label, re: new RegExp(k.pattern, "i") }))

/**
 * 从文件名 / 种子标题粗略解析 分辨率 · 字幕语言 · 来源 · 编码 · 容器（单集页资源列表与种子列表共用）。
 * path：文件路径（容器取扩展名）；没有时从文本里找 .mkv / [MKV] 等。
 */
export function releaseTags(text: string, path?: string) {
  const tags: string[] = []
  const res = text.match(/\b(2160|1080|720|480)[pP]\b/)?.[1] ?? text.match(/\d{3,4}\s*[x×]\s*(2160|1080|720|480)\b/)?.[1] ?? (/\b4K\b/i.test(text) ? "2160" : undefined)
  if (res) tags.push(res === "2160" ? "4K" : `${res}P`)
  for (const l of LANGS) if (l.re.test(text)) tags.push(l.tag)
  const src = text.match(/\b(Baha|B-Global|CR|ABEMA|Netflix|AMZN|Bilibili|BDRip|BD|WEB-?DL|WebRip)\b/i)?.[1]
  if (src) tags.push(src)
  if (/\b(HEVC|[xh]\.?265)\b/i.test(text)) tags.push("HEVC")
  else if (/\b(AVC|[xh]\.?264)\b/i.test(text)) tags.push("AVC")
  const ext = (path ?? "").match(/\.(mkv|mp4|webm|avi)$/i)?.[1] ?? (path ? undefined : text.match(/(?:\.|\b)(mkv|mp4)\b/i)?.[1])
  if (ext) tags.push(ext.toUpperCase())
  return tags
}

/** 标签排序：分辨率（高→低）· 字幕语言 · 来源 · 编码 · 容器 */
const RES_ORDER = ["4K", "1080P", "720P", "480P"]
export function releaseTagRank(tag: string) {
  const r = RES_ORDER.indexOf(tag)
  if (r >= 0) return r
  const l = LANGS.findIndex((x) => x.tag === tag)
  if (l >= 0) return 50 + l
  if (tag === "HEVC" || tag === "AVC") return 200
  if (["MKV", "MP4", "WEBM", "AVI"].includes(tag)) return 300
  return 100 // 来源
}
