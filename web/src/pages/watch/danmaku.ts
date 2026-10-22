// 原生零回流弹幕渲染器（Web Animations API + 离屏 Canvas 测量 + 二分时间游标）。
// 单独成块：由 DanmakuControls 动态加载。弹幕显示在 ArtPlayer 的 .art-danmuku 层（z 30）：
// 视频之上，内封字幕（z 35）与控制栏之下。
import type Artplayer from "artplayer"
import type { DanmakuItem } from "@/lib/types"
import {
  DEFAULT_DANMAKU_SETTINGS,
  LaneAllocator,
  lowerBoundDanmu,
  pluginOptions,
  toDanmu,
  type DanmakuArea,
  type DanmakuSettings,
  type ParsedDanmu,
} from "@/lib/danmaku"

const DANMU_FONT_FAMILY = 'SimHei, "Microsoft JhengHei", Arial, Helvetica, sans-serif'
const DANMU_BASE_CSS = [
  "user-select:none",
  "position:absolute",
  "top:0",
  "left:0",
  "white-space:pre",
  "pointer-events:none",
  "display:inline-block",
  "will-change:transform",
  "font-weight:normal",
  "line-height:1.125",
  `font-family:${DANMU_FONT_FAMILY}`,
  "text-shadow:rgb(0,0,0) 1px 0 1px,rgb(0,0,0) 0 1px 1px,rgb(0,0,0) 0 -1px 1px,rgb(0,0,0) -1px 0 1px",
].join(";")

const STATIC_DURATION = 5 // 顶部/底部弹幕显示秒数
const WINDOW_BEHIND = 0.3 // 发射时间窗口下限（秒）
const SEEK_JUMP = 0.6 // 判定为跳转/拖动的时间突变阈值（秒）

let measureCtx: CanvasRenderingContext2D | null = null
let lastFontSize = 0
const widthCache = new Map<string, number>()

function measureWidth(text: string, fontSize: number, bordered: boolean): number {
  const key = `${fontSize}:${bordered ? 1 : 0}:${text}`
  const hit = widthCache.get(key)
  if (hit !== undefined) return hit
  if (!measureCtx && typeof document !== "undefined") {
    measureCtx = document.createElement("canvas").getContext("2d")
  }
  let w = text.length * fontSize
  if (measureCtx) {
    if (lastFontSize !== fontSize) {
      measureCtx.font = `normal ${fontSize}px ${DANMU_FONT_FAMILY}`
      lastFontSize = fontSize
    }
    w = Math.ceil(measureCtx.measureText(text).width)
  }
  if (bordered) w += 6
  if (widthCache.size > 8000) widthCache.clear()
  widthCache.set(key, w)
  return w
}

interface ActiveItem {
  el: HTMLDivElement
  anim: Animation
}

class NativeDanmakuLayer {
  private readonly art: Artplayer
  private readonly $danmuku: HTMLElement
  private readonly $player: HTMLElement
  private readonly lanes = new LaneAllocator()
  private readonly pool: HTMLDivElement[] = []
  private readonly active = new Set<ActiveItem>()
  private readonly pendingImmediate: ParsedDanmu[] = []
  private items: ParsedDanmu[] = []
  private cursor = 0
  private lastVideoTime = -1
  private rafId = 0
  private ro: ResizeObserver | null = null
  private clientWidth = 0
  private clientHeight = 0
  private settings: DanmakuSettings = DEFAULT_DANMAKU_SETTINGS
  private fontSize = 24
  private speed = 5
  private area: DanmakuArea = 0.75

  constructor(art: Artplayer, items: DanmakuItem[], settings: DanmakuSettings) {
    this.art = art
    this.$danmuku = art.template.$danmuku
    this.$player = art.template.$player
    this.readDimensions()
    this.applySettings(settings, true)
    this.setItems(items)

    if (typeof ResizeObserver !== "undefined") {
      this.ro = new ResizeObserver((entries) => {
        for (const entry of entries) {
          const cr = entry.contentRect
          if (cr.width > 0 && cr.height > 0) {
            this.clientWidth = Math.round(cr.width)
            this.clientHeight = Math.round(cr.height)
            this.lanes.configure(this.clientHeight, this.fontSize, this.area)
          }
        }
      })
      this.ro.observe(this.$player)
    }

    art.on("resize", this.onResize)
    art.on("video:play", this.onPlay)
    art.on("video:playing", this.onPlay)
    art.on("video:pause", this.onPause)
    art.on("video:waiting", this.onPause)
    art.on("video:seeked", this.onSeeked)
    art.on("video:ratechange", this.onRateChange)
    art.on("destroy", this.destroy)

    if (art.playing) this.startLoop()
  }

