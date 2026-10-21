import { useMemo, useState, type FormEvent } from "react"
import { Link } from "react-router-dom"
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { AlertTriangle, CheckCircle2, ChevronDown, DatabaseZap, FlaskConical, Loader2, Magnet, Pencil, Play, Plus, RefreshCw, Trash2 } from "lucide-react"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Card } from "@/components/ui/card"
import { Dialog, DialogContent, DialogFooter } from "@/components/ui/dialog"
import { Field, Input, Select } from "@/components/ui/input"
import { Switch } from "@/components/ui/switch"
import { toast } from "@/components/ui/toast"
import { EmptyState, ErrorState, Spinner, errorMessage } from "@/components/states"
import { isApiError } from "@/lib/api"
import { adminApi } from "./api"
import type { MetadataJob, Source, SourceInput, SourceProbeResult, SourceSchema } from "./types"
import { formatDateTime, timeAgo } from "@/lib/format"
import { useNow } from "@/lib/hooks"
import { cn, watchHref } from "@/lib/utils"
import { AdminHeader, DataView, Mono, StatusDot, type Column } from "./components"

const KEY = ["admin", "sources"]
const SCHEMA_KEY = ["admin", "sources", "schema"]
const JOBS_KEY = ["admin", "metadata-jobs"]

const FALLBACK_SCHEMA: SourceSchema = {
  kinds: [{ value: "rss", label: "RSS / Atom", description: "通用 RSS/Atom：enclosure/link 中的 .torrent、磁力链接或 nyaa:infoHash", example_url: "" }],
  min_interval_min: 5,
  default_interval_min: 15,
}

function intervalLabel(min: number) {
  if (min % 1440 === 0) return `${min / 1440} 天`
  if (min % 60 === 0) return `${min / 60} 小时`
  return `${min} 分钟`
}

