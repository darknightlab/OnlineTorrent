// 决策模型（Cloudflare Workers AI · Clef）：状态、设置、试运行、决策记录与审核队列
import { useEffect, useMemo, useState } from "react"
import { Link } from "react-router-dom"
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { AlertTriangle, BrainCircuit, Check, FlaskConical, Loader2, RefreshCw, RotateCcw, Save, Undo2, X } from "lucide-react"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Card } from "@/components/ui/card"
import { Input } from "@/components/ui/input"
import { Skeleton } from "@/components/ui/skeleton"
import { Tabs, TabsList, TabsTrigger } from "@/components/ui/tabs"
import { toast } from "@/components/ui/toast"
import { EmptyState, ErrorState, Pager, Spinner, errorMessage } from "@/components/states"
import { api } from "@/lib/api"
import type { ClefAnswerSummary, ClefDecision, ClefFileDecision, ClefStatus, ClefStatusName, ClefTestResponse, SettingItem, SettingValue } from "@/lib/types"
import { formatDateTime, timeAgo } from "@/lib/format"
import { useNow } from "@/lib/hooks"
import { cn, watchHref } from "@/lib/utils"
import { AdminHeader, Mono, StatusDot } from "./components"
import { SettingRow } from "./Settings"

const KEY = ["admin", "clef"]
const GROUP = "决策模型"

const STATUS_LABEL: Record<ClefStatusName, string> = { applied: "已应用", pending: "待审核", rejected: "已拒绝", discarded: "未映射" }
const STATUS_VARIANT: Record<ClefStatusName, "soft" | "warning" | "destructive" | "muted"> = {
  applied: "soft",
  pending: "warning",
  rejected: "destructive",
  discarded: "muted",
}
const REASON_LABEL: Record<string, string> = {
  none: "模型选择「无」",
  invalid_subject: "条目不在候选中",
  not_episode: "不是正片 / 特别篇",
  no_number: "没有集数",
  invalid_number: "集数无效",
  episode_not_official: "该集不在官方章节列表",
  season_mismatch: "季数与文件名不符",
  low_confidence: "置信度不足",
  conflict: "文件已被修改",
  no_answer: "缺少回答",
  old_entry: "条目早已完结，不自动应用",
}
const KIND_LABEL: Record<string, string> = { episode: "正片", sp: "SP", op: "NCOP", ed: "NCED", pv: "PV", cm: "CM" }
const WHY_LABEL: Record<string, string> = { anchor: "规则锚点", hint: "规则推测", search: "搜索", related: "关联条目" }

const pct = (p: number) => `${Math.round(p * 1000) / 10}%`

export default function ClefPage() {
  const qc = useQueryClient()
  const q = useQuery({ queryKey: KEY, queryFn: api.admin.clef, refetchInterval: 15_000 })
  return (
    <div>
      <AdminHeader
        title="决策模型"
        description="规则无法确定的新收录文件，由 Cloudflare Clef 在候选条目与文件名中的数字里选择；高置信直接写入映射（来源 clef），中等进入审核队列，其余不映射"
        actions={
          <Button variant="outline" size="sm" onClick={() => void qc.invalidateQueries({ queryKey: ["admin", "clef"] })}>
            <RefreshCw />
            刷新
          </Button>
        }
      />
      {q.error ? (
        <Card>
          <ErrorState error={q.error} onRetry={() => q.refetch()} />
        </Card>
      ) : (
        <StatusCard s={q.data} />
      )}
      <div className="mt-4 grid gap-4 xl:grid-cols-[minmax(0,1fr)_400px]">
        <div className="min-w-0 space-y-4">
          <Decisions />
        </div>
        <div className="min-w-0 space-y-4">
          <TestBox />
          <SettingsCard />
          <JobsCard s={q.data} />
        </div>
      </div>
    </div>
  )
}

