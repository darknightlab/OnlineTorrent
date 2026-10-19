// ArtPlayer 的创建与站点定制。单独成块（ArtVideo 里动态 import，Player 挂载时预加载），
// 不阻塞播放页首屏；种子元数据就绪前通常已经加载完。
import Artplayer from "artplayer"
import { formatSpeed } from "@/lib/format"
import { segmentsGradient } from "@/lib/pieces"
import "./artplayer.css"

// ---- 全局配置（本站只有这一种播放器，静态属性直接改） ----
// 右键使用浏览器自带菜单（不加 ArtPlayer 的统计信息 / 翻转等菜单）
Artplayer.CONTEXTMENU = false
// 解码失败不自动重连：交给下方的编码提示（重试同一地址只会再次失败）；流卡死由 useStallWatchdog 处理
Artplayer.RECONNECT_TIME_MAX = 0
// 网页全屏时把播放器移到 <body>，避免被吸顶容器的层叠上下文盖住
Artplayer.FULLSCREEN_WEB_IN_BODY = true
Artplayer.SEEK_STEP = 5

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
}

export interface ArtHandle {
  art: Artplayer
  video: HTMLVideoElement
  setPoster: (poster: string | undefined) => void
  setSegments: (segments: Uint8Array) => void
  setSpeed: (speed: number) => void
  destroy: () => void
}

/**
 * 在 el（空的 div）里创建 ArtPlayer 并开始播放（autoplay 被浏览器拦截时显示大播放按钮）。
 * 返回的 destroy 会移除全部 DOM 与事件，可在 React effect 清理里直接调用（StrictMode 安全）。
 */
export function mountArtplayer(el: HTMLDivElement, o: MountOptions): ArtHandle {
  const mobile = Artplayer.utils.isMobile
  const art: Artplayer = new Artplayer({
    container: el,
    url: o.url,
    poster: o.poster ?? "",
    theme: "#4caf50",
    lang: "zh-cn",
    volume: 1,
    autoplay: false, // 由下面的 play() 发起，以便处理浏览器拦截
    playsInline: true,
    pip: true,
    fullscreen: true,
    fullscreenWeb: !mobile,
    setting: false,
    playbackRate: false,
    hotkey: true,
    mutex: true,
    backdrop: true,
    gesture: true,
    moreVideoAttr: { preload: "metadata" },
    icons: {
      state: `<span class="ot-state">${ICON_PLAY}</span>`,
      loading: `<div class="ot-loading"><span class="ot-spin">${ICON_SPIN}</span><span class="ot-loading-text"></span></div>`,
    },
    controls: [
      {
        name: "rate",
        position: "right",
        index: 5,
        html: rateLabel(1),
        tooltip: "播放速度",
        selector: RATES.map((r) => ({ html: r === 1 ? "1.0x" : `${r}x`, value: r, default: r === 1 })),
        onSelect(item) {
          art.playbackRate = Number(item.value)
          return rateLabel(Number(item.value))
        },
      },
    ],
  })
  const video = art.video
  let lastPoster = o.poster
  const { $player, $state, $poster } = art.template
  $state.setAttribute("role", "button")
  $state.setAttribute("aria-label", "播放")

  // 进度条里的 piece 可用性图层：位于「已缓冲 / 已播放」之下
  const pieces = document.createElement("div")
  pieces.className = "ot-progress-pieces"
  art.query(".art-control-progress-inner")?.prepend(pieces)
  const setSegments = (s: Uint8Array) => {
    pieces.style.backgroundImage = segmentsGradient(s)
  }
  setSegments(o.segments)
  const speedText = art.query(".ot-loading-text")
  const setSpeed = (speed: number) => {
    if (speedText) speedText.textContent = bufferingText(speed)
  }
  setSpeed(o.speed)

  // 额外快捷键（ArtPlayer 自带 空格 / ←→ 5 秒 / ↑↓ 音量 / Esc 退出网页全屏），桌面端生效
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
  art.play().catch((e: unknown) => {
    if (art.isDestroy || (e as Error)?.name !== "NotAllowedError") return
    needsTap = true
    reconcile()
  })

  return {
    art,
    video,
    setPoster(poster) {
      if (poster === undefined || poster === lastPoster) return
      lastPoster = poster
      art.poster = poster
    },
    setSegments,
    setSpeed,
    destroy() {
      el.removeEventListener("focusin", focusIn)
      el.removeEventListener("focusout", focusOut)
      // 网页全屏时播放器在 <body> 里：先移回容器再销毁
      if (art.fullscreenWeb) art.fullscreenWeb = false
      if (document.fullscreenElement && $player.contains(document.fullscreenElement)) document.exitFullscreen().catch(() => {})
      art.destroy(true) // 同时移除 src，释放 Service Worker 流请求
    },
  }
}
