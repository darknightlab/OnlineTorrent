import { useEffect } from "react"
import { Navigate, useLocation, useNavigate, useSearchParams } from "react-router-dom"
import { Magnet } from "lucide-react"
import { Container } from "@/components/layout/Container"
import { TorrentSearchBox } from "@/components/TorrentSearchBox"
import { useSiteOrFallback } from "@/lib/queries"
import type { Torrent } from "@/lib/types"
import { useDocumentTitle } from "@/lib/hooks"
import { normalizeInfohash, parseTorrentInput, watchHref } from "@/lib/utils"
import { StatusPage } from "@/pages/NotFound"
import { usePlayAttempt, usePlayResolve, type PlayHint, type PlayPreload, type PlaySource, type ResolveState } from "./usePlayResolve"
import { PlayGate } from "./PlayGate"
import { Player, PlayerShell } from "./Player"
import { FilesSkeleton, GRID, WatchInfoSkeleton } from "@/pages/Episode"

/** 列表页跳转时附带的 location.state */
export interface WatchLocationState {
  torrent?: Torrent
  preload?: PlayPreload
}

export function hintOf(t: Torrent | undefined): PlayHint | undefined {
  return t ? { infohash: t.infohash, title: t.title, team: t.team?.name ?? null } : undefined
}

/** 旧链接 /watch?uri=|ih=|t=；无参数时显示粘贴磁力页 */
export function LegacyWatchPage() {
  const [sp] = useSearchParams()
  const loc = useLocation()
  const uri = sp.get("uri")
  const ih = sp.get("ih")
  const t = sp.get("t")
  if (uri || ih) {
    const h = uri ? parseTorrentInput(uri) : normalizeInfohash(ih)
    return h ? <Navigate replace to={watchHref(h)} state={loc.state} /> : <StatusPage code="400" title="链接无效" description="无法从链接中解析出 infohash。" />
  }
  if (t) {
    if (t.startsWith("ih:")) {
      const h = normalizeInfohash(t.slice(3))
      if (h) return <Navigate replace to={watchHref(h)} state={loc.state} />
    }
    return <MoeWatch id={t} />
  }
  return <IdleWatch />
}

/** /watch?t={bangumi.moe ID}：按 ID 打开，成功后把 URL 换成 /watch/hash/{ih} 并复用本次结果 */
function MoeWatch({ id }: { id: string }) {
  const loc = useLocation()
  const navigate = useNavigate()
  const st = (loc.state as WatchLocationState | null) ?? {}
  const site = useSiteOrFallback()
  const attempt = usePlayAttempt()
  const hint = hintOf(st.torrent)
  const src: PlaySource = { kind: "moe", id }
  const { state, retry } = usePlayResolve(src, { hint, trackers: site.trackers, attempt })
  const play = state.status === "ready" ? state.play : null

  useEffect(() => {
    if (state.status === "redirect") navigate(state.path, { replace: true })
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [state])

  useEffect(() => {
    if (!play) return
    const next: WatchLocationState = { torrent: st.torrent, preload: { play, attempt, at: Date.now() } }
    navigate(watchHref(play.infohash), { replace: true, state: next })
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [play])

  // 拿到结果后马上跳转，不在旧地址上启动播放器
  const shown: ResolveState = play ? { status: "loading" } : state
  return <WatchView state={shown} retry={retry} hint={hint} fileIndex={null} />
}

function WatchView({
  state,
  retry,
  hint,
  fileIndex,
  onFileChange,
}: {
  state: ResolveState
  retry: () => void
  hint?: PlayHint
  fileIndex: number | null
  onFileChange?: (index: number) => void
}) {
  const loc = useLocation()
  const here = `${loc.pathname}${loc.search}`
  const shareURL = (ih: string | null, idx: number | null) => (ih ? `${location.origin}${watchHref(ih, idx)}` : `${location.origin}${here}`)
  const waiting = state.status === "loading" || state.status === "pending" || state.status === "redirect"

  if (state.status === "idle") return <IdleWatch />
  return (
    <Container className="pb-6 lg:py-5">
      {state.status === "ready" ? (
        <Player
          key={state.magnet}
          magnet={state.magnet}
          play={state.play}
          direct={state.direct}
          hint={hint}
          shareURL={shareURL}
          fileIndex={fileIndex}
          onFileChange={onFileChange}
        />
      ) : (
        <div className={GRID}>
          <section className="min-w-0">
            <PlayerShell>
              <PlayGate overlay state={state} title={hint?.title} here={here} retry={retry} />
            </PlayerShell>
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

function IdleWatch() {
  useDocumentTitle("播放")
  return (
    <Container className="py-10 sm:py-16">
      <div className="mx-auto max-w-2xl text-center">
        <span className="inline-flex h-14 w-14 items-center justify-center rounded-full bg-primary-soft text-primary-text">
          <Magnet className="h-7 w-7" />
        </span>
        <h1 className="mt-4 text-xl font-bold">粘贴磁力链接开始播放</h1>
        <p className="mt-1.5 text-sm text-muted-foreground">支持 magnet 链接、40 位 infohash</p>
        <TorrentSearchBox className="mt-6" autoFocus />
      </div>
    </Container>
  )
}