export default function SourcesPage() {
  const qc = useQueryClient()
  const now = useNow(15_000)
  const q = useQuery({
    queryKey: KEY,
    queryFn: adminApi.sources,
    // 有正在抓取的源时加快刷新
    refetchInterval: (query) => (query.state.data?.items.some((s) => s.running) ? 3_000 : 30_000),
  })
  const schemaQ = useQuery({ queryKey: SCHEMA_KEY, queryFn: adminApi.sourceSchema, staleTime: Infinity })
  const schema = schemaQ.data ?? FALLBACK_SCHEMA
  const kindLabel = (k: string) => schema.kinds.find((x) => x.value === k)?.label ?? k
  const [editing, setEditing] = useState<Source | "new" | null>(null)
  const invalidate = () => qc.invalidateQueries({ queryKey: KEY, exact: true })
  const patch = (s: Source) =>
    qc.setQueryData<{ items: Source[] }>(KEY, (d) => (d ? { items: d.items.map((x) => (x.id === s.id ? s : x)) } : d))

  const toggle = useMutation({
    mutationFn: (s: Source) => adminApi.updateSource(s.id, { enabled: !s.enabled }),
    onSuccess: patch,
    onError: (e) => toast.error(errorMessage(e)),
  })
  const run = useMutation({
    mutationFn: (s: Source) => adminApi.runSource(s.id),
    onSuccess: (s) => {
      patch(s)
      toast.success(`已开始抓取「${s.name}」，结果稍后显示`)
    },
    onError: (e, s) => {
      if (isApiError(e) && e.status === 409) {
        toast.info(`「${s.name}」正在抓取，请稍候`)
        void invalidate()
      } else toast.error(errorMessage(e))
    },
  })
  const del = useMutation({
    mutationFn: (id: number) => adminApi.deleteSource(id),
    onSuccess: (d) => {
      qc.setQueryData(KEY, d)
      toast.success("已删除（已收录的种子会保留）")
    },
    onError: (e) => toast.error(errorMessage(e)),
  })

  const columns: Column<Source>[] = [
    {
      key: "name",
      header: "名称",
      mobile: "title",
      cell: (s) => (
        <div className="flex min-w-0 flex-wrap items-center gap-1.5">
          <StatusDot ok={s.enabled ? (s.last_error ? false : s.last_ok_at ? true : null) : null} label="" />
          <span className="font-medium">{s.name}</span>
          <Badge variant="outline">{kindLabel(s.kind)}</Badge>
          {s.proxy_disabled && <Badge variant="muted">直连</Badge>}
        </div>
      ),
    },
    {
      key: "url",
      header: "地址",
      className: "max-w-64",
      cell: (s) => (
        <a href={s.url} target="_blank" rel="noreferrer noopener" className="hover:text-primary-text hover:underline" title={s.url}>
          <Mono className="line-clamp-2">{s.url}</Mono>
        </a>
      ),
    },
    { key: "interval", header: "间隔", cell: (s) => <span className="whitespace-nowrap tabular-nums">{intervalLabel(s.interval_min)}</span> },
    {
      key: "last",
      header: "上次抓取",
      className: "max-w-72",
      cell: (s) => <LastRun s={s} now={now} />,
    },
    {
      key: "next",
      header: "下次",
      cell: (s) => (
        <span className="whitespace-nowrap text-xs tabular-nums text-muted-foreground" title={s.next_run_at ? formatDateTime(s.next_run_at) : undefined}>
          {s.running ? "抓取中" : !s.enabled ? "已停用" : s.next_run_at ? timeAgo(s.next_run_at, now) : "-"}
        </span>
      ),
    },
    { key: "enabled", header: "启用", cell: (s) => <Switch checked={s.enabled} onCheckedChange={() => toggle.mutate(s)} aria-label={`启用 ${s.name}`} /> },
  ]

  return (
    <div>
      <AdminHeader
        title="抓取源"
        description={
          <>
            后台定时抓取的种子来源（只抓第 1 页，按 ETag 条件请求）。全局开关与并发数见
            <Link to="/admin/settings#group-抓取" className="mx-0.5 text-primary-text hover:underline">
              站点设置 · 抓取
            </Link>
            。
          </>
        }
        actions={
          <>
            <Button variant="outline" onClick={() => invalidate()} disabled={q.isFetching} aria-label="刷新">
              <RefreshCw className={cn(q.isFetching && "animate-spin")} />
              刷新
            </Button>
            <Button onClick={() => setEditing("new")}>
              <Plus />
              添加抓取源
            </Button>
          </>
        }
      />
      <DataView
        columns={columns}
        rows={q.data?.items}
        loading={q.isPending}
        error={q.error}
        onRetry={() => q.refetch()}
        rowKey={(s) => s.id}
        empty="还没有抓取源"
        actions={(s) => (
          <>
            <Button variant="outline" size="sm" onClick={() => run.mutate(s)} disabled={s.running || (run.isPending && run.variables?.id === s.id)}>
              {s.running || (run.isPending && run.variables?.id === s.id) ? <Loader2 className="animate-spin" /> : <Play />}
              立即抓取
            </Button>
            <Button variant="ghost" size="icon-sm" aria-label={`编辑 ${s.name}`} onClick={() => setEditing(s)}>
              <Pencil />
            </Button>
            <Button
              variant="ghost"
              size="icon-sm"
              aria-label={`删除 ${s.name}`}
              className="text-destructive-text hover:bg-destructive/10"
              onClick={() => confirm(`确定删除抓取源「${s.name}」？已收录的种子会保留。`) && del.mutate(s.id)}
            >
              <Trash2 />
            </Button>
          </>
        )}
      />
      <MetadataJobs sources={q.data?.items ?? []} now={now} />
      {editing && (
        <SourceDialog
          source={editing === "new" ? null : editing}
          schema={schema}
          onClose={() => setEditing(null)}
          onSaved={(s) => {
            patch(s)
            void invalidate()
          }}
        />
      )}
    </div>
  )
}

function LastRun({ s, now }: { s: Source; now: number }) {
  if (s.running)
    return (
      <span className="inline-flex items-center gap-1.5 text-[13px] text-primary-text">
        <Loader2 className="h-3.5 w-3.5 animate-spin" />
        正在抓取…
      </span>
    )
  if (!s.last_run_at) return <span className="text-xs text-muted-foreground">从未抓取</span>
  const failed = !!s.last_error
  return (
    <div className="min-w-0 text-left">
      <span className="flex flex-wrap items-center gap-x-2 gap-y-0.5 text-[13px] md:justify-start">
        <span className={cn("inline-flex items-center gap-1", failed ? "text-destructive-text" : "text-primary-text")}>
          {failed ? <AlertTriangle className="h-3.5 w-3.5" /> : <CheckCircle2 className="h-3.5 w-3.5" />}
          {failed ? "失败" : "成功"}
        </span>
        <span className="tabular-nums text-muted-foreground" title={formatDateTime(s.last_run_at)}>
          {timeAgo(s.last_run_at, now)}
        </span>
        {s.last_ok_at && (
          <span className="tabular-nums text-muted-foreground" title={`上次成功 ${formatDateTime(s.last_ok_at)}`}>
            新增 <strong className="font-semibold text-foreground">{s.last_new}</strong> / {s.last_items} 条
          </span>
        )}
      </span>
      {failed && (
        <p className="mt-0.5 line-clamp-2 wrap-anywhere text-xs text-muted-foreground" title={s.last_error}>
          {s.fail_count > 1 && <span className="mr-1 text-destructive-text">连续 {s.fail_count} 次</span>}
          {s.last_error}
        </p>
      )}
      {failed && s.last_ok_at && <p className="text-[11px] text-muted-foreground">上次成功 {timeAgo(s.last_ok_at, now)}</p>}
    </div>
  )
}

