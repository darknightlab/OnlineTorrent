// 播放引擎抽象：真实实现基于 webtorrent@3，mock 模式下用模拟实现，便于脱离后端开发与截图。

export type FileKind = "video" | "audio" | "image" | "subtitle" | "other"

export interface FileSnap {
  index: number
  name: string
  path: string
  length: number
  progress: number
  kind: FileKind
}

export interface PeerSnap {
  id: string
  type: "webrtc" | "webSeed" | "other"
  addr: string
  downloadSpeed: number
  uploadSpeed: number
}

export interface TorrentSnap {
  name: string
  infoHash: string
  length: number
  pieceLength: number
  numPieces: number
  progress: number
  downloaded: number
  uploaded: number
  downloadSpeed: number
  uploadSpeed: number
  numPeers: number
  timeRemaining: number
  done: boolean
  files: FileSnap[]
  peers: PeerSnap[]
}

export type LogLevel = "info" | "warn" | "error"

export interface SessionEvents {
  metadata: () => void
  log: (level: LogLevel, msg: string) => void
  /** 无法继续（如播放器内核不兼容）；会话已停止 */
  fatal: (msg: string) => void
  /** 边缘节点拒绝了令牌（401/403）：请重新连接 WebSeed 状态订阅以取得新令牌 */
  webseedToken: (url: string) => void
}

export interface Session {
  /** 元数据就绪前为 null */
  snapshot(): TorrentSnap | null
  /** 文件分段状态：0=未下载，1=部分，2=完成 */
  segments(fileIndex: number, n: number): Uint8Array
  /** Service Worker 流式 URL；不可用时为 null */
  fileURL(fileIndex: number): string | null
  torrentFileURL(): string | null
  /**
   * 加入 HTTP WebSeed；同一 URL 只加一次，返回是否为新加入。
   * avail：服务器可提供的分块（qB 还在下载时）；省略 = 全部
   */
  addWebSeed(url: string, avail?: WebSeedAvail): boolean
  /** 服务器可提供的分块变化（SSE have / lost / complete；重连后 reset） */
  updateWebSeed?(url: string, ev: WebSeedUpdate): void
  /** 换上边缘节点的新访问令牌（SSE token 事件；URL 不变） */
  setWebSeedToken?(url: string, token: string): void
  /** SSE 又推送了该 WebSeed（重连后的 ready / seed）：停用中的立即重新启用 */
  webSeedRefreshed?(url: string): void
  /** WebSeed 已宣告的分块数（日志用） */
  /** 该 WebSeed 已有的分块数（正在播放的文件范围内） */
  webSeedPieces?(url: string): { have: number; of: number } | null
  /**
   * 元数据尚未获取时，改用服务器提供的 .torrent（GET /torrents/hash/{ih}/torrent）。
   * 返回是否已采用；已有元数据时不做任何事。mock 引擎不实现。
   */
  loadTorrentFile?(data: Uint8Array): Promise<boolean>
  /** 只下载这个文件（其余文件不下载；null = 都不下载）。元数据就绪前调用会在就绪后生效。 */
  focusFile?(index: number | null): void
  /** MKV 文件的内封字幕（其他文件 / mock 引擎为 null）；正在播放的文件由 focusFile 预先建好 */
  subtitles?(fileIndex: number): import("./subs/set").SubtitleSet | null
  /** 该视频的外挂字幕与种子附带的字体文件（请求服务器做种时一并下载） */
  companionFiles?(fileIndex: number): number[]
  /** 进度条的字节 → 时间映射（容器索引）；不支持的容器 / 没有索引时为 null */
  timeMap?(fileIndex: number): Promise<TimeMapResult | null>
  /** 读取文件 [start, end) 的字节（辅助读取：容器索引等；缺的 piece 会下载） */
  readRange?(fileIndex: number, start: number, end: number): Promise<Uint8Array>
  /** 播放器拿到的媒体时长（秒），用于按码率估算缓存的读前窗口 */
  setMediaDuration?(seconds: number): void
  /** 致命错误（会话已停止）；没有为 null */
  fatalError?(): string | null
  on<K extends keyof SessionEvents>(ev: K, cb: SessionEvents[K]): () => void
  destroy(): void
}

