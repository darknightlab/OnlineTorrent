import { useState, type FormEvent } from "react"
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { FlaskConical, Loader2, Pencil, Plus, Trash2 } from "lucide-react"
import { Button } from "@/components/ui/button"
import { Card } from "@/components/ui/card"
import { Dialog, DialogContent, DialogFooter } from "@/components/ui/dialog"
import { Field, Input, Select, Textarea } from "@/components/ui/input"
import { Switch } from "@/components/ui/switch"
import { Badge } from "@/components/ui/badge"
import { toast } from "@/components/ui/toast"
import { ActionBadge } from "@/components/badges"
import { errorMessage } from "@/components/states"
import { toList } from "@/lib/api"
import { adminApi } from "./api"
import type { AccessAction } from "@/lib/types"
import type { PolicyTestResponse, Rule, RuleField, RuleInput, RuleLabel, RuleOp } from "./types"
import { AdminHeader, DataView, Mono, type Column } from "./components"

const FIELDS: { v: RuleField; l: string }[] = [
  { v: "title", l: "标题" },
  { v: "file", l: "文件名" },
  { v: "infohash", l: "Infohash" },
  { v: "team", l: "字幕组" },
  { v: "tag", l: "标签" },
  { v: "any", l: "任意字段" },
]
const OPS: { v: RuleOp; l: string }[] = [
  { v: "contains", l: "包含" },
  { v: "regex", l: "正则" },
  { v: "equals", l: "等于" },
]
const ACTIONS: { v: AccessAction; l: string }[] = [
  { v: "allow", l: "允许" },
  { v: "login", l: "需登录" },
  { v: "vip", l: "需大会员" },
  { v: "block", l: "屏蔽" },
]
const LABELS: { v: RuleLabel; l: string }[] = [
  { v: "", l: "无" },
  { v: "political", l: "政治" },
  { v: "porn", l: "色情" },
  { v: "copyright", l: "版权" },
  { v: "other", l: "其他" },
]
const fieldLabel = (f: RuleField) => FIELDS.find((x) => x.v === f)?.l ?? f
const opLabel = (o: RuleOp) => OPS.find((x) => x.v === o)?.l ?? o
const labelText = (l: RuleLabel) => LABELS.find((x) => x.v === l)?.l ?? l

const EMPTY: RuleInput = { name: "", enabled: true, priority: 50, match: { field: "title", op: "contains", value: "" }, action: "block", label: "", note: "" }
const KEY = ["admin", "rules"]

export default function Rules() {
  const qc = useQueryClient()
  const q = useQuery({ queryKey: KEY, queryFn: adminApi.rules })
  const [editing, setEditing] = useState<Rule | "new" | null>(null)
  const invalidate = () => qc.invalidateQueries({ queryKey: KEY })
  const toggle = useMutation({ mutationFn: (r: Rule) => adminApi.updateRule(r.id, { enabled: !r.enabled }), onSuccess: invalidate, onError: (e) => toast.error(errorMessage(e)) })
  const del = useMutation({ mutationFn: (id: number) => adminApi.deleteRule(id), onSuccess: invalidate, onError: (e) => toast.error(errorMessage(e)) })

  const columns: Column<Rule>[] = [
    {
      key: "name",
      header: "规则",
      mobile: "title",
      cell: (r) => (
        <div className={r.enabled ? "md:min-w-32" : "opacity-55 md:min-w-32"}>
          <p className="font-medium">{r.name}</p>
          {r.note && <p className="text-xs text-muted-foreground">{r.note}</p>}
        </div>
      ),
    },
    { key: "priority", header: "优先级", cell: (r) => <span className="tabular-nums">{r.priority}</span>, className: "w-20" },
    {
      key: "match",
      header: "匹配",
      cell: (r) => (
        <span className="inline-flex flex-wrap items-center justify-end gap-1 md:justify-start">
          <Badge variant="outline">{fieldLabel(r.match.field)}</Badge>
          <span className="text-xs text-muted-foreground">{opLabel(r.match.op)}</span>
          <Mono className="max-w-56 truncate">{r.match.value}</Mono>
        </span>
      ),
    },
    {
      key: "action",
      header: "动作",
      cell: (r) => (
        <span className="inline-flex items-center gap-1">
          <ActionBadge action={r.action} />
          {r.label && <Badge variant="muted">{labelText(r.label)}</Badge>}
        </span>
      ),
    },
    { key: "enabled", header: "启用", cell: (r) => <Switch checked={r.enabled} onCheckedChange={() => toggle.mutate(r)} aria-label={`启用 ${r.name}`} />, className: "w-16" },
  ]

  return (
    <div>
      <AdminHeader
        title="策略规则"
        description="按优先级从高到低匹配，第一条命中的规则决定访问级别；政治/色情标签一律屏蔽"
        actions={
          <Button onClick={() => setEditing("new")}>
            <Plus />
            新建规则
          </Button>
        }
      />
      <div className="grid gap-5 xl:grid-cols-[minmax(0,1fr)_340px]">
        <div className="min-w-0">
          <DataView
            columns={columns}
            rows={q.data ? toList(q.data).items : undefined}
            loading={q.isPending}
            error={q.error}
            onRetry={() => q.refetch()}
            rowKey={(r) => r.id}
            empty="还没有规则"
            actions={(r) => (
              <>
                <Button variant="ghost" size="icon-sm" aria-label={`编辑 ${r.name}`} onClick={() => setEditing(r)}>
                  <Pencil />
                </Button>
                <Button variant="ghost" size="icon-sm" aria-label={`删除 ${r.name}`} className="text-destructive-text hover:bg-destructive/10" onClick={() => confirm(`删除规则「${r.name}」？`) && del.mutate(r.id)}>
                  <Trash2 />
                </Button>
              </>
            )}
          />
        </div>
        <PolicyTester />
      </div>
      {editing && <RuleDialog rule={editing === "new" ? null : editing} onClose={() => setEditing(null)} onSaved={invalidate} />}
    </div>
  )
}

