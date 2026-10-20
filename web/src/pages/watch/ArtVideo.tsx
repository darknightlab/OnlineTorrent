import { useEffect, useRef, useState } from "react"
import { Loader2 } from "lucide-react"
import type { ArtHandle } from "./artplayer-setup"

let setupModule: Promise<typeof import("./artplayer-setup")> | null = null
/** 加载 ArtPlayer（单独的 chunk，含样式）；Player 挂载时调用以预加载 */
export function loadArtplayer() {
  setupModule ??= import("./artplayer-setup").catch((e) => {
    setupModule = null // 允许下次重试（如网络抖动）
    throw e
  })
  return setupModule
}

export interface ArtVideoProps {
  url: string
  poster?: string
  /** 当前文件的分段下载状态（见 lib/pieces.segmentCount / session.segments） */
  segments: Uint8Array
  /** 当前下载速度（字节/秒），显示在缓冲提示里 */
  speed: number
  /** ArtPlayer 内部的 <video>（卸载时为 null），供看门狗使用 */
  onVideo?: (v: HTMLVideoElement | null) => void
  onLoadedMetadata?: (v: HTMLVideoElement) => void
  onMediaError?: (err: MediaError) => void
  /** 控制栏「资源」按钮 */
  onPanel?: () => void
}

/**
 * ArtPlayer 的 React 包装：每个 url 一个实例（父组件以 key={url} 挂载），卸载即销毁。
 * StrictMode 下 effect 会执行两次：第一次的实例在清理时 destroy（移除 DOM 与事件，释放流请求），第二次重新创建；
 * 模块尚未加载完就被清理时不会创建实例。
 */
export function ArtVideo({ url, poster, segments, speed, onVideo, onLoadedMetadata, onMediaError, onPanel }: ArtVideoProps) {
  const box = useRef<HTMLDivElement>(null)
  const handle = useRef<ArtHandle | null>(null)
  const [ready, setReady] = useState(false)
  const [loadError, setLoadError] = useState<string | null>(null)
  // 回调与动态数据用 ref 读取最新值，不触发重建
  const latest = useRef({ poster, segments, speed, onVideo, onLoadedMetadata, onMediaError, onPanel })
  latest.current = { poster, segments, speed, onVideo, onLoadedMetadata, onMediaError, onPanel }
  const hasPanel = !!onPanel

  useEffect(() => {
    const el = box.current
    if (!el) return
    let cancelled = false
    let h: ArtHandle | null = null
    loadArtplayer()
      .then(({ mountArtplayer }) => {
        if (cancelled) return
        const l = latest.current
        h = mountArtplayer(el, {
          url,
          poster: l.poster,
          segments: l.segments,
          speed: l.speed,
          onLoadedMetadata: (v) => latest.current.onLoadedMetadata?.(v),
          onMediaError: (e) => latest.current.onMediaError?.(e),
          onPanel: hasPanel ? () => latest.current.onPanel?.() : undefined,
        })
        handle.current = h
        setReady(true)
        latest.current.onVideo?.(h.video)
      })
      .catch((e) => {
        console.error("[artplayer]", e)
        if (!cancelled) setLoadError(String(e?.message ?? e))
      })
    return () => {
      cancelled = true
      if (h) {
        latest.current.onVideo?.(null)
        h.destroy()
      }
      handle.current = null
      setReady(false)
    }
  }, [url, hasPanel])

  useEffect(() => handle.current?.setPoster(poster), [poster, ready])
  useEffect(() => handle.current?.setSegments(segments), [segments, ready])
  useEffect(() => handle.current?.setSpeed(speed), [speed, ready])

  return (
    <>
      {!ready && (
        <div className="absolute inset-0 flex flex-col items-center justify-center gap-2 text-sm text-white">
          {poster && <div aria-hidden className="absolute inset-0 bg-contain bg-center bg-no-repeat" style={{ backgroundImage: `url("${poster}")` }} />}
          {loadError ? (
            <p className="relative z-10 rounded bg-black/70 px-3 py-1.5">播放器加载失败，请刷新页面重试</p>
          ) : (
            <Loader2 className="relative z-10 h-8 w-8 animate-spin text-primary" />
          )}
        </div>
      )}
      {/* ArtPlayer 接管此 div 的内容（React 不渲染子节点） */}
      <div
        ref={box}
        tabIndex={0}
        aria-label="视频播放器（空格 / k 播放暂停，←/→ 快退快进，↑/↓ 音量，f 全屏，m 静音）"
        className="ot-artplayer absolute inset-0 isolate z-0 outline-none"
      />
    </>
  )
}