function StatusCard({ s }: { s?: ClefStatus }) {
  const now = useNow(15_000)
  if (!s)
    return (
      <Card className="space-y-3 p-4">
        <Skeleton className="h-4 w-1/3" />
        <Skeleton className="h-14 w-full" />
      </Card>
    )
  const stats = [
    { label: "今日请求", value: `${s.today.toLocaleString("zh-CN")} / ${s.daily_cap.toLocaleString("zh-CN")}` },
    { label: "队列", value: (s.jobs.pending ?? 0).toLocaleString("zh-CN") },
    { label: "待审核", value: (s.decisions.pending ?? 0).toLocaleString("zh-CN"), warn: (s.decisions.pending ?? 0) > 0 },
    { label: "已应用", value: (s.decisions.applied ?? 0).toLocaleString("zh-CN") },
    { label: "未映射", value: (s.decisions.discarded ?? 0).toLocaleString("zh-CN") },
  ]
  const th = s.thresholds
  return (
    <Card className="overflow-hidden">
      <div className="flex flex-wrap items-center gap-x-4 gap-y-1.5 border-b px-4 py-2.5 text-[13px]">
        <span className="flex items-center gap-1.5 font-semibold">
          <BrainCircuit className="h-4 w-4 text-primary-text" />
          Clef
        </span>
        <StatusDot ok={s.enabled && s.configured ? true : s.enabled ? false : null} label={s.enabled ? (s.configured ? "已启用" : "已启用 · 未配置") : "未启用"} />
        <Badge variant="outline" className="font-mono">
          {s.model}
        </Badge>
        <span className="text-xs text-muted-foreground">
          {th.metric === "prob" ? "概率" : "confidence"} ≥ {th.apply} 且差 ≥ {th.margin} 直接应用 · ≥ {th.review} 审核 · {s.rate_per_min} 次/分钟
        </span>
        {s.current && (
          <span className="inline-flex items-center gap-1 text-xs text-primary-text">
            <Loader2 className="h-3 w-3 animate-spin" />
            处理中 <Mono>{s.current.slice(0, 10)}…</Mono>
          </span>
        )}
      </div>
      <dl className="grid grid-cols-2 gap-2 p-4 sm:grid-cols-5">
        {stats.map((x) => (
          <div key={x.label} className="rounded-lg bg-muted/60 px-3 py-2">
            <dt className="text-xs text-muted-foreground">{x.label}</dt>
            <dd className={cn("text-lg font-bold tabular-nums", x.warn && "text-warning-text")}>{x.value}</dd>
          </div>
        ))}
      </dl>
      {s.last_error && (
        <div className="mx-4 mb-4 flex gap-2 rounded-lg bg-destructive/10 px-3 py-2 text-xs text-destructive-text">
          <AlertTriangle className="mt-px h-3.5 w-3.5 shrink-0" />
          <p className="min-w-0 wrap-anywhere">
            {s.last_error}
            {s.last_error_at && <span className="ml-1 opacity-80">· {timeAgo(s.last_error_at, now)}</span>}
          </p>
        </div>
      )}
    </Card>
  )
}

// ---- decisions / review queue ----

function Decisions() {
  const [status, setStatus] = useState<string>("pending")
  const [page, setPage] = useState(1)
  const q = useQuery({ queryKey: ["admin", "clef", "decisions", status, page], queryFn: () => api.admin.clefDecisions(status === "all" ? "" : status, page) })
  const items = q.data?.items ?? []
  return (
    <section aria-labelledby="clef-decisions">
      <div className="mb-3 flex flex-wrap items-center justify-between gap-2">
        <h2 id="clef-decisions" className="text-sm font-semibold">
          决策记录
        </h2>
        <Tabs
          value={status}
          onValueChange={(v) => {
            setStatus(v)
            setPage(1)
          }}
        >
          <TabsList className="max-w-full overflow-x-auto">
            <TabsTrigger value="pending">审核队列</TabsTrigger>
            <TabsTrigger value="applied">已应用</TabsTrigger>
            <TabsTrigger value="discarded">未映射</TabsTrigger>
            <TabsTrigger value="rejected">已拒绝</TabsTrigger>
            <TabsTrigger value="all">全部</TabsTrigger>
          </TabsList>
        </Tabs>
      </div>
      {q.error ? (
        <Card>
          <ErrorState error={q.error} onRetry={() => q.refetch()} />
        </Card>
      ) : q.isPending ? (
        <Card className="flex h-40 items-center justify-center">
          <Spinner />
        </Card>
      ) : items.length === 0 ? (
        <Card>
          <EmptyState icon={Check} title={status === "pending" ? "审核队列已清空" : "暂无记录"} />
        </Card>
      ) : (
        <ul className="grid gap-3">
          {items.map((d) => (
            <DecisionCard key={d.id} d={d} />
          ))}
        </ul>
      )}
      <Pager page={page} pageCount={q.data?.page_count ?? 1} onChange={setPage} />
    </section>
  )
}

