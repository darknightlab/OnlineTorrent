// 后台「Tracker」：WebTorrent / BT tracker 列表（编辑写入站点设置）+ 最后一次实际使用时的状态（被动记录，不主动探测）
// + 按需统计做种人数的计数。GET /admin/trackers，PUT /admin/settings
import { useEffect, useState } from "react"
import { Link } from "react-router-dom"
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { Loader2, PencilLine, RefreshCw, Save, Settings2, X } from "lucide-react"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Card } from "@/components/ui/card"
import { Textarea } from "@/components/ui/input"
import { toast } from "@/components/ui/toast"
import { ErrorState, Spinner, errorMessage } from "@/components/states"
import { api } from "@/lib/api"
import type { TrackerHealth, TrackersResponse } from "@/lib/types"
import { formatDateTime, timeAgo } from "@/lib/format"
import { useNow } from "@/lib/hooks"
import { AdminHeader, DataView, Mono, StatusDot, type Column } from "./components"

const KEY = ["admin", "trackers"]

const RESULT_LABEL: Record<string, string> = {
  ok: "正常",
  timeout: "超时",
  dns: "域名解析失败",
  network: "连接失败",
  protocol: "协议错误",
  error: "错误",
}

const LISTS = [
  {
    list: "webtorrent" as const,
    key: "webtorrent.trackers",
    title: "WebTorrent Tracker",
    hint: "wss://，公开下发给浏览器；qB 推送时也会加上（qB 设置了「补 wss tracker」时）。用于统计 WebRTC 做种人数。",
    placeholder: "wss://tracker.example.org",
  },
  {
    list: "bt" as const,
    key: "bt.trackers",
    title: "BT Tracker",
    hint: "udp:// 或 http(s)://：推送给 qB（与种子自带的 tracker、wss tracker 合并去重，最多 30 个），并用于统计 BT 做种人数。",
    placeholder: "udp://tracker.example.org:1337/announce",
  },
]

export default function TrackersPage() {
  const now = useNow(15_000)
  const q = useQuery({ queryKey: KEY, queryFn: api.admin.trackers, refetchInterval: 30_000 })
  if (q.error) return <ErrorState error={q.error} onRetry={() => q.refetch()} />
  if (!q.data)
    return (
      <div className="flex h-40 items-center justify-center">
        <Spinner />
      </div>
    )
  const d = q.data
  return (
    <div>
      <AdminHeader
        title="Tracker"
        description="状态为每个 tracker 最后一次实际使用（按需统计做种人数）时的结果；本站不会为检测而主动请求 tracker。"
        actions={
          <Button variant="outline" size="sm" onClick={() => q.refetch()} disabled={q.isFetching}>
            {q.isFetching ? <Loader2 className="animate-spin" /> : <RefreshCw />}
            刷新
          </Button>
        }
      />
      <div className="grid gap-4">
        <SwarmCard d={d} now={now} />
        {LISTS.map((l) => (
          <ListCard key={l.list} spec={l} urls={d.lists[l.list]} health={d.items.filter((h) => h.list === l.list)} now={now} />
        ))}
      </div>
    </div>
  )
}

function SwarmCard({ d, now }: { d: TrackersResponse; now: number }) {
  const s = d.swarm
  const stats = [
    { label: "种子总数", value: d.stats?.torrents ?? 0 },
    { label: "已统计", value: d.stats?.scraped ?? 0 },
    { label: "有数据", value: d.stats?.known ?? 0, hint: "含 Nyaa RSS 初始值" },
    { label: "无人做种", value: d.stats?.dead ?? 0 },
  ]
  return (
    <Card className="overflow-hidden">
      <div className="flex flex-wrap items-center justify-between gap-2 border-b px-4 py-2.5">
        <div className="min-w-0">
          <h2 className="text-sm font-semibold">做种人数统计</h2>
          <p className="text-xs text-muted-foreground">
            按需：单集 / 条目 / 搜索 / 种子状态返回的种子没有数据或超过 30 分钟时，当场批量查询这些种子（最多等待约 1.5 秒），不做全库定时查询
          </p>
        </div>
        <div className="flex items-center gap-2">
          <StatusDot ok={s.enabled} label={s.enabled ? "已启用" : "已关闭"} />
          <Button asChild variant="ghost" size="sm" className="text-muted-foreground">
            <Link to="/admin/settings#group-Tracker">
              <Settings2 />
              设置
            </Link>
          </Button>
        </div>
      </div>
      <div className="space-y-3 p-4">
        <dl className="grid grid-cols-2 gap-2 sm:grid-cols-4">
          {stats.map((x) => (
            <div key={x.label} className="rounded-lg bg-muted/60 px-3 py-2" title={x.hint}>
              <dt className="text-xs text-muted-foreground">{x.label}</dt>
              <dd className="text-lg font-bold tabular-nums">{x.value.toLocaleString("zh-CN")}</dd>
            </div>
          ))}
        </dl>
        <p className="text-xs tabular-nums text-muted-foreground">
          启动以来查询 {s.runs} 次、共 {s.torrents} 个种子
          {s.running > 0 && ` · 正在查询 ${s.running}`}
          {s.skipped_busy > 0 && ` · 繁忙跳过 ${s.skipped_busy}`}
          {s.last_at && (
            <span title={formatDateTime(s.last_at)}>
              {" "}
              · 最近一次 {timeAgo(s.last_at, now)}：{s.last_answered}/{s.last_torrents} 个有响应，耗时 {s.last_ms} ms
            </span>
          )}
        </p>
      </div>
    </Card>
  )
}