/** 分块区间：闭区间 [start, end] 依次展开 */
export interface WebSeedAvail {
  pieces: number | null
  have: number[] | null
  complete?: boolean
  /** 边缘节点的访问令牌：对该 URL 的请求加 Authorization: Bearer（主站自己的 URL 没有） */
  token?: string
}

export interface WebSeedUpdate {
  have?: number[]
  lost?: number[]
  /** 与之完全一致（重连后服务器重发的 ready） */
  reset?: number[]
  complete?: boolean
}

export interface StartOptions {
  announce: string[]
  iceServers: RTCIceServer[]
  /** site.max_push_size_bytes：浏览器缓存预算 = min(此值, 存储配额的 25%) */
  maxCacheBytes?: number
}

export interface Engine {
  readonly kind: "webtorrent" | "mock"
  start(magnet: string, opts: StartOptions): Promise<Session>
  /** Service Worker 是否可用（流式播放所需） */
  streamingReady(): Promise<boolean>
  resetServiceWorker(): Promise<void>
}

const VIDEO = ["mp4", "m4v", "webm", "mkv", "mov", "ogv", "ogm", "3gp", "3g2", "m2ts", "ts", "avi", "flv"]
const AUDIO = ["mp3", "m4a", "aac", "flac", "wav", "ogg", "opus", "ac3", "eac3", "dts"]
const IMAGE = ["jpg", "jpeg", "png", "webp", "gif", "avif", "bmp"]
const SUB = ["ass", "ssa", "srt", "vtt", "sup", "idx", "sub"]

export function extOf(name: string) {
  const i = name.lastIndexOf(".")
  return i < 0 ? "" : name.slice(i + 1).toLowerCase()
}

export function fileKind(name: string): FileKind {
  const e = extOf(name)
  if (VIDEO.includes(e)) return "video"
  if (AUDIO.includes(e)) return "audio"
  if (IMAGE.includes(e)) return "image"
  if (SUB.includes(e)) return "subtitle"
  return "other"
}

export const isPlayable = (k: FileKind) => k === "video" || k === "audio" || k === "image"

/** 浏览器是否声明能解码某种编码（canPlayType 返回 "probably"/"maybe" 视为支持；无 DOM 时视为支持） */
function canDecode(type: string): boolean {
  if (typeof document === "undefined") return true
  try {
    return document.createElement("video").canPlayType(type) !== ""
  } catch {
    return true
  }
}

/**
 * 当前浏览器确实无法原生解码的文件：先按文件名猜编码，再用浏览器自己的 canPlayType 确认，
 * 只有浏览器明确不支持时才返回（现代 Chrome / Edge / Safari 大多能硬解 HEVC、10bit，不再误报）。
 */
export function likelyUnsupported(name: string): string | null {
  const n = name.toLowerCase()
  // 容器本身浏览器就不支持
  if (["avi", "flv", "ts", "m2ts"].includes(extOf(n))) return extOf(n).toUpperCase()
  if (/\b(hevc|x265|h\.?265)\b/.test(n)) {
    const tenBit = /\b10-?bit\b|\bma10p?\b|\bhi10p?\b|main ?10/.test(n)
    // hvc1.2 = Main 10 profile；hvc1.1 = Main
    if (!canDecode(tenBit ? 'video/mp4; codecs="hvc1.2.4.L120.B0"' : 'video/mp4; codecs="hvc1.1.6.L120.B0"')) return tenBit ? "HEVC 10bit" : "HEVC/H.265"
  } else if (/\b10-?bit\b|\bhi10p?\b/.test(n)) {
    // H.264 High 10：几乎没有浏览器能解码
    if (!canDecode('video/mp4; codecs="avc1.6E0028"')) return "H.264 10bit"
  }
  if (extOf(n) === "mkv") {
    if (/\b(e?ac-?3)\b/.test(n) && !canDecode('audio/mp4; codecs="ac-3"')) return "AC3 音轨"
    if (/\b(dts|truehd)\b/.test(n)) return "DTS/TrueHD 音轨"
    if (/\bflac\b/.test(n) && !canDecode('audio/mp4; codecs="flac"')) return "FLAC 音轨"
  }
  return null
}

/** 字节 → 时间映射；partial：来自播放中观察到的位置（MKV 无 Cues），之后可能更完整 */
export interface TimeMapResult {
  map: import("@/lib/timemap").TimeMap
  partial: boolean
}
