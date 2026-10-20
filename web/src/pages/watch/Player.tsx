import { useCallback, useEffect, useMemo, useRef, useState } from "react"
import { Link, useLocation } from "react-router-dom"
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
  Share2,
  Users,
  Captions,
  Layers,
  Tv,
} from "lucide-react"
import { loadEngine } from "@/player/engine"
import { isPlayable, likelyUnsupported, type Engine, type FileKind, type FileSnap, type LogLevel, type Session, type TorrentSnap } from "@/player/types"
import { segmentCount } from "@/lib/pieces"
import { ArtVideo, loadArtplayer } from "./ArtVideo"
import { useWebseedEvents } from "./useWebseedEvents"
import { useStallWatchdog } from "./useStallWatchdog"
import { ReportDialog } from "./ReportDialog"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Dialog, DialogContent } from "@/components/ui/dialog"
import { BLEED, SideSection } from "@/pages/Episode"
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs"
import { toast } from "@/components/ui/toast"
import { errorMessage } from "@/components/states"
import { SeedRequests } from "@/lib/seed"
import { api, isApiError } from "@/lib/api"
import { PERM, can, useMe, useSiteOrFallback, useSubject } from "@/lib/queries"
import type { PlayResponse } from "@/lib/types"
import type { PlayHint } from "./usePlayResolve"
import { formatBytes, formatPercent, formatSpeed } from "@/lib/format"
import { cn, copyText, episodeHref, subjectHref, subjectPath } from "@/lib/utils"
import { canonicalEpisodeKey, episodeLabel, parseEpisodeKey } from "@/lib/episode"
import { useDocumentTitle } from "@/lib/hooks"

