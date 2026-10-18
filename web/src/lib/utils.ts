import { clsx, type ClassValue } from "clsx"
import { twMerge } from "tailwind-merge"

export function cn(...inputs: ClassValue[]) {
  return twMerge(clsx(inputs))
}

const HEX40 = /^[0-9a-f]{40}$/i
const BASE32 = /^[a-z2-7]{32}$/i

/** base32（32 位）infohash → 40 位小写 hex */
function base32ToHex(s: string): string {
  const alpha = "abcdefghijklmnopqrstuvwxyz234567"
  let bits = ""
  for (const c of s.toLowerCase()) bits += alpha.indexOf(c).toString(2).padStart(5, "0")
  let hex = ""
  for (let i = 0; i + 4 <= bits.length; i += 4) hex += parseInt(bits.slice(i, i + 4), 2).toString(16)
  return hex
}

/** 规范化 infohash：40 位 hex → 小写；32 位 base32 → hex；其他返回 null */
export function normalizeInfohash(raw: string | null | undefined): string | null {
  const s = (raw ?? "").trim()
  if (HEX40.test(s)) return s.toLowerCase()
  if (BASE32.test(s)) return base32ToHex(s)
  return null
}

/** 从磁力链接提取 infohash（统一为 40 位小写 hex） */
export function infohashFromMagnet(magnet: string): string | null {
  const m = magnet.match(/xt=urn:btih:([0-9a-z]+)/i)
  return m ? normalizeInfohash(m[1]) : null
}

/** 判断输入是否为可直接播放的种子标识（磁力链接 / 40 位 hex / 32 位 base32），返回 infohash */
export function parseTorrentInput(raw: string): string | null {
  const s = raw.trim()
  if (!s) return null
  if (/^magnet:\?/i.test(s)) return infohashFromMagnet(s)
  return normalizeInfohash(s)
}

export function buildMagnet(infohash: string, name?: string, trackers: string[] = []) {
  let m = `magnet:?xt=urn:btih:${infohash}`
  if (name) m += `&dn=${encodeURIComponent(name)}`
  for (const t of trackers) m += `&tr=${encodeURIComponent(t)}`
  return m
}

// ---------- 页面路径（见 docs/api.md「路径约定」） ----------

/** 播放页：/watch/hash/{ih}[/file/{index}] */
export function watchHref(infohash: string, fileIndex?: number | null) {
  const base = `/watch/hash/${encodeURIComponent(infohash.toLowerCase())}`
  return fileIndex === undefined || fileIndex === null ? base : `${base}/file/${fileIndex}`
}

export interface SubjectRef {
  bgm_id?: number | string | null
  subject_id?: number | null
}

/** 条目路径参数：有 bgm id 时优先 /subject/bgm/{bgm_id}，否则 /subject/id/{subject_id} */
export function subjectPath(s: SubjectRef): { provider: string; id: string } | null {
  if (s.bgm_id !== undefined && s.bgm_id !== null && s.bgm_id !== "" && Number(s.bgm_id) > 0) return { provider: "bgm", id: String(s.bgm_id) }
  if (s.subject_id) return { provider: "id", id: String(s.subject_id) }
  return null
}

/** 条目页 */
export function subjectHref(s: SubjectRef) {
  const p = subjectPath(s)
  return p ? `/subject/${p.provider}/${encodeURIComponent(p.id)}` : "/"
}

/** 单集页 /subject/{provider}/{id}/episode/{n} */
export function episodeHref(s: SubjectRef, n: number) {
  const p = subjectPath(s)
  return p ? `/subject/${p.provider}/${encodeURIComponent(p.id)}/episode/${n}` : "/"
}

/** 单集页指定资源：/subject/{provider}/{id}/episode/{n}/hash/{ih}[/file/{index}] */
export function episodeSourceHref(s: SubjectRef, n: number, infohash: string, fileIndex?: number | null) {
  const base = episodeHref(s, n)
  if (base === "/") return watchHref(infohash, fileIndex)
  const ih = `${base}/hash/${encodeURIComponent(infohash.toLowerCase())}`
  return fileIndex === undefined || fileIndex === null ? ih : `${ih}/file/${fileIndex}`
}

/** 从播放类路径（/watch/hash/{ih}… 或 …/episode/{n}/hash/{ih}…）中取 infohash */
export function infohashOfPath(pathname: string): string | null {
  return pathname.match(/^\/(?:watch|subject\/[^/]+\/[^/]+\/episode\/[^/]+)\/hash\/([^/]+)/)?.[1]?.toLowerCase() ?? null
}

/** 搜索页 /search/{keyword}[?page=n] */
export function searchHref(keyword: string, page?: number) {
  const k = keyword.trim()
  if (!k) return "/search"
  return `/search/${encodeURIComponent(k)}${page && page > 1 ? `?page=${page}` : ""}`
}

export async function copyText(text: string) {
  try {
    await navigator.clipboard.writeText(text)
    return true
  } catch {
    const ta = document.createElement("textarea")
    ta.value = text
    ta.style.position = "fixed"
    ta.style.opacity = "0"
    document.body.appendChild(ta)
    ta.select()
    const ok = document.execCommand("copy")
    ta.remove()
    return ok
  }
}
