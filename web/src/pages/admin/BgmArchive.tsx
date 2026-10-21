// bangumi Archive（bgm.tv 官方每周数据导出）同步状态卡片：GET /admin/bgm-archive，POST /admin/bgm-archive/run
import { Link } from "react-router-dom"
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { AlertTriangle, Check, Database, Loader2, RefreshCw, Settings2 } from "lucide-react"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Card } from "@/components/ui/card"
import { Skeleton } from "@/components/ui/skeleton"
import { toast } from "@/components/ui/toast"
import { ErrorState, errorMessage } from "@/components/states"
import { isApiError } from "@/lib/api"
import { adminApi } from "./api"
import type { BgmArchiveStatus } from "./types"
import { formatDateTime, formatDuration, timeAgo } from "@/lib/format"
import { useNow } from "@/lib/hooks"
import { cn } from "@/lib/utils"
import { Mono, StatusDot } from "./components"

const KEY = ["admin", "bgm-archive"]

const PHASES: { key: string; label: string }[] = [
  { key: "subjects", label: "条目" },
  { key: "episodes", label: "章节" },
  { key: "relations", label: "关联" },
]

const num = (n: number) => (n > 0 ? n.toLocaleString("zh-CN") : "-")

export function BgmArchiveCard() {
  const qc = useQueryClient()
  const now = useNow(15_000)
  const q = useQuery({
    queryKey: KEY,
    queryFn: adminApi.bgmArchive,
    // 同步中每 3 秒刷新
    refetchInterval: (query) => (query.state.data?.running ? 3_000 : 60_000),
  })
  const run = useMutation({
    mutationFn: adminApi.runBgmArchive,
    onSuccess: (st) => {
      if (st && typeof st === "object" && "running" in st) qc.setQueryData(KEY, st)
      else void qc.invalidateQueries({ queryKey: KEY })
      toast.success("已开始同步 bangumi 数据")
    },
    onError: (e) => {
      if (isApiError(e) && e.status === 409) {
        toast.info("正在同步，请稍候")
        void qc.invalidateQueries({ queryKey: KEY })
      } else toast.error(errorMessage(e))
    },
  })
  const s = q.data
  const running = !!s?.running || run.isPending

  return (
    <Card className="overflow-hidden">
      <div className="flex flex-wrap items-center justify-between gap-2 border-b px-4 py-2.5">
        <div className="min-w-0">
          <h2 className="flex items-center gap-1.5 text-sm font-semibold">
            <Database className="h-4 w-4 text-primary-text" />
            bangumi 数据同步
          </h2>
          <p className="text-xs text-muted-foreground">bgm.tv 官方每周数据导出：条目、章节标题与放送日期、条目关联</p>
        </div>
        <div className="ml-auto flex items-center gap-1">
          <Button asChild variant="ghost" size="sm" className="text-muted-foreground">
            <Link to="/admin/settings#group-抓取">
              <Settings2 />
              设置
            </Link>
          </Button>
          <Button size="sm" onClick={() => run.mutate()} disabled={running || !s}>
            {running ? <Loader2 className="animate-spin" /> : <RefreshCw />}
            {s?.running ? "同步中" : "立即同步"}
          </Button>
        </div>
      </div>
      {q.error ? (
        <ErrorState error={q.error} onRetry={() => q.refetch()} />
      ) : !s ? (
        <div className="space-y-3 p-4">
          <Skeleton className="h-4 w-1/3" />
          <Skeleton className="h-12 w-full" />
        </div>
      ) : (
        <ArchiveBody s={s} now={now} />
      )}
    </Card>
  )
}

