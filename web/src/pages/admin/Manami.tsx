// anime-offline-database（manami-project，ODbL）导入状态卡片：GET /admin/manami，POST /admin/manami/run
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { AlertTriangle, BookA, Loader2, RefreshCw } from "lucide-react"
import { Button } from "@/components/ui/button"
import { Card } from "@/components/ui/card"
import { Skeleton } from "@/components/ui/skeleton"
import { toast } from "@/components/ui/toast"
import { ErrorState, errorMessage } from "@/components/states"
import { isApiError } from "@/lib/api"
import { adminApi } from "./api"
import { formatDateTime, formatDuration, timeAgo } from "@/lib/format"
import { useNow } from "@/lib/hooks"
import { cn } from "@/lib/utils"
import { StatusDot } from "./components"

const KEY = ["admin", "manami"]

export function ManamiCard() {
  const qc = useQueryClient()
  const now = useNow(15_000)
  const q = useQuery({
    queryKey: KEY,
    queryFn: adminApi.manami,
    refetchInterval: (query) => (query.state.data?.running ? 3_000 : 60_000),
  })
  const run = useMutation({
    mutationFn: adminApi.runManami,
    onSuccess: (st) => {
      qc.setQueryData(KEY, st)
      toast.success("已开始导入 anime-offline-database")
    },
    onError: (e) => {
      if (isApiError(e) && e.status === 409) toast.info("正在导入，请稍候")
      else toast.error(errorMessage(e))
      void qc.invalidateQueries({ queryKey: KEY })
    },
  })
  const s = q.data
  const running = !!s?.running || run.isPending
  const failing = !!s?.error && (!s.synced_at || !s.error_at || new Date(s.error_at) >= new Date(s.synced_at))

  return (
    <Card className="overflow-hidden">
      <div className="flex flex-wrap items-center justify-between gap-2 border-b px-4 py-2.5">
        <div className="min-w-0">
          <h2 className="flex items-center gap-1.5 text-sm font-semibold">
            <BookA className="h-4 w-4 text-primary-text" />
            anime-offline-database 同步
          </h2>
          <p className="text-xs text-muted-foreground">
            manami-project 的番剧名称库（ODbL）：按 MAL / AniDB / AniList ID 对应条目，标题与别名只用于名称匹配，每周导入一次
          </p>
        </div>
        <Button size="sm" className="ml-auto" onClick={() => run.mutate()} disabled={running || !s || !s.url}>
          {running ? <Loader2 className="animate-spin" /> : <RefreshCw />}
          {s?.running ? "导入中" : "立即同步"}
        </Button>
      </div>
      {q.error ? (
        <ErrorState error={q.error} onRetry={() => q.refetch()} />
      ) : !s ? (
        <div className="p-4">
          <Skeleton className="h-4 w-1/2" />
        </div>
      ) : (
        <div className="space-y-2 p-4 text-[13px]">
          <div className="flex flex-wrap items-center gap-x-3 gap-y-1">
            {s.running ? (
              <span className="inline-flex items-center gap-1.5 font-medium text-primary-text">
                <Loader2 className="h-3.5 w-3.5 animate-spin" />
                正在导入
              </span>
            ) : (
              <StatusDot
                ok={s.synced_at ? !failing : failing ? false : null}
                label={!s.url ? "未启用" : failing ? "上次导入失败" : s.synced_at ? "已导入" : "尚未导入"}
              />
            )}
            {s.synced_at && (
              <span className="tabular-nums text-muted-foreground" title={formatDateTime(s.synced_at)}>
                {timeAgo(s.synced_at, now)}
                {s.release && ` · 版本 ${s.release}`} · {s.entries.toLocaleString("zh-CN")} 条，对应 {s.linked.toLocaleString("zh-CN")} 部番剧、
                {s.names.toLocaleString("zh-CN")} 个名称 · 耗时 {formatDuration(s.duration_sec * 1000)}
              </span>
            )}
            {s.next_at && !s.running && (
              <span className="tabular-nums text-muted-foreground" title={formatDateTime(s.next_at)}>
                下次 {formatDateTime(s.next_at)}
              </span>
            )}
          </div>
          <p className="truncate text-xs text-muted-foreground" title={s.url}>
            {s.url ? `来源 ${s.url}` : "未设置地址（设置 → 上游），不导入"}
          </p>
          {s.error && (
            <div className={cn("flex gap-2 rounded-lg px-3 py-2 text-xs", failing ? "bg-destructive/10 text-destructive-text" : "bg-muted text-muted-foreground")}>
              <AlertTriangle className="mt-px h-3.5 w-3.5 shrink-0" />
              <p className="min-w-0 wrap-anywhere">
                {s.error}
                {s.error_at && ` · ${timeAgo(s.error_at, now)}`}
                {!failing && " · 之后已成功导入"}
              </p>
            </div>
          )}
        </div>
      )}
    </Card>
  )
}
