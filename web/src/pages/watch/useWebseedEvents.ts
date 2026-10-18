import { useEffect, useRef } from "react"
import { api } from "@/lib/api"
import type { WebseedEndEvent, WebseedReadyEvent } from "@/lib/types"
import type { LogLevel, Session } from "@/player/types"

/** 连续多少次连接错误（期间未收到任何事件）后放弃 */
const MAX_ERRORS = 5

function parse<T>(e: Event): T | null {
  try {
    return JSON.parse((e as MessageEvent).data) as T
  } catch {
    return null
  }
}

/**
 * 服务器通过 SSE 推送 WebSeed 可用性：收到 ready 后才把（新签名的）WebSeed 加入种子，
 * 避免服务器还没有文件时 webtorrent 高频请求 webseed 得到 404。
 *
 * - armed：已获取 WebSeed 配置且 delay_sec 已过（由调用方控制权限与延迟）
 * - fileIndex：undefined = 元数据未就绪（不连接）；null = 任意 / 单文件；number = 正在播放的文件
 * - 文件切换时关闭并为新文件重连；会话销毁、浏览器内已下载完成、已启用 WebSeed 后不再连接
 */
export function useWebseedEvents({
  session,
  ih,
  armed,
  fileIndex,
  done,
  log,
}: {
  session: Session | null
  ih: string | null
  armed: boolean
  fileIndex: number | null | undefined
  done: boolean
  log: (level: LogLevel, msg: string) => void
}) {
  // 每个会话一份：是否已启用、各类日志是否已输出（只在状态变化时记一次）
  const states = useRef(new WeakMap<Session, { enabled: boolean; waiting: boolean; ended: boolean; failed: boolean }>())

  useEffect(() => {
    if (!session || !ih || !armed || done || fileIndex === undefined) return
    const s = states.current.get(session) ?? { enabled: false, waiting: false, ended: false, failed: false }
    states.current.set(session, s)
    if (s.enabled) return
    const es = new EventSource(api.webseedEventsURL(ih, fileIndex), { withCredentials: true })
    let errors = 0
    let closed = false
    const close = () => {
      if (closed) return
      closed = true
      es.close()
    }
    const onEvent = () => {
      errors = 0
    }

    es.addEventListener("ready", (e) => {
      onEvent()
      close()
      const urls = (parse<WebseedReadyEvent>(e)?.webseeds ?? []).filter((u): u is string => typeof u === "string" && !!u)
      let added = 0
      for (const u of urls) if (session.addWebSeed(u)) added++
      if (added > 0) {
        s.enabled = true
        log("info", "已启用 HTTP WebSeed 加速")
      } else if (!urls.length && !s.ended) {
        s.ended = true
        log("warn", "服务器未提供可用的 WebSeed，仅使用 P2P 网络")
      }
    })
    es.addEventListener("waiting", () => {
      onEvent()
      if (s.waiting) return
      s.waiting = true
      log("info", "服务器正在准备该文件，完成后自动启用 WebSeed")
    })
    const onEnd = (e: Event) => {
      onEvent()
      close()
      if (s.ended) return
      s.ended = true
      const reason = parse<WebseedEndEvent>(e)?.reason
      log("warn", `WebSeed 暂不可用，仅使用 P2P 网络${reason ? `（${reason}）` : ""}`)
    }
    es.addEventListener("timeout", onEnd)
    es.addEventListener("unavailable", onEnd)
    es.onmessage = onEvent
    es.onerror = () => {
      if (closed) return
      // CLOSED：浏览器不会再重连（如 HTTP 错误、非 text/event-stream）；否则允许自动重连若干次
      if (es.readyState !== EventSource.CLOSED && ++errors < MAX_ERRORS) return
      close()
      if (s.failed) return
      s.failed = true
      log("warn", "WebSeed 状态连接失败，仅使用 P2P 网络")
    }
    return close
  }, [session, ih, armed, fileIndex, done, log])
}
