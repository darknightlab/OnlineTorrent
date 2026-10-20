import { useEffect, useRef } from "react"
import { api } from "@/lib/api"
import { PERM, can, useMe } from "@/lib/queries"
import { WebseedSubscription, type EventSourceLike, type SubState } from "@/lib/webseed"
import type { LogLevel, Session } from "@/player/types"

/**
 * 服务器通过 SSE 推送 WebSeed 可用性（internal/api/webseed_stream.go），按 WebSeed 地址分别处理：
 * WebSeed 只由边缘节点提供（直接从 qB 的下载目录读取，边下边供）；没有边缘节点的服务器不提供 WebSeed。
 * - ready / seed：某个地址可用 → 加入 WebSeed，只宣告该地址已有的分块；边缘节点带访问令牌；
 * - have / lost：该地址可提供的分块增减 → 更新宣告；
 * - token：边缘节点的新令牌（到期前推送），换上即可（URL 不变）；
 * - complete：该地址已有全部分块；done：全部地址都完整，订阅结束。
 *
 * 永不放弃：页面打开、文件未在本地完整、WebSeed 未 complete 时，断线 / timeout / unavailable 后
 * 按 15 s、30 s、60 s … 最长 5 min 退避重连；页面重新可见或网络恢复时立即重连；
 * not_seeded 时若有做种权限先重新请求做种。日志只在状态变化时输出。
 *
 * - armed：有 WebSeed 权限且已启用（由调用方判断）
 * - fileIndex：undefined = 元数据未就绪（不连接）；null = 任意 / 单文件；number = 正在播放的文件
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
  const me = useMe()
  const canSeed = can(me.data, PERM.seed)
  const canSeedRef = useRef(canSeed)
  canSeedRef.current = canSeed
  // 每个会话一份：已加入的 WebSeed、是否已 complete、上一条日志（只在变化时记）
  const states = useRef(new WeakMap<Session, { urls: Set<string>; complete: boolean; last: string; edges: number }>())

  useEffect(() => {
    if (!session || !ih || !armed || done || fileIndex === undefined) return
    const st = states.current.get(session) ?? { urls: new Set<string>(), complete: false, last: "", edges: 0 }
    states.current.set(session, st)
    if (st.complete) return
    const say = (level: LogLevel, msg: string, key: string) => {
      if (st.last === key) return
      st.last = key
      log(level, msg)
    }
    const sub = new WebseedSubscription(
      {
        open: () => new EventSource(api.webseedEventsURL(ih, fileIndex), { withCredentials: true }) as unknown as EventSourceLike,
        setTimeout: (cb, ms) => window.setTimeout(cb, ms),
        clearTimeout: (t) => window.clearTimeout(t as number),
      },
      {
        seed: ({ url, pieces, have, complete, token }) => {
          if (st.urls.has(url)) {
            // 重连后服务器重发：同步分块、换上新令牌
            if (token) session.setWebSeedToken?.(url, token)
            session.updateWebSeed?.(url, complete ? { complete: true } : have ? { reset: have } : {})
            return
          }
          if (!session.addWebSeed(url, { pieces, have, complete, token })) return
          st.urls.add(url)
          if (token) st.edges++
          const info = session.webSeedPieces?.(url)
          const pct = info && info.have < info.of ? `，已有 ${Math.floor((info.have / Math.max(1, info.of)) * 100)}% 的数据，随下载进度增加` : ""
          say("info", `已启用 HTTP WebSeed 加速（边缘节点${pct}）`, `enabled:${url}`)
        },
        have: (url, r) => session.updateWebSeed?.(url, { have: r }),
        lost: (url, r) => session.updateWebSeed?.(url, { lost: r }),
        complete: (url) => session.updateWebSeed?.(url, { complete: true }),
        token: (url, token) => session.setWebSeedToken?.(url, token),
        done: () => {
          st.complete = true
          if (st.urls.size) say("info", "WebSeed 已可提供全部数据", "complete")
        },
        waiting: () => say("info", "服务器正在获取该种子，就绪后自动启用 WebSeed", "waiting"),
        unavailable: async (reason) => {
          if (reason === "not_seeded" && canSeedRef.current) {
            say("info", "服务器尚未做种该资源，已重新请求做种", "reseed")
            try {
              await api.seed(ih, fileIndex ?? 0) // null: single-file torrent
            } catch {
              /* 稍后重试 */
            }
            return
          }
          say("warn", `WebSeed 暂不可用${reason ? `（${reason}）` : ""}，稍后自动重试`, `unavailable:${reason}`)
        },
        state: (s: SubState, info) => {
          if (s === "waiting-retry" && st.last !== "reseed" && !st.last.startsWith("unavailable"))
            say("warn", `WebSeed 状态连接中断，${Math.round((info?.retryInMs ?? 0) / 1000)} 秒后自动重连`, "retry")
          else if (s === "open" && st.last === "retry") say("info", "WebSeed 状态连接已恢复", "open")
        },
      },
    )
    sub.start()
    const kick = () => {
      if (document.visibilityState === "visible") sub.kick()
    }
    document.addEventListener("visibilitychange", kick)
    window.addEventListener("online", kick)
    return () => {
      document.removeEventListener("visibilitychange", kick)
      window.removeEventListener("online", kick)
      sub.stop()
    }
  }, [session, ih, armed, fileIndex, done, log])
}
