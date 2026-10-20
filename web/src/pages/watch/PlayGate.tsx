import { createContext, useContext } from "react"
import { Link } from "react-router-dom"
import { AlertTriangle, Ban, CloudOff, Crown, Loader2, Lock, RotateCw, SearchCode } from "lucide-react"
import { Button } from "@/components/ui/button"
import { Card } from "@/components/ui/card"
import { useUser } from "@/lib/queries"
import { useDocumentTitle } from "@/lib/hooks"
import { cn } from "@/lib/utils"
import type { ResolveState } from "./usePlayResolve"

/**
 * 播放前的状态页（请求中 / 获取种子信息中 / 需登录 / 需大会员 / 已屏蔽（无权播放）/ 获取超时 / 错误）。
 * 播放页与单集页共用；`here` 为登录后返回的路径。
 */
export function PlayGate({
  state,
  title,
  here,
  retry,
  docTitle = true,
  className,
  overlay,
}: {
  state: Exclude<ResolveState, { status: "ready" } | { status: "idle" }>
  title?: string
  here: string
  /** 旧接口保留（不再有直连回退） */
  skipWait?: () => void
  retry: () => void
  /** 是否用状态标题设置 document.title */
  docTitle?: boolean
  /** 外框附加样式（单集页移动端全宽） */
  className?: string
  /** 显示在播放器画面里（深色、紧凑，无卡片外框） */
  overlay?: boolean
}) {
  const { user } = useUser()
  const next = encodeURIComponent(here)
  return (
    <OverlayCtx.Provider value={!!overlay}>
      <GateFrame className={className} subject={state.status === "loading" || state.status === "redirect" ? undefined : title}>
        {(state.status === "loading" || state.status === "redirect") && (
          <Gate docTitle={docTitle} icon={<Loader2 className="h-7 w-7 animate-spin text-primary" />} title="正在请求播放…" description={title ?? "服务器正在检查资源"} />
        )}
        {state.status === "pending" && (
          <Gate
            docTitle={docTitle}
            icon={<SearchCode className="h-7 w-7 animate-pulse" />}
            title="正在获取种子信息…"
            description="本站还没有这个种子的信息，服务器正在获取文件列表并判断能否播放，通常需要十几秒。"
          />
        )}
        {state.status === "timeout" && (
          <Gate
            docTitle={docTitle}
            tone="warning"
            icon={<CloudOff className="h-7 w-7" />}
            title="暂时无法获取种子信息"
            description="可能暂时没有节点分享这个种子。服务器会继续尝试，稍后可以重试。"
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
        {state.status === "login" && (
          <Gate
            docTitle={docTitle}
            tone="accent"
            icon={<Lock className="h-7 w-7" />}
            title="登录后即可观看"
            description={state.message || "该资源仅限会员观看，登录或注册后继续。"}
            actions={
              <>
                <Button asChild>
                  <Link to={`/login?next=${next}`}>登录</Link>
                </Button>
                <Button asChild variant="outline">
                  <Link to={`/register?next=${next}`}>注册账号</Link>
                </Button>
              </>
            }
          />
        )}
        {state.status === "vip" && (
          <Gate
            docTitle={docTitle}
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
                    <Link to={`/login?next=${next}`}>登录</Link>
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
            docTitle={docTitle}
            tone="destructive"
            icon={<Ban className="h-7 w-7" />}
            title="无权播放"
            description={state.message || "该资源无法在本站播放。"}
            actions={
              <Button asChild variant="outline">
                <Link to="/">返回首页</Link>
              </Button>
            }
          />
        )}
        {state.status === "error" && (
          <Gate
            docTitle={docTitle}
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
    </OverlayCtx.Provider>
  )
}

const OverlayCtx = createContext(false)

function GateFrame({ children, subject, className }: { children: React.ReactNode; subject?: string; className?: string }) {
  if (useContext(OverlayCtx)) return <div className="flex flex-col items-center px-4 text-white">{children}</div>
  return (
    <Card className={cn("relative mx-auto max-w-4xl overflow-hidden", className)}>
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
  docTitle,
}: {
  icon: React.ReactNode
  title: React.ReactNode
  description?: React.ReactNode
  actions?: React.ReactNode
  tone?: keyof typeof toneCls
  docTitle: boolean
}) {
  useDocumentTitle(docTitle ? (typeof title === "string" ? title : "播放") : undefined)
  if (useContext(OverlayCtx))
    return (
      <div className="flex max-w-md flex-col items-center text-center" role="status">
        <span className={cn("inline-flex h-10 w-10 items-center justify-center rounded-full [&_svg]:h-5 [&_svg]:w-5", toneCls[tone])}>{icon}</span>
        <h1 className="mt-2 text-[15px] font-semibold sm:mt-3 sm:text-base">{title}</h1>
        {description && <p className="mt-1 line-clamp-2 wrap-anywhere text-xs text-white/70 sm:text-[13px]">{description}</p>}
        {actions && <div className="mt-3 flex flex-wrap justify-center gap-2 [&_a]:h-8 [&_button]:h-8 sm:mt-4">{actions}</div>}
      </div>
    )
  return (
    <div className="flex max-w-md flex-col items-center text-center" role="status">
      <span className={cn("inline-flex h-14 w-14 items-center justify-center rounded-full", toneCls[tone])}>{icon}</span>
      <h1 className="mt-4 text-lg font-semibold">{title}</h1>
      {description && <p className="mt-1.5 line-clamp-3 wrap-anywhere text-sm text-muted-foreground">{description}</p>}
      {actions && <div className="mt-5 flex flex-wrap justify-center gap-2">{actions}</div>}
    </div>
  )
}
