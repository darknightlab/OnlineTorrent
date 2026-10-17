import { clsx, type ClassValue } from "clsx"
import { twMerge } from "tailwind-merge"

export function cn(...inputs: ClassValue[]) {
  return twMerge(clsx(inputs))
}

const HEX40 = /^[0-9a-f]{40}$/i
const BASE32 = /^[a-z2-7]{32}$/i

/** 判断输入是否为可直接播放的种子标识（磁力链接 / 40 位 infohash / 32 位 base32） */
export function parseTorrentInput(raw: string): { magnet: string } | { infohash: string } | null {
  const s = raw.trim()
  if (!s) return null
  if (/^magnet:\?/i.test(s)) return { magnet: s }
  if (HEX40.test(s)) return { infohash: s.toLowerCase() }
  if (BASE32.test(s)) return { magnet: `magnet:?xt=urn:btih:${s}` }
  return null
}

/** 从磁力链接提取 infohash（hex 小写；base32 原样返回） */
export function infohashFromMagnet(magnet: string): string | null {
  const m = magnet.match(/xt=urn:btih:([0-9a-z]+)/i)
  if (!m) return null
  return HEX40.test(m[1]) ? m[1].toLowerCase() : m[1]
}

export function buildMagnet(infohash: string, name?: string, trackers: string[] = []) {
  let m = `magnet:?xt=urn:btih:${infohash}`
  if (name) m += `&dn=${encodeURIComponent(name)}`
  for (const t of trackers) m += `&tr=${encodeURIComponent(t)}`
  return m
}

/** 播放页 URL */
export function watchHref(p: { t?: string; ih?: string; uri?: string }) {
  const s = new URLSearchParams()
  if (p.t) s.set("t", p.t)
  else if (p.ih) s.set("ih", p.ih)
  else if (p.uri) s.set("uri", p.uri)
  return `/watch?${s.toString()}`
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