  private isMobile(): boolean {
    return (this.art.constructor as typeof Artplayer).utils.isMobile
  }

  private readDimensions() {
    this.clientWidth = this.$player.clientWidth
    this.clientHeight = this.$player.clientHeight
  }

  private readonly onResize = () => {
    this.readDimensions()
    this.lanes.configure(this.clientHeight, this.fontSize, this.area)
  }

  private readonly onPlay = () => {
    for (const item of this.active) item.anim.play()
    this.startLoop()
  }

  private readonly onPause = () => {
    this.stopLoop()
    for (const item of this.active) item.anim.pause()
  }

  private readonly onSeeked = () => {
    this.clearActive()
    this.lanes.reset()
    const now = this.art.currentTime
    this.lastVideoTime = now
    this.cursor = lowerBoundDanmu(this.items, now)
  }

  private readonly onRateChange = () => {
    const rate = this.art.playbackRate || 1
    for (const item of this.active) {
      if (typeof item.anim.updatePlaybackRate === "function") item.anim.updatePlaybackRate(rate)
      else item.anim.playbackRate = rate
    }
  }

  private startLoop() {
    if (!this.rafId && !this.art.isDestroy) {
      this.rafId = window.requestAnimationFrame(this.tick)
    }
  }

  private stopLoop() {
    if (this.rafId) {
      window.cancelAnimationFrame(this.rafId)
      this.rafId = 0
    }
  }

  private readonly tick = () => {
    this.rafId = 0
    if (this.art.isDestroy || !this.art.playing) return

    const now = this.art.currentTime
    if (this.lastVideoTime < 0 || now < this.lastVideoTime - 0.05 || now - this.lastVideoTime > SEEK_JUMP) {
      this.cursor = lowerBoundDanmu(this.items, now)
    }
    this.lastVideoTime = now

    if (this.settings.visible && this.clientWidth > 0 && this.clientHeight > 0) {
      while (this.pendingImmediate.length > 0) {
        const d = this.pendingImmediate.shift()!
        this.launch(d, now, true)
      }
      while (this.cursor < this.items.length) {
        const d = this.items[this.cursor]
        if (d.time > now) break
        this.cursor++
        if (d.time >= now - WINDOW_BEHIND) {
          this.launch(d, now, false)
        }
      }
    } else {
      // 隐藏状态下仍正常推进时间游标，避免重新打开时爆发式堆积
      while (this.cursor < this.items.length && this.items[this.cursor].time <= now) {
        this.cursor++
      }
    }

    if (this.art.playing && !this.art.isDestroy) {
      this.rafId = window.requestAnimationFrame(this.tick)
    }
  }

  private launch(d: ParsedDanmu, videoNow: number, forceShow: boolean) {
    const W = this.clientWidth
    const w = measureWidth(d.text, this.fontSize, !!d.border)
    const dur = d.mode === 0 ? this.speed : STATIC_DURATION
    let top = this.lanes.allocate(d.mode, videoNow, w, W, dur)
    if (top === null) {
      if (!forceShow) return
      top = 10
    }

    const el = this.pool.pop() ?? document.createElement("div")
    el.style.cssText = DANMU_BASE_CSS
    el.textContent = d.text
    el.style.fontSize = `${this.fontSize}px`
    el.style.color = d.color
    el.style.top = `${top}px`
    el.dataset.mode = String(d.mode)
    if (d.border) {
      el.style.border = `1px solid ${d.color}`
      el.style.backgroundColor = "rgb(0 0 0 / 50%)"
      el.style.padding = "0 2px"
    }

    let keyframes: Keyframe[]
    if (d.mode === 0) {
      el.style.left = `${W}px`
      const dist = W + w
      keyframes = [
        { transform: "translate3d(0, 0, 0)" },
        { transform: `translate3d(${-dist}px, 0, 0)` },
      ]
    } else {
      el.style.left = "50%"
      keyframes = [
        { transform: "translate3d(-50%, 0, 0)" },
        { transform: "translate3d(-50%, 0, 0)" },
      ]
    }

    if (!el.isConnected) this.$danmuku.appendChild(el)

    const anim = el.animate(keyframes, {
      duration: dur * 1000,
      easing: "linear",
    })
    const rate = this.art.playbackRate || 1
    if (rate !== 1) {
      if (typeof anim.updatePlaybackRate === "function") anim.updatePlaybackRate(rate)
      else anim.playbackRate = rate
    }
    if (!this.art.playing) anim.pause()

    const activeItem: ActiveItem = { el, anim }
    this.active.add(activeItem)
    anim.onfinish = () => this.recycle(activeItem)
  }

  private recycle(item: ActiveItem) {
    if (!this.active.delete(item)) return
    item.el.remove()
    if (this.pool.length < 120) this.pool.push(item.el)
  }

