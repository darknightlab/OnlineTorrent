// 一个 MKV 文件的内封字幕：头部（轨道、字体附件）+ 从视频读取流里收集到的字幕块（见 tap.ts）。
// 引擎为正在播放的 MKV 文件创建；字幕块按轨道去重保存，渲染器随时可以取全部已收集的 + 订阅新的。
import { children, readString } from "./ebml.ts"
import { ID } from "./ebml.ts"
import { decodeContent, isFont, parseMkvHeader, readAttachment, type MkvHeader, type ReadRange, type SubTrack } from "./mkv.ts"
import { ClusterTap, type RawBlock } from "./tap.ts"

export interface Cue {
  /** 开始时间与时长（毫秒） */
  start: number
  duration: number
  /** Matroska ASS 块格式：ReadOrder,Layer,Style,Name,MarginL,MarginR,MarginV,Effect,Text（SRT 已转换） */
  text: string
}

export type CueListener = (track: number, cue: Cue) => void

/** 字体附件合计超过此值时不加载（手机流量 / 内存） */
const MAX_FONT_BYTES = 64 << 20

export class SubtitleSource {
  readonly header: Promise<MkvHeader | null>
  private h: MkvHeader | null = null
  private tracksFromStream = new Map<number, number>()
  private cues = new Map<number, Map<string, Cue>>()
  private listeners = new Set<CueListener>()
  private fontsP: Promise<Uint8Array[]> | null = null
  private read: ReadRange
  private srtOrder = 0
  private pending: Promise<void> = Promise.resolve()

  constructor(read: ReadRange, size: number) {
    this.read = read
    this.header = parseMkvHeader(read, size).then(
      (h) => (this.h = h),
      () => null,
    )
  }

  /** 已知的字幕轨道（头部解析完成前为空） */
  tracks(): SubTrack[] {
    return this.h?.subtitles ?? []
  }

  /** 为一个视频读取流（从 start 开始）创建解析器 */
  tap(start: number): ClusterTap {
    return new ClusterTap(start, {
      isSub: (n) => {
        const t = this.h?.trackTypes.get(n) ?? this.tracksFromStream.get(n)
        if (t === undefined) return undefined
        return t === 17 && (this.h ? this.h.subtitles.some((s) => s.number === n) : true)
      },
      timestampScale: () => this.h?.timestampScale ?? 1_000_000,
      onBlock: (b) => this.onBlock(b),
      onTracks: (data) => {
        for (const e of children(data)) {
          if (e.id !== ID.TrackEntry) continue
          let num = 0
          let type = 0
          for (const c of children(e.data)) {
            if (c.id === ID.TrackNumber) num = c.data.reduce((v, x) => v * 256 + x, 0)
            else if (c.id === ID.TrackType) type = c.data.reduce((v, x) => v * 256 + x, 0)
          }
          if (num) this.tracksFromStream.set(num, type)
        }
      },
    })
  }

  private onBlock(b: RawBlock) {
    // 解压是异步的：串起来保证同一轨道的顺序
    this.pending = this.pending.then(async () => {
      const h = this.h ?? (await this.header)
      const tr = h?.subtitles.find((s) => s.number === b.track)
      if (!tr) return
      let raw: Uint8Array
      try {
        raw = await decodeContent(b.data, tr.encodings, 1)
      } catch {
        return
      }
      const text = tr.codec === "srt" ? srtToAss(readString(raw), ++this.srtOrder) : readString(raw)
      const cue: Cue = { start: Math.round(b.timeMs), duration: Math.round(b.durationMs), text }
      let m = this.cues.get(b.track)
      if (!m) this.cues.set(b.track, (m = new Map()))
      // 同一块可能被读到多次（拖动、重复请求）：SRT 按时间 + 文本，ASS 按 ReadOrder（文本开头）
      const key = tr.codec === "srt" ? `${cue.start}|${text.slice(text.indexOf(",") + 1)}` : `${cue.start}|${text}`
      if (m.has(key)) return
      m.set(key, cue)
      this.listeners.forEach((l) => l(b.track, cue))
    })
  }

  /** 该轨道已收集的字幕块 */
  cuesOf(track: number): Cue[] {
    return [...(this.cues.get(track)?.values() ?? [])]
  }

  onCue(l: CueListener): () => void {
    this.listeners.add(l)
    return () => void this.listeners.delete(l)
  }

  /** 字体附件（第一次调用时读取；合计过大时为空） */
  fonts(): Promise<Uint8Array[]> {
    this.fontsP ??= (async () => {
      const h = this.h ?? (await this.header)
      const fonts = h?.attachments.filter(isFont) ?? []
      const total = fonts.reduce((s, a) => s + a.size, 0)
      if (!fonts.length || total > MAX_FONT_BYTES) return []
      const out: Uint8Array[] = []
      for (const a of fonts) {
        try {
          out.push(await readAttachment(this.read, a))
        } catch {
          /* 单个字体失败不影响其他 */
        }
      }
      return out
    })()
    return this.fontsP
  }
}

/** SRT 轨道的 ASS 文件头（只有一个默认样式） */
export function srtHeader(): string {
  return [
    "[Script Info]",
    "ScriptType: v4.00+",
    "PlayResX: 1280",
    "PlayResY: 720",
    "ScaledBorderAndShadow: yes",
    "",
    "[V4+ Styles]",
    "Format: Name, Fontname, Fontsize, PrimaryColour, SecondaryColour, OutlineColour, BackColour, Bold, Italic, Underline, StrikeOut, ScaleX, ScaleY, Spacing, Angle, BorderStyle, Outline, Shadow, Alignment, MarginL, MarginR, MarginV, Encoding",
    "Style: Default,sans-serif,48,&H00FFFFFF,&H000000FF,&H00000000,&H64000000,0,0,0,0,100,100,0,0,1,2.5,1,2,40,40,34,1",
    "",
    "[Events]",
    "Format: Layer, Start, End, Style, Name, MarginL, MarginR, MarginV, Effect, Text",
    "",
  ].join("\n")
}

/** SRT 文本 → Matroska ASS 块（基本标签 <i> <b> <u> <s> <font color>，换行 → \N） */
export function srtToAss(text: string, order: number): string {
  const t = text
    .replace(/\r/g, "")
    .trim()
    .replace(/[{}]/g, (c) => (c === "{" ? "(" : ")"))
    .replace(/<\s*(\/?)\s*([ibus])\s*>/gi, (_, close: string, tag: string) => `{\\${tag.toLowerCase()}${close ? 0 : 1}}`)
    .replace(/<font\s+color\s*=\s*["']?#?([0-9a-f]{6})["']?\s*>/gi, (_, c: string) => `{\\c&H${c.slice(4, 6)}${c.slice(2, 4)}${c.slice(0, 2)}&}`)
    .replace(/<\/font\s*>/gi, "{\\c}")
    .replace(/<[^>]*>/g, "")
    .replace(/\n/g, "\\N")
  return `${order},0,Default,,0,0,0,,${t}`
}