function subjectLabel(d: { title?: string; title_cn?: string }) {
  return d.title_cn || d.title || ""
}

function DecisionCard({ d }: { d: ClefDecision }) {
  const qc = useQueryClient()
  const m = useMutation({
    mutationFn: (accept: boolean) => (accept ? api.admin.clefAccept(d.id) : api.admin.clefReject(d.id)),
    onSuccess: (r) => {
      toast.success(r.status === "applied" ? "已应用映射" : d.status === "applied" ? "已撤销映射" : "已拒绝")
      void qc.invalidateQueries({ queryKey: KEY })
    },
    onError: (e) => toast.error(errorMessage(e)),
  })
  const base = d.path.split("/").pop() ?? d.path
  return (
    <li>
      <Card className="p-4">
        <div className="flex flex-wrap items-start justify-between gap-2">
          <div className="min-w-0 flex-1">
            <a href={watchHref(d.infohash, d.file_index)} target="_blank" rel="noreferrer" className="wrap-anywhere text-sm font-medium hover:text-primary-text hover:underline">
              {base}
            </a>
            <p className="mt-0.5 wrap-anywhere text-xs text-muted-foreground">{d.torrent_title}</p>
          </div>
          <span className="flex shrink-0 items-center gap-2 text-xs text-muted-foreground">
            <Badge variant={STATUS_VARIANT[d.status]}>{STATUS_LABEL[d.status]}</Badge>
            <span title={formatDateTime(d.created_at)}>{timeAgo(d.created_at)}</span>
          </span>
        </div>
        <div className="mt-2 flex flex-wrap items-center gap-x-3 gap-y-1 rounded-md bg-muted/60 px-3 py-2 text-[13px]">
          {d.work_key ? (
            <>
              {d.subject_id ? (
                <Link to={`/subject/id/${d.subject_id}`} className="font-medium hover:text-primary-text hover:underline">
                  {subjectLabel(d)}
                </Link>
              ) : (
                <span className="font-medium">{d.work_key}</span>
              )}
              {d.episode_key && <Badge variant="soft">{KIND_LABEL[d.kind] && d.kind !== "episode" ? d.episode_key.toUpperCase() : `第 ${d.episode_key} 话`}</Badge>}
            </>
          ) : (
            <span className="text-muted-foreground">无条目</span>
          )}
          <span className="tabular-nums text-xs text-muted-foreground">
            指标 {pct(d.score)} · 差 {pct(d.margin)} · conf {pct(d.confidence)} · <span className="font-mono">{d.model}</span>
          </span>
          {d.reason && <span className="text-xs text-warning-text">{REASON_LABEL[d.reason] ?? d.reason}</span>}
        </div>
        <AnswerDetails answers={d.answers} />
        <div className="mt-3 flex flex-wrap items-center justify-end gap-2">
          {d.decided_by && d.status !== "pending" && (
            <span className="mr-auto text-xs text-muted-foreground">
              {d.decided_by === "clef" ? "自动应用" : `${d.decided_by} 处理`} · {timeAgo(d.decided_at)}
            </span>
          )}
          {d.status === "pending" && (
            <>
              <Button variant="outline" size="sm" onClick={() => m.mutate(false)} disabled={m.isPending}>
                <X />
                拒绝
              </Button>
              <Button size="sm" onClick={() => m.mutate(true)} disabled={m.isPending || !d.work_key}>
                {m.isPending ? <Loader2 className="animate-spin" /> : <Check />}
                接受
              </Button>
            </>
          )}
          {d.status === "applied" && (
            <Button variant="outline" size="sm" onClick={() => m.mutate(false)} disabled={m.isPending}>
              {m.isPending ? <Loader2 className="animate-spin" /> : <Undo2 />}
              撤销映射
            </Button>
          )}
        </div>
      </Card>
    </li>
  )
}

