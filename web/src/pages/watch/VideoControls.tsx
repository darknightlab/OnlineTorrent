import { useCallback, useEffect, useRef, useState } from "react"
import { Maximize, Minimize, Pause, PictureInPicture2, Play, Volume1, Volume2, VolumeX } from "lucide-react"
import { PieceLayer, segmentsDone } from "./PieceBar"
import { cn } from "@/lib/utils"

const HIDE_MS = 2500
const DOUBLE_MS = 280
const SEEK_STEP = 5
const RATES = [0.5, 0.75, 1, 1.25, 1.5, 2]

// 非标准 / 带前缀的全屏 API（Safari）
type WKDoc = Document & { webkitFullscreenElement?: Element | null; webkitExitFullscreen?: () => Promise<void> | void }
type WKEl = HTMLElement & { webkitRequestFullscreen?: () => Promise<void> | void }
type WKVideo = HTMLVideoElement & { webkitEnterFullscreen?: () => void; webkitExitFullscreen?: () => void; webkitDisplayingFullscreen?: boolean }

function fmt(sec: number) {
  if (!Number.isFinite(sec) || sec < 0) sec = 0
  const s = Math.floor(sec)
  const h = Math.floor(s / 3600)
  const m = Math.floor((s % 3600) / 60)
  const ss = String(s % 60).padStart(2, "0")
  return h ? `${h}:${String(m).padStart(2, "0")}:${ss}` : `${m}:${ss}`
}

const fsElement = () => document.fullscreenElement ?? (document as WKDoc).webkitFullscreenElement ?? null

/**
 * 自定义视频控件（B 站 / YouTube 风格，精简）：渲染为容器内的绝对定位图层。
 * - container：要全屏的播放器容器（全屏后自定义控件仍可见）；键盘与鼠标移动监听挂在容器上
 * - segments：当前文件的 piece 分段状态，绘制在进度条里
 * - suspended：有覆盖层（如「点击播放」）时隐藏控件
 */
