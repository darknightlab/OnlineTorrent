// 一个 MKV 文件的内封字幕：头部（轨道、字体附件）+ 从视频读取流里收集到的字幕块（见 tap.ts）。
// 引擎为正在播放的 MKV 文件创建；字幕块按轨道去重保存，渲染器随时可以取全部已收集的 + 订阅新的。
import { children, readString } from "./ebml.ts"
import { ID } from "./ebml.ts"
import { decodeContent, isFont, parseMkvHeader, readAttachment, readCuePoints, type MkvHeader, type ReadRange, type SubTrack } from "./mkv.ts"
import { ClusterTap, type RawBlock } from "./tap.ts"

export interface Cue {
  /** 开始时间与时长（毫秒） */
  start: number
  duration: number
  /** Matroska ASS 块格式：ReadOrder,Layer,Style,Name,MarginL,MarginR,MarginV,Effect,Text（SRT 已转换） */
  text: string
}

export type CueListener = (track: number, cue: Cue) => void

/** 一个视频读取流的字节入口（按顺序推入） */
export interface TapSink {
  push(chunk: Uint8Array): void
}

/** 补读：播放位置之前多少秒内开始的字幕块（字幕通常不超过这么长） */
const BACKFILL_SECONDS = 10
/** 补读一次最多读取的字节数 */
const BACKFILL_MAX_BYTES = 32 << 20

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
  /** 视频读过的 Cluster（文件偏移 → 毫秒），Cues 缺失时用于进度条的时间映射 */
  readonly clusters = new Map<number, number>()

  private size: number
  private readBg: ReadRange
  private cuePoints: Promise<[number, number][] | null> | null = null
  private backfilled = new Set<number>()

  /** read：辅助读取（头部、字体）；readBackground：低优先级读取（补读字幕块，见 backfill） */
  constructor(read: ReadRange, size: number, readBackground: ReadRange = read) {
    this.read = read
    this.size = size
    this.readBg = readBackground
    this.header = parseMkvHeader(read, size).then(
      (h) => (this.h = h),
      () => null,
    )
  }

  /** 已知的字幕轨道（头部解析完成前为空） */
  tracks(): SubTrack[] {
    return this.h?.subtitles ?? []
  }

  /** 解析器（每个连续读取的区段一个；新的读取流接着已有区段时继续用它） */
  private taps: ClusterTap[] = []

  /**
   * 一个视频读取流（从 start 开始）的入口。浏览器常把文件分成许多相邻的短请求来读（数据已在本地缓存时尤其如此：
   * 重新打开页面后每个请求只有几十 KB），每个请求单独从头同步的话，短于一个 Cluster 的请求永远找不到
   * Cluster 头，字幕块一个也收不到。所以接着已有解析器的读取流继续用那个解析器（只送它还没见过的字节），
   * 只有真正的跳跃（拖动）才新建解析器、从下一个 Cluster 开始同步。
   */
  tap(start: number): TapSink {
    let pos = start
    let t: ClusterTap | null = this.taps.find((x) => !x.dead && x.start <= start && start <= x.next) ?? null
    return {
      push: (chunk: Uint8Array) => {
        const end = pos + chunk.length
        if (!t || t.dead || t.next < pos) {
          // 没有、或者与已解析的区段之间有空隙：找一个正好接得上的，否则新建
          t = this.taps.find((x) => !x.dead && x.start <= pos && pos <= x.next) ?? this.newTap(pos)
        }
        if (t.next < end) t.push(chunk.subarray(t.next - pos)) // 已解析过的部分跳过
        pos = end
      },
    }
  }

  private newTap(start: number): ClusterTap {
    const t = this.createTap(start)
    this.taps.push(t)
    if (this.taps.length > 16) this.taps.shift()
    return t
  }

  private createTap(start: number): ClusterTap {
    return new ClusterTap(start, {
      isSub: (n) => {
        const t = this.h?.trackTypes.get(n) ?? this.tracksFromStream.get(n)
        if (t === undefined) return undefined
        return t === 17 && (this.h ? this.h.subtitles.some((s) => s.number === n) : true)
      },
      timestampScale: () => this.h?.timestampScale ?? 1_000_000,
      onBlock: (b) => this.onBlock(b),
      onCluster: (pos, ms) => {
        if (this.clusters.size < 100_000) this.clusters.set(pos, ms)
      },
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

  /**
   * 补读播放位置之前的字幕块：字幕块存放在它开始时间所在的 Cluster 里，而拖动（以及从本地缓存重新打开，
   * 浏览器直接从播放位置附近读起）后视频从目标时间的关键帧所在的 Cluster 开始读——此前开始、此刻仍在显示的
   * 那一句永远读不到。读取播放位置前约 10 秒的 Cluster 到播放位置所在的 Cluster（按 Cues 索引；没有 Cues 时
   * 按已见到的 Cluster），送进同一个解析器。低优先级读取，不与视频抢；同一段只读一次。
   */
  async backfill(ms: number): Promise<void> {
    const h = this.h ?? (await this.header)
    if (!h || !h.subtitles.length) return
    this.cuePoints ??= readCuePoints(this.read, h).catch(() => null)
    const idx = (await this.cuePoints) ?? [...this.clusters].map(([p, t]): [number, number] => [p, t / 1000]).sort((a, b) => a[0] - b[0])
    if (!idx.length) return
    const sec = ms / 1000
    let from = idx[0][0]
    let to = this.size
    for (const [p, t] of idx) {
      if (t <= sec - BACKFILL_SECONDS) from = p
      if (t > sec) {
        to = p
        break
      }
    }
    if (to <= from || to - from > BACKFILL_MAX_BYTES || this.backfilled.has(from)) return
    this.backfilled.add(from)
    try {
      const bytes = await this.readBg(from, to)
      this.tap(from).push(bytes)
    } catch {
      this.backfilled.delete(from) // 下次再试
    }
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

/** SRT 文本 → Matroska ASS 块（保留 {\an8} 等 ASS 控制标签；基本标签 <i> <b> <u> <s> <font color>，换行 → \N） */
export function srtToAss(text: string, order: number): string {
  const tags: string[] = []
  const t = text
    .replace(/\r/g, "")
    .trim()
    .replace(/\{\\[^}]*\}/g, (m) => `\0${tags.push(m) - 1}\0`)
    .replace(/[{}]/g, (c) => (c === "{" ? "(" : ")"))
    .replace(/\0(\d+)\0/g, (_, i: string) => tags[Number(i)])
    .replace(/<\s*(\/?)\s*([ibus])\s*>/gi, (_, close: string, tag: string) => `{\\${tag.toLowerCase()}${close ? 0 : 1}}`)
    .replace(/<font\s+color\s*=\s*["']?#?([0-9a-f]{6})["']?\s*>/gi, (_, c: string) => `{\\c&H${c.slice(4, 6)}${c.slice(2, 4)}${c.slice(0, 2)}&}`)
    .replace(/<\/font\s*>/gi, "{\\c}")
    .replace(/<[^>]*>/g, "")
    .replace(/\n/g, "\\N")
  return `${order},0,Default,,0,0,0,,${t}`
}
