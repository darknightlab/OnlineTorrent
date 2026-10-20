// ASS 渲染：JASSUB（libass 的 WASM 版，OffscreenCanvas + WebGL，在 Worker 里渲染）。
// 本模块与 jassub 只在文件有内封字幕时才加载（动态 import）。没有 SharedArrayBuffer（站点不设置
// COOP/COEP）时 JASSUB 自动用单线程模式。
//
// 画布由我们创建、放在播放器元素里（<video> 后面），随播放器裁剪、全屏 / 网页全屏；
// 生命周期：组件卸载时 destroy；另外每秒自检，<video> 不在文档里了（任何原因）就自行销毁——
// 字幕绝不能残留在其他页面上。
import JASSUB from "jassub"
import workerUrl from "jassub/dist/worker/worker.js?worker&url"
import wasmUrl from "jassub/dist/wasm/jassub-worker.wasm?url"
import modernWasmUrl from "jassub/dist/wasm/jassub-worker-modern.wasm?url"
import defaultFontUrl from "jassub/dist/default.woff2?url"
import { srtHeader, type Cue, type SubtitleSource } from "./source.ts"
import type { SubTrack } from "./mkv.ts"

export class SubtitleRenderer {
  private j: JASSUB
  private src: SubtitleSource
  private track: number | null = null
  private unsub: (() => void) | null = null
  private fontsLoaded = false
  private destroyed = false
  /** 换轨道时作废旧的异步操作 */
  private gen = 0

  private canvas: HTMLCanvasElement
  private watchdog = 0

  /** player：播放器元素（画布放在里面）；video 必须在它里面 */
  constructor(video: HTMLVideoElement, player: HTMLElement, src: SubtitleSource, mobile: boolean) {
    this.src = src
    const canvas = document.createElement("canvas")
    canvas.className = "ot-subtitles"
    canvas.style.position = "absolute"
    if (video.parentElement && player.contains(video)) video.insertAdjacentElement("afterend", canvas)
    else player.appendChild(canvas)
    this.canvas = canvas
    this.j = new JASSUB({
      video,
      canvas,
      subContent: srtHeader(),
      workerUrl,
      wasmUrl,
      modernWasmUrl,
      availableFonts: { "liberation sans": defaultFontUrl },
      defaultFont: "liberation sans",
      // 只用已授权的本地字体（不会弹权限请求）；不从 Google Fonts 下载
      queryFonts: "local",
      // 手机：限制渲染分辨率
      maxRenderHeight: mobile ? 720 : 0,
    })
    this.watchdog = window.setInterval(() => {
      if (!video.isConnected || !canvas.isConnected) this.destroy()
    }, 1000)
  }

  /** 显示某个轨道（null = 关闭） */
  async show(track: SubTrack | null) {
    const gen = ++this.gen
    this.unsub?.()
    this.unsub = null
    this.track = track?.number ?? null
    await this.j.ready
    if (this.destroyed || gen !== this.gen) return
    this.canvas.style.display = track ? "" : "none"
    if (!track) {
      await this.j.renderer.freeTrack()
      return
    }
    if (!this.fontsLoaded) {
      this.fontsLoaded = true
      // 字体附件（MKV attachments）：只在第一次显示字幕时读取
      void this.src.fonts().then((fonts) => {
        if (this.destroyed) return
        console.info(`[player] 字幕字体 ${fonts.length} 个（MKV 附件）`)
        if (fonts.length) void this.j.renderer.addFonts(fonts)
      })
    }
    await this.j.renderer.setTrack(track.codec === "srt" || !track.header.trim() ? srtHeader() : track.header)
    if (this.destroyed || gen !== this.gen) return
    const add = (c: Cue) => this.j.renderer.processChunk(c.text, c.start, c.duration)
    // 先订阅再取已有的（去重由 source 保证；processChunk 对同一 ReadOrder 也会去重）
    this.unsub = this.src.onCue((tr, c) => {
      if (tr === this.track) void add(c)
    })
    for (const c of this.src.cuesOf(track.number)) void add(c)
  }

  destroy() {
    if (this.destroyed) return
    this.destroyed = true
    window.clearInterval(this.watchdog)
    this.unsub?.()
    this.canvas.remove()
    void this.j.destroy().catch(() => {})
  }
}