export function VideoControls({
  video,
  container,
  segments,
  suspended,
}: {
  video: HTMLVideoElement | null
  container: React.RefObject<HTMLDivElement | null>
  segments: Uint8Array
  suspended?: boolean
}) {
  const [paused, setPaused] = useState(true)
  const [time, setTime] = useState(0)
  const [duration, setDuration] = useState(0)
  const [volume, setVolume] = useState(1)
  const [muted, setMuted] = useState(false)
  const [rate, setRate] = useState(1)
  const [fullscreen, setFullscreen] = useState(false)
  const [pip, setPip] = useState(false)
  const [shown, setShown] = useState(true)
  const [scrubbing, setScrubbing] = useState(false)
  const [menu, setMenu] = useState(false)
  const hideTimer = useRef(0)
  const tapTimer = useRef(0)
  const lastTap = useRef(0)

  // 同步 <video> 状态
  useEffect(() => {
    if (!video) return
    const sync = () => {
      setPaused(video.paused)
      setTime(video.currentTime)
      setDuration(Number.isFinite(video.duration) ? video.duration : 0)
      setVolume(video.volume)
      setMuted(video.muted)
      setRate(video.playbackRate)
      setPip(document.pictureInPictureElement === video)
    }
    sync()
    const evs = ["play", "pause", "timeupdate", "durationchange", "loadedmetadata", "volumechange", "ratechange", "seeking", "enterpictureinpicture", "leavepictureinpicture", "emptied"]
    evs.forEach((e) => video.addEventListener(e, sync))
    // iOS 原生全屏
    const wkIn = () => setFullscreen(true)
    const wkOut = () => setFullscreen(!!fsElement())
    video.addEventListener("webkitbeginfullscreen", wkIn)
    video.addEventListener("webkitendfullscreen", wkOut)
    return () => {
      evs.forEach((e) => video.removeEventListener(e, sync))
      video.removeEventListener("webkitbeginfullscreen", wkIn)
      video.removeEventListener("webkitendfullscreen", wkOut)
    }
  }, [video])

  useEffect(() => {
    const on = () => setFullscreen(!!fsElement() && fsElement() === container.current)
    document.addEventListener("fullscreenchange", on)
    document.addEventListener("webkitfullscreenchange", on)
    return () => {
      document.removeEventListener("fullscreenchange", on)
      document.removeEventListener("webkitfullscreenchange", on)
    }
  }, [container])

  // ---- 显示 / 自动隐藏 ----
  const poke = useCallback(() => {
    setShown(true)
    window.clearTimeout(hideTimer.current)
    hideTimer.current = window.setTimeout(() => setShown(false), HIDE_MS)
  }, [])
  const hide = useCallback(() => {
    window.clearTimeout(hideTimer.current)
    setShown(false)
  }, [])
  useEffect(
    () => () => {
      window.clearTimeout(hideTimer.current)
      window.clearTimeout(tapTimer.current)
    },
    [],
  )
  // 播放开始后从头计时自动隐藏
  useEffect(() => {
    if (!paused) poke()
  }, [paused, poke])
  const visible = !suspended && (shown || paused || scrubbing || menu)

  // ---- 操作 ----
  const togglePlay = useCallback(() => {
    if (!video) return
    if (video.paused || video.ended) video.play().catch(() => {})
    else video.pause()
  }, [video])

  const seekTo = useCallback(
    (t: number) => {
      if (!video) return
      const d = Number.isFinite(video.duration) ? video.duration : 0
      video.currentTime = Math.max(0, d ? Math.min(d, t) : t)
      setTime(video.currentTime)
    },
    [video],
  )

  const setVol = useCallback(
    (v: number) => {
      if (!video) return
      video.volume = Math.max(0, Math.min(1, v))
      video.muted = video.volume === 0
    },
    [video],
  )

  const toggleMute = useCallback(() => {
    if (!video) return
    if (video.muted || video.volume === 0) {
      video.muted = false
      if (video.volume === 0) video.volume = 0.5
    } else video.muted = true
  }, [video])

  const toggleFullscreen = useCallback(() => {
    const el = container.current as WKEl | null
    const v = video as WKVideo | null
    const d = document as WKDoc
    if (fsElement()) {
      void (d.exitFullscreen ? d.exitFullscreen() : d.webkitExitFullscreen?.())?.catch?.(() => {})
      return
    }
    if (v?.webkitDisplayingFullscreen) return v.webkitExitFullscreen?.()
    const req = el?.requestFullscreen ? el.requestFullscreen.bind(el) : el?.webkitRequestFullscreen?.bind(el)
    if (req) {
      Promise.resolve(req())
        .then(() => {
          // 移动端横屏视频：尝试锁定横屏（不支持则忽略）
          const o = screen.orientation as ScreenOrientation & { lock?: (o: string) => Promise<void> }
          if (v && v.videoWidth > v.videoHeight && window.matchMedia("(pointer: coarse)").matches) o?.lock?.("landscape").catch(() => {})
        })
        .catch(() => v?.webkitEnterFullscreen?.())
    } else v?.webkitEnterFullscreen?.() // iPhone：只能全屏 <video>（使用系统控件）
  }, [container, video])

  const pipSupported = typeof document !== "undefined" && document.pictureInPictureEnabled && !video?.disablePictureInPicture
  const togglePip = useCallback(() => {
    if (!video) return
    if (document.pictureInPictureElement) document.exitPictureInPicture().catch(() => {})
    else video.requestPictureInPicture().catch(() => {})
  }, [video])

  // ---- 容器：鼠标移动显示控件、键盘快捷键 ----
  useEffect(() => {
    const el = container.current
    if (!el) return
    const onMove = (e: PointerEvent) => {
      if (e.pointerType === "mouse") poke()
    }
    const onLeave = (e: PointerEvent) => {
      if (e.pointerType === "mouse" && video && !video.paused) hide()
    }
    const onKey = (e: KeyboardEvent) => {
      if (e.ctrlKey || e.metaKey || e.altKey || !video) return
      const t = e.target as HTMLElement
      const tag = t.tagName
      // 让按钮 / 菜单项 / 音量滑块处理自己的按键
      if ((e.key === " " || e.key === "Enter") && (tag === "BUTTON" || t.getAttribute("role") === "menuitemradio")) return
      if (tag === "INPUT" && e.key.startsWith("Arrow")) return
      let handled = true
      switch (e.key) {
        case " ":
        case "k":
        case "K":
          togglePlay()
          break
        case "ArrowLeft":
          seekTo(video.currentTime - SEEK_STEP)
          break
        case "ArrowRight":
          seekTo(video.currentTime + SEEK_STEP)
          break
        case "ArrowUp":
          setVol(video.volume + 0.1)
          break
        case "ArrowDown":
          setVol(video.volume - 0.1)
          break
        case "f":
        case "F":
          toggleFullscreen()
          break
        case "m":
        case "M":
          toggleMute()
          break
        case "Escape":
          if (menu) setMenu(false)
          else handled = false
          break
        default:
          handled = false
      }
      if (handled) {
        e.preventDefault()
        poke()
      }
    }
    el.addEventListener("pointermove", onMove)
    el.addEventListener("pointerleave", onLeave)
    el.addEventListener("keydown", onKey)
    return () => {
      el.removeEventListener("pointermove", onMove)
      el.removeEventListener("pointerleave", onLeave)
      el.removeEventListener("keydown", onKey)
    }
  }, [container, video, poke, hide, togglePlay, seekTo, setVol, toggleFullscreen, toggleMute, menu])

  // 点击画面：桌面单击播放/暂停、双击全屏；触屏单击显示/隐藏控件、双击全屏
  const onSurfaceUp = (e: React.PointerEvent) => {
    if (e.button !== 0) return
    container.current?.focus({ preventScroll: true })
    if (menu) return setMenu(false)
    const now = performance.now()
    const dbl = now - lastTap.current < DOUBLE_MS
    lastTap.current = dbl ? 0 : now
    if (e.pointerType === "mouse") {
      if (dbl) toggleFullscreen()
      else togglePlay()
      return
    }
    window.clearTimeout(tapTimer.current)
    if (dbl) return toggleFullscreen()
    tapTimer.current = window.setTimeout(() => (visible ? hide() : poke()), DOUBLE_MS)
  }

  const VolIcon = muted || volume === 0 ? VolumeX : volume < 0.5 ? Volume1 : Volume2

  return (
    <>
      {/* 点击层（位于视频之上、控件之下） */}
      <div aria-hidden className={cn("absolute inset-0 z-10", !visible && "cursor-none")} onPointerUp={onSurfaceUp} />

      {/* 触屏：画面中央的大播放按钮 */}
      <button
        type="button"
        aria-label={paused ? "播放" : "暂停"}
        onClick={() => {
          togglePlay()
          poke()
        }}
        className={cn(
          "absolute left-1/2 top-1/2 z-20 hidden h-14 w-14 -translate-x-1/2 -translate-y-1/2 items-center justify-center rounded-full bg-black/45 text-white backdrop-blur-sm transition-opacity outline-none focus-visible:ring-2 focus-visible:ring-white/80 pointer-coarse:flex",
          visible ? "opacity-100" : "pointer-events-none opacity-0",
        )}
      >
        {paused ? <Play className="ml-0.5 h-7 w-7 fill-current" /> : <Pause className="h-7 w-7 fill-current" />}
      </button>

      {/* 底部控件栏（始终在暗色渐变上，与亮/暗主题无关） */}
      <div
        className={cn(
          "absolute inset-x-0 bottom-0 z-20 bg-gradient-to-t from-black/85 via-black/45 to-transparent px-2 pb-1 pt-10 text-white transition-opacity duration-200 sm:px-3 sm:pb-1.5",
          visible ? "opacity-100" : "pointer-events-none opacity-0",
        )}
        onPointerDown={poke}
        onFocusCapture={poke}
      >
        <SeekBar time={time} duration={duration} segments={segments} onSeek={seekTo} onScrub={setScrubbing} />
        <div className="flex h-10 items-center gap-0.5 sm:gap-1">
          <CtlButton label={paused ? "播放 (k)" : "暂停 (k)"} onClick={togglePlay}>
            {paused ? <Play className="h-5 w-5 fill-current" /> : <Pause className="h-5 w-5 fill-current" />}
          </CtlButton>
          <div className="group/vol flex items-center">
            <CtlButton label={muted ? "取消静音 (m)" : "静音 (m)"} onClick={toggleMute}>
              <VolIcon className="h-5 w-5" />
            </CtlButton>
            <input
              type="range"
              min={0}
              max={1}
              step={0.05}
              value={muted ? 0 : volume}
              onChange={(e) => setVol(Number(e.target.value))}
              aria-label="音量"
              className="h-1 w-0 cursor-pointer accent-primary opacity-0 transition-all duration-200 group-hover/vol:mr-2 group-hover/vol:w-16 group-hover/vol:opacity-100 focus-visible:mr-2 focus-visible:w-16 focus-visible:opacity-100 pointer-coarse:hidden"
            />
          </div>
          <span className="ml-1 whitespace-nowrap text-xs tabular-nums text-white/90 sm:text-[13px]">
            {fmt(time)}
            <span className="mx-1 text-white/50">/</span>
            {fmt(duration)}
          </span>
          <span className="flex-1" />
          <div className="relative">
            <CtlButton label="播放速度" onClick={() => setMenu((m) => !m)} aria-haspopup="menu" aria-expanded={menu} className="w-auto min-w-9 px-1.5 text-[13px] font-semibold tabular-nums">
              {rate === 1 ? "倍速" : `${rate}x`}
            </CtlButton>
            {menu && <RateMenu rate={rate} onPick={(r) => {
              if (video) video.playbackRate = r
              setMenu(false)
            }} onClose={() => setMenu(false)} />}
          </div>
          {pipSupported && (
            <CtlButton label={pip ? "退出画中画" : "画中画"} onClick={togglePip} className="max-[359px]:hidden">
              <PictureInPicture2 className="h-5 w-5" />
            </CtlButton>
          )}
          <CtlButton label={fullscreen ? "退出全屏 (f)" : "全屏 (f)"} onClick={toggleFullscreen}>
            {fullscreen ? <Minimize className="h-5 w-5" /> : <Maximize className="h-5 w-5" />}
          </CtlButton>
        </div>
      </div>
    </>
  )
}