function ListCard({ spec, urls, health, now }: { spec: (typeof LISTS)[number]; urls: string[]; health: TrackerHealth[]; now: number }) {
  const qc = useQueryClient()
  const [editing, setEditing] = useState(false)
  const [text, setText] = useState(urls.join("\n"))
  useEffect(() => {
    if (!editing) setText(urls.join("\n"))
  }, [urls, editing])
  const save = useMutation({
    mutationFn: () => api.admin.saveSettings({ values: { [spec.key]: text.split("\n").map((s) => s.trim()).filter(Boolean) } }),
    onSuccess: () => {
      setEditing(false)
      void qc.invalidateQueries({ queryKey: KEY })
      void qc.invalidateQueries({ queryKey: ["admin", "settings"] })
      void qc.invalidateQueries({ queryKey: ["site"] })
      toast.success(`${spec.title} 已保存`)
    },
    onError: (e) => toast.error(errorMessage(e)),
  })
  const columns: Column<TrackerHealth>[] = [
    {
      key: "url",
      header: "地址",
      mobile: "title",
      cell: (h) => (
        <span className="flex items-center gap-1.5">
          <Badge variant="outline" className="uppercase">
            {h.kind}
          </Badge>
          <Mono>{h.url}</Mono>
        </span>
      ),
    },
    {
      key: "status",
      header: "最后一次使用",
      cell: (h) =>
        h.status === "unused" ? (
          <StatusDot ok={null} label="尚未使用" />
        ) : (
          <span className="inline-flex flex-wrap items-center gap-x-2 gap-y-0.5">
            <StatusDot ok={h.status === "ok"} label={RESULT_LABEL[h.result] ?? h.result} />
            {h.checked_at && (
              <span className="text-xs tabular-nums text-muted-foreground" title={formatDateTime(h.checked_at)}>
                {timeAgo(h.checked_at, now)}
              </span>
            )}
          </span>
        ),
    },
    {
      key: "latency",
      header: "延迟",
      className: "text-right tabular-nums",
      cell: (h) => (h.last_ok_at ? `${h.latency_ms} ms` : "—"),
    },
    {
      key: "count",
      header: "成功 / 失败",
      className: "text-right tabular-nums",
      cell: (h) => (
        <span>
          {h.ok_count} / <span className={h.fail_count ? "text-destructive-text" : undefined}>{h.fail_count}</span>
        </span>
      ),
    },
    {
      key: "error",
      header: "最近错误",
      cell: (h) =>
        h.last_error ? (
          <span className="block max-w-72 text-xs text-muted-foreground" title={h.last_error_at ? `${formatDateTime(h.last_error_at)}\n${h.last_error}` : h.last_error}>
            <span className="line-clamp-2 wrap-anywhere">{h.last_error}</span>
            {h.last_error_at && <span className="tabular-nums opacity-80">{timeAgo(h.last_error_at, now)}</span>}
          </span>
        ) : (
          <span className="text-muted-foreground">—</span>
        ),
    },
  ]
  return (
    <section aria-label={spec.title}>
      <div className="mb-2 flex flex-wrap items-end justify-between gap-2">
        <div className="min-w-0">
          <h2 className="text-sm font-semibold">
            {spec.title} <span className="font-normal text-muted-foreground">· {urls.length} 个</span>
          </h2>
          <p className="text-xs text-muted-foreground">{spec.hint}</p>
        </div>
        {!editing && (
          <Button variant="outline" size="sm" onClick={() => setEditing(true)}>
            <PencilLine />
            编辑列表
          </Button>
        )}
      </div>
      {editing && (
        <Card className="mb-3 p-3">
          <label htmlFor={`trackers-${spec.list}`} className="mb-1.5 block text-xs text-muted-foreground">
            每行一个；保存后立即生效（<Mono>{spec.key}</Mono>）
          </label>
          <Textarea
            id={`trackers-${spec.list}`}
            rows={Math.min(10, Math.max(4, text.split("\n").length))}
            className="font-mono text-xs"
            placeholder={spec.placeholder}
            value={text}
            onChange={(e) => setText(e.target.value)}
          />
          <div className="mt-2 flex justify-end gap-2">
            <Button variant="ghost" size="sm" onClick={() => setEditing(false)}>
              <X />
              取消
            </Button>
            <Button size="sm" onClick={() => save.mutate()} disabled={save.isPending}>
              {save.isPending ? <Loader2 className="animate-spin" /> : <Save />}
              保存
            </Button>
          </div>
        </Card>
      )}
      <DataView columns={columns} rows={health} rowKey={(h) => h.url} empty="列表为空" />
    </section>
  )
}
