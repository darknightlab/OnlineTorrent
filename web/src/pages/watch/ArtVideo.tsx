import { useEffect, useRef, useState } from "react"
import { PlayerSpinner } from "@/components/PlayerSpinner"
import type { ArtHandle } from "./artplayer-setup"
import type { SubtitleSource } from "@/player/subs/source"
import { chooseTrack, loadPref, prefFor, savePref } from "@/player/subs/prefs"
import { trackLabel } from "@/player/subs/lang"
import { byteToTimeFraction } from "@/lib/timemap"
import type { TimeMapResult } from "@/player/types"

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
  /** 当前文件的内封字幕（MKV）；有字幕轨道时控制栏出现「字幕」 */
  subtitles?: SubtitleSource | null
  onLog?: (level: "info" | "warn", msg: string) => void
  /** 进度条 piece 图层的字节 → 时间映射（容器索引，见 session.timeMap）；元数据加载后调用一次 */
  timeMap?: () => Promise<TimeMapResult | null>
  /** 当前文件长度（字节），与 timeMap 一起使用 */
  fileLength?: number
}

/**
 * ArtPlayer 的 React 包装：每个 url 一个实例（父组件以 key={url} 挂载），卸载即销毁。
 * StrictMode 下 effect 会执行两次：第一次的实例在清理时 destroy（移除 DOM 与事件，释放流请求），第二次重新创建；
 * 模块尚未加载完就被清理时不会创建实例。
 */
export function ArtVideo({ url, poster, segments, speed, onVideo, onLoadedMetadata, onMediaError, onPanel, subtitles, onLog, timeMap, fileLength }: ArtVideoProps) {
  const box = useRef<HTMLDivElement>(null)
  const handle = useRef<ArtHandle | null>(null)
  const [ready, setReady] = useState(false)
  const [loadError, setLoadError] = useState<string | null>(null)
  // 回调与动态数据用 ref 读取最新值，不触发重建
  const latest = useRef({ poster, segments, speed, onVideo, onLoadedMetadata, onMediaError, onPanel, onLog, timeMap, fileLength })
  latest.current = { poster, segments, speed, onVideo, onLoadedMetadata, onMediaError, onPanel, onLog, timeMap, fileLength }
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

  // 进度条 piece 图层按时间画：元数据加载后（浏览器已读过 moov / 头部）取容器索引，每个文件算一次；
  // 只有观察值（MKV 没有 Cues）时每 10 秒更新一次
  useEffect(() => {
    const h = handle.current
    if (!ready || !h || !url) return
    let cancelled = false
    let timer = 0
    const v = h.video
    const load = () => {
      const l = latest.current
      if (!l.timeMap || !l.fileLength || !(v.duration > 0) || !Number.isFinite(v.duration)) return
      void l.timeMap().then((r) => {
        if (cancelled) return
        if (r) h.setTimeMap(byteToTimeFraction(r.map, l.fileLength!, v.duration))
        if (!r || r.partial) timer = window.setTimeout(load, 10_000)
      })
    }
    if (v.readyState >= HTMLMediaElement.HAVE_METADATA) load()
    else v.addEventListener("loadedmetadata", load, { once: true })
    return () => {
      cancelled = true
      window.clearTimeout(timer)
      v.removeEventListener("loadedmetadata", load)
    }
  }, [ready, url])

  // 内封字幕：头部解析出字幕轨道后显示「字幕」按钮，按会话偏好 / 浏览器语言选默认轨道；
  // 渲染器（JASSUB，较大）只在真的要显示字幕时才加载
  useEffect(() => {
    const h = handle.current
    if (!ready || !h || !subtitles || !url) return
    let cancelled = false
    let renderer: import("@/player/subs/render").SubtitleRenderer | null = null
    let rendererP: Promise<import("@/player/subs/render").SubtitleRenderer | null> | null = null
    const log = (level: "info" | "warn", msg: string) => latest.current.onLog?.(level, msg)
    const getRenderer = () =>
      (rendererP ??= import("@/player/subs/render")
        .then(({ SubtitleRenderer }) => {
          if (cancelled) return null
          renderer = new SubtitleRenderer(h.video, subtitles, window.matchMedia?.("(pointer: coarse)").matches ?? false)
          return renderer
        })
        .catch((e) => {
          log("warn", `字幕渲染器加载失败：${String(e?.message ?? e)}`)
          return null
        }))
    void subtitles.header.then((hd) => {
      if (cancelled || !hd?.subtitles.length) return
      const tracks = hd.subtitles
      let current = chooseTrack(tracks, loadPref(), navigator.languages ?? [navigator.language])
      const apply = async (i: number) => {
        current = i
        if (i < 0 && !renderer) return // 从没显示过：不必加载渲染器
        const r = await getRenderer()
        if (!cancelled && current === i) await r?.show(i >= 0 ? tracks[i] : null)
      }
      h.setSubtitleMenu(
        tracks.map((t, i) => ({ label: trackLabel(t, i), value: i })),
        current,
        (i) => {
          savePref(prefFor(tracks, i))
          void apply(i)
          log("info", i >= 0 ? `字幕：${trackLabel(tracks[i], i)}` : "已关闭字幕")
        },
      )
      log("info", `内封字幕 ${tracks.length} 条${current >= 0 ? `，已选择「${trackLabel(tracks[current], current)}」` : "，没有与浏览器语言匹配的，默认不显示"}`)
      if (current >= 0) void apply(current)
    })
    return () => {
      cancelled = true
      renderer?.destroy()
      handle.current?.setSubtitleMenu(null, -1, () => {})
    }
  }, [ready, subtitles, url])

  return (
    <>
      {/* 播放器模块加载中（空 url 的外壳由外层显示状态，这里不再重复转圈） */}
      {!ready && (url || loadError) && (
        <div className="absolute inset-0 flex flex-col items-center justify-center gap-2 text-sm text-white">
          {poster && <div aria-hidden className="absolute inset-0 bg-contain bg-center bg-no-repeat" style={{ backgroundImage: `url("${poster}")` }} />}
          {loadError ? (
            <p className="relative z-10 rounded bg-black/70 px-3 py-1.5">播放器加载失败，请刷新页面重试</p>
          ) : (
            <PlayerSpinner className="relative z-10" />
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