type Form = Required<Omit<SourceInput, "name">> & { name: string }

function SourceDialog({ source, schema, onClose, onSaved }: { source: Source | null; schema: SourceSchema; onClose: () => void; onSaved: (s: Source) => void }) {
  const [f, setF] = useState<Form>(() =>
    source
      ? { name: source.name, kind: source.kind, url: source.url, interval_min: source.interval_min, enabled: source.enabled, proxy_disabled: source.proxy_disabled }
      : { name: "", kind: schema.kinds[0]?.value ?? "rss", url: "", interval_min: schema.default_interval_min, enabled: true, proxy_disabled: false },
  )
  const [probe, setProbe] = useState<{ key: string; result?: SourceProbeResult; error?: string } | null>(null)
  const kind = schema.kinds.find((k) => k.value === f.kind)
  const probeKey = `${f.kind}\n${f.url}\n${f.proxy_disabled}`
  const probeShown = probe && probe.key === probeKey ? probe : null

  const test = useMutation({
    mutationFn: () => adminApi.probeSource({ kind: f.kind, url: f.url.trim(), proxy_disabled: f.proxy_disabled }),
    onMutate: () => setProbe(null),
    onSuccess: (result) => setProbe({ key: probeKey, result }),
    onError: (e) => setProbe({ key: probeKey, error: errorMessage(e) }),
  })
  const save = useMutation({
    mutationFn: () => {
      const body: SourceInput = { ...f, url: f.url.trim(), name: f.name.trim() || undefined }
      if (!source) return adminApi.createSource(body)
      // 部分更新：只提交改过的字段（改 url/kind 时后端会先测试）
      const diff: Partial<SourceInput> = {}
      for (const k of Object.keys(body) as (keyof SourceInput)[]) {
        if (body[k] !== undefined && body[k] !== source[k]) (diff as Record<string, unknown>)[k] = body[k]
      }
      return adminApi.updateSource(source.id, diff)
    },
    onSuccess: (s) => {
      toast.success(source ? "已保存" : `已添加「${s.name}」`)
      onSaved(s)
      onClose()
    },
    onError: (e) => {
      if (isApiError(e) && e.code === "source_probe_failed") setProbe({ key: probeKey, error: e.message })
      toast.error(errorMessage(e))
    },
  })

  function submit(e: FormEvent) {
    e.preventDefault()
    save.mutate()
  }
  const setText = (k: "name" | "url") => (e: React.ChangeEvent<HTMLInputElement>) => setF((s) => ({ ...s, [k]: e.target.value }))

  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent
        title={source ? `编辑 ${source.name}` : "添加抓取源"}
        description={source ? "修改地址或类型时会先测试一次" : "保存前会先测试，没有可识别的种子条目时无法添加"}
      >
        <form onSubmit={submit} className="grid gap-3">
          <Field label="类型" htmlFor="src-kind" hint={kind?.description}>
            <Select id="src-kind" value={f.kind} onChange={(e) => setF((s) => ({ ...s, kind: e.target.value }))}>
              {schema.kinds.map((k) => (
                <option key={k.value} value={k.value}>
                  {k.label}
                </option>
              ))}
            </Select>
          </Field>
          <Field
            label="地址"
            htmlFor="src-url"
            hint={
              kind?.example_url && !f.url ? (
                <button type="button" className="text-left text-primary-text hover:underline" onClick={() => setF((s) => ({ ...s, url: kind.example_url }))}>
                  示例：<span className="wrap-anywhere font-mono">{kind.example_url}</span>
                </button>
              ) : undefined
            }
          >
            <div className="flex gap-2">
              <Input id="src-url" required type="url" value={f.url} onChange={setText("url")} placeholder="https://…/rss.xml" className="min-w-0 flex-1 font-mono text-[13px]" />
              <Button type="button" variant="outline" onClick={() => test.mutate()} disabled={!f.url.trim() || test.isPending} className="shrink-0">
                {test.isPending ? <Loader2 className="animate-spin" /> : <FlaskConical />}
                测试
              </Button>
            </div>
          </Field>
          {(test.isPending || probeShown) && <ProbeResult pending={test.isPending} probe={probeShown} />}
          <div className="grid gap-3 sm:grid-cols-[1fr_9rem]">
            <Field label="名称" htmlFor="src-name" hint={source ? undefined : "留空则使用 Feed 标题"}>
              <Input id="src-name" value={f.name} onChange={setText("name")} placeholder={probeShown?.result?.title || "例如 Nyaa"} required={!!source} />
            </Field>
            <Field label="间隔（分钟）" htmlFor="src-interval" hint={`最小 ${schema.min_interval_min}`}>
              <Input
                id="src-interval"
                type="number"
                required
                min={schema.min_interval_min}
                max={10080}
                value={f.interval_min}
                onChange={(e) => setF((s) => ({ ...s, interval_min: Number(e.target.value) }))}
              />
            </Field>
          </div>
          <ToggleRow label="启用" checked={f.enabled} onChange={(v) => setF((s) => ({ ...s, enabled: v }))} />
          <ToggleRow
            label="直连（不走上游代理）"
            description="开启后不使用「上游代理」设置与环境变量代理"
            checked={f.proxy_disabled}
            onChange={(v) => setF((s) => ({ ...s, proxy_disabled: v }))}
          />
          <DialogFooter className="mt-1">
            <Button variant="outline" onClick={onClose}>
              取消
            </Button>
            <Button type="submit" disabled={save.isPending}>
              {save.isPending && <Loader2 className="animate-spin" />}
              {source ? "保存" : "测试并添加"}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}

