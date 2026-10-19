import { useEffect, useMemo, useState } from "react"
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { Eye, Loader2, PencilLine, RotateCcw, Save } from "lucide-react"
import { Button } from "@/components/ui/button"
import { Card } from "@/components/ui/card"
import { Input, Select, Textarea } from "@/components/ui/input"
import { Switch } from "@/components/ui/switch"
import { Badge } from "@/components/ui/badge"
import { toast } from "@/components/ui/toast"
import { ErrorState, Spinner, errorMessage } from "@/components/states"
import { Markdown } from "@/components/Markdown"
import { api } from "@/lib/api"
import type { SettingItem, SettingValue, SettingsResponse } from "@/lib/types"
import { formatBytes } from "@/lib/format"
import { cn } from "@/lib/utils"
import { AdminHeader } from "./components"

const KEY = ["admin", "settings"]

/** 常见枚举值的中文显示（保存的仍是原始值） */
const ENUM_LABEL: Record<string, string> = {
  allow: "允许",
  login: "需登录",
  vip: "需大会员",
  block: "屏蔽",
  open: "开放注册",
  invite: "仅邀请",
  closed: "关闭注册",
}

/** 编辑态初值：secret 恒为 ""（表示不修改） */
function initial(items: SettingItem[]) {
  return Object.fromEntries(items.map((i) => [i.key, i.type === "secret" ? "" : i.value])) as Record<string, SettingValue>
}

export default function SettingsPage() {
  const qc = useQueryClient()
  const q = useQuery({ queryKey: KEY, queryFn: api.admin.settings })
  const items = useMemo(() => q.data?.items ?? [], [q.data])
  const base = useMemo(() => initial(items), [items])
  const [draft, setDraft] = useState<Record<string, SettingValue>>({})
  useEffect(() => setDraft(base), [base])

  const dirty = useMemo(() => items.filter((i) => JSON.stringify(draft[i.key]) !== JSON.stringify(base[i.key])).map((i) => i.key), [items, draft, base])
  const groups = useMemo(() => {
    const m = new Map<string, SettingItem[]>()
    for (const i of items) m.set(i.group || "其他", [...(m.get(i.group || "其他") ?? []), i])
    return [...m.entries()]
  }, [items])

  const save = useMutation({
    mutationFn: () => api.admin.saveSettings({ values: Object.fromEntries(dirty.map((k) => [k, draft[k]])) }),
    onSuccess: (data: SettingsResponse) => {
      qc.setQueryData(KEY, data)
      void qc.invalidateQueries({ queryKey: ["site"] })
      toast.success("设置已保存")
    },
    onError: (e) => toast.error(errorMessage(e)),
  })

  if (q.error) return <ErrorState error={q.error} onRetry={() => q.refetch()} />
  if (!q.data)
    return (
      <div className="flex h-40 items-center justify-center">
        <Spinner />
      </div>
    )

  return (
    <div className="pb-20">
      <AdminHeader title="站点设置" description="所有设置保存在数据库中，保存后立即生效" />
      {groups.length > 1 && (
        <nav aria-label="设置分组" className="-mx-3 mb-4 flex gap-2 overflow-x-auto px-3 scrollbar-none sm:mx-0 sm:flex-wrap sm:px-0">
          {groups.map(([g]) => (
            <a key={g} href={`#group-${g}`} className="inline-flex h-9 shrink-0 items-center rounded-full border bg-card px-3.5 text-[13px] hover:border-primary hover:text-primary-text">
              {g}
            </a>
          ))}
        </nav>
      )}
      <div className="grid gap-4">
        {groups.map(([g, list]) => (
          <Card key={g} id={`group-${g}`} className="scroll-mt-32 divide-y">
            <h2 className="px-4 py-3 text-sm font-semibold sm:px-5">{g}</h2>
            {list.map((item) => (
              <SettingRow key={item.key} item={item} value={draft[item.key] ?? null} dirty={dirty.includes(item.key)} onChange={(v) => setDraft((d) => ({ ...d, [item.key]: v }))} />
            ))}
          </Card>
        ))}
      </div>
      <div
        className={cn(
          "fixed inset-x-0 bottom-0 z-30 border-t bg-header pb-safe backdrop-blur-md transition-transform lg:left-[max(14rem,calc((100vw-1440px)/2+14rem))]",
          dirty.length ? "translate-y-0" : "translate-y-full",
        )}
      >
        <div className="flex h-14 items-center justify-between gap-3 px-3 sm:px-5 lg:px-8">
          <span className="text-[13px] text-muted-foreground">已修改 {dirty.length} 项</span>
          <div className="flex gap-2">
            <Button variant="outline" size="sm" onClick={() => setDraft(base)}>
              <RotateCcw />
              撤销
            </Button>
            <Button size="sm" onClick={() => save.mutate()} disabled={save.isPending}>
              {save.isPending ? <Loader2 className="animate-spin" /> : <Save />}
              保存
            </Button>
          </div>
        </div>
      </div>
    </div>
  )
}

