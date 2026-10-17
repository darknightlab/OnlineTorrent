import { useCallback, useEffect, useMemo, useRef, useState } from "react"
import { Link } from "react-router-dom"
import {
  AlertTriangle,
  ArrowDown,
  ArrowUp,
  Copy,
  Download,
  FileAudio,
  FileImage,
  FileText,
  FileVideo,
  Flag,
  Info,
  ListTree,
  Loader2,
  Play,
  RefreshCw,
  ScrollText,
  Server,
  Share2,
  Users,
  Captions,
  Tv,
} from "lucide-react"
import { loadEngine } from "@/player/engine"
import { isPlayable, likelyUnsupported, type Engine, type FileKind, type FileSnap, type LogLevel, type Session, type TorrentSnap } from "@/player/types"
import { PieceBar, SEGMENTS } from "./PieceBar"
import { ReportDialog } from "./ReportDialog"
import { animeHref } from "@/components/AnimeCard"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Card } from "@/components/ui/card"
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs"
import { toast } from "@/components/ui/toast"
import { useAnime, useSiteOrFallback } from "@/lib/queries"
import type { PlayResponse, Torrent } from "@/lib/types"
import { formatBytes, formatDuration, formatPercent, formatSpeed } from "@/lib/format"
import { cn, copyText } from "@/lib/utils"
import { useDocumentTitle } from "@/lib/hooks"

interface LogLine {
  t: number
  level: LogLevel
  msg: string
}

const kindIcon: Record<FileKind, typeof FileVideo> = {
  video: FileVideo,
  audio: FileAudio,
  image: FileImage,
  subtitle: Captions,
  other: FileText,
}

const collator = new Intl.Collator("zh-CN", { numeric: true })

function pickInitial(files: FileSnap[]) {
  const sorted = [...files].sort((a, b) => collator.compare(a.path, b.path))
  return (sorted.find((f) => f.kind === "video") ?? sorted.find((f) => f.kind === "audio") ?? sorted.find((f) => f.kind === "image"))?.index ?? null
}