function ArchiveBody({ s, now }: { s: BgmArchiveStatus; now: number }) {
  // 错误发生在最近一次成功之后才算「当前失败」
  const failing = !!s.error && (!s.synced_at || !s.error_at || new Date(s.error_at) >= new Date(s.synced_at))
  const stats = [
    { label: "条目", value: num(s.subjects) },
    { label: "章节", value: num(s.episodes) },
    { label: "关联", value: num(s.relations) },
    { label: "耗时", value: s.duration_sec > 0 ? formatDuration(s.duration_sec * 1000) : "-" },
  ]
  return (
    <div className="space-y-3 p-4">
      <div className="flex flex-wrap items-center gap-x-3 gap-y-1.5 text-[13px]">
        {s.running ? (
          <PhaseSteps phase={s.phase} />
        ) : s.synced_at ? (
          <>
            <StatusDot ok={!failing} label={failing ? "上次同步失败" : "已同步"} />
            <span className="tabular-nums text-muted-foreground" title={formatDateTime(s.synced_at)}>
              {timeAgo(s.synced_at, now)}导入
            </span>
          </>
        ) : (
          <StatusDot ok={failing ? false : null} label={failing ? "同步失败" : "尚未同步"} />
        )}
        {s.checked_at && !s.running && (
          <span className="text-xs tabular-nums text-muted-foreground" title={formatDateTime(s.checked_at)}>
            上次检查 {timeAgo(s.checked_at, now)}
          </span>
        )}
      </div>

      <dl className="grid grid-cols-2 gap-2 sm:grid-cols-4">
        {stats.map((x) => (
          <div key={x.label} className="rounded-lg bg-muted/60 px-3 py-2">
            <dt className="text-xs text-muted-foreground">{x.label}</dt>
            <dd className="text-lg font-bold tabular-nums">{x.value}</dd>
          </div>
        ))}
      </dl>

      <dl className="grid gap-x-6 gap-y-1 text-[13px] sm:grid-cols-2">
        <Row label="导出版本">
          {s.dump ? <span title={s.dump}>{s.dump_at ? formatDateTime(s.dump_at) : s.dump}</span> : "-"}
        </Row>
        <Row label="同步于">{s.synced_at ? formatDateTime(s.synced_at) : "-"}</Row>
        {s.digest && (
          <Row label="摘要" className="sm:col-span-2">
            <span title={s.digest}>
              <Mono className="block truncate text-muted-foreground">{s.digest}</Mono>
            </span>
          </Row>
        )}
      </dl>

      {s.error && (
        <div className={cn("flex gap-2 rounded-lg px-3 py-2 text-xs", failing ? "bg-destructive/10 text-destructive-text" : "bg-muted text-muted-foreground")}>
          <AlertTriangle className="mt-px h-3.5 w-3.5 shrink-0" />
          <div className="min-w-0">
            <p className="wrap-anywhere">{s.error}</p>
            {s.error_at && (
              <p className="mt-0.5 opacity-80" title={formatDateTime(s.error_at)}>
                {timeAgo(s.error_at, now)}
                {!failing && " · 之后已成功同步"}
              </p>
            )}
          </div>
        </div>
      )}
    </div>
  )
}

function Row({ label, children, className }: { label: string; children: React.ReactNode; className?: string }) {
  return (
    <div className={cn("flex min-w-0 items-baseline gap-2", className)}>
      <dt className="shrink-0 text-muted-foreground">{label}</dt>
      <dd className="min-w-0 flex-1 truncate tabular-nums">{children}</dd>
    </div>
  )
}

/** 同步阶段：条目 → 章节 → 关联 */
function PhaseSteps({ phase }: { phase?: string }) {
  const cur = PHASES.findIndex((p) => p.key === phase)
  return (
    <span className="flex flex-wrap items-center gap-1.5" aria-live="polite">
      <span className="inline-flex items-center gap-1.5 font-medium text-primary-text">
        <Loader2 className="h-3.5 w-3.5 animate-spin" />
        正在同步
      </span>
      {PHASES.map((p, i) => (
        <Badge
          key={p.key}
          variant={i === cur ? "soft" : i < cur ? "muted" : "outline"}
          className={cn(i === cur && "font-semibold")}
          aria-current={i === cur ? "step" : undefined}
        >
          {i < cur && <Check />}
          {i === cur && <Loader2 className="animate-spin" />}
          {i + 1}. 导入{p.label}
        </Badge>
      ))}
      {cur < 0 && phase && <Badge variant="outline">{phase}</Badge>}
    </span>
  )
}
