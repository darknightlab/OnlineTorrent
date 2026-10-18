import { useEffect, useState } from "react"

/** 每 intervalMs 刷新一次的当前时间（用于相对时间、播出状态） */
export function useNow(intervalMs = 60_000) {
  const [now, setNow] = useState(() => Date.now())
  useEffect(() => {
    const t = window.setInterval(() => setNow(Date.now()), intervalMs)
    return () => window.clearInterval(t)
  }, [intervalMs])
  return now
}

/** 设置页面标题；null = 站点名，undefined = 不修改 */
export function useDocumentTitle(title: string | null | undefined) {
  useEffect(() => {
    if (title === undefined) return
    document.title = title ? `${title} - Project Kawaii` : "Project Kawaii"
  }, [title])
}

export function useMediaQuery(q: string) {
  const [m, setM] = useState(() => window.matchMedia(q).matches)
  useEffect(() => {
    const mq = window.matchMedia(q)
    const on = () => setM(mq.matches)
    mq.addEventListener("change", on)
    return () => mq.removeEventListener("change", on)
  }, [q])
  return m
}