export function SettingRow({ item, value, dirty, onChange }: { item: SettingItem; value: SettingValue; dirty: boolean; onChange: (v: SettingValue) => void }) {
  const id = `setting-${item.key}`
  const [preview, setPreview] = useState(false)
  const label = (
    <div className="min-w-0">
      <label htmlFor={id} className="flex items-center gap-1.5 text-[13px] font-medium">
        {item.label || item.key}
        {dirty && <span className="h-1.5 w-1.5 rounded-full bg-accent" aria-label="已修改" />}
      </label>
      <p className="mt-0.5 font-mono text-[11px] text-muted-foreground">{item.key}</p>
      {item.description && <p className="mt-0.5 text-xs leading-relaxed text-muted-foreground">{item.description}</p>}
    </div>
  )

  if (item.type === "bool")
    return (
      <div className="flex items-center justify-between gap-4 px-4 py-3 sm:px-5">
        {label}
        <Switch id={id} checked={!!value} onCheckedChange={onChange} aria-label={item.label || item.key} />
      </div>
    )

  let control: React.ReactNode
  switch (item.type) {
    case "number":
      control = (
        <div className="flex items-center gap-2">
          <Input id={id} type="number" inputMode="decimal" value={value === null ? "" : String(value)} onChange={(e) => onChange(e.target.value === "" ? 0 : Number(e.target.value))} className="max-w-56 tabular-nums" />
          {/bytes/i.test(item.key) && typeof value === "number" && <span className="text-xs text-muted-foreground">≈ {formatBytes(value)}</span>}
        </div>
      )
      break
    case "string_list":
      control = (
        <Textarea
          id={id}
          rows={Math.min(8, Math.max(3, (value as string[] | null)?.length ?? 3))}
          className="font-mono text-xs"
          placeholder="每行一项"
          value={((value as string[] | null) ?? []).join("\n")}
          onChange={(e) => onChange(e.target.value.split("\n"))}
          onBlur={(e) => onChange(e.target.value.split("\n").map((s) => s.trim()).filter(Boolean))}
        />
      )
      break
    case "markdown":
      control = (
        <div>
          <div className="mb-1.5 flex gap-1">
            <Button variant={preview ? "ghost" : "secondary"} size="xs" onClick={() => setPreview(false)}>
              <PencilLine />
              编辑
            </Button>
            <Button variant={preview ? "secondary" : "ghost"} size="xs" onClick={() => setPreview(true)}>
              <Eye />
              预览
            </Button>
          </div>
          {preview ? (
            <div className="min-h-24 rounded-md border bg-muted/40 px-3 py-2">
              <Markdown>{String(value ?? "")}</Markdown>
            </div>
          ) : (
            <Textarea id={id} rows={6} className="font-mono text-xs" value={String(value ?? "")} onChange={(e) => onChange(e.target.value)} />
          )}
        </div>
      )
      break
    case "text":
      control = <Textarea id={id} rows={3} value={String(value ?? "")} onChange={(e) => onChange(e.target.value)} />
      break
    case "enum": {
      const opts = item.options ?? []
      control =
        opts.length <= 4 ? (
          <div role="radiogroup" aria-labelledby={id} className="inline-flex flex-wrap rounded-lg border bg-muted p-0.5">
            {opts.map((c) => (
              <button
                key={c}
                id={c === value ? id : undefined}
                type="button"
                role="radio"
                aria-checked={value === c}
                onClick={() => onChange(c)}
                title={c}
                className={cn(
                  "h-8 rounded-md px-3 text-xs font-medium",
                  !ENUM_LABEL[c] && "font-mono",
                  value === c ? "bg-card text-primary-text" : "text-muted-foreground hover:text-foreground",
                )}
              >
                {ENUM_LABEL[c] ?? c}
              </button>
            ))}
          </div>
        ) : (
          <Select id={id} value={String(value ?? "")} onChange={(e) => onChange(e.target.value)} className="max-w-xs">
            {opts.map((o) => (
              <option key={o} value={o}>
                {ENUM_LABEL[o] ? `${ENUM_LABEL[o]}（${o}）` : o}
              </option>
            ))}
          </Select>
        )
      break
    }
    case "secret":
      control = (
        <div className="flex items-center gap-2">
          <Input
            id={id}
            type="password"
            autoComplete="new-password"
            placeholder={item.is_set ? "已设置，留空则不修改" : "未设置"}
            value={String(value ?? "")}
            onChange={(e) => onChange(e.target.value)}
          />
          <Badge variant={item.is_set ? "soft" : "muted"} className="shrink-0">
            {item.is_set ? "已设置" : "未设置"}
          </Badge>
        </div>
      )
      break
    default:
      control = <Input id={id} type="text" autoComplete="off" value={String(value ?? "")} onChange={(e) => onChange(e.target.value)} />
  }
  return (
    <div className="grid gap-2 px-4 py-3 sm:px-5 md:grid-cols-[minmax(0,260px)_minmax(0,1fr)] md:gap-6">
      {label}
      <div className="min-w-0">{control}</div>
    </div>
  )
}