function RuleDialog({ rule, onClose, onSaved }: { rule: Rule | null; onClose: () => void; onSaved: () => void }) {
  const [f, setF] = useState<RuleInput>(() => (rule ? { ...rule } : EMPTY))
  const [regexError, setRegexError] = useState("")
  const m = useMutation({
    mutationFn: () => (rule ? adminApi.updateRule(rule.id, f) : adminApi.createRule(f)),
    onSuccess: () => {
      toast.success("规则已保存")
      onSaved()
      onClose()
    },
    onError: (e) => toast.error(errorMessage(e)),
  })
  function submit(e: FormEvent) {
    e.preventDefault()
    if (f.match.op === "regex") {
      try {
        new RegExp(f.match.value.replace(/^\(\?i\)/, ""))
      } catch (err) {
        return setRegexError(`正则无效：${(err as Error).message}`)
      }
    }
    m.mutate()
  }
  const setMatch = (patch: Partial<RuleInput["match"]>) => setF((s) => ({ ...s, match: { ...s.match, ...patch } }))
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent title={rule ? "编辑规则" : "新建规则"}>
        <form onSubmit={submit} className="grid gap-3">
          <div className="grid grid-cols-[1fr_96px] gap-3">
            <Field label="名称" htmlFor="r-name">
              <Input id="r-name" required value={f.name} onChange={(e) => setF((s) => ({ ...s, name: e.target.value }))} />
            </Field>
            <Field label="优先级" htmlFor="r-pri">
              <Input id="r-pri" type="number" value={f.priority} onChange={(e) => setF((s) => ({ ...s, priority: Number(e.target.value) }))} />
            </Field>
          </div>
          <div className="grid grid-cols-2 gap-3">
            <Field label="匹配字段" htmlFor="r-field">
              <Select id="r-field" value={f.match.field} onChange={(e) => setMatch({ field: e.target.value as RuleField })}>
                {FIELDS.map((x) => (
                  <option key={x.v} value={x.v}>
                    {x.l}
                  </option>
                ))}
              </Select>
            </Field>
            <Field label="方式" htmlFor="r-op">
              <Select id="r-op" value={f.match.op} onChange={(e) => setMatch({ op: e.target.value as RuleOp })}>
                {OPS.map((x) => (
                  <option key={x.v} value={x.v}>
                    {x.l}
                  </option>
                ))}
              </Select>
            </Field>
          </div>
          <Field label="匹配值" htmlFor="r-value" error={regexError || undefined} hint={f.match.op === "regex" ? "RE2 语法，(?i) 表示忽略大小写" : undefined}>
            <Input
              id="r-value"
              required
              className="font-mono"
              value={f.match.value}
              onChange={(e) => {
                setRegexError("")
                setMatch({ value: e.target.value })
              }}
            />
          </Field>
          <div className="grid grid-cols-2 gap-3">
            <Field label="动作" htmlFor="r-action">
              <Select id="r-action" value={f.action} onChange={(e) => setF((s) => ({ ...s, action: e.target.value as AccessAction }))}>
                {ACTIONS.map((x) => (
                  <option key={x.v} value={x.v}>
                    {x.l}
                  </option>
                ))}
              </Select>
            </Field>
            <Field label="标签" htmlFor="r-label">
              <Select id="r-label" value={f.label} onChange={(e) => setF((s) => ({ ...s, label: e.target.value as RuleLabel }))}>
                {LABELS.map((x) => (
                  <option key={x.v} value={x.v}>
                    {x.l}
                  </option>
                ))}
              </Select>
            </Field>
          </div>
          <Field label="备注" htmlFor="r-note">
            <Textarea id="r-note" rows={2} className="min-h-0" value={f.note} onChange={(e) => setF((s) => ({ ...s, note: e.target.value }))} />
          </Field>
          <label className="flex items-center justify-between rounded-md border px-3 py-2.5 text-sm">
            启用
            <Switch checked={f.enabled} onCheckedChange={(v) => setF((s) => ({ ...s, enabled: v }))} aria-label="启用规则" />
          </label>
          <DialogFooter>
            <Button variant="outline" onClick={onClose}>
              取消
            </Button>
            <Button type="submit" disabled={m.isPending}>
              {m.isPending && <Loader2 className="animate-spin" />}
              保存
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}