  private clearActive() {
    for (const item of this.active) {
      item.anim.onfinish = null
      item.anim.cancel()
      item.el.remove()
      if (this.pool.length < 120) this.pool.push(item.el)
    }
    this.active.clear()
  }

  setItems(raw: DanmakuItem[]) {
    this.clearActive()
    this.pendingImmediate.length = 0
    this.lanes.reset()
    this.items = toDanmu(raw)
    const now = this.art.currentTime
    this.lastVideoTime = now
    this.cursor = lowerBoundDanmu(this.items, now)
  }

  appendItems(raw: DanmakuItem[]) {
    if (!raw.length) return
    const added = toDanmu(raw)
    if (!added.length) return
    if (!this.items.length) {
      this.items = added
    } else {
      this.items = [...this.items, ...added].sort((a, b) => a.time - b.time)
    }
    const cutoff = this.lastVideoTime >= 0 ? this.lastVideoTime : this.art.currentTime
    let c = lowerBoundDanmu(this.items, cutoff)
    while (c < this.items.length && this.items[c].time <= cutoff && this.lastVideoTime >= 0) c++
    this.cursor = c
  }

  emitOwn(raw: DanmakuItem) {
    const [d] = toDanmu([raw])
    if (!d) return
    const now = this.art.currentTime
    const own: ParsedDanmu = { ...d, time: now, border: true }
    // 放入时间轴以便回看时也能看到
    const idx = lowerBoundDanmu(this.items, own.time)
    this.items.splice(idx, 0, own)
    if (idx <= this.cursor) this.cursor++

    if (this.art.playing && this.settings.visible && this.clientWidth > 0 && this.clientHeight > 0) {
      this.launch(own, now, true)
    } else {
      this.pendingImmediate.push(own)
    }
  }

  applySettings(next: DanmakuSettings, isInit = false) {
    const opts = pluginOptions(next, this.isMobile())
    const layoutChanged =
      !isInit && (opts.fontSize !== this.fontSize || opts.speed !== this.speed || next.area !== this.area)

    this.settings = next
    this.fontSize = opts.fontSize
    this.speed = opts.speed
    this.area = next.area

    this.$danmuku.style.opacity = next.visible ? String(opts.opacity) : "0"
    this.$danmuku.style.visibility = next.visible ? "visible" : "hidden"
    this.lanes.configure(this.clientHeight, this.fontSize, this.area)

    if (layoutChanged) {
      for (const item of this.active) {
        item.el.style.fontSize = `${this.fontSize}px`
      }
    }
  }

  clear() {
    this.clearActive()
    this.pendingImmediate.length = 0
    this.items = []
    this.cursor = 0
    this.lanes.reset()
  }

  readonly destroy = () => {
    this.stopLoop()
    this.clearActive()
    this.pool.length = 0
    this.ro?.disconnect()
    this.ro = null
    this.art.off("resize", this.onResize)
    this.art.off("video:play", this.onPlay)
    this.art.off("video:playing", this.onPlay)
    this.art.off("video:pause", this.onPause)
    this.art.off("video:waiting", this.onPause)
    this.art.off("video:seeked", this.onSeeked)
    this.art.off("video:ratechange", this.onRateChange)
    this.art.off("destroy", this.destroy)
  }
}

const layers = new WeakMap<Artplayer, NativeDanmakuLayer>()

/** 显示一集的弹幕（items 为空 = 清空，弹幕层仍在，可以发送） */
export function showDanmaku(art: Artplayer, items: DanmakuItem[], settings: DanmakuSettings) {
  let layer = layers.get(art)
  if (!layer) {
    layer = new NativeDanmakuLayer(art, items, settings)
    layers.set(art, layer)
    return
  }
  layer.setItems(items)
  layer.applySettings(settings)
}

/** 采集/用户弹幕快照到达后追加，不打断正在飞行的弹幕 */
export function appendDanmaku(art: Artplayer, items: DanmakuItem[]) {
  if (!art.isDestroy) layers.get(art)?.appendItems(items)
}

/** 显示设置（开关、不透明度、字号、速度、区域）立即生效 */
export function applyDanmakuSettings(art: Artplayer, settings: DanmakuSettings) {
  if (!art.isDestroy) layers.get(art)?.applySettings(settings)
}

/** 自己刚发送的弹幕：在当前位置立即出现（带边框；暂停时在继续播放后出现） */
export function emitDanmaku(art: Artplayer, item: DanmakuItem) {
  if (!art.isDestroy) layers.get(art)?.emitOwn(item)
}

/** 清空弹幕（换集 / 离开单集页）；没有弹幕层时什么也不做 */
export function clearDanmaku(art: Artplayer) {
  if (!art.isDestroy) layers.get(art)?.clear()
}