function ToggleRow({ label, description, checked, onChange }: { label: string; description?: string; checked: boolean; onChange: (v: boolean) => void }) {
  return (
    <label className="flex items-center justify-between gap-3 rounded-md border px-3 py-2.5 text-sm">
      <span>
        {label}
        {description && <span className="block text-xs text-muted-foreground">{description}</span>}
      </span>
      <Switch checked={checked} onCheckedChange={onChange} aria-label={label} />
    </label>
  )
}

function ProbeResult({ pending, probe }: { pending: boolean; probe: { result?: SourceProbeResult; error?: string } | null }) {
  if (pending)
    return (
      <div className="flex items-center gap-2 rounded-md border border-dashed px-3 py-3 text-[13px] text-muted-foreground">
        <Loader2 className="h-4 w-4 animate-spin text-primary" />
        正在请求 Feed…
      </div>
    )
  if (!probe) return null
  if (probe.error)
    return (
      <div role="alert" className="flex gap-2 rounded-md border border-destructive/30 bg-destructive/5 px-3 py-2.5 text-[13px]">
        <AlertTriangle className="mt-0.5 h-4 w-4 shrink-0 text-destructive-text" />
        <span className="wrap-anywhere">
          <strong className="font-semibold">测试失败</strong> {probe.error}
        </span>
      </div>
    )
  const r = probe.result!
  return (
    <div className="rounded-md border border-primary/25 bg-primary-soft/40 px-3 py-2.5 text-[13px]">
      <p className="flex flex-wrap items-center gap-x-2 gap-y-1">
        <CheckCircle2 className="h-4 w-4 text-primary-text" />
        <strong className="min-w-0 truncate font-semibold" title={r.title}>
          {r.title || "（无标题）"}
        </strong>
        <Badge variant="outline">{r.format}</Badge>
        <span className="text-muted-foreground">{r.item_count} 条</span>
      </p>
      {r.samples.length > 0 && (
        <ul className="mt-2 space-y-1.5 border-t border-primary/15 pt-2">
          {r.samples.map((x, i) => (
            <li key={i} className="min-w-0">
              <p className="line-clamp-2 wrap-anywhere leading-snug" title={x.title}>
                {x.title}
              </p>
              <p className="mt-0.5 flex flex-wrap items-center gap-x-2 text-[11px] text-muted-foreground">
                {x.infohash ? <Mono className="text-[11px]">{x.infohash.slice(0, 12)}…</Mono> : null}
                {x.torrent_url && <span>.torrent</span>}
                {x.magnet && (
                  <span className="inline-flex items-center gap-0.5">
                    <Magnet className="h-3 w-3" />
                    磁力
                  </span>
                )}
                {x.published_at && <span>{formatDateTime(x.published_at)}</span>}
              </p>
            </li>
          ))}
        </ul>
      )}
    </div>
  )
}

const JOB_STATUS: Record<MetadataJob["status"], { label: string; variant: "soft" | "accent-soft" | "muted" | "destructive" }> = {
  pending: { label: "等待", variant: "muted" },
  running: { label: "获取中", variant: "accent-soft" },
  done: { label: "完成", variant: "soft" },
  failed: { label: "失败", variant: "destructive" },
}
const JOBS_COLLAPSED = 10