/** renderInfo 可复用的部件 */
export interface PlayerParts {
  /** 分享 / 下载 / 举报（紧凑图标按钮） */
  actions: React.ReactNode
  /** 下载 / 上传 / 节点 / 进度（单行） */
  stats: React.ReactNode
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

/**
 * 播放器（播放页与单集页共用）。换种子时由父组件以 key={infohash} 重新挂载；
 * 同一种子在播放页 / 单集页之间切换时保持同一实例（见 Playback.tsx），会话与播放进度不变。
 * - fileIndex：要播放的文件（URL /file/{index} 或单集映射）；无效或为空时自动选择
 * - onFileChange：用户在文件列表里切换可播放文件时回调（播放页据此更新 URL）
 */
export function Player({
  magnet,
  play,
  direct,
  hint,
  shareURL,
  fileIndex = null,
  onFileChange,
  onCurrentChange,
  poster: posterProp,
  showMeta = true,
  renderInfo,
  side,
  panelSources,
}: {
  magnet: string
  play: PlayResponse | null
  direct: boolean
  hint?: PlayHint
  shareURL: (infohash: string | null, fileIndex: number | null) => string
  fileIndex?: number | null
  onFileChange?: (index: number) => void
  /** 正在播放的文件变化（含自动选择） */
  onCurrentChange?: (index: number | null) => void
  /** 封面（不传时按 play.meta 查询条目） */
  poster?: string
  /** 是否在标题下显示条目 / 集数链接 */
  showMeta?: boolean
  /** 自定义播放器下方的信息区（替换默认的标题 / 操作 / 统计块），参数为可复用的部件 */
  renderInfo?: (parts: PlayerParts) => React.ReactNode
  /** 右栏（移动端为信息区下方）的内容，如单集页的选集 */
  side?: React.ReactNode
  /** 播放器「资源」弹窗的第一个标签页（如单集页的资源列表）；参数 close 关闭弹窗 */
  panelSources?: (close: () => void) => React.ReactNode
}) {
  const site = useSiteOrFallback()
  const [engine, setEngine] = useState<Engine | null>(null)
  const [session, setSession] = useState<Session | null>(null)
  const [snap, setSnap] = useState<TorrentSnap | null>(null)
  const [streaming, setStreaming] = useState<boolean | null>(null)
  const [current, setCurrent] = useState<number | null>(null)
  const [segments, setSegments] = useState<Uint8Array>(() => new Uint8Array(1))
  const [mediaError, setMediaError] = useState<string | null>(null)
  /** 会话无法继续（如播放器内核不兼容） */
  const [fatal, setFatal] = useState<string | null>(null)
  const [noPicture, setNoPicture] = useState(false)
  const [reportOpen, setReportOpen] = useState(false)
  const [tab, setTab] = useState(panelSources ? "sources" : side ? "files" : "peers")
  // 「资源」弹窗：资源 / 文件 / 节点 / 详情 / 日志（页面上不再常驻）
  const [panelOpen, setPanelOpen] = useState(false)
  const openPanel = useCallback(() => setPanelOpen(true), [])
  const playerBox = useRef<HTMLDivElement>(null)
  // 播放器全屏时弹窗要挂在全屏元素里，否则看不到
  const [fsHost, setFsHost] = useState<HTMLElement | null>(null)
  useEffect(() => {
    const on = () => {
      const el = document.fullscreenElement as HTMLElement | null
      setFsHost(el && playerBox.current?.contains(el) ? el : null)
    }
    document.addEventListener("fullscreenchange", on)
    return () => document.removeEventListener("fullscreenchange", on)
  }, [])
  /** 有 WebSeed 权限且已启用：连接 SSE 等待服务器就绪 */
  const [webseedArmed, setWebseedArmed] = useState(false)
  /** ArtPlayer 内部的 <video>（看门狗用） */
  const videoRef = useRef<HTMLVideoElement | null>(null)
  const setVideo = useCallback((el: HTMLVideoElement | null) => {
    videoRef.current = el
  }, [])
  const autoPicked = useRef(false)
  const loc = useLocation()
  const here = `${loc.pathname}${loc.search}`

  // 接口权限：服务器做种 / WebSeed / 举报
  const me = useMe()
  const meReady = !!me.data
  const loggedIn = !!me.data?.user
  const canSeed = can(me.data, PERM.seed)
  const canWebseed = can(me.data, PERM.webseed)
  const canReport = can(me.data, PERM.report)
  const canTorrentFile = can(me.data, PERM.torrentFile) && can(me.data, PERM.torrentStatus)

  const meta = play?.meta ?? null
  const metaPath = meta ? subjectPath(meta) : null
  // 集的键（旧接口只有 episode 数字时退回该数字）
  const metaKey = meta ? (canonicalEpisodeKey(meta.episode_key) ?? (meta.episode !== null && meta.episode >= 0 ? String(meta.episode) : null)) : null
  const subject = useSubject(posterProp === undefined ? (metaPath?.provider ?? "") : "", metaPath?.id ?? "")
  const poster = (posterProp !== undefined ? posterProp : subject.data?.subject.cover) || undefined
  const wantFile = useRef<number | null>(fileIndex)

  // 播放日志只输出到浏览器控制台（F12），页面上不显示
  const log = useCallback((level: LogLevel, msg: string) => {
    const out = level === "error" ? console.error : level === "warn" ? console.warn : console.info
    out(`[player] ${msg}`)
  }, [])

  // 预加载 ArtPlayer（单独的 chunk），通常在种子元数据就绪前完成
  useEffect(() => {
    loadArtplayer().catch(() => {})
  }, [])

  // 启动引擎
  useEffect(() => {
    let cancelled = false
    let s: Session | null = null
    const unsubs: (() => void)[] = []
    autoPicked.current = false
    setSnap(null)
    setCurrent(null)
    setFatal(null)
    log("info", direct ? "服务器不可用，直接通过 P2P 网络连接" : "服务器已接受请求")
    loadEngine()
      .then(async (eng) => {
        if (cancelled) return
        setEngine(eng)
        log("info", "正在连接 tracker，寻找节点…")
        s = await eng.start(magnet, {
          announce: site.trackers,
          iceServers: site.ice_servers as RTCIceServer[],
          maxCacheBytes: site.max_push_size_bytes,
        })
        if (cancelled) return s.destroy()
        unsubs.push(s.on("log", log))
        unsubs.push(s.on("fatal", setFatal))
        const f0 = s.fatalError?.()
        if (f0) setFatal(f0)
        setSession(s)
        const ok = await eng.streamingReady()
        if (!cancelled) {
          setStreaming(ok)
          if (!ok && eng.kind === "webtorrent") log("warn", "Service Worker 不可用，无法边下边播（需要 HTTPS 且浏览器支持 Service Worker）")
        }
      })
      .catch((e) => {
        const msg = String(e?.message ?? e)
        console.error("[player]", e)
        log("error", `播放器启动失败：${msg}`)
        if (!cancelled && e?.name === "IncompatibleError") setFatal(msg)
      })
    return () => {
      cancelled = true
      unsubs.forEach((u) => u())
      s?.destroy()
      setSession(null)
    }
    // site 配置变化不重启种子
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [magnet])

  // 引擎启动后订阅 WebSeed 就绪推送（SSE 自己会等元数据与服务器文件就绪，就绪即加入，不再额外延迟）。
  // 服务器做种在选定文件后请求（见下方 SeedRequests）。
  const ih = play?.infohash ?? null
  useEffect(() => {
    if (!session || direct || !ih || !meReady) return
    // WebSeed 只由边缘节点提供：地址与访问令牌都由 SSE 推送（ready / seed / token）后才加入
    setWebseedArmed(canWebseed && !!site.webseed?.enabled)
  }, [session, direct, ih, meReady, canWebseed, site.webseed?.enabled])

  // 元数据兜底：磁力没有 xs=（打开时服务器还没有该种子的信息，如刚粘贴的磁力）或 xs 失败时，
  // 请求做种后轮询收录状态，ready 后下载 .torrent 交给 WebTorrent（无需 WebRTC 节点提供 ut_metadata）
  const hasMeta = !!snap
  useEffect(() => {
    if (!session?.loadTorrentFile || hasMeta || direct || !ih || !meReady || !canSeed || !canTorrentFile) return
    const load = session.loadTorrentFile.bind(session)
    let cancelled = false
    let timer = 0
    const ctrl = new AbortController()
    const started = Date.now()
    const tick = async () => {
      if (cancelled || session.snapshot()) return
      try {
        const st = await api.torrentStatus(ih, ctrl.signal)
        if (cancelled) return
        if (st.status === "ready") {
          const buf = await api.torrentFile(ih, ctrl.signal)
          if (cancelled || session.snapshot()) return
          if (await load(buf)) {
            log("info", "已从服务器获取种子信息")
            return
          }
        }
      } catch (e) {
        if (cancelled) return
        // 没有权限 / 被策略拦截：不再重试；404（服务器暂无种子文件）与网络错误稍后重试
        if (isApiError(e) && [401, 402, 403, 451].includes(e.status)) return
      }
      if (Date.now() - started < 3 * 60_000) timer = window.setTimeout(tick, 3000)
    }
    // 先给 xs= / P2P 一点时间
    timer = window.setTimeout(tick, 3000)
    return () => {
      cancelled = true
      ctrl.abort()
      window.clearTimeout(timer)
    }
  }, [session, hasMeta, direct, ih, meReady, canSeed, canTorrentFile, log])

  // 轮询状态
  useEffect(() => {
    if (!session) return
    const update = () => {
      const sn = session.snapshot()
      setSnap(sn)
      if (sn && !autoPicked.current) {
        autoPicked.current = true
        const want = wantFile.current
        const first = want !== null && sn.files[want] && isPlayable(sn.files[want].kind) ? want : pickInitial(sn.files)
        if (first !== null) setCurrent(first)
      }
    }
    update()
    const t = window.setInterval(update, 1000)
    return () => window.clearInterval(t)
  }, [session])

  useEffect(() => {
    if (!session || current === null) return
    const up = () => {
      const sn = session.snapshot()
      const f = sn?.files[current]
      setSegments(session.segments(current, f && sn ? segmentCount(f.length, sn.pieceLength) : 1))
    }
    up()
    const t = window.setInterval(up, 1000)
    return () => window.clearInterval(t)
  }, [session, current])

  // 外部指定的文件变化（URL 前进/后退、单集页切换同一种子内的文件）
  useEffect(() => {
    wantFile.current = fileIndex
    if (fileIndex === null) return
    const f = session?.snapshot()?.files[fileIndex]
    if (f && isPlayable(f.kind)) setCurrent(fileIndex)
  }, [fileIndex, session])

  // 服务器做种：qB 只下载正在播放的文件。选定文件后请求一次，切到同一种子的另一个文件时再请求；
  // 失败只记日志，不影响播放（SeedRequests 决定是否重试）
  const seedReqs = useRef<{ session: Session | null; reqs: SeedRequests }>({ session: null, reqs: new SeedRequests() })
  useEffect(() => {
    if (!session || direct || !ih || !meReady || !canSeed || current === null) return
    if (seedReqs.current.session !== session) seedReqs.current = { session, reqs: new SeedRequests() }
    const reqs = seedReqs.current.reqs
    const file = current
    if (!reqs.begin(file)) return
    void (async () => {
      try {
        const r = await api.seed(ih, file)
        reqs.end(file, r.seeded ? "seeded" : "no_server")
        if (r.seeded) log("info", "已请求服务器下载并做种当前文件")
        else log("warn", "服务器暂无可用的做种节点，仅使用 P2P 网络")
      } catch (e) {
        reqs.end(file, SeedRequests.outcomeOfError(isApiError(e) ? e.status : undefined))
        log("warn", `服务器做种未启用：${errorMessage(e)}`)
      }
    })()
  }, [session, direct, ih, meReady, canSeed, current, log])

  const onCurrentRef = useRef(onCurrentChange)
  onCurrentRef.current = onCurrentChange
  useEffect(() => onCurrentRef.current?.(current), [current])

  const file = current !== null ? snap?.files[current] : undefined
  // 只下载正在播放的文件（合集里其余文件不下载）；元数据就绪前调用会在就绪后生效
  useEffect(() => {
    session?.focusFile?.(current)
  }, [session, current])
  const url = session && current !== null && streaming ? session.fileURL(current) : null
  useWebseedEvents({
    session,
    ih,
    armed: webseedArmed,
    // 元数据就绪后再连接；多文件种子带上正在播放的文件，单文件省略
    fileIndex: snap ? (snap.files.length > 1 ? current : null) : undefined,
    done: !!snap?.done,
    log,
  })
  // WebTorrent SW 流的请求偶尔永久挂起：检测到卡死且数据已在本地时自动重新加载
  useStallWatchdog({ video: videoRef, url: file?.kind === "video" ? url : null, segments, log })
  const unsupported = file ? likelyUnsupported(file.name) || likelyUnsupported(snap?.name ?? "") : null
  useDocumentTitle(file?.name ?? snap?.name ?? play?.name ?? hint?.title ?? "播放")

  useEffect(() => {
    setMediaError(null)
    setNoPicture(false)
  }, [current])

  const onLoadedMetadata = useCallback(
    (v: HTMLVideoElement) => {
      if (v.videoWidth === 0 && v.duration > 0) setNoPicture(true)
      session?.setMediaDuration?.(v.duration)
    },
    [session],
  )
  const onMediaError = useCallback(
    (err: MediaError) => {
      setMediaError(err.message || `MediaError ${err.code}`)
      log("error", `视频解码失败：${err.message || `code ${err.code}`}`)
    },
    [log],
  )

  const infoHash = snap?.infoHash ?? play?.infohash ?? hint?.infohash ?? null
  const title = snap?.name ?? play?.name ?? hint?.title ?? "正在获取种子信息…"

  async function share() {
    const link = shareURL(infoHash, current)
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
      setPanelOpen(false)
      setCurrent(f.index)
      onFileChange?.(f.index)
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
  // 文件列表：种子播放页在右栏，单集页在「资源」弹窗里
  const filesInSide = !side
  const fileList = !snap ? (
    <PanelLoading text="等待种子信息…" />
  ) : (
    <ul className="divide-y">
      {sortedFiles.map((f) => (
        <FileRow key={f.index} f={f} active={f.index === current} onSelect={() => onSelectFile(f)} downloadURL={session?.fileURL(f.index) ?? null} />
      ))}
    </ul>
  )

  const loginHref = `/login?next=${encodeURIComponent(here)}`
  const compactActions = (
    <div className="flex shrink-0 items-center sm:gap-1">
      <ActionButton icon={Share2} label="分享" onClick={share} />
      {file && url ? (
        <ActionButton icon={Download} label="下载" href={url} download={file.name} />
      ) : (
        <ActionButton icon={Download} label="下载" disabled />
      )}
      {canReport ? (
        <ActionButton icon={Flag} label="举报" onClick={() => setReportOpen(true)} disabled={!infoHash} />
      ) : meReady && !loggedIn ? (
        <ActionButton icon={Flag} label="举报" to={loginHref} title="登录后可举报该资源" />
      ) : null}
    </div>
  )
  const compactStats = (
    <div className="flex min-w-0 flex-wrap items-center gap-x-3.5 gap-y-1 text-[13px] tabular-nums text-muted-foreground">
      <span className="inline-flex items-center gap-1">
        <Users className="h-3.5 w-3.5" />
        {snap?.numPeers ?? 0}
      </span>
      <span className="inline-flex items-center gap-0.5" title={`已下载 ${formatBytes(snap?.downloaded ?? 0)}`}>
        <ArrowDown className="h-3.5 w-3.5 text-primary-text" />
        {formatSpeed(snap?.downloadSpeed ?? 0)}
      </span>
      <span className="inline-flex items-center gap-0.5" title={`已上传 ${formatBytes(snap?.uploaded ?? 0)}`}>
        <ArrowUp className="h-3.5 w-3.5 text-accent-text" />
        {formatSpeed(snap?.uploadSpeed ?? 0)}
      </span>
      {direct && <Badge variant="warning">直连模式</Badge>}
    </div>
  )

  return (
    <div className="grid gap-4 lg:grid-cols-[minmax(0,1fr)_360px] lg:gap-5 xl:grid-cols-[minmax(0,1fr)_400px]">
      <section className="min-w-0">
        {/* 播放区：移动端吸顶、全宽 */}
        <div ref={playerBox} className="sticky top-[env(safe-area-inset-top)] z-20 -mx-3 bg-black sm:-mx-4 sm:top-14 lg:static lg:mx-0 lg:overflow-hidden lg:rounded-xl">
          <div className="relative aspect-video w-full overflow-hidden bg-black select-none">
            {/* 未出画面时：竖版封面两侧用模糊背景填满，避免大片纯黑 */}
            {poster && !url && (
              <>
                <div
                  aria-hidden
                  className="absolute inset-0 scale-110 bg-cover bg-center opacity-50 blur-2xl"
                  style={{ backgroundImage: `url("${poster}")` }}
                />
                <div aria-hidden className="absolute inset-0 bg-contain bg-center bg-no-repeat" style={{ backgroundImage: `url("${poster}")` }} />
              </>
            )}
            {/* 状态提示下的暗层（在播放器之下，控制栏不被压暗） */}
            {!url && <div aria-hidden className="absolute inset-0 bg-black/55" />}
            {file?.kind === "video" || !file ? (
              // 还没有视频地址时也先挂上播放器（空 url）：控制栏与「资源」按钮始终可用
              <ArtVideo
                key={url ?? "idle"}
                url={url ?? ""}
                poster={url ? poster : undefined}
                segments={segments}
                speed={snap?.downloadSpeed ?? 0}
                onVideo={url ? setVideo : undefined}
                onLoadedMetadata={onLoadedMetadata}
                onMediaError={onMediaError}
                onPanel={openPanel}
                subtitles={url && current !== null ? (session?.subtitles?.(current) ?? null) : null}
                onLog={log}
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

            {/* 非视频文件（音频 / 图片）没有 ArtPlayer 控制栏：右上角的「资源」按钮 */}
            {file && file.kind !== "video" && (
              <button
                type="button"
                onClick={openPanel}
                className="absolute right-2 top-2 z-20 inline-flex h-8 items-center gap-1 rounded-md bg-black/55 px-2.5 text-[13px] text-white hover:bg-black/70"
              >
                <Layers className="h-4 w-4" />
                资源
              </button>
            )}
            {/* 状态浮层 */}
            {fatal ? (
              <Overlay>
                <AlertTriangle className="h-8 w-8 text-warning" />
                <p className="font-medium">无法播放</p>
                <p className="max-w-sm wrap-anywhere text-xs text-white/70">{fatal}</p>
              </Overlay>
            ) : !url && (
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
          </div>
        </div>

        {/* 编码提示 */}
        {(mediaError || noPicture) && file?.kind === "video" ? (
          <CodecHint severe codec={unsupported} noPicture={noPicture} />
        ) : unsupported && file?.kind === "video" ? (
          <CodecHint codec={unsupported} />
        ) : null}

        {renderInfo ? (
          renderInfo({ actions: compactActions, stats: compactStats })
        ) : (
          // 种子播放页：与单集页同样的布局（标题 · 信息行 · 统计与操作一行）
          <div className="pt-3 lg:pt-4">
            <h1 className="line-clamp-2 wrap-anywhere text-base font-semibold leading-snug lg:text-xl" title={title}>
              {title}
            </h1>
            <div className="mt-1 flex flex-wrap items-center gap-x-2 gap-y-1 text-xs text-muted-foreground lg:text-[13px]">
              {hint?.team && <Badge variant="accent-soft">{hint.team}</Badge>}
              {showMeta && meta && metaPath && (
                <span className="inline-flex h-6 items-center overflow-hidden rounded bg-primary-soft font-medium text-primary-text">
                  <Link to={subjectHref(meta)} className="inline-flex h-full items-center gap-1 px-1.5 hover:underline" title="番剧详情">
                    <Tv className="h-3 w-3" />
                    {subject.data ? subject.data.subject.title_cn || subject.data.subject.title : meta.title || "番剧详情"}
                  </Link>
                  {metaKey && (
                    <Link to={episodeHref(meta, metaKey)} className="inline-flex h-full items-center border-l border-primary/20 px-1.5 hover:underline" title="本集所有资源">
                      {meta.episode_label || episodeLabel(parseEpisodeKey(metaKey)!)}
                    </Link>
                  )}
                </span>
              )}
              {snap && (
                <span className="tabular-nums">
                  {formatBytes(snap.length)} · {snap.files.length} 个文件
                </span>
              )}
              {direct && <Badge variant="warning">直连模式</Badge>}
            </div>
            <div className="mt-2 flex items-center justify-between gap-2 border-b pb-2 lg:mt-3 lg:pb-3">
              {compactStats}
              <div className="-mr-2">{compactActions}</div>
            </div>
          </div>
        )}
      </section>

      <aside className="min-w-0 space-y-4 lg:space-y-5">
        {side ?? (
          <SideSection id="files-title" icon={ListTree} title="文件" extra={snap ? `${snap.files.length} 个` : undefined}>
            <div className={cn(BLEED, "border-y lg:-mx-4 lg:-mb-4 lg:max-h-[min(70vh,40rem)] lg:overflow-y-auto lg:border-b-0")}>{fileList}</div>
          </SideSection>
        )}
      </aside>

      {/* 「资源」弹窗（全屏时挂在全屏元素里） */}
      <Dialog open={panelOpen} onOpenChange={setPanelOpen}>
        <DialogContent container={fsHost} title="资源与播放详情" className="gap-3 sm:max-w-2xl">
          <Tabs value={tab} onValueChange={setTab} className="-mx-5 -mb-5 min-h-0 min-w-0">
            <TabsList className="overflow-x-auto px-3 scrollbar-none">
              {panelSources && (
                <TabsTrigger value="sources">
                  <Layers />
                  资源
                </TabsTrigger>
              )}
              {!filesInSide && (
                <TabsTrigger value="files">
                  <ListTree />
                  文件{snap ? ` ${snap.files.length}` : ""}
                </TabsTrigger>
              )}
              <TabsTrigger value="peers">
                <Users />
                节点
              </TabsTrigger>
              <TabsTrigger value="info">
                <Info />
                详情
              </TabsTrigger>
            </TabsList>
            <div className="h-[min(60dvh,32rem)] overflow-y-auto">
              {panelSources && <TabsContent value="sources">{panelSources(() => setPanelOpen(false))}</TabsContent>}
              {!filesInSide && <TabsContent value="files">{fileList}</TabsContent>}
              <TabsContent value="peers">
                <PeersPanel snap={snap} />
              </TabsContent>
              <TabsContent value="info">
                <InfoPanel snap={snap} infoHash={infoHash} magnet={magnet} torrentURL={session?.torrentFileURL() ?? null} />
              </TabsContent>
            </div>
          </Tabs>
        </DialogContent>
      </Dialog>

      {infoHash && canReport && <ReportDialog open={reportOpen} onOpenChange={setReportOpen} infohash={infoHash} title={title} />}
    </div>
  )
}

function Overlay({ children }: { children: React.ReactNode }) {
  return (
    // 不盖住底部控制栏（「资源」等按钮仍可点），点击穿透到播放器
    <div className="pointer-events-none absolute inset-x-0 bottom-12 top-0 z-10 flex flex-col items-center justify-center gap-2 px-6 text-center text-sm text-white">
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

/** 哔哩哔哩式的紧凑操作按钮：移动端图标在上、文字在下；桌面端图标在左 */
function ActionButton({
  icon: Icon,
  label,
  onClick,
  href,
  download,
  to,
  title,
  disabled,
}: {
  icon: typeof Users
  label: string
  onClick?: () => void
  href?: string
  download?: string
  to?: string
  title?: string
  disabled?: boolean
}) {
  const cls = cn(
    // 手机：只有图标（文字给读屏 / 悬停提示）；sm 以上：图标 + 文字横排
    "inline-flex h-9 w-9 items-center justify-center rounded-md text-foreground/75 transition-colors hover:bg-muted hover:text-primary-text focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring sm:w-auto sm:gap-1.5 sm:px-2.5 sm:text-[13px]",
    disabled && "pointer-events-none opacity-45",
  )
  const inner = (
    <>
      <Icon className="h-5 w-5 sm:h-[18px] sm:w-[18px]" strokeWidth={1.8} />
      <span className="sr-only sm:not-sr-only">{label}</span>
    </>
  )
  if (to)
    return (
      <Link to={to} className={cls} title={title ?? label}>
        {inner}
      </Link>
    )
  if (href)
    return (
      <a href={href} download={download} className={cls} title={title ?? label}>
        {inner}
      </a>
    )
  return (
    <button type="button" onClick={onClick} disabled={disabled} className={cls} title={title ?? label}>
      {inner}
    </button>
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

function PeersPanel({ snap }: { snap: TorrentSnap | null }) {
  return (
    <div>
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

function InfoPanel({ snap, infoHash, magnet, torrentURL }: { snap: TorrentSnap | null; infoHash: string | null; magnet: string; torrentURL: string | null }) {
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
      </div>
    </div>
  )
}

/**
 * 还没有播放会话时（请求中 / 需登录 / 无权播放…）的播放器外壳：与 Player 同样的画面区域与控制栏
 * （空的 ArtPlayer，「资源」按钮可用），children 为画面中央的状态提示。
 */
export function PlayerShell({ poster, panelSources, children }: { poster?: string; panelSources?: (close: () => void) => React.ReactNode; children?: React.ReactNode }) {
  const [open, setOpen] = useState(false)
  const openPanel = useCallback(() => setOpen(true), [])
  const box = useRef<HTMLDivElement>(null)
  const [fsHost, setFsHost] = useState<HTMLElement | null>(null)
  useEffect(() => {
    const on = () => {
      const el = document.fullscreenElement as HTMLElement | null
      setFsHost(el && box.current?.contains(el) ? el : null)
    }
    document.addEventListener("fullscreenchange", on)
    return () => document.removeEventListener("fullscreenchange", on)
  }, [])
  return (
    <div ref={box} className="sticky top-[env(safe-area-inset-top)] z-20 -mx-3 bg-black sm:-mx-4 sm:top-14 lg:static lg:mx-0 lg:overflow-hidden lg:rounded-xl">
      <div className="relative aspect-video w-full overflow-hidden bg-black select-none">
        {poster && (
          <>
            <div aria-hidden className="absolute inset-0 scale-110 bg-cover bg-center opacity-50 blur-2xl" style={{ backgroundImage: `url("${poster}")` }} />
            <div aria-hidden className="absolute inset-0 bg-contain bg-center bg-no-repeat" style={{ backgroundImage: `url("${poster}")` }} />
          </>
        )}
        <div aria-hidden className="absolute inset-0 bg-black/65" />
        <ArtVideo url="" segments={EMPTY_SEGMENTS} speed={0} onPanel={panelSources ? openPanel : undefined} />
        {/* 状态提示：不盖住底部控制栏；按钮可点 */}
        <div className="pointer-events-none absolute inset-x-0 bottom-12 top-0 z-10 flex items-center justify-center [&_a]:pointer-events-auto [&_button]:pointer-events-auto">
          {children}
        </div>
      </div>
      {panelSources && (
        <Dialog open={open} onOpenChange={setOpen}>
          <DialogContent container={fsHost} title="资源" className="gap-3 sm:max-w-2xl">
            <div className="-mx-5 -mb-5 h-[min(60dvh,32rem)] min-w-0 overflow-y-auto border-t">{panelSources(() => setOpen(false))}</div>
          </DialogContent>
        </Dialog>
      )}
    </div>
  )
}

const EMPTY_SEGMENTS = new Uint8Array(1)
