import type { ReactNode } from "react"
import { AlertTriangle, Inbox, Loader2, RotateCw } from "lucide-react"
import { Button } from "@/components/ui/button"
import { isApiError } from "@/lib/api"
import { cn } from "@/lib/utils"

export function Spinner({ className }: { className?: string }) {
  return <Loader2 aria-hidden className={cn("h-5 w-5 animate-spin text-primary", className)} />
}

export function PageLoading({ label = "加载中…" }: { label?: string }) {
  return (
    <div role="status" className="flex min-h-[50vh] flex-col items-center justify-center gap-3 text-sm text-muted-foreground">
      <Spinner className="h-7 w-7" />
      {label}
    </div>
  )
}

export function EmptyState({
  icon: Icon = Inbox,
  title,
  description,
  action,
  className,
}: {
  icon?: typeof Inbox
  title: ReactNode
  description?: ReactNode
  action?: ReactNode
  className?: string
}) {
  return (
    <div className={cn("flex flex-col items-center justify-center gap-2 px-4 py-12 text-center", className)}>
      <span className="mb-1 inline-flex h-12 w-12 items-center justify-center rounded-full bg-muted text-muted-foreground">
        <Icon className="h-6 w-6" />
      </span>
      <p className="text-sm font-medium">{title}</p>
      {description && <p className="max-w-sm text-[13px] text-muted-foreground">{description}</p>}
      {action && <div className="mt-2">{action}</div>}
    </div>
  )
}

export function errorMessage(err: unknown) {
  if (isApiError(err)) return err.message
  if (err instanceof Error) return err.message
  return "未知错误"
}

export function ErrorState({ error, onRetry, className }: { error: unknown; onRetry?: () => void; className?: string }) {
  const offline = isApiError(error) && error.unavailable
  return (
    <EmptyState
      icon={AlertTriangle}
      className={className}
      title={offline ? "服务器暂时无法连接" : "加载失败"}
      description={offline ? "后端服务可能正在维护，请稍后重试。你仍然可以粘贴磁力链接直接播放。" : errorMessage(error)}
      action={
        onRetry && (
          <Button variant="outline" size="sm" onClick={onRetry}>
            <RotateCw />
            重试
          </Button>
        )
      }
    />
  )
}

export function SectionHeader({
  title,
  icon: Icon,
  extra,
  className,
  id,
}: {
  title: ReactNode
  icon?: typeof Inbox
  extra?: ReactNode
  className?: string
  id?: string
}) {
  return (
    <div className={cn("mb-3 flex items-center justify-between gap-3", className)}>
      <h2 id={id} className="flex items-center gap-2 text-base font-bold sm:text-lg">
        {Icon && (
          <span className="inline-flex h-7 w-7 items-center justify-center rounded-md bg-primary-soft text-primary-text">
            <Icon className="h-4 w-4" />
          </span>
        )}
        {title}
      </h2>
      {extra}
    </div>
  )
}

export function Pager({ page, pageCount, onChange }: { page: number; pageCount: number; onChange: (p: number) => void }) {
  if (pageCount <= 1) return null
  return (
    <nav aria-label="分页" className="mt-4 flex items-center justify-center gap-2">
      <Button variant="outline" size="sm" disabled={page <= 1} onClick={() => onChange(page - 1)}>
        上一页
      </Button>
      <span className="min-w-20 text-center text-[13px] tabular-nums text-muted-foreground">
        {page} / {pageCount}
      </span>
      <Button variant="outline" size="sm" disabled={page >= pageCount} onClick={() => onChange(page + 1)}>
        下一页
      </Button>
    </nav>
  )
}
