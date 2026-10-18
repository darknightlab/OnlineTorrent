import { useEffect, useRef, type RefObject } from "react"
import type { LogLevel } from "@/player/types"

/**
 * WebTorrent 的 Service Worker 流偶尔会把 <video> 的请求永久挂起（多年的老问题）：
 * 请求在数据尚未下载时发出，之后 SW 被回收或消息通道断开，这个请求再也不会返回；
 * Chrome 的媒体元素不会自动重试，于是停在 0:00（readyState 0）或卡在某一帧，
 * 哪怕文件早已下载完成。重新发起同一地址的请求即可恢复。
 *
 * 看门狗每 2 秒检查一次：
 * - 起播卡死：readyState 一直为 0 超过 STALL_MS，且文件开头的数据已经下载到；
 * - 播放中卡死：用户在播放、readyState < HAVE_FUTURE_DATA、currentTime 超过 STALL_MS 没有前进，
 *   且当前位置附近的数据已经下载到（或整个文件已下载完成）。
 * 满足时丢弃旧请求、重新加载同一地址，并恢复进度与播放状态。每个地址最多自动恢复 MAX_RELOADS 次。
 */

const TICK_MS = 2000
const STALL_MS = 12_000
const GRACE_MS = 15_000 // 重新加载后的观察期
const MAX_RELOADS = 6

type Opts = {
  video: RefObject<HTMLVideoElement | null>
  url: string | null
  /** 当前文件的分段下载状态（0 未下载 / 1 部分 / 2 完成），见 computeSegments */
  segments: Uint8Array
  log: (level: LogLevel, msg: string) => void
}

export function useStallWatchdog({ video, url, segments, log }: Opts) {
  const segRef = useRef(segments)
  segRef.current = segments
  const logRef = useRef(log)
  logRef.current = log

  useEffect(() => {
    if (!url) return
    let since = performance.now() // 开始等待（或上次有进展）的时间
    let lastTime = -1
    let reloads = 0
    let graceUntil = performance.now() + GRACE_MS

    const have = (frac: number) => {
      const s = segRef.current
      if (!s.length) return false
      if (s.every((x) => x === 2)) return true
      const i = Math.min(s.length - 1, Math.max(0, Math.floor(frac * s.length)))
      // 当前段与下一段都已下载完成
      return s[i] === 2 && (i + 1 >= s.length || s[i + 1] === 2)
    }

    const reload = (v: HTMLVideoElement, why: string) => {
      reloads++
      const t = v.currentTime
      const wasPlaying = !v.paused
      logRef.current("warn", `播放器${why}，已自动重新加载（第 ${reloads} 次）`)
      v.removeAttribute("src")
      v.load() // 丢弃挂起的请求
      v.src = url
      v.load()
      const resume = () => {
        if (t > 0 && Number.isFinite(t)) v.currentTime = t
        if (wasPlaying) v.play().catch(() => {})
      }
      v.addEventListener("loadedmetadata", resume, { once: true })
      since = performance.now()
      lastTime = -1
      graceUntil = performance.now() + GRACE_MS
    }

    const tick = () => {
      const v = video.current
      if (!v || reloads >= MAX_RELOADS) return
      const now = performance.now()
      if (now < graceUntil) {
        if (v.readyState > 0) since = now
        return
      }
      if (v.readyState === HTMLMediaElement.HAVE_NOTHING) {
        // 起播：开头的数据已经在本地，却一直拿不到元数据
        if (now - since > STALL_MS && have(0)) reload(v, "长时间无法读取视频")
        return
      }
      const progressing = v.currentTime !== lastTime
      lastTime = v.currentTime
      const stalled = !v.paused && !v.ended && v.readyState < HTMLMediaElement.HAVE_FUTURE_DATA
      if (!stalled || progressing) {
        since = now
        return
      }
      const frac = v.duration > 0 && Number.isFinite(v.duration) ? v.currentTime / v.duration : 0
      if (now - since > STALL_MS && have(frac)) reload(v, "卡住")
    }

    const timer = window.setInterval(tick, TICK_MS)
    return () => window.clearInterval(timer)
  }, [url, video])
}