export function Player({
  magnet,
  play,
  direct,
  hint,
  shareURL,
}: {
  magnet: string
  play: PlayResponse | null
  direct: boolean
  hint?: Torrent
  shareURL: (infohash: string | null) => string
}) {
  const site = useSiteOrFallback()
  const [engine, setEngine] = useState<Engine | null>(null)
  const [session, setSession] = useState<Session | null>(null)
  const [snap, setSnap] = useState<TorrentSnap | null>(null)
  const [streaming, setStreaming] = useState<boolean | null>(null)
  const [current, setCurrent] = useState<number | null>(null)
  const [segments, setSegments] = useState<Uint8Array>(() => new Uint8Array(SEGMENTS))
  const [logs, setLogs] = useState<LogLine[]>([])
  const [mediaError, setMediaError] = useState<string | null>(null)
  const [noPicture, setNoPicture] = useState(false)
  const [buffering, setBuffering] = useState(false)
  const [needsTap, setNeedsTap] = useState(false)
  const [reportOpen, setReportOpen] = useState(false)
  const [tab, setTab] = useState("files")
  const videoRef = useRef<HTMLVideoElement>(null)
  const autoPicked = useRef(false)

  const anime = useAnime(play?.meta?.anime_key ?? "")
  const poster = anime.data?.anime.cover ?? undefined

  const log = useCallback((level: LogLevel, msg: string) => {
    setLogs((l) => [...l.slice(-199), { t: Date.now(), level, msg }])
  }, [])

  // 启动引擎
  useEffect(() => {
    let cancelled = false
    let s: Session | null = null
    const unsubs: (() => void)[] = []
    autoPicked.current = false
    setSnap(null)
    setCurrent(null)
    setLogs([])
    log("info", direct ? "服务器不可用，直接通过 P2P 网络连接" : `服务器已接受请求${play?.seeded_by?.length ? `，做种节点：${play.seeded_by.join(", ")}` : ""}`)
    loadEngine()
      .then(async (eng) => {
        if (cancelled) return
        setEngine(eng)
        log("info", "正在连接 tracker，寻找节点…")
        s = await eng.start(magnet, { announce: site.trackers, iceServers: site.ice_servers as RTCIceServer[] })
        if (cancelled) return s.destroy()
        unsubs.push(s.on("log", log))
        if (play?.webseeds?.length) {
          const seeds = play.webseeds
          const delay = Math.max(0, play.webseed_delay_sec ?? 0)
          if (delay > 0) log("info", `${delay} 秒后启用 HTTP WebSeed 加速（优先使用 P2P）`)
          const timer = setTimeout(() => {
            if (!cancelled && s) s.addWebSeeds(seeds)
          }, delay * 1000)
          unsubs.push(() => clearTimeout(timer))
        }
        setSession(s)
        const ok = await eng.streamingReady()
        if (!cancelled) {
          setStreaming(ok)
          if (!ok && eng.kind === "webtorrent") log("warn", "Service Worker 不可用，无法边下边播（需要 HTTPS 且浏览器支持 Service Worker）")
        }
      })
      .catch((e) => log("error", `播放器启动失败：${String(e?.message ?? e)}`))
    return () => {
      cancelled = true
      unsubs.forEach((u) => u())
      s?.destroy()
      setSession(null)
    }
    // site 配置变化不重启种子
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [magnet])

  // 轮询状态
  useEffect(() => {
    if (!session) return
    const update = () => {
      const sn = session.snapshot()
      setSnap(sn)
      if (sn && !autoPicked.current) {
        autoPicked.current = true
        const first = pickInitial(sn.files)
        if (first !== null) setCurrent(first)
      }
    }
    update()
    const t = window.setInterval(update, 1000)
    return () => window.clearInterval(t)
  }, [session])

  useEffect(() => {
    if (!session || current === null) return
    const up = () => setSegments(session.segments(current, SEGMENTS))
    up()
    const t = window.setInterval(up, 1000)
    return () => window.clearInterval(t)
  }, [session, current])

  const file = current !== null ? snap?.files[current] : undefined
  const url = session && current !== null && streaming ? session.fileURL(current) : null
  const unsupported = file ? likelyUnsupported(file.name) || likelyUnsupported(snap?.name ?? "") : null
  useDocumentTitle(file?.name ?? snap?.name ?? play?.name ?? hint?.title ?? "播放")

  useEffect(() => {
    setMediaError(null)
    setNoPicture(false)
    setNeedsTap(false)
  }, [current])

  // 自动播放（可能被浏览器拦截，则显示播放按钮）
  useEffect(() => {
    const v = videoRef.current
    if (!v || !url || file?.kind !== "video") return
    v.play().catch(() => setNeedsTap(true))
  }, [url, file?.kind])

  const infoHash = snap?.infoHash ?? play?.infohash ?? hint?.infohash ?? null
  const title = snap?.name ?? play?.name ?? hint?.title ?? "正在获取种子信息…"

  async function share() {
    const link = shareURL(infoHash)
    if (navigator.share && window.matchMedia("(pointer: coarse)").matches) {
      try {
        await navigator.share({ title, url: link })
        return
      } catch {
        /* 用户取消，回退到复制 */
      }
    }
    if (await copyText(link)) toast.success("分享链接已复制")
    else toast.error("复制失败，请手动复制地址栏链接")
  }

  const onSelectFile = (f: FileSnap) => {
    if (isPlayable(f.kind)) {
      setCurrent(f.index)
      if (window.matchMedia("(max-width: 1023px)").matches) window.scrollTo({ top: 0, behavior: "smooth" })
    } else {
      const u = session?.fileURL(f.index)
      if (u) {
        const a = document.createElement("a")
        a.href = u
        a.download = f.name
        a.click()
      } else toast.info("当前无法下载该文件（Service Worker 不可用）")
    }
  }

  const isMock = engine?.kind === "mock"
  const sortedFiles = useMemo(() => (snap ? [...snap.files].sort((a, b) => collator.compare(a.path, b.path)) : []), [snap])

  return (
    <div className="grid gap-4 lg:grid-cols-[minmax(0,1fr)_360px] lg:gap-5 xl:grid-cols-[minmax(0,1fr)_400px]">
      <section className="min-w-0">
        {/* 播放区：移动端吸顶、全宽 */}
        <div className="sticky top-14 z-20 -mx-3 bg-black sm:static sm:mx-0 sm:overflow-hidden sm:rounded-xl sm:shadow-lg">
          <div className="relative aspect-video w-full overflow-hidden bg-black">
            {/* 未出画面时：竖版封面两侧用模糊背景填满，避免大片纯黑 */}
            {poster && !url && (
              <div
                aria-hidden
                className="absolute inset-0 scale-110 bg-cover bg-center opacity-50 blur-2xl"
                style={{ backgroundImage: `url("${poster}")` }}
              />
            )}
            {file?.kind === "video" || !file ? (
              <video
                key={`${current}-${url ?? ""}`}
                ref={videoRef}
                className="kawaii-video absolute inset-0 h-full w-full"
                src={url ?? undefined}
                poster={poster}
                controls={!!url}
                playsInline
                preload="metadata"
                onWaiting={() => setBuffering(true)}
                onPlaying={() => {
                  setBuffering(false)
                  setNeedsTap(false)
                }}
                onCanPlay={() => setBuffering(false)}
                onLoadedMetadata={(e) => {
                  const v = e.currentTarget
                  if (v.videoWidth === 0 && v.duration > 0) setNoPicture(true)
                }}
                onError={(e) => {
                  const err = e.currentTarget.error
                  if (err) {
                    setMediaError(err.message || `MediaError ${err.code}`)
                    log("error", `视频解码失败：${err.message || `code ${err.code}`}`)
                  }
                }}
              />
            ) : file.kind === "audio" ? (
              <div className="absolute inset-0 flex flex-col items-center justify-center gap-4 bg-gradient-to-br from-primary/40 to-accent/40 p-6">
                <FileAudio className="h-12 w-12 text-white/80" />
                <p className="line-clamp-2 max-w-md text-center text-sm text-white">{file.name}</p>
                {url && <audio key={url} src={url} controls autoPlay className="w-full max-w-md" />}
              </div>
            ) : (
              url && <img key={url} src={url} alt={file.name} className="absolute inset-0 h-full w-full object-contain" />
            )}

            {/* 状态浮层 */}
            {!url && (
              <Overlay>
                {!snap ? (
                  <>
                    <Loader2 className="h-8 w-8 animate-spin text-primary" />
                    <p className="font-medium">正在获取种子信息…</p>
                    <p className="text-xs text-white/70">首次连接节点可能需要十几秒</p>
                  </>
                ) : isMock ? (
                  <>
                    <Tv className="h-8 w-8 text-white/80" />
                    <p className="font-medium">模拟模式</p>
                    <p className="max-w-xs text-xs text-white/70">mock 引擎不加载真实视频数据，加 ?real=1 可使用真实 WebTorrent</p>
                  </>
                ) : streaming === false ? (
                  <>
                    <AlertTriangle className="h-8 w-8 text-warning" />
                    <p className="font-medium">无法边下边播</p>
                    <p className="max-w-xs text-xs text-white/70">需要 HTTPS 与 Service Worker 支持，请使用最新版 Chrome / Edge 打开</p>
                  </>
                ) : current === null ? (
                  <>
                    <FileText className="h-8 w-8 text-white/80" />
                    <p className="font-medium">没有可在线播放的文件</p>
                    <p className="text-xs text-white/70">可在文件列表中下载</p>
                  </>
                ) : (
                  <Loader2 className="h-8 w-8 animate-spin text-primary" />
                )}
              </Overlay>
            )}
            {url && file?.kind === "video" && needsTap && (
              <button
                type="button"
                aria-label="播放"
                onClick={() => videoRef.current?.play().then(() => setNeedsTap(false)).catch(() => {})}
                className="absolute inset-0 flex items-center justify-center bg-black/30"
              >
                <span className="inline-flex h-16 w-16 items-center justify-center rounded-full bg-primary text-white shadow-xl transition-transform hover:scale-105">
                  <Play className="ml-1 h-7 w-7 fill-current" />
                </span>
              </button>
            )}
            {url && buffering && !needsTap && (
              <div className="pointer-events-none absolute left-3 top-3 inline-flex items-center gap-1.5 rounded-full bg-black/60 px-2.5 py-1 text-xs text-white">
                <Loader2 className="h-3.5 w-3.5 animate-spin" />
                缓冲中 · {formatSpeed(snap?.downloadSpeed ?? 0)}
              </div>
            )}
          </div>
          <PieceBar segments={segments} />
        </div>

        {/* 编码提示 */}
        {(mediaError || noPicture) && file?.kind === "video" ? (
          <CodecHint severe codec={unsupported} noPicture={noPicture} />
        ) : unsupported && file?.kind === "video" ? (
          <CodecHint codec={unsupported} />
        ) : null}

        {/* 标题与操作 */}
        <div className="mt-3 flex flex-col gap-3 sm:mt-4 md:flex-row md:items-start md:justify-between">
          <div className="min-w-0">
            <h1 className="line-clamp-2 wrap-anywhere text-[15px] font-semibold leading-snug sm:text-lg" title={title}>
              {title}
            </h1>
            <div className="mt-1.5 flex flex-wrap items-center gap-x-2.5 gap-y-1.5 text-xs text-muted-foreground">
              {hint?.team?.name && <Badge variant="accent-soft">{hint.team.name}</Badge>}
              {play?.meta?.anime_key && (
                <Link to={animeHref(play.meta.anime_key)} className="inline-flex h-6 items-center gap-1 rounded bg-primary-soft px-1.5 font-medium text-primary-text hover:underline">
                  <Tv className="h-3 w-3" />
                  {anime.data ? anime.data.anime.title_cn || anime.data.anime.title : "番剧详情"}
                  {play.meta.episode ? ` · 第 ${play.meta.episode} 话` : ""}
                </Link>
              )}
              {snap && <span className="tabular-nums">{formatBytes(snap.length)}</span>}
              {snap && <span>{snap.files.length} 个文件</span>}
              {direct && <Badge variant="warning">直连模式</Badge>}
              {play?.seeded_by?.length ? (
                <span className="inline-flex items-center gap-1">
                  <Server className="h-3 w-3" />
                  {play.seeded_by.join(" / ")}
                </span>
              ) : null}
            </div>
          </div>
          <div className="flex shrink-0 gap-2">
            <Button variant="soft" size="sm" onClick={share} className="flex-1 md:flex-none">
              <Share2 />
              分享
            </Button>
            {file && url ? (
              <Button asChild variant="outline" size="sm" className="flex-1 md:flex-none">
                <a href={url} download={file.name}>
                  <Download />
                  下载
                </a>
              </Button>
            ) : (
              <Button variant="outline" size="sm" disabled className="flex-1 md:flex-none">
                <Download />
                下载
              </Button>
            )}
            <Button variant="ghost" size="sm" onClick={() => setReportOpen(true)} disabled={!infoHash} className="flex-1 text-muted-foreground md:flex-none">
              <Flag />
              举报
            </Button>
          </div>
        </div>

        {/* 统计 */}
        <dl className="mt-3 grid grid-cols-4 overflow-hidden rounded-lg border bg-card text-center shadow-card sm:mt-4">
          <Stat icon={ArrowDown} label="下载" value={formatSpeed(snap?.downloadSpeed ?? 0)} sub={formatBytes(snap?.downloaded ?? 0)} tone="text-primary-text" />
          <Stat icon={ArrowUp} label="上传" value={formatSpeed(snap?.uploadSpeed ?? 0)} sub={formatBytes(snap?.uploaded ?? 0)} tone="text-accent-text" />
          <Stat icon={Users} label="节点" value={String(snap?.numPeers ?? 0)} sub={snap?.peers.some((p) => p.type === "webSeed") ? "含 WebSeed" : "WebRTC"} />
          <Stat
            icon={ListTree}
            label="进度"
            value={formatPercent(file?.progress ?? snap?.progress ?? 0)}
            sub={snap?.done ? "已完成" : snap ? `剩余 ${formatDuration(snap.timeRemaining)}` : "-"}
          />
        </dl>
      </section>

      {/* 侧栏 / 移动端下方：标签页 */}
      <aside className="min-w-0 lg:sticky lg:top-[4.5rem] lg:self-start">
        <Card className="overflow-hidden">
          <Tabs value={tab} onValueChange={setTab}>
            <TabsList className="px-1">
              <TabsTrigger value="files">
                <ListTree />
                文件{snap ? ` ${snap.files.length}` : ""}
              </TabsTrigger>
              <TabsTrigger value="peers">
                <Users />
                节点
              </TabsTrigger>
              <TabsTrigger value="info">
                <Info />
                详情
              </TabsTrigger>
              <TabsTrigger value="log">
                <ScrollText />
                日志
              </TabsTrigger>
            </TabsList>
            <div className="max-h-[60vh] overflow-y-auto lg:max-h-[calc(100dvh-10rem)]">
              <TabsContent value="files">
                {!snap ? (
                  <PanelLoading text="等待种子信息…" />
                ) : (
                  <ul className="divide-y">
                    {sortedFiles.map((f) => (
                      <FileRow key={f.index} f={f} active={f.index === current} onSelect={() => onSelectFile(f)} downloadURL={session?.fileURL(f.index) ?? null} />
                    ))}
                  </ul>
                )}
              </TabsContent>
              <TabsContent value="peers">
                <PeersPanel snap={snap} seededBy={play?.seeded_by ?? []} />
              </TabsContent>
              <TabsContent value="info">
                <InfoPanel snap={snap} infoHash={infoHash} magnet={magnet} torrentURL={session?.torrentFileURL() ?? null} engine={engine} />
              </TabsContent>
              <TabsContent value="log">
                <LogPanel logs={logs} />
              </TabsContent>
            </div>
          </Tabs>
        </Card>
      </aside>

      {infoHash && <ReportDialog open={reportOpen} onOpenChange={setReportOpen} infohash={infoHash} title={title} />}
    </div>
  )
}

function Overlay({ children }: { children: React.ReactNode }) {
  return (
    <div className="absolute inset-0 flex flex-col items-center justify-center gap-2 bg-black/60 px-6 text-center text-sm text-white backdrop-blur-[2px]">
      {children}
    </div>
  )
}

function CodecHint({ codec, severe, noPicture }: { codec: string | null; severe?: boolean; noPicture?: boolean }) {
  return (
    <div
      role={severe ? "alert" : "note"}
      className={cn(
        "mt-3 flex gap-2.5 rounded-lg border px-3 py-2.5 text-[13px] leading-relaxed",
        severe ? "border-warning/40 bg-warning/10" : "border-accent/25 bg-accent-soft",
      )}
    >
      {severe ? <AlertTriangle className="mt-0.5 h-4 w-4 shrink-0 text-warning-text" /> : <Info className="mt-0.5 h-4 w-4 shrink-0 text-accent-text" />}
      <p>
        {severe ? (
          <>
            <strong className="font-semibold">{noPicture ? "只有声音没有画面？" : "视频无法播放"}</strong>
            {" "}
            当前浏览器可能不支持该视频的编码{codec ? `（${codec}）` : "（如 HEVC/H.265、10bit、AC3/DTS 音轨）"}。建议使用最新版 <strong>Chrome</strong> 或 <strong>Edge</strong>，或点击「下载」后用本地播放器观看。
          </>
        ) : (
          <>
            该资源可能使用 <strong className="font-semibold">{codec}</strong> 编码，部分浏览器无法播放。若出现黑屏或无声，请改用最新版 Chrome / Edge，或下载后观看。
          </>
        )}
      </p>
    </div>
  )
}

function Stat({ icon: Icon, label, value, sub, tone }: { icon: typeof Users; label: string; value: string; sub: string; tone?: string }) {
  return (
    <div className="min-w-0 border-r px-1 py-2.5 last:border-r-0 sm:px-3">
      <dt className="flex items-center justify-center gap-1 text-[11px] text-muted-foreground">
        <Icon className={cn("h-3.5 w-3.5", tone)} />
        {label}
      </dt>
      <dd className="mt-0.5 truncate text-[13px] font-semibold tabular-nums sm:text-[15px]">{value}</dd>
      <dd className="truncate text-[10px] tabular-nums text-muted-foreground sm:text-[11px]">{sub}</dd>
    </div>
  )
}

function FileRow({ f, active, onSelect, downloadURL }: { f: FileSnap; active: boolean; onSelect: () => void; downloadURL: string | null }) {
  const Icon = kindIcon[f.kind]
  const playable = isPlayable(f.kind)
  return (
    <li className={cn("relative flex items-center gap-1", active && "bg-primary-soft")}>
      {active && <span aria-hidden className="absolute inset-y-0 left-0 w-0.5 bg-primary" />}
      <button
        type="button"
        onClick={onSelect}
        aria-current={active ? "true" : undefined}
        className="flex min-h-12 min-w-0 flex-1 items-center gap-3 py-2 pl-3 pr-1 text-left hover:bg-muted/60 focus-visible:bg-muted/60 focus-visible:outline-none"
      >
        <Icon className={cn("h-5 w-5 shrink-0", active ? "text-primary-text" : playable ? "text-foreground/70" : "text-muted-foreground")} />
        <span className="min-w-0 flex-1">
          <span className={cn("line-clamp-2 wrap-anywhere text-[13px] leading-snug", active && "font-medium text-primary-text")}>{f.name}</span>
          <span className="mt-1 flex items-center gap-2 text-[11px] tabular-nums text-muted-foreground">
            {formatBytes(f.length)}
            <span className="h-1 w-16 overflow-hidden rounded-full bg-piece-empty">
              <span className={cn("block h-full rounded-full", f.progress >= 1 ? "bg-primary" : "bg-accent")} style={{ width: `${Math.max(2, f.progress * 100)}%` }} />
            </span>
            {formatPercent(f.progress)}
          </span>
        </span>
      </button>
      {downloadURL ? (
        <a
          href={downloadURL}
          download={f.name}
          aria-label={`下载 ${f.name}`}
          className="mr-1.5 inline-flex h-10 w-10 shrink-0 items-center justify-center rounded-md text-muted-foreground hover:bg-muted hover:text-foreground"
        >
          <Download className="h-4 w-4" />
        </a>
      ) : (
        <span className="mr-1.5 inline-flex h-10 w-10 shrink-0 items-center justify-center text-muted-foreground/40" aria-hidden>
          <Download className="h-4 w-4" />
        </span>
      )}
    </li>
  )
}

function PanelLoading({ text }: { text: string }) {
  return (
    <div className="flex items-center justify-center gap-2 py-10 text-[13px] text-muted-foreground">
      <Loader2 className="h-4 w-4 animate-spin text-primary" />
      {text}
    </div>
  )
}

function PeersPanel({ snap, seededBy }: { snap: TorrentSnap | null; seededBy: string[] }) {
  return (
    <div>
      {seededBy.length > 0 && (
        <div className="flex flex-wrap items-center gap-1.5 border-b px-3 py-2.5 text-xs text-muted-foreground">
          <Server className="h-3.5 w-3.5" />
          服务器做种：
          {seededBy.map((s) => (
            <Badge key={s} variant="soft">
              {s}
            </Badge>
          ))}
        </div>
      )}
      {!snap ? (
        <PanelLoading text="正在寻找节点…" />
      ) : snap.peers.length === 0 ? (
        <p className="py-10 text-center text-[13px] text-muted-foreground">暂无连接的节点</p>
      ) : (
        <ul className="divide-y text-[13px]">
          {snap.peers.map((p) => (
            <li key={p.id} className="flex items-center gap-2 px-3 py-2.5">
              <Badge variant={p.type === "webSeed" ? "warning" : "accent-soft"}>{p.type === "webSeed" ? "WebSeed" : "WebRTC"}</Badge>
              <span className="min-w-0 flex-1 truncate font-mono text-xs" title={p.addr}>
                {p.addr}
              </span>
              <span className="shrink-0 text-right text-[11px] tabular-nums text-muted-foreground">
                <span className="text-primary-text">↓ {formatSpeed(p.downloadSpeed)}</span>
                <br />
                <span className="text-accent-text">↑ {formatSpeed(p.uploadSpeed)}</span>
              </span>
            </li>
          ))}
        </ul>
      )}
    </div>
  )
}

function InfoPanel({ snap, infoHash, magnet, torrentURL, engine }: { snap: TorrentSnap | null; infoHash: string | null; magnet: string; torrentURL: string | null; engine: Engine | null }) {
  const copy = async (text: string, what: string) => {
    if (await copyText(text)) toast.success(`${what}已复制`)
    else toast.error("复制失败")
  }
  const rows: [string, React.ReactNode][] = [
    ["名称", snap?.name ?? "-"],
    ["大小", snap ? formatBytes(snap.length) : "-"],
    ["分块", snap ? `${snap.numPieces} × ${formatBytes(snap.pieceLength)}` : "-"],
  ]
  return (
    <div className="space-y-3 p-3 text-[13px]">
      <dl className="space-y-2">
        {rows.map(([k, v]) => (
          <div key={k} className="flex gap-3">
            <dt className="w-10 shrink-0 text-muted-foreground">{k}</dt>
            <dd className="min-w-0 flex-1 wrap-anywhere">{v}</dd>
          </div>
        ))}
        <div className="flex gap-3">
          <dt className="w-10 shrink-0 text-muted-foreground">Hash</dt>
          <dd className="min-w-0 flex-1 wrap-anywhere font-mono text-xs">{infoHash ?? "-"}</dd>
        </div>
      </dl>
      <div className="grid grid-cols-2 gap-2">
        <Button variant="outline" size="sm" onClick={() => copy(magnet, "磁力链接")}>
          <Copy />
          复制磁力
        </Button>
        {torrentURL ? (
          <Button asChild variant="outline" size="sm">
            <a href={torrentURL} download={`${snap?.name ?? infoHash}.torrent`}>
              <Download />
              .torrent
            </a>
          </Button>
        ) : (
          <Button variant="outline" size="sm" disabled>
            <Download />
            .torrent
          </Button>
        )}
        {infoHash && (
          <Button variant="outline" size="sm" onClick={() => copy(infoHash, "Infohash ")}>
            <Copy />
            复制 Hash
          </Button>
        )}
        <Button variant="outline" size="sm" onClick={() => engine?.resetServiceWorker()} title="播放异常时可尝试重新注册 Service Worker（会刷新页面）">
          <RefreshCw />
          重置 SW
        </Button>
      </div>
    </div>
  )
}

function LogPanel({ logs }: { logs: LogLine[] }) {
  const box = useRef<HTMLDivElement>(null)
  useEffect(() => {
    const scroller = box.current?.closest(".overflow-y-auto")
    if (scroller) scroller.scrollTop = scroller.scrollHeight
  }, [logs.length])
  return (
    <div ref={box} className="space-y-1 p-3 font-mono text-[11.5px] leading-relaxed">
      {logs.map((l, i) => (
        <p key={i} className={cn(l.level === "error" ? "text-destructive-text" : l.level === "warn" ? "text-warning-text" : "text-foreground/80")}>
          <span className="mr-2 text-muted-foreground">{new Date(l.t).toLocaleTimeString("zh-CN", { hour12: false })}</span>
          {l.msg}
        </p>
      ))}
    </div>
  )
}
