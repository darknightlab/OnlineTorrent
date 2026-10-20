// 播放页与单集页共用的路由组件：
//   /watch/hash/:ih[/file/:index]
//   /subject/:provider/:id/episode/:key[/hash/:ih[/file/:index]]（key：集的键，见 lib/episode.ts）
// 这几条路由在 App.tsx 中渲染同一个元素（同一 lazy 组件、同一层级），React Router 不给路由元素加 key，
// 因此在它们之间导航时本组件不会重新挂载。播放器始终渲染在 <Container><Player key={infohash} /></Container>
// 这一固定位置，所以「播放页 → 单集页（同一 infohash）」或「单集页切换同一种子内的文件」时 Player 实例、
// WebTorrent 会话与 <video> 元素都保持不变，播放进度不会中断。
import { useCallback, useEffect, useMemo, useRef, useState } from "react"
import { Navigate, useLocation, useNavigate, useParams } from "react-router-dom"
import { useQuery } from "@tanstack/react-query"
import { Container } from "@/components/layout/Container"
import { Card } from "@/components/ui/card"
import { ErrorState } from "@/components/states"
import { api, isApiError } from "@/lib/api"
import { PERM, can, qk, useEpisode, useMe, useSite, useSiteOrFallback } from "@/lib/queries"
import { DEFAULT_RANK_RULES, rankFiles, type Scored } from "@/lib/rank"
import { displayTitle } from "@/lib/schedule"
import type { EpisodeFile, EpisodeResponse, TorrentStatus } from "@/lib/types"
import { useDocumentTitle } from "@/lib/hooks"
import { useEpisodeItem, episodeFullLabel } from "@/components/EpisodeGrid"
import { canonicalEpisodeKey, formatEpisodeKey, parseEpisodeKey, type EpisodeKey } from "@/lib/episode"
import { episodeSourceHref, normalizeInfohash, watchHref } from "@/lib/utils"
import { StatusPage } from "@/pages/NotFound"
import {
  EmptyEpisode,
  EpisodeInfo,
  EpisodeInfoSkeleton,
  EpisodeList,
  EpisodeSideSkeleton,
  EpisodeSkeleton,
  FilesSkeleton,
  GRID,
  SourceList,
  sourceKey,
  STATS_SKELETON,
  WatchInfoSkeleton,
} from "@/pages/Episode"
import { episodeRedirect } from "@/lib/playresolve"
import { usePlayAttempt, usePlayResolve, type PlayHint } from "./usePlayResolve"
import { PlayGate } from "./PlayGate"
import { Player, PlayerShell } from "./Player"
import { hintOf, type WatchLocationState } from "./Watch"

/** 做种后轮询收录状态：间隔与最长时间 */
const POLL_MS = 3_000
const POLL_MAX_MS = 3 * 60_000

const seg = encodeURIComponent