function CtlButton({ label, className, children, ...rest }: { label: string } & React.ButtonHTMLAttributes<HTMLButtonElement>) {
  return (
    <button
      type="button"
      aria-label={label}
      title={label}
      className={cn(
        "inline-flex h-9 w-9 shrink-0 items-center justify-center rounded-md text-white/90 transition-colors outline-none hover:bg-white/10 hover:text-white focus-visible:ring-2 focus-visible:ring-white/80",
        className,
      )}
      {...rest}
    >
      {children}
    </button>
  )
}

function RateMenu({ rate, onPick, onClose }: { rate: number; onPick: (r: number) => void; onClose: () => void }) {
  const box = useRef<HTMLDivElement>(null)
  useEffect(() => {
    box.current?.querySelector<HTMLButtonElement>("[aria-checked=true]")?.focus()
    const out = (e: PointerEvent) => {
      if (!box.current?.parentElement?.contains(e.target as Node)) onClose()
    }
    document.addEventListener("pointerdown", out)
    return () => document.removeEventListener("pointerdown", out)
  }, [onClose])
  const onKey = (e: React.KeyboardEvent) => {
    if (e.key !== "ArrowUp" && e.key !== "ArrowDown") return
    e.preventDefault()
    e.stopPropagation()
    const items = [...(box.current?.querySelectorAll<HTMLButtonElement>("button") ?? [])]
    const i = items.indexOf(document.activeElement as HTMLButtonElement)
    items[(i + (e.key === "ArrowDown" ? 1 : -1) + items.length) % items.length]?.focus()
  }
  return (
    <div
      ref={box}
      role="menu"
      aria-label="播放速度"
      onKeyDown={onKey}
      className="absolute bottom-full right-0 mb-2 w-24 overflow-hidden rounded-lg bg-black/85 py-1 text-[13px] shadow-xl ring-1 ring-white/10 backdrop-blur"
    >
      {[...RATES].reverse().map((r) => (
        <button
          key={r}
          type="button"
          role="menuitemradio"
          aria-checked={r === rate}
          onClick={() => onPick(r)}
          className={cn(
            "flex h-8 w-full items-center justify-center tabular-nums outline-none hover:bg-white/10 focus-visible:bg-white/15",
            r === rate ? "font-semibold text-primary" : "text-white/90",
          )}
        >
          {r === 1 ? "1.0x" : `${r}x`}
        </button>
      ))}
    </div>
  )
}

