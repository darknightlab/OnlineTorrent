import { Link, useLocation, useSearchParams } from "react-router-dom"
import { Ban, Crown, Loader2, Lock, Magnet, RotateCw, WifiOff, Zap, AlertTriangle } from "lucide-react"
import { Container } from "@/components/layout/Container"
import { TorrentSearchBox } from "@/components/TorrentSearchBox"
import { Button } from "@/components/ui/button"
import { Card } from "@/components/ui/card"
import { useSiteOrFallback, useUser } from "@/lib/queries"
import type { Torrent } from "@/lib/types"
import { useDocumentTitle } from "@/lib/hooks"
import { cn } from "@/lib/utils"
import { usePlayResolve } from "./usePlayResolve"
import { Player } from "./Player"

export default function WatchPage() {
  const [sp] = useSearchParams()
  const loc = useLocation()
  const hint = (loc.state as { torrent?: Torrent } | null)?.torrent
  const params = { uri: sp.get("uri"), t: sp.get("t"), ih: sp.get("ih") }
  const site = useSiteOrFallback()
  const { user, loading } = useUser()
  const attempt = loading ? "pending" : `${user?.id ?? "guest"}:${user?.role ?? ""}:${user?.vip_until ?? ""}`
  const { state, skipWait, retry } = usePlayResolve(params, hint, site.trackers, attempt)
  const here = `${loc.pathname}${loc.search}`

  const shareURL = (ih: string | null) => {
    const origin = location.origin
    if (params.t) return `${origin}/watch?t=${encodeURIComponent(params.t)}`
    if (ih) return `${origin}/watch?uri=${encodeURIComponent(`magnet:?xt=urn:btih:${ih}`)}`
    return `${origin}${here}`
  }

  if (state.status === "idle") return <IdleWatch />

  return (
    <Container className="py-0 sm:py-5">
      {state.status === "ready" ? (
        <Player key={state.magnet} magnet={state.magnet} play={state.play} direct={state.direct} hint={hint} shareURL={shareURL} />
      ) : (
        <div className="pt-3 sm:pt-0">
          <GateFrame subject={state.status === "loading" ? undefined : hint?.title}>
            {state.status === "loading" && (
              <Gate icon={<Loader2 className="h-7 w-7 animate-spin text-primary" />} title="正在请求播放…" description={hint?.title ?? "服务器正在检查资源并准备做种"} />
            )}
            {state.status === "login" && (
              <Gate
                tone="accent"
                icon={<Lock className="h-7 w-7" />}
                title="登录后即可观看"
                description={state.message || "该资源仅限会员观看，登录或注册后继续。"}
                actions={
                  <>
                    <Button asChild>
                      <Link to={`/login?next=${encodeURIComponent(here)}`}>登录</Link>
                    </Button>
                    <Button asChild variant="outline">
                      <Link to={`/register?next=${encodeURIComponent(here)}`}>注册账号</Link>
                    </Button>
                  </>
                }
              />
            )}
            {state.status === "vip" && (
              <Gate
                tone="warning"
                icon={<Crown className="h-7 w-7" />}
                title="大会员专享资源"
                description={state.message || "开通大会员后即可观看。"}
                actions={
                  user ? (
                    <Button asChild>
                      <Link to="/me">使用兑换码开通</Link>
                    </Button>
                  ) : (
                    <>
                      <Button asChild>
                        <Link to={`/login?next=${encodeURIComponent(here)}`}>登录</Link>
                      </Button>
                      <Button asChild variant="outline">
                        <Link to="/me">了解大会员</Link>
                      </Button>
                    </>
                  )
                }
              />
            )}
            {state.status === "blocked" && (
              <Gate
                tone="destructive"
                icon={<Ban className="h-7 w-7" />}
                title="该资源已被屏蔽"
                description={state.message || "根据相关规定，该资源无法在本站播放。"}
                actions={
                  <Button asChild variant="outline">
                    <Link to="/">返回首页</Link>
                  </Button>
                }
              />
            )}
            {state.status === "fallback" && (
              <Gate
                tone="warning"
                icon={<WifiOff className="h-7 w-7" />}
                title={`${state.reason}`}
                description={
                  <>
                    将在 <span className="font-semibold tabular-nums text-foreground">{state.seconds}</span> 秒后直接通过 P2P 网络连接（无服务器做种时速度可能较慢）
                  </>
                }
                actions={
                  <>
                    <Button onClick={skipWait}>
                      <Zap />
                      立即连接
                    </Button>
                    <Button variant="outline" onClick={retry}>
                      <RotateCw />
                      重试服务器
                    </Button>
                  </>
                }
              />
            )}
            {state.status === "error" && (
              <Gate
                tone="destructive"
                icon={<AlertTriangle className="h-7 w-7" />}
                title="无法播放"
                description={state.message}
                actions={
                  <>
                    <Button onClick={retry}>
                      <RotateCw />
                      重试
                    </Button>
                    <Button asChild variant="outline">
                      <Link to="/">返回首页</Link>
                    </Button>
                  </>
                }
              />
            )}
          </GateFrame>
        </div>
      )}
    </Container>
  )
}

function GateFrame({ children, subject }: { children: React.ReactNode; subject?: string }) {
  return (
    <Card className="relative mx-auto max-w-4xl overflow-hidden">
      <div aria-hidden className="pointer-events-none absolute inset-0 bg-[radial-gradient(ellipse_at_top,var(--primary-soft),transparent_60%)]" />
      <div className="relative flex min-h-[340px] flex-col items-center justify-center p-6 sm:aspect-video sm:min-h-0">
        {children}
        {subject && (
          <p className="mt-6 line-clamp-2 max-w-lg wrap-anywhere text-center text-xs text-muted-foreground" title={subject}>
            {subject}
          </p>
        )}
      </div>
    </Card>
  )
}

const toneCls = {
  primary: "bg-primary-soft text-primary-text",
  accent: "bg-accent-soft text-accent-text",
  warning: "bg-warning/15 text-warning-text",
  destructive: "bg-destructive/12 text-destructive-text",
}

function Gate({
  icon,
  title,
  description,
  actions,
  tone = "primary",
}: {
  icon: React.ReactNode
  title: React.ReactNode
  description?: React.ReactNode
  actions?: React.ReactNode
  tone?: keyof typeof toneCls
}) {
  useDocumentTitle(typeof title === "string" ? title : "播放")
  return (
    <div className="flex max-w-md flex-col items-center text-center" role="status">
      <span className={cn("inline-flex h-14 w-14 items-center justify-center rounded-full", toneCls[tone])}>{icon}</span>
      <h1 className="mt-4 text-lg font-semibold">{title}</h1>
      {description && <p className="mt-1.5 line-clamp-3 wrap-anywhere text-sm text-muted-foreground">{description}</p>}
      {actions && <div className="mt-5 flex flex-wrap justify-center gap-2">{actions}</div>}
    </div>
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
