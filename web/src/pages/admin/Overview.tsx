import { Link } from "react-router-dom"
import { useQuery } from "@tanstack/react-query"
import { Activity, ChevronRight, ClipboardCheck, Server, Users } from "lucide-react"
import { Card } from "@/components/ui/card"
import { Skeleton } from "@/components/ui/skeleton"
import { ErrorState } from "@/components/states"
import { ActionBadge } from "@/components/badges"
import { api, toList } from "@/lib/api"
import { timeAgo } from "@/lib/format"
import { cn } from "@/lib/utils"
import { AdminHeader, StatusDot } from "./components"

export default function Overview() {
  const stats = useQuery({ queryKey: ["admin", "stats"], queryFn: api.admin.stats, refetchInterval: 30_000 })
  const qb = useQuery({ queryKey: ["admin", "qb"], queryFn: api.admin.qbServers })
  const plays = useQuery({ queryKey: ["admin", "plays", 1], queryFn: () => api.admin.plays(1) })
  const s = stats.data
  const tiles = [
    { label: "注册用户", value: s?.users, icon: Users, to: "/admin/users", tone: "text-primary-text bg-primary-soft" },
    { label: "24 小时播放", value: s?.plays_24h, icon: Activity, to: "/admin/plays", tone: "text-accent-text bg-accent-soft" },
    { label: "qB 在线", value: s ? `${s.qb_online} / ${s.qb_total}` : undefined, icon: Server, to: "/admin/qb", tone: s && s.qb_online < s.qb_total ? "text-warning-text bg-warning/15" : "text-primary-text bg-primary-soft" },
    { label: "待审核", value: s?.pending_reviews, icon: ClipboardCheck, to: "/admin/reviews", tone: s?.pending_reviews ? "text-destructive-text bg-destructive/12" : "text-muted-foreground bg-muted" },
  ]
  return (
    <div>
      <AdminHeader title="概览" description="站点运行状态一览" />
      {stats.error ? (
        <Card>
          <ErrorState error={stats.error} onRetry={() => stats.refetch()} />
        </Card>
      ) : (
        <div className="grid grid-cols-2 gap-3 xl:grid-cols-4">
          {tiles.map((t) => (
            <Link key={t.label} to={t.to} className="group rounded-lg focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring">
              <Card className="flex items-center gap-3 p-3.5 transition-colors group-hover:border-primary/40 sm:p-4">
                <span className={cn("inline-flex h-10 w-10 shrink-0 items-center justify-center rounded-lg sm:h-11 sm:w-11", t.tone)}>
                  <t.icon className="h-5 w-5" />
                </span>
                <div className="min-w-0">
                  <p className="text-xs text-muted-foreground">{t.label}</p>
                  {t.value === undefined ? <Skeleton className="mt-1 h-6 w-12" /> : <p className="text-xl font-bold tabular-nums sm:text-2xl">{t.value}</p>}
                </div>
              </Card>
            </Link>
          ))}
        </div>
      )}

      <div className="mt-5 grid gap-4 xl:grid-cols-2">
        <Card className="overflow-hidden">
          <PanelTitle title="qB 服务器" to="/admin/qb" />
          <ul className="divide-y">
            {toList(qb.data).items.map((q) => (
              <li key={q.id} className="flex items-center gap-3 px-4 py-3">
                <StatusDot ok={q.enabled ? q.online : null} label="" />
                <div className="min-w-0 flex-1">
                  <p className="text-sm font-medium">{q.name}</p>
                  <p className="truncate text-xs text-muted-foreground">{q.last_error || q.url}</p>
                </div>
                <span className="shrink-0 text-xs text-muted-foreground">{q.enabled ? (q.online ? q.version : "离线") : "已停用"}</span>
              </li>
            ))}
          </ul>
        </Card>
        <Card className="overflow-hidden">
          <PanelTitle title="最近播放" to="/admin/plays" />
          <ul className="divide-y">
            {toList(plays.data)
              .items.slice(0, 6)
              .map((p) => (
                <li key={p.id} className="flex items-center gap-3 px-4 py-2.5">
                  <div className="min-w-0 flex-1">
                    <p className="truncate text-[13px]">{p.name}</p>
                    <p className="text-xs text-muted-foreground">
                      {p.user ?? "游客"} · {timeAgo(p.created_at)}
                    </p>
                  </div>
                  <ActionBadge action={p.action} />
                </li>
              ))}
          </ul>
        </Card>
      </div>
    </div>
  )
}

function PanelTitle({ title, to }: { title: string; to: string }) {
  return (
    <div className="flex items-center justify-between border-b px-4 py-2.5">
      <h2 className="text-sm font-semibold">{title}</h2>
      <Link to={to} className="inline-flex h-8 items-center text-xs text-muted-foreground hover:text-primary-text">
        全部
        <ChevronRight className="h-3.5 w-3.5" />
      </Link>
    </div>
  )
}