/** 元数据队列：只有磁力的条目由 qB 获取文件列表 */
function MetadataJobs({ sources, now }: { sources: Source[]; now: number }) {
  const q = useQuery({
    queryKey: JOBS_KEY,
    queryFn: adminApi.metadataJobs,
    refetchInterval: (query) => (query.state.data?.items.some((j) => j.status === "pending" || j.status === "running") ? 5_000 : 30_000),
  })
  const [expanded, setExpanded] = useState(false)
  const items = useMemo(() => q.data?.items ?? [], [q.data])
  const counts = useMemo(() => {
    const c: Record<string, number> = {}
    for (const j of items) c[j.status] = (c[j.status] ?? 0) + 1
    return c
  }, [items])
  const srcName = (id: number) => sources.find((s) => s.id === id)?.name ?? (id ? `#${id}` : "-")
  const shown = expanded ? items : items.slice(0, JOBS_COLLAPSED)

  return (
    <section aria-labelledby="meta-jobs" className="mt-8">
      <div className="mb-3 flex flex-wrap items-end justify-between gap-2">
        <div>
          <h2 id="meta-jobs" className="flex items-center gap-1.5 text-base font-semibold">
            <DatabaseZap className="h-4 w-4 text-primary-text" />
            元数据队列
          </h2>
          <p className="mt-0.5 text-[13px] text-muted-foreground">只有磁力的条目由 qB 获取文件列表后入库（最近 200 条，未完成的在前）</p>
        </div>
        {items.length > 0 && (
          <div className="flex flex-wrap gap-1.5">
            {(Object.keys(JOB_STATUS) as MetadataJob["status"][]).map((k) =>
              counts[k] ? (
                <Badge key={k} variant={JOB_STATUS[k].variant}>
                  {JOB_STATUS[k].label} {counts[k]}
                </Badge>
              ) : null,
            )}
          </div>
        )}
      </div>
      {q.error ? (
        <Card>
          <ErrorState error={q.error} onRetry={() => q.refetch()} />
        </Card>
      ) : q.isPending ? (
        <Card className="flex h-24 items-center justify-center">
          <Spinner />
        </Card>
      ) : items.length === 0 ? (
        <Card>
          <EmptyState className="py-8" title="队列为空" description="抓取到只有磁力链接的条目时会出现在这里" />
        </Card>
      ) : (
        <Card className="overflow-hidden">
          <ul className="divide-y">
            {shown.map((j) => {
              const st = JOB_STATUS[j.status] ?? { label: j.status, variant: "muted" as const }
              return (
                <li key={j.infohash} className="flex flex-col gap-1 px-3 py-2.5 text-[13px] sm:flex-row sm:items-center sm:gap-3 sm:px-4">
                  <div className="flex min-w-0 items-center gap-2 sm:w-[22rem] sm:shrink-0">
                    <Badge variant={st.variant} className="w-14 justify-center">
                      {j.status === "running" && <Loader2 className="animate-spin" />}
                      {st.label}
                    </Badge>
                    <Link to={watchHref(j.infohash)} className="min-w-0 truncate hover:text-primary-text hover:underline" title={j.infohash}>
                      <Mono>{j.infohash}</Mono>
                    </Link>
                  </div>
                  <div className="flex min-w-0 flex-1 flex-wrap items-center gap-x-3 gap-y-0.5 text-xs text-muted-foreground">
                    <span>{srcName(j.source_id)}</span>
                    <span className="tabular-nums">尝试 {j.attempts} 次</span>
                    {j.last_error && (
                      <span className="min-w-0 truncate text-destructive-text" title={j.last_error}>
                        {j.last_error}
                      </span>
                    )}
                  </div>
                  <span className="shrink-0 text-xs tabular-nums text-muted-foreground" title={formatDateTime(j.updated_at)}>
                    {j.status === "pending" && j.next_at ? `计划 ${timeAgo(j.next_at, now)}` : timeAgo(j.updated_at, now)}
                  </span>
                </li>
              )
            })}
          </ul>
          {items.length > JOBS_COLLAPSED && (
            <button
              type="button"
              onClick={() => setExpanded((v) => !v)}
              className="flex h-10 w-full items-center justify-center gap-1 border-t text-[13px] text-muted-foreground hover:bg-muted/60 hover:text-foreground"
              aria-expanded={expanded}
            >
              {expanded ? "收起" : `显示全部 ${items.length} 条`}
              <ChevronDown className={cn("h-4 w-4 transition-transform", expanded && "rotate-180")} />
            </button>
          )}
        </Card>
      )}
    </section>
  )
}
