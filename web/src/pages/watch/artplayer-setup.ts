// ArtPlayer 的创建与站点定制。单独成块（ArtVideo 里动态 import，Player 挂载时预加载），
// 不阻塞播放页首屏；种子元数据就绪前通常已经加载完。
import Artplayer from "artplayer"
import { formatSpeed } from "@/lib/format"
import { segmentsGradient } from "@/lib/pieces"
import "./artplayer.css"
import { phoneControls, centerSender } from "@/lib/player-controls"

// ---- 全局配置（本站只有这一种播放器，静态属性直接改） ----
// 右键使用浏览器自带菜单（不加 ArtPlayer 的统计信息 / 翻转等菜单）
Artplayer.CONTEXTMENU = false
// 解码失败不自动重连：交给下方的编码提示（重试同一地址只会再次失败）；流卡死由 useStallWatchdog 处理
Artplayer.RECONNECT_TIME_MAX = 0
Artplayer.SEEK_STEP = 5
// 移动端左右滑动整个播放器宽度移动 1/12 视频时长（默认 0.5）
Artplayer.TOUCH_MOVE_RATIO = 1 / 12

const RATES = [2, 1.5, 1.25, 1, 0.75, 0.5]
const rateLabel = (r: number) => (r === 1 ? "倍速" : `${r}x`)
const bufferingText = (speed: number) => `缓冲中 · ${formatSpeed(speed)}`

const ICON_PLAY = `<svg viewBox="0 0 24 24" fill="currentColor" aria-hidden="true"><path d="M8 5.14v13.72a1 1 0 0 0 1.5.86l11.04-6.86a1 1 0 0 0 0-1.72L9.5 4.28A1 1 0 0 0 8 5.14z"/></svg>`
const ICON_SPIN = `<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.25" stroke-linecap="round" aria-hidden="true"><path d="M21 12a9 9 0 1 1-6.219-8.56"/></svg>`

type Writable<T> = { -readonly [K in keyof T]: T[K] }

export interface MountOptions {
  url: string
  poster?: string
  segments: Uint8Array
  speed: number
  onLoadedMetadata: (v: HTMLVideoElement) => void
  onMediaError: (err: MediaError) => void
  /** 控制栏「资源」按钮（倍速左边） */
  onPanel?: () => void
  onDanmaku: () => void
}

export interface SubtitleMenuItem {
  label: string
  /** 轨道序号；-1 = 关闭 */
  value: number
}

export interface ArtHandle {
  art: Artplayer
  video: HTMLVideoElement
  /** 控制栏「字幕」选择（「资源」左边）；items 为 null 时移除按钮 */
  setSubtitleMenu: (items: SubtitleMenuItem[] | null, current: number, onSelect: (value: number) => void) => void
  danmakuSlot: HTMLDivElement
  /** Sender is actually visible: device/fullscreen policy AND sufficient free width. */
  hasCenterSender: () => boolean
  onLayout: (cb: () => void) => () => void
  fullscreenHost: () => HTMLElement | null
  setPoster: (poster: string | undefined) => void
  setSegments: (segments: Uint8Array) => void
  /** 字节比例 → 时间比例（进度条是时间轴）；null = 按字节比例 */
  setTimeMap: (toTime: ((byteFrac: number) => number) | null) => void
  setSpeed: (speed: number) => void
  destroy: () => void
}

/**
 * 在 el（空的 div）里创建 ArtPlayer 并开始播放（autoplay 被浏览器拦截时显示大播放按钮）。
 * 返回的 destroy 会移除全部 DOM 与事件，可在 React effect 清理里直接调用（StrictMode 安全）。
 */