/** 每个问题的选择与前几个选项的概率 */
function AnswerDetails({ answers, open }: { answers: Record<string, ClefAnswerSummary>; open?: boolean }) {
  const ids = Object.keys(answers).sort((a, b) => order(a) - order(b) || a.localeCompare(b))
  if (ids.length === 0) return null
  return (
    <details className="group mt-2" open={open}>
      <summary className="cursor-pointer select-none text-xs text-muted-foreground hover:text-foreground">模型回答（{ids.length} 个问题）</summary>
      <div className="mt-2 grid gap-2 sm:grid-cols-2">
        {ids.map((id) => (
          <AnswerBox key={id} id={id} a={answers[id]} />
        ))}
      </div>
    </details>
  )
}

function order(id: string) {
  if (id.endsWith("subject")) return 0
  if (id === "verify") return 0.5
  if (id.endsWith("_kind")) return 1
  return 2
}

function questionLabel(id: string) {
  if (id.endsWith("subject")) return "条目"
  if (id === "verify") return "条目确认（是 / 否）"
  if (id.endsWith("_kind")) return "类型"
  if (id.endsWith("_ep")) return "集数"
  return id
}

function AnswerBox({ id, a }: { id: string; a: ClefAnswerSummary }) {
  const list = a.conditioned?.length ? a.conditioned : a.top
  return (
    <div className="rounded-md border px-2.5 py-2">
      <div className="mb-1 flex items-center justify-between gap-2 text-xs">
        <span className="font-medium">
          {questionLabel(id)} <Mono className="text-muted-foreground">{id}</Mono>
        </span>
        <span className="tabular-nums text-muted-foreground">conf {pct(a.confidence)}</span>
      </div>
      <ul className="grid gap-1">
        {list.map((o) => (
          <li key={o.id} className="grid grid-cols-[minmax(0,1fr)_3.5rem] items-center gap-2 text-xs">
            <span className="relative min-w-0 overflow-hidden rounded bg-muted">
              <span className="absolute inset-y-0 left-0 bg-primary/25" style={{ width: `${Math.max(2, o.p * 100)}%` }} />
              <span className="relative block truncate px-1.5 py-0.5 font-mono">{o.id}</span>
            </span>
            <span className="text-right tabular-nums">{pct(o.p)}</span>
          </li>
        ))}
      </ul>
      {a.conditioned?.length ? <p className="mt-1 text-[11px] text-muted-foreground">已排除没有该集 / 季数不符的候选（原始首选 {a.top[0]?.id} {pct(a.top[0]?.p ?? 0)}）</p> : null}
    </div>
  )
}

// ---- dry run ----

function TestBox() {
  const [ih, setIh] = useState("")
  const qc = useQueryClient()
  const m = useMutation({
    mutationFn: () => api.admin.clefTest(ih.trim()),
    onSuccess: () => void qc.invalidateQueries({ queryKey: KEY }),
    onError: (e) => toast.error(errorMessage(e)),
  })
  const valid = /^[0-9a-fA-F]{40}$/.test(ih.trim())
  return (
    <Card className="overflow-hidden">
      <div className="border-b px-4 py-2.5">
        <h2 className="flex items-center gap-1.5 text-sm font-semibold">
          <FlaskConical className="h-4 w-4 text-primary-text" />
          测试
        </h2>
        <p className="text-xs text-muted-foreground">对本地库中的种子试运行（不写入，计入每日请求数）</p>
      </div>
      <form
        className="flex gap-2 p-4"
        onSubmit={(e) => {
          e.preventDefault()
          if (valid) m.mutate()
        }}
      >
        <Input aria-label="infohash" placeholder="40 位 infohash" value={ih} onChange={(e) => setIh(e.target.value)} className="font-mono text-xs" spellCheck={false} />
        <Button type="submit" disabled={!valid || m.isPending}>
          {m.isPending ? <Loader2 className="animate-spin" /> : <FlaskConical />}
          运行
        </Button>
      </form>
      {m.data && <TestResult r={m.data} />}
    </Card>
  )
}