export default function PlaybackPage() {
  const params = useParams()
  const loc = useLocation()
  const navigate = useNavigate()
  const st = (loc.state as WatchLocationState | null) ?? {}
  const site = useSiteOrFallback()
  const attempt = usePlayAttempt()

  // ---- 解析路由 ----
  const isEpisode = params.key !== undefined
  const provider = params.provider ?? ""
  const subjectId = params.id ?? ""
  // 集的键：正片 "12" / "12.5" / "0"，特别篇 "sp1" / "ncop1" / "nced1" / "pv1" / "cm1"
  const rawKey = params.key
  const ep = useMemo(() => (rawKey !== undefined ? parseEpisodeKey(rawKey) : null), [rawKey])
  const epKey = ep ? formatEpisodeKey(ep) : null
  const rawIh = params.ih
  const urlIh = rawIh !== undefined ? normalizeInfohash(rawIh) : null
  const badIndex = params.index !== undefined && !/^\d+$/.test(params.index)
  const urlIndex = params.index !== undefined && !badIndex ? Number(params.index) : null
  /** 单集页路径前缀（保持 URL 中的 provider/id 写法） */
  const epBase = isEpisode ? `/subject/${seg(provider)}/${seg(subjectId)}/episode/${seg(epKey ?? rawKey ?? "")}` : ""
  const sourceHref = useCallback(
    (ih: string, idx: number | null) => (isEpisode ? `${epBase}/hash/${seg(ih)}${idx !== null ? `/file/${idx}` : ""}` : watchHref(ih, idx)),
    [isEpisode, epBase],
  )

  // ---- 单集数据 ----
  const epQ = useEpisode(isEpisode ? provider : "", subjectId, epKey)
  const epData = epQ.data

  // ---- 推荐排序（lib/rank.ts）：依赖当前用户权限与站点规则，二者加载完才自动选择 ----
  const me = useMe()
  const siteQ = useSite()
  const canSeed = can(me.data, PERM.seed)
  const canWebseed = can(me.data, PERM.webseed)
  const rankReady = !me.isPending && !siteQ.isPending
  const ranked = useMemo<Scored<EpisodeFile>[]>(
    () => rankFiles(epData?.items ?? [], site.rank_rules ?? DEFAULT_RANK_RULES, { canSeed, canWebseed }),
    [epData, site.rank_rules, canSeed, canWebseed],
  )
  const items = useMemo(() => ranked.map((r) => r.file), [ranked])

  // ---- 要播放的资源：URL 指定，否则单集页自动推荐（排序后的第一个）。
  // 选中后固定（做种人数刷新导致重新排序时不切换正在播放的资源），换集或身份变化时重新选择 ----
  const pickScope = `${provider}/${subjectId}/${epKey}/${canSeed}/${canWebseed}`
  const pickRef = useRef<{ scope: string; key: string } | null>(null)
  let auto: EpisodeFile | null = null
  if (isEpisode && !urlIh && rankReady && items.length > 0) {
    const prev = pickRef.current?.scope === pickScope ? items.find((f) => sourceKey(f) === pickRef.current!.key) : undefined
    // 后端只返回当前用户可播放的资源；管理员能看到全部（含被屏蔽的），自动播放跳过这些
    auto = prev ?? items.find((f) => f.access.action === "allow") ?? items[0]
  }
  useEffect(() => {
    if (auto) pickRef.current = { scope: pickScope, key: sourceKey(auto) }
  }, [auto, pickScope])
  const ih = urlIh ?? auto?.infohash ?? null
  const fileIndex = urlIh ? urlIndex : (auto?.file_index ?? null)
  const listed = urlIh ? (items.find((f) => f.infohash === urlIh && (urlIndex === null || f.file_index === urlIndex)) ?? null) : auto
  const hint: PlayHint | undefined = listed
    ? { infohash: listed.infohash, title: listed.torrent_title, team: listed.team || null }
    : st.torrent?.infohash === ih
      ? hintOf(st.torrent)
      : undefined
  const { state, retry } = usePlayResolve(ih ? { kind: "hash", ih } : null, {
    hint,
    trackers: site.trackers,
    attempt,
    preload: urlIh ? st.preload : undefined,
    // 自动推荐的资源已知 access（非 allow 时直接提示，不发请求）；URL 指定的以服务端判定为准
    known: auto?.access,
    file: fileIndex,
  })

  // 新种子判定后已映射（redirect），或从播放页打开一个已映射的种子：跳到对应单集页。
  // 同一路由组件、同一 Player 实例，跳转后就地继续（redirect 时随后重新请求 play 拿磁力）
  const redirectTo =
    state.status === "redirect" ? state.path : !isEpisode && state.status === "ready" && ih ? episodeRedirect(state.play?.meta, ih, urlIndex) : null
  useEffect(() => {
    if (redirectTo) navigate(redirectTo, { replace: true, state: loc.state })
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [redirectTo])

  // 正在播放的文件（Player 回报，含自动选择）
  const [playing, setPlaying] = useState<number | null>(null)
  useEffect(() => setPlaying(null), [ih])

  const ready = state.status === "ready"
  useAutoEpisodeSwitch({
    ih: !isEpisode && ready && !state.direct ? ih : null,
    playing,
    onMapped: (href) => navigate(href, { replace: true, state: loc.state }),
  })

  const epItem = useEpisodeItem(epData?.subject, epKey)
  useDocumentTitle(isEpisode && epData && ep ? `${displayTitle(epData.subject)} ${episodeFullLabel(ep, epItem?.title)}` : isEpisode ? "单集" : undefined)

  // ---- 校验 / 规范化 ----
  if (rawIh !== undefined && (!urlIh || badIndex)) return <StatusPage code="404" title="链接无效" description="播放链接格式有误，请检查 infohash。" />
  if (isEpisode && !ep) return <StatusPage code="404" title="集数无效" description="链接中的集数格式有误。" />
  // 集的键统一为规范写法（"SP01" → "sp1"、"ncop" → "ncop1"、"012" → "12"）
  if (isEpisode && rawKey !== epKey)
    return <Navigate replace to={`${epBase}${urlIh ? `/hash/${seg(urlIh)}${urlIndex !== null ? `/file/${urlIndex}` : ""}` : ""}${loc.search}`} state={loc.state} />
  // 统一为小写 hex（base32 / 大写链接）
  if (urlIh && rawIh !== urlIh) return <Navigate replace to={sourceHref(urlIh, urlIndex)} state={loc.state} />

  const here = `${loc.pathname}${loc.search}`
  // 等待播放会话时：统计行（单集页）、信息与文件列表（播放页）的骨架；需登录 / 无权播放等结果不显示
  const waiting = state.status === "loading" || state.status === "pending" || state.status === "redirect"

  // ================= 播放页 =================
  if (!isEpisode) {
    const shareURL = (h: string | null, idx: number | null) => `${location.origin}${h ? watchHref(h, idx) : here}`
    return (
      <Container className="pb-6 lg:py-5">
        {ready ? (
          <Player
            key={ih}
            magnet={state.magnet}
            play={state.play}
            direct={state.direct}
            hint={hint}
            shareURL={shareURL}
            fileIndex={fileIndex}
            onFileChange={(i) => i !== urlIndex && navigate(watchHref(ih!, i), { replace: true, state: loc.state })}
            onCurrentChange={setPlaying}
          />
        ) : (
          // 还没有播放会话：同样的播放器外壳与布局（就绪后右栏是文件列表），状态提示在画面里
          <div className={GRID}>
            <section className="min-w-0">
              <PlayerShell>{state.status !== "idle" && <PlayGate overlay state={state} title={hint?.title} here={here} retry={retry} />}</PlayerShell>
              {waiting && <WatchInfoSkeleton />}
            </section>
            {waiting && (
              <aside className="min-w-0">
                <FilesSkeleton />
              </aside>
            )}
          </div>
        )}
      </Container>
    )
  }

  // ================= 单集页 =================
  if (!urlIh) {
    if (epQ.isPending || (items.length > 0 && !rankReady))
      return (
        <Container className="pb-6 lg:py-5">
          <EpisodeSkeleton />
        </Container>
      )
    if (epQ.error) return <EpisodeError error={epQ.error} retry={() => epQ.refetch()} />
    if (items.length === 0)
      return (
        <Container className="pb-6 lg:py-5">
          <EmptyEpisode data={epData!} ep={ep!} />
        </Container>
      )
  }

  const selected = ih ? (listed ? sourceKey(listed) : sourceKey({ infohash: ih, file_index: fileIndex ?? playing ?? -1 })) : null
  const select = (f: EpisodeFile) => {
    if (sourceKey(f) === selected) return
    navigate(`${epBase}/hash/${seg(f.infohash)}/file/${f.file_index}`, { replace: true })
    window.scrollTo({ top: 0, behavior: "smooth" })
  }
  const sideProps = epData
    ? { data: epData, ranked, ep: ep!, ih, fileIndex: fileIndex ?? playing, listed: !!listed, playName: ready ? state.play?.name : undefined, hint, selected, onSelect: select }
    : null
  // 资源列表只在播放器的「资源」弹窗里（任何状态都有播放器外壳），页面上只留选集
  const side = sideProps ? (
    <EpisodeSide {...sideProps} part="episodes" />
  ) : epQ.error ? (
    <Card>
      <ErrorState error={epQ.error} onRetry={() => epQ.refetch()} />
    </Card>
  ) : (
    <EpisodeSideSkeleton />
  )
  const panelSources = sideProps ? (close: () => void) => <EpisodeSide {...sideProps} part="sources" onPicked={close} /> : undefined
  const info = (parts?: Parameters<typeof EpisodeInfo>[0]["parts"]) =>
    epData ? <EpisodeInfo subject={epData.subject} ep={ep!} parts={parts} /> : <EpisodeInfoSkeleton />
  const shareURL = (h: string | null, idx: number | null) => `${location.origin}${h ? sourceHref(h, idx) : here}`

  return (
    <Container className="pb-6 lg:py-5">
      {ready ? (
        <Player
          key={ih}
          magnet={state.magnet}
          play={state.play}
          direct={state.direct}
          hint={hint}
          shareURL={shareURL}
          fileIndex={fileIndex}
          onFileChange={(i) => i !== fileIndex && navigate(sourceHref(ih!, i), { replace: true, state: loc.state })}
          onCurrentChange={setPlaying}
          poster={epData ? (epData.subject.cover ?? "") : undefined}
          renderInfo={(parts) => info(parts)}
          side={side}
          panelSources={panelSources}
        />
      ) : (
        <div className={GRID}>
          <section className="min-w-0">
            {/* 还没有播放会话：同样的播放器外壳（「资源」按钮可用），状态提示在画面里 */}
            <PlayerShell poster={epData?.subject.cover ?? undefined} panelSources={panelSources}>
              {state.status !== "idle" && <PlayGate overlay state={state} title={hint?.title} here={here} retry={retry} docTitle={false} />}
            </PlayerShell>
            {info(waiting ? STATS_SKELETON : undefined)}
          </section>
          <aside className="min-w-0 space-y-4 lg:space-y-5">{side}</aside>
        </div>
      )}
    </Container>
  )
}

function EpisodeError({ error, retry }: { error: unknown; retry: () => void }) {
  if (isApiError(error) && error.status === 404) return <StatusPage code="404" title="番剧不存在" description="该番剧可能尚未收录，或链接有误。" />
  return (
    <Container className="py-8">
      <Card>
        <ErrorState error={error} onRetry={retry} />
      </Card>
    </Container>
  )
}

/** 单集页右栏：选集 + 资源（URL 指定但未收录到本集的资源置顶） */
function EpisodeSide({
  data,
  ranked,
  ep,
  ih,
  fileIndex,
  listed,
  playName,
  hint,
  selected,
  onSelect,
  part = "all",
  onPicked,
}: {
  data: EpisodeResponse
  ranked: Scored<EpisodeFile>[]
  ep: EpisodeKey
  ih: string | null
  fileIndex: number | null
  listed: boolean
  playName?: string
  hint?: PlayHint
  selected: string | null
  onSelect: (f: EpisodeFile) => void
  /** all：选集 + 资源（没有播放器时）；episodes：只有选集；sources：只有资源列表（播放器弹窗里） */
  part?: "all" | "episodes" | "sources"
  /** 资源被点选后（弹窗里用来关闭弹窗） */
  onPicked?: () => void
}) {
  // 播放页轮询留下的收录状态（只读缓存，不发请求）提供文件名与大小
  const status = useQuery<TorrentStatus>({ queryKey: qk.torrentStatus(ih ?? ""), queryFn: () => api.torrentStatus(ih!), enabled: false }).data
  const extra = useMemo<EpisodeFile | null>(() => {
    if (!ih || listed) return null
    const f = fileIndex !== null ? status?.files.find((x) => x.index === fileIndex) : undefined
    const title = status?.title || playName || hint?.title || ih
    return {
      infohash: ih,
      file_index: fileIndex ?? -1,
      path: f?.path ?? title,
      size_bytes: f?.size_bytes ?? 0,
      torrent_title: title,
      team: hint?.team ?? "",
      published_at: "",
      source_id: "",
      source: "user",
      confidence: 0,
      access: { action: "allow", reason: "" },
    }
  }, [ih, listed, fileIndex, status, playName, hint?.title, hint?.team])
  const sources = (
    <SourceList
      ranked={ranked}
      extra={extra}
      selected={extra ? sourceKey(extra) : selected}
      onSelect={(f) => {
        onSelect(f)
        onPicked?.()
      }}
      bare={part === "sources"}
    />
  )
  if (part === "sources") return sources
  return (
    <>
      <EpisodeList subject={data.subject} ep={ep} hasItems={data.items.length > 0 || !!ih} />
      {part === "all" && sources}
    </>
  )
}

/**
 * 播放页（有做种权限时）：服务器做种后轮询 GET /torrents/hash/{ih}，
 * 正在播放的文件被刮削到某番某集时切到对应单集页（同一 Player 实例，不中断播放）。
 * ih 为 null 时不轮询；收录完成（ready）且当前文件未映射、或超过 3 分钟后停止。
 */
function useAutoEpisodeSwitch({ ih, playing, onMapped }: { ih: string | null; playing: number | null; onMapped: (href: string) => void }) {
  const me = useMe()
  const allowed = can(me.data, PERM.seed) && can(me.data, PERM.torrentStatus)
  const [stopped, setStopped] = useState<string | null>(null)
  const startedAt = useRef<{ ih: string; at: number } | null>(null)
  const active = !!ih && allowed && stopped !== ih
  if (active && startedAt.current?.ih !== ih) startedAt.current = { ih: ih!, at: Date.now() }

  const q = useQuery({
    queryKey: qk.torrentStatus(ih ?? ""),
    queryFn: ({ signal }) => api.torrentStatus(ih!, signal),
    enabled: active,
    refetchInterval: POLL_MS,
    refetchIntervalInBackground: true,
    retry: false,
    staleTime: 0,
    gcTime: 10 * 60_000,
  })
  const data = active ? q.data : undefined
  const onMappedRef = useRef(onMapped)
  onMappedRef.current = onMapped

  useEffect(() => {
    if (!active || !ih) return
    const timeout = () => Date.now() - (startedAt.current?.at ?? Date.now()) > POLL_MAX_MS
    if (q.error && !(isApiError(q.error) && q.error.unavailable)) return setStopped(ih)
    if (data && playing !== null) {
      const f = data.files.find((x) => x.index === playing)
      // 任意类型的集（正片 / SP / NCOP …）都切换
      const key = canonicalEpisodeKey(f?.episode_key)
      if (f?.subject && key) {
        setStopped(ih)
        onMappedRef.current(episodeSourceHref(f.subject, key, ih, playing))
        return
      }
      // 收录完成但当前文件没有映射：留在播放页
      if (data.status === "ready") return setStopped(ih)
    }
    if (timeout()) setStopped(ih)
  }, [active, ih, data, playing, q.error, q.dataUpdatedAt])
}