export function mountArtplayer(el: HTMLDivElement, o: MountOptions): ArtHandle {
  // Width follows the site's sm breakpoint; device identity remains phone in landscape.
  const phoneDevice = Artplayer.utils.isMobile || (navigator as Navigator & { userAgentData?: { mobile?: boolean } }).userAgentData?.mobile === true
  let mobile = phoneControls(window.innerWidth, phoneDevice)
  const art: Artplayer = new Artplayer({
    container: el,
    url: o.url,
    poster: o.poster ?? "",
    theme: "#4caf50",
    lang: "zh-cn",
    volume: 1,
    autoplay: false, // 由下面的 play() 发起，以便处理浏览器拦截
    playsInline: true,
    pip: true, // Construct once; phone layouts detach this control below.
    fullscreen: true,
    fullscreenWeb: false,
    setting: false,
    playbackRate: false,
    hotkey: true,
    mutex: true,
    backdrop: true,
    gesture: true,
    fastForward: true,
    moreVideoAttr: { preload: "metadata" },
    icons: {
      state: `<span class="ot-state">${ICON_PLAY}</span>`,
      loading: `<div class="ot-loading"><span class="ot-spin">${ICON_SPIN}</span><span class="ot-loading-text"></span></div>`,
    },
    controls: [
      { name: "danmaku", position: "right", index: 2, html: "弹幕", click: () => o.onDanmaku() },
      { name: "panel", position: "right", index: 4, html: "资源", click: () => o.onPanel?.() },
    ],
  })
  const video = art.video
  // Create once, then detach/restore the DOM at the sm breakpoint. Never re-register
  // built-in mounted handlers on resize (volume adds document listeners).
  art.controls.add({
    name: "rate",
    position: "right",
    index: 5,
    html: rateLabel(1),
    selector: RATES.map((r) => ({ html: r === 1 ? "1.0x" : `${r}x`, value: r, default: r === 1 })),
    onSelect(item) {
      art.playbackRate = Number(item.value)
      return rateLabel(Number(item.value))
    },
  })
  const desktopNodes = [art.query(".art-control-volume")!, art.query(".art-control-rate")!, art.query(".art-control-pip")!] as HTMLElement[]
  const setDeviceControls = () => {
    mobile = phoneControls(window.innerWidth, phoneDevice)
    art.template.$player.classList.toggle("ot-phone", mobile)
    for (const node of desktopNodes) {
      if (mobile) node.remove()
      else if (!node.isConnected) {
        const parent = node.classList.contains("art-control-volume") ? art.template.$controlsLeft : art.template.$controlsRight
        const next = [...parent.children].find((el) => Number((el as HTMLElement).dataset.index) > Number(node.dataset.index))
        parent.insertBefore(node, next ?? null)
      }
    }
  }
  setDeviceControls()
  // 空 url：还没有视频（获取种子信息等）时先显示播放器与控制栏（「资源」按钮可用），不加载、不播放；
  // 画面由外层的封面与状态提示负责（.ot-idle 让播放器背景透明、隐藏大按钮与加载动画）
  const idle = !o.url
  if (idle) el.classList.add("ot-idle")
  let lastPoster = o.poster
  const { $player, $state, $poster } = art.template
  $state.setAttribute("role", "button")
  $state.setAttribute("aria-label", "播放")

  // 进度条里的 piece 可用性图层：位于「已缓冲 / 已播放」之下
  const pieces = document.createElement("div")
  pieces.className = "ot-progress-pieces"
  art.query(".art-control-progress-inner")?.prepend(pieces)
  let lastSegments = o.segments
  let toTime: ((byteFrac: number) => number) | null = null
  const setSegments = (s: Uint8Array) => {
    lastSegments = s
    pieces.style.backgroundImage = segmentsGradient(s, toTime ?? undefined)
  }
  setSegments(o.segments)
  const speedText = art.query(".ot-loading-text")
  const setSpeed = (speed: number) => {
    if (speedText) speedText.textContent = bufferingText(speed)
  }
  setSpeed(o.speed)

  // 额外快捷键（ArtPlayer 自带 空格 / ←→ 5 秒 / ↑↓ 音量），桌面端生效
  art.hotkey.add("KeyF", () => {
    art.fullscreen = !art.fullscreen
  })
  art.hotkey.add("KeyM", () => {
    art.muted = !art.muted
  })
  art.hotkey.add("KeyK", () => {
    if (video.paused || video.ended) art.play().catch(() => {})
    else art.pause()
  })

  // ---- 大播放按钮（mask）/ 加载动画（loading）----
  // ArtPlayer 在 loadstart / loadedmetadata（移动端）等事件里切换二者，但不知道自动播放被拦截：
  // loadstart 会显示 loading，而 .art-loading-show 会隐藏 mask → 按钮消失。这里在它的处理之后校正
  // （同名事件按注册顺序执行，我们的晚注册）。
  const showPauseIcon = (pause: boolean) => {
    const [iPlay, iPause] = art.query(".art-control-playAndPause")?.querySelectorAll<HTMLElement>(".art-icon") ?? []
    if (!iPlay || !iPause) return
    iPlay.style.display = pause ? "none" : "flex"
    iPause.style.display = pause ? "flex" : "none"
  }
  let needsTap = false
  const reconcile = () => {
    if (art.isDestroy) return
    if (idle) {
      art.loading.show = false
      art.mask.show = false
      return
    }
    if (needsTap) {
      art.loading.show = false
      art.mask.show = true
      return
    }
    if (!video.paused && !video.ended && video.readyState < HTMLMediaElement.HAVE_FUTURE_DATA) {
      // 已在播放（含刚点了播放按钮）但还没有数据：隐藏按钮，显示加载 + 速度
      art.mask.show = false
      art.loading.show = true
    } else if (video.paused && !video.seeking) {
      art.loading.show = false
    }
  }
  // 手机全屏时锁定横屏（横向视频）；退出全屏时解除。不支持锁定的浏览器（如 iOS Safari 用系统播放器全屏，
  // 本身会随设备旋转）静默忽略。ArtPlayer 自带的 autoOrientation 失败时会把异常显示在画面上，所以自己处理。
  let locked = false
  type LockableOrientation = ScreenOrientation & { lock?: (o: "landscape") => Promise<void> }
  art.on("fullscreen", (on: boolean) => {
    const so = screen.orientation as LockableOrientation | undefined
    if (!mobile || !so?.lock) return
    if (on) {
      if (video.videoWidth > 0 && video.videoWidth < video.videoHeight) return // 竖屏视频不旋转
      so.lock("landscape").then(
        () => (locked = true),
        () => {},
      )
    } else if (locked) {
      locked = false
      try {
        so.unlock()
      } catch {
        /* ignore */
      }
    }
  })
  art.on("video:play", () => {
    // 点击按钮后 play 事件立即触发（play() 的 Promise 要等到有数据才 resolve）
    needsTap = false
    reconcile()
    // ArtPlayer 在 play 时就隐藏封面；流还没有画面时保留封面（与原生 poster 一致），出画面后再隐藏
    if (video.readyState < HTMLMediaElement.HAVE_CURRENT_DATA) $poster.style.display = ""
    // 控件栏的播放/暂停按钮：ArtPlayer 要等 playing 才切到「暂停」，等数据时也应能暂停
    showPauseIcon(true)
  })
  art.on("video:loadeddata", () => {
    if (!video.paused) $poster.style.display = "none"
  })
  art.on("video:playing", () => {
    $poster.style.display = "none"
  })
  for (const e of ["loadstart", "loadedmetadata", "waiting", "pause", "seeked", "emptied"]) art.on(`video:${e}`, reconcile)

  art.on("video:loadedmetadata", () => o.onLoadedMetadata(video))
  art.on("video:error", () => {
    if (idle) return
    // 无法解码：大按钮换成错误图标（.art-error，见 artplayer.css），控件栏回到「播放」
    showPauseIcon(false)
    if (video.error) o.onMediaError(video.error)
  })

  // 键盘：Tab 聚焦到播放器时也启用快捷键（ArtPlayer 只在点击播放器后启用）
  const focusIn = () => {
    ;(art as Writable<Artplayer>).isFocus = true
  }
  // 焦点移到页面其他元素（Tab）时停用；relatedTarget 为空（窗口失焦、进入全屏、点击空白处）交给 ArtPlayer 自己的点击判断
  const focusOut = (e: FocusEvent) => {
    const to = e.relatedTarget as Node | null
    if (to && !el.contains(to) && !$player.contains(to)) (art as Writable<Artplayer>).isFocus = false
  }
  el.addEventListener("focusin", focusIn)
  el.addEventListener("focusout", focusOut)

  // 自动播放：被拦截（NotAllowedError）时显式显示大播放按钮
  if (!idle)
    art.play().catch((e: unknown) => {
      if (art.isDestroy || (e as Error)?.name !== "NotAllowedError") return
      needsTap = true
      reconcile()
    })

  // ---- 弹幕：控制栏「弹幕」按钮与中间的发送框容器 ----
  const danmakuSlot = document.createElement("div")
  danmakuSlot.className = "ot-danmaku-slot"
  art.template.$controlsCenter.appendChild(danmakuSlot)
  // 发送框里打字时控制栏不自动隐藏（ArtPlayer 只在点击输入框时设 isInput，键盘 Tab 进来也要算）
  danmakuSlot.addEventListener("focusin", (e) => {
    if ((e.target as HTMLElement).tagName === "INPUT") (art as Writable<Artplayer>).isInput = true
  })
  danmakuSlot.addEventListener("focusout", () => {
    ;(art as Writable<Artplayer>).isInput = false
  })
  // 弹窗（「弹幕」/「资源」，全屏时挂在播放器里）中按键不触发播放器快捷键：ArtPlayer 只在焦点在播放器上
  // （isFocus）时处理，点击弹窗也算点击播放器，所以按键时（捕获阶段，先于 ArtPlayer）把 isFocus 取消。
  // 否则在弹窗里按空格会暂停
  const guardKeys = (e: KeyboardEvent) => {
    if ((e.target as Element | null)?.closest?.('[role="dialog"]')) (art as Writable<Artplayer>).isFocus = false
  }
  window.addEventListener("keydown", guardKeys, true)
  const layoutListeners = new Set<() => void>()
  let sender = false
  const isFullscreen = () => art.fullscreen || document.fullscreenElement === $player
  const updateLayout = () => {
    if (art.isDestroy) return
    setDeviceControls()
    const left = art.template.$controlsLeft.getBoundingClientRect().width
    const right = art.template.$controlsRight.getBoundingClientRect().width
    const free = art.template.$controls.getBoundingClientRect().width - left - right - 24
    const on = centerSender(isFullscreen(), mobile, $player.clientWidth, $player.clientHeight, free)
    sender = on
    $player.classList.toggle("ot-center-send", on)
    for (const cb of layoutListeners) cb()
  }
  // Browser fullscreen, viewport resize and actual controls widths (subtitles arriving,
  // duration/time labels) all affect the free space; no guessed aspect-only visibility.
  const relayout = () => {
    updateLayout()
    requestAnimationFrame(updateLayout)
  }
  art.on("fullscreen", relayout)
  art.on("resize", relayout)
  window.addEventListener("resize", relayout)
  document.addEventListener("fullscreenchange", relayout)
  screen.orientation?.addEventListener?.("change", relayout)
  const layoutObserver = new ResizeObserver(relayout)
  for (const el of [art.template.$controls, art.template.$controlsLeft, art.template.$controlsRight]) layoutObserver.observe(el)
  relayout()

  return {
    art,
    video,
    danmakuSlot,
    hasCenterSender: () => sender,
    onLayout(cb) {
      layoutListeners.add(cb)
      return () => layoutListeners.delete(cb)
    },
    fullscreenHost: () => isFullscreen() ? $player : null,
    setPoster(poster) {
      if (poster === undefined || poster === lastPoster) return
      lastPoster = poster
      art.poster = poster
    },
    setSegments,
    setTimeMap(fn) {
      toTime = fn
      setSegments(lastSegments)
    },
    setSpeed,
    setSubtitleMenu(items, current, onSelect) {
      if (art.isDestroy) return
      if (art.controls["subtitle" as keyof typeof art.controls]) art.controls.remove("subtitle")
      if (!items?.length) return
      const all = [...items, { label: "关闭", value: -1 }]
      art.controls.add({
        name: "subtitle",
        position: "right",
        index: 3, // 「资源」是 4、「倍速」是 5
        html: "字幕",
        selector: all.map((i) => ({ html: escapeHtml(i.label), value: i.value, default: i.value === current })),
        onSelect(item) {
          onSelect(Number(item.value))
          return "字幕"
        },
      })
    },
    destroy() {
      layoutListeners.clear()
      layoutObserver.disconnect()
      window.removeEventListener("keydown", guardKeys, true)
      window.removeEventListener("resize", relayout)
      document.removeEventListener("fullscreenchange", relayout)
      screen.orientation?.removeEventListener?.("change", relayout)
      el.removeEventListener("focusin", focusIn)
      el.removeEventListener("focusout", focusOut)
      if (document.fullscreenElement && $player.contains(document.fullscreenElement)) document.exitFullscreen().catch(() => {})
      art.destroy(true) // 同时移除 src，释放 Service Worker 流请求
    },
  }
}

function escapeHtml(s: string) {
  return s.replace(/[&<>"']/g, (c) => `&#${c.charCodeAt(0)};`)
}
