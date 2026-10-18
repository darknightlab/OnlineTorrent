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
}

export interface Session {
  /** 元数据就绪前为 null */
  snapshot(): TorrentSnap | null
  /** 文件分段状态：0=未下载，1=部分，2=完成 */
  segments(fileIndex: number, n: number): Uint8Array
  /** Service Worker 流式 URL；不可用时为 null */
  fileURL(fileIndex: number): string | null
  torrentFileURL(): string | null
  /** 加入 HTTP WebSeed；同一 URL 只加一次，返回是否为新加入 */
  addWebSeed(url: string): boolean
  /**
   * 元数据尚未获取时，改用服务器提供的 .torrent（GET /torrents/hash/{ih}/torrent）。
   * 返回是否已采用；已有元数据时不做任何事。mock 引擎不实现。
   */
  loadTorrentFile?(data: Uint8Array): Promise<boolean>
  /** 只下载这个文件（其余文件不下载；null = 都不下载）。元数据就绪前调用会在就绪后生效。 */
  focusFile?(index: number | null): void
  /** 播放器拿到的媒体时长（秒），用于按码率估算缓存的读前窗口 */
  setMediaDuration?(seconds: number): void
  /** 致命错误（会话已停止）；没有为 null */
  fatalError?(): string | null
  on<K extends keyof SessionEvents>(ev: K, cb: SessionEvents[K]): () => void
  destroy(): void
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

/** 浏览器大概率无法原生解码的文件 */
export function likelyUnsupported(name: string): string | null {
  const n = name.toLowerCase()
  if (/\b(hevc|x265|h\.?265)\b/.test(n)) return "HEVC/H.265"
  if (/\b(e?ac-?3|dts|truehd|flac)\b/.test(n) && extOf(n) === "mkv") return "AC3/DTS/FLAC 音轨"
  if (/\b10-?bit\b|\bhi10p?\b/.test(n)) return "10bit"
  if (["avi", "flv", "ts", "m2ts"].includes(extOf(n))) return extOf(n).toUpperCase()
  return null
}