/** 进度条：轨道 → piece 可用性 → 已播放（绿）→ 拖动圆点；支持点击、拖动（含触屏）与悬停时间提示 */
function SeekBar({
  time,
  duration,
  segments,
  onSeek,
  onScrub,
}: {
  time: number
  duration: number
  segments: Uint8Array
  onSeek: (t: number) => void
  onScrub: (v: boolean) => void
}) {
  const track = useRef<HTMLDivElement>(null)
  const [drag, setDrag] = useState<number | null>(null)
  const [hover, setHover] = useState<number | null>(null)
  const ratioAt = (clientX: number) => {
    const r = track.current?.getBoundingClientRect()
    if (!r || !r.width) return 0
    return Math.max(0, Math.min(1, (clientX - r.left) / r.width))
  }
  const shownTime = drag ?? time
  const pct = duration > 0 ? Math.min(100, (shownTime / duration) * 100) : 0
  const tip = drag ?? hover

  return (
    <div
      ref={track}
      role="slider"
      tabIndex={0}
      aria-label="播放进度"
      aria-valuemin={0}
      aria-valuemax={Math.round(duration)}
      aria-valuenow={Math.round(shownTime)}
      aria-valuetext={`${fmt(shownTime)} / ${fmt(duration)}，已下载 ${Math.round(segmentsDone(segments) * 100)}%`}
      data-active={drag !== null || undefined}
      className="group/seek relative flex h-4 cursor-pointer touch-none items-center rounded-sm outline-none focus-visible:ring-2 focus-visible:ring-white/80 pointer-coarse:h-6"
      onKeyDown={(e) => {
        if (e.key === "Home") onSeek(0)
        else if (e.key === "End" && duration) onSeek(duration - 1)
        else return
        e.preventDefault()
      }}
      onPointerDown={(e) => {
        if (e.button !== 0 || !duration) return
        e.currentTarget.setPointerCapture(e.pointerId)
        setDrag(ratioAt(e.clientX) * duration)
        onScrub(true)
      }}
      onPointerMove={(e) => {
        if (!duration) return
        const t = ratioAt(e.clientX) * duration
        if (drag !== null) setDrag(t)
        else if (e.pointerType === "mouse") setHover(t)
      }}
      onPointerUp={(e) => {
        if (drag === null) return
        onSeek(ratioAt(e.clientX) * duration)
        setDrag(null)
        onScrub(false)
      }}
      onPointerCancel={() => {
        setDrag(null)
        onScrub(false)
      }}
      onPointerLeave={() => setHover(null)}
    >
      <div className="relative h-[3px] w-full overflow-hidden rounded-full bg-white/20 transition-[height] duration-150 group-hover/seek:h-[5px] group-data-active/seek:h-[5px]">
        <PieceLayer segments={segments} />
        {hover !== null && drag === null && duration > 0 && (
          <div className="absolute inset-y-0 left-0 bg-white/20" style={{ width: `${(hover / duration) * 100}%` }} />
        )}
        <div className="absolute inset-y-0 left-0 bg-primary" style={{ width: `${pct}%` }} />
      </div>
      <span
        aria-hidden
        className="pointer-events-none absolute top-1/2 h-3.5 w-3.5 -translate-x-1/2 -translate-y-1/2 scale-0 rounded-full bg-primary shadow-[0_0_0_3px_rgba(76,175,80,0.25)] transition-transform duration-150 group-hover/seek:scale-100 group-focus-visible/seek:scale-100 group-data-active/seek:scale-100 pointer-coarse:scale-100"
        style={{ left: `${pct}%` }}
      />
      {tip !== null && duration > 0 && (
        <span
          aria-hidden
          className="pointer-events-none absolute bottom-full mb-2 -translate-x-1/2 rounded bg-black/80 px-1.5 py-0.5 text-xs tabular-nums text-white shadow"
          style={{ left: `clamp(1.5rem, ${(tip / duration) * 100}%, calc(100% - 1.5rem))` }}
        >
          {fmt(tip)}
        </span>
      )}
    </div>
  )
}