function PolicyTester() {
  const [f, setF] = useState({ title: "[黒ネズミたち] 药屋少女的呢喃 第三季 - 02 (CR 3840x2160 HEVC AAC MKV)", infohash: "", team: "", tags: "", files: "" })
  const [res, setRes] = useState<PolicyTestResponse | null>(null)
  const m = useMutation({
    mutationFn: () =>
      adminApi.testPolicy({
        title: f.title,
        infohash: f.infohash || undefined,
        team: f.team || undefined,
        tags: f.tags ? f.tags.split(/[,，]/).map((s) => s.trim()).filter(Boolean) : undefined,
        files: f.files ? f.files.split("\n").map((s) => s.trim()).filter(Boolean) : undefined,
      }),
    onSuccess: setRes,
    onError: (e) => toast.error(errorMessage(e)),
  })
  const set = (k: keyof typeof f) => (e: React.ChangeEvent<HTMLInputElement | HTMLTextAreaElement>) => setF((s) => ({ ...s, [k]: e.target.value }))
  return (
    <Card className="h-fit p-4 xl:sticky xl:top-20">
      <h2 className="flex items-center gap-2 text-sm font-semibold">
        <FlaskConical className="h-4 w-4 text-accent-text" />
        测试策略
      </h2>
      <p className="mt-0.5 text-xs text-muted-foreground">输入种子信息，查看会命中哪条规则</p>
      <form
        className="mt-3 grid gap-3"
        onSubmit={(e) => {
          e.preventDefault()
          m.mutate()
        }}
      >
        <Field label="标题" htmlFor="t-title">
          <Textarea id="t-title" rows={2} className="min-h-0 text-[13px]" value={f.title} onChange={set("title")} required />
        </Field>
        <div className="grid grid-cols-2 gap-3">
          <Field label="字幕组" htmlFor="t-team">
            <Input id="t-team" value={f.team} onChange={set("team")} />
          </Field>
          <Field label="标签（逗号分隔）" htmlFor="t-tags">
            <Input id="t-tags" value={f.tags} onChange={set("tags")} />
          </Field>
        </div>
        <Field label="Infohash" htmlFor="t-ih">
          <Input id="t-ih" value={f.infohash} onChange={set("infohash")} className="font-mono text-xs" />
        </Field>
        <Field label="文件名（每行一个）" htmlFor="t-files">
          <Textarea id="t-files" rows={2} className="min-h-0 font-mono text-xs" value={f.files} onChange={set("files")} />
        </Field>
        <Button type="submit" variant="accent" disabled={m.isPending}>
          {m.isPending ? <Loader2 className="animate-spin" /> : <FlaskConical />}
          测试
        </Button>
      </form>
      {res && (
        <div className="mt-3 rounded-lg border bg-muted/50 p-3 text-[13px]" aria-live="polite">
          <div className="flex items-center gap-2">
            结果：
            <ActionBadge action={res.access.action} />
          </div>
          <p className="mt-1.5 text-muted-foreground">
            {res.matched_rule ? (
              <>
                命中规则 <span className="font-medium text-foreground">#{res.matched_rule.id} {res.matched_rule.name}</span>
              </>
            ) : (
              `未命中任何规则（${res.access.reason || "默认动作"}）`
            )}
          </p>
        </div>
      )}
    </Card>
  )
}