function TestResult({ r }: { r: ClefTestResponse }) {
  const o = r.outcome
  const byIndex = useMemo(() => new Map((o?.decisions ?? []).map((d) => [d.file.index, d])), [o])
  const cands = useMemo(() => new Map((o?.task.candidates ?? []).map((c) => [c.work_key, c])), [o])
  return (
    <div className="space-y-3 border-t px-4 py-3 text-[13px]">
      {r.error && <p className="rounded-md bg-destructive/10 px-3 py-2 text-xs text-destructive-text">{r.error}</p>}
      {o && (
        <p className="text-xs text-muted-foreground">
          {o.task.title} · {o.sent} 次请求 · {o.usage.input_tokens.toLocaleString("zh-CN")} tokens · {o.task.per_file ? "逐文件判断条目" : "整个种子一个条目问题"}
        </p>
      )}
      <div>
        <h3 className="mb-1 text-xs font-semibold text-muted-foreground">文件（规则 → 模型）</h3>
        <ul className="grid gap-2">
          {r.rules
            .filter((f) => f.queue || f.work_key)
            .map((f) => {
              const d = byIndex.get(f.index)
              const rule = f.work_key ? cands.get(f.work_key) : undefined
              return (
                <li key={f.index} className="rounded-md border px-2.5 py-2">
                  <p className="wrap-anywhere font-mono text-[11px]">{f.path}</p>
                  <p className="mt-1 flex flex-wrap items-center gap-1.5 text-xs">
                    <span className="text-muted-foreground">规则：</span>
                    {f.work_key ? (
                      <Badge variant="soft">
                        {rule ? rule.cn || rule.title : f.work_key} {f.episode ?? ""}
                      </Badge>
                    ) : (
                      <Badge variant="warning">不确定 → 模型</Badge>
                    )}
                  </p>
                  {d && <FileOutcome d={d} />}
                </li>
              )
            })}
        </ul>
      </div>
      {o && o.task.candidates.length > 0 && (
        <details>
          <summary className="cursor-pointer select-none text-xs font-semibold text-muted-foreground">候选条目（{o.task.candidates.length}）</summary>
          <ul className="mt-2 grid gap-1">
            {o.task.candidates.map((c) => (
              <li key={c.id} className="flex flex-wrap items-baseline gap-x-2 text-xs">
                <Mono className="text-muted-foreground">{c.id}</Mono>
                <span className="font-medium">{c.cn || c.title}</span>
                <span className="text-muted-foreground">
                  {[c.type, c.season ? `第 ${c.season} 季` : "", c.air_date, c.eps ? `${c.eps} 集` : ""].filter(Boolean).join(" · ")}
                </span>
                <Badge variant="outline">{WHY_LABEL[c.why] ?? c.why}</Badge>
              </li>
            ))}
          </ul>
        </details>
      )}
    </div>
  )
}

function FileOutcome({ d }: { d: ClefFileDecision }) {
  return (
    <div className="mt-1">
      <p className="flex flex-wrap items-center gap-1.5 text-xs">
        <span className="text-muted-foreground">模型：</span>
        <Badge variant={STATUS_VARIANT[d.status]}>{d.status === "applied" ? "将直接应用" : d.status === "pending" ? "将进入审核" : "不映射"}</Badge>
        {d.candidate && <span className="font-medium">{d.candidate.cn || d.candidate.title}</span>}
        {d.episode_key && <Badge variant="outline">{d.episode_key}</Badge>}
        <span className="tabular-nums text-muted-foreground">
          {pct(d.score)} · 差 {pct(d.margin)}
        </span>
        {d.reason && <span className="text-warning-text">{REASON_LABEL[d.reason] ?? d.reason}</span>}
      </p>
      <AnswerDetails answers={d.answers} />
    </div>
  )
}

// ---- settings (group 决策模型 of /admin/settings) ----

