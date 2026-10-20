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
// 默认字体（字幕要的字体都不在时使用）：Noto Sans CJK SC 子集（简繁日韩常用字 + 拉丁，2.7 MB，见 fonts/），
// 只在 libass 真的需要默认字体时下载。JASSUB 自带的 default.woff2 没有中文，外挂字幕的字体
// （如「方正准圆_GBK」，字幕组常把字体打包成 .rar / .7z）不在时会显示成方框
import defaultFontUrl from "./fonts/NotoSansCJKsc-Medium-subset.woff2?url"
import { srtHeader, type Cue } from "./source.ts"
import type { MenuTrack, SubtitleSet } from "./set.ts"

export class SubtitleRenderer {
  private j: JASSUB
  private set: SubtitleSet
  private video: HTMLVideoElement
  private track: number | null = null
  private unsub: (() => void) | null = null
  private fontsLoaded = false
  private destroyed = false
  /** 换轨道时作废旧的异步操作 */
  private gen = 0

  private canvas: HTMLCanvasElement
  private watchdog = 0

  /** player：播放器元素（画布放在里面）；video 必须在它里面 */
  constructor(video: HTMLVideoElement, player: HTMLElement, set: SubtitleSet, mobile: boolean) {
    this.set = set
    this.video = video
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
      availableFonts: { "noto sans cjk sc": defaultFontUrl },
      defaultFont: "noto sans cjk sc",
      // 只用已授权的本地字体（不会弹权限请求）；不从 Google Fonts 下载
      queryFonts: "local",
      // 手机：限制渲染分辨率
      maxRenderHeight: mobile ? 720 : 0,
    })
    this.watchdog = window.setInterval(() => {
      if (!video.isConnected || !canvas.isConnected) this.destroy()
    }, 1000)
  }

  /** 视频已出画面（外挂字幕 / 字体在此之后才下载，不与起播抢带宽） */
  private started() {
    const v = this.video
    if (v.readyState >= HTMLMediaElement.HAVE_CURRENT_DATA) return Promise.resolve()
    return new Promise<void>((r) => {
      v.addEventListener("loadeddata", () => r(), { once: true })
      setTimeout(r, 15_000) // 迟迟不出画面也照样加载
    })
  }

  private loadFonts() {
    if (this.fontsLoaded) return
    this.fontsLoaded = true
    // 字体：MKV 附件 + 种子的 Fonts/ 文件夹，只在第一次显示字幕时读取
    void this.started()
      .then(() => this.set.fonts())
      .then(({ fonts, attachments, files }) => {
        if (this.destroyed) return
        console.info(`[player] 字幕字体 ${fonts.length} 个（MKV 附件 ${attachments} 个，种子字体文件 ${files} 个）`)
        if (fonts.length) void this.j.renderer.addFonts(fonts)
      })
  }

  /** 显示某个轨道（null = 关闭） */
  async show(track: MenuTrack | null) {
    const gen = ++this.gen
    this.unsub?.()
    this.unsub = null
    this.track = track?.kind === "embedded" ? track.track.number : null
    await this.j.ready
    if (this.destroyed || gen !== this.gen) return
    this.canvas.style.display = track ? "" : "none"
    if (!track) {
      await this.j.renderer.freeTrack()
      this.repaint()
      return
    }
    this.loadFonts()
    if (track.kind === "external") {
      // 整个文件交给 libass 解析（ASS 原样；SRT / VTT 已转换）
      await this.started()
      if (this.destroyed || gen !== this.gen) return
      let text: string
      try {
        text = await track.ext.load()
      } catch (e) {
        console.warn(`[player] 外挂字幕读取失败：${String((e as Error)?.message ?? e)}`)
        return
      }
      if (this.destroyed || gen !== this.gen) return
      await this.j.renderer.setTrack(text)
      this.repaint()
      return
    }
    const src = this.set.embedded
    if (!src) return
    const t = track.track
    await this.j.renderer.setTrack(t.codec === "srt" || !t.header.trim() ? srtHeader() : t.header)
    if (this.destroyed || gen !== this.gen) return
    const add = (c: Cue) => this.j.renderer.processChunk(c.text, c.start, c.duration)
    // 先订阅再取已有的（去重由 source 保证；processChunk 对同一 ReadOrder 也会去重）
    this.unsub = src.onCue((tr, c) => {
      if (tr === this.track) void add(c)
    })
    await Promise.all(src.cuesOf(t.number).map(add))
    this.repaint()
  }

  /** 暂停时 JASSUB 不会重画（它跟着视频帧渲染）：换轨道 / 关闭后手动重画当前帧 */
  private repaint() {
    if (!this.destroyed && this.video.paused) void this.j.resize(true).catch(() => {})
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