function SettingsCard() {
  const qc = useQueryClient()
  const q = useQuery({ queryKey: ["admin", "settings"], queryFn: api.admin.settings })
  const items = useMemo(() => (q.data?.items ?? []).filter((i) => i.group === GROUP), [q.data])
  const base = useMemo(() => Object.fromEntries(items.map((i) => [i.key, i.type === "secret" ? "" : i.value])) as Record<string, SettingValue>, [items])
  const [draft, setDraft] = useState<Record<string, SettingValue>>({})
  useEffect(() => setDraft(base), [base])
  const dirty = items.filter((i) => JSON.stringify(draft[i.key]) !== JSON.stringify(base[i.key])).map((i) => i.key)
  const save = useMutation({
    mutationFn: () => api.admin.saveSettings({ values: Object.fromEntries(dirty.map((k) => [k, draft[k]])) }),
    onSuccess: (data) => {
      qc.setQueryData(["admin", "settings"], data)
      void qc.invalidateQueries({ queryKey: KEY })
      toast.success("设置已保存")
    },
    onError: (e) => toast.error(errorMessage(e)),
  })
  return (
    <Card className="overflow-hidden">
      <div className="flex items-center justify-between gap-2 border-b px-4 py-2.5">
        <h2 className="text-sm font-semibold">设置</h2>
        <div className="flex gap-1">
          {dirty.length > 0 && (
            <Button variant="ghost" size="xs" onClick={() => setDraft(base)}>
              <RotateCcw />
              撤销
            </Button>
          )}
          <Button size="xs" onClick={() => save.mutate()} disabled={!dirty.length || save.isPending}>
            {save.isPending ? <Loader2 className="animate-spin" /> : <Save />}
            保存{dirty.length ? `（${dirty.length}）` : ""}
          </Button>
        </div>
      </div>
      {q.error ? (
        <ErrorState error={q.error} onRetry={() => q.refetch()} />
      ) : !q.data ? (
        <div className="flex h-24 items-center justify-center">
          <Spinner />
        </div>
      ) : (
        <div className="divide-y [&>div]:md:grid-cols-1!">
          {items.map((item: SettingItem) => (
            <SettingRow key={item.key} item={item} value={draft[item.key] ?? null} dirty={dirty.includes(item.key)} onChange={(v) => setDraft((d) => ({ ...d, [item.key]: v }))} />
          ))}
        </div>
      )}
    </Card>
  )
}

// ---- queue ----

function JobsCard({ s }: { s?: ClefStatus }) {
  const qc = useQueryClient()
  const rerun = useMutation({
    mutationFn: (ih: string) => api.admin.clefRequeue(ih),
    onSuccess: (st) => {
      qc.setQueryData(KEY, st)
      toast.success("已重新加入队列")
    },
    onError: (e) => toast.error(errorMessage(e)),
  })
  const jobs = s?.recent_jobs ?? []
  return (
    <Card className="overflow-hidden">
      <div className="border-b px-4 py-2.5">
        <h2 className="text-sm font-semibold">最近任务</h2>
        <p className="text-xs text-muted-foreground">每个种子一次请求（问题超过 64 个时拆分）；失败按退避重试，最多 5 次</p>
      </div>
      {jobs.length === 0 ? (
        <p className="px-4 py-6 text-center text-xs text-muted-foreground">暂无任务</p>
      ) : (
        <ul className="divide-y">
          {jobs.map((j) => (
            <li key={j.infohash} className="flex items-start gap-2 px-4 py-2 text-xs">
              <div className="min-w-0 flex-1">
                <p className="truncate font-medium" title={j.title}>
                  {j.title || j.infohash}
                </p>
                <p className="text-muted-foreground">
                  <Badge variant={j.status === "done" ? "soft" : j.status === "failed" ? "destructive" : j.status === "pending" ? "warning" : "muted"}>
                    {{ pending: "排队", done: "完成", failed: "失败", expired: "过期" }[j.status]}
                  </Badge>{" "}
                  {j.requests} 次请求 · {timeAgo(j.updated_at)}
                  {j.last_error && <span className="ml-1 text-destructive-text">{j.last_error}</span>}
                </p>
              </div>
              <Button variant="ghost" size="xs" aria-label="重新运行" title="重新运行" onClick={() => rerun.mutate(j.infohash)} disabled={rerun.isPending}>
                <RefreshCw />
              </Button>
            </li>
          ))}
        </ul>
      )}
    </Card>
  )
}
