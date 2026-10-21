import { useEffect, useId, useMemo, useRef, useState } from "react"
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { Ban, Info, Loader2, Plus, RotateCcw, Save, ShieldCheck, Tags, X } from "lucide-react"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Card } from "@/components/ui/card"
import { toast } from "@/components/ui/toast"
import { ErrorState, Spinner, errorMessage } from "@/components/states"
import { adminApi } from "./api"
import type { ContentScope, ContentScopeInput, ContentScopesResponse, GrantRole, TagMode, TagOperator } from "./types"
import { MAX_SCOPE_TAGS, MAX_SCOPE_TAG_CHARS, expressionString, validateScope } from "./scope"
import { cn } from "@/lib/utils"
import { AdminHeader } from "./components"

const KEY = ["admin", "content-scopes"]
const ROLE_LABEL: Record<GrantRole, string> = { guest: "游客", member: "会员", vip: "大会员" }
const ROLE_HINT: Record<GrantRole, string> = { guest: "未登录访客", member: "已登录用户", vip: "大会员（含永久）" }

const toInput = (s: ContentScope): ContentScopeInput => ({ tag_mode: s.tag_mode, tags: s.tags, tag_operators: s.tag_operators, exclude_tags: s.exclude_tags })
const same = (a: ContentScopeInput, b: ContentScopeInput) => JSON.stringify(a) === JSON.stringify(b)
const restricted = (s: ContentScopeInput) => s.tag_mode === "selected" || s.exclude_tags.length > 0

export default function ContentScopePage() {
  const q = useQuery({ queryKey: KEY, queryFn: adminApi.contentScopes })

  if (q.error) return <ErrorState error={q.error} onRetry={() => q.refetch()} />
  if (!q.data)
    return (
      <div className="flex h-40 items-center justify-center">
        <Spinner />
      </div>
    )

  return (
    <div>
      <AdminHeader title="内容范围" description="按等级限制可见的番剧条目（时间表、搜索、条目页、播放页的条目信息），保存后立即生效" />

      <div role="note" className="mb-4 flex gap-2.5 rounded-lg border border-accent/25 bg-accent-soft px-3 py-2.5 text-[13px] leading-relaxed sm:mb-5">
        <Info className="mt-0.5 h-4 w-4 shrink-0 text-accent-text" />
        <div className="grid gap-0.5">
          <p>
            条目的标签 = bangumi 用户标签 ∪ 公共标签。<strong className="font-semibold">AND 优先于 OR</strong>：
            <code className="mx-1 rounded bg-background/60 px-1 font-mono text-[12px]">原创 AND 校园 OR 战斗</code>表示
            <code className="mx-1 rounded bg-background/60 px-1 font-mono text-[12px]">(原创 AND 校园) OR 战斗</code>。
          </p>
          <p className="text-muted-foreground">
            排除标签对两种模式都生效：带有任意一个排除标签的条目一律隐藏（例如排除 里番 / R18 / 18禁）。「仅所选标签」模式下没有标签的条目（如仅来自 bangumi-data 的条目）也会被隐藏。
            被隐藏的条目对该等级返回「不存在」，包含其文件的种子不出现在搜索结果中。
          </p>
        </div>
      </div>

      <div className="grid gap-4 xl:grid-cols-2">
        {q.data.items.map((s) => (
          <ScopeCard key={s.role} scope={s} />
        ))}
        <Card className="flex items-start gap-3 self-start p-4 sm:p-5">
          <span className="flex h-9 w-9 shrink-0 items-center justify-center rounded-full bg-primary-soft text-primary-text">
            <ShieldCheck className="h-4.5 w-4.5" />
          </span>
          <div>
            <div className="flex items-center gap-2">
              <h2 className="text-sm font-semibold">管理员</h2>
              <Badge variant="soft">不受限制</Badge>
            </div>
            <p className="mt-1 text-[13px] text-muted-foreground">管理员始终可以看到全部条目，不可配置。</p>
          </div>
        </Card>
      </div>
    </div>
  )
}

function ScopeCard({ scope }: { scope: ContentScope }) {
  const qc = useQueryClient()
  const base = useMemo(() => toInput(scope), [scope])
  const [draft, setDraft] = useState<ContentScopeInput>(base)
  useEffect(() => setDraft(base), [base])
  const dirty = !same(draft, base)
  const invalid = validateScope(draft)
  const role = scope.role
  const preview = expressionString(draft.tags, draft.tag_operators)

  const save = useMutation({
    mutationFn: () => adminApi.saveContentScope(role, draft),
    onSuccess: (data) => {
      qc.setQueryData<ContentScopesResponse>(KEY, (old) => (old ? { ...old, items: old.items.map((i) => (i.role === role ? data : i)) } : old))
      toast.success(`已保存「${ROLE_LABEL[role]}」的内容范围`)
    },
    onError: (e) => toast.error(errorMessage(e)),
  })

  const setMode = (m: TagMode) => setDraft((d) => ({ ...d, tag_mode: m }))
  const addTag = (t: string) =>
    setDraft((d) => ({ ...d, tags: [...d.tags, t], tag_operators: d.tags.length ? [...d.tag_operators, "or" as TagOperator] : d.tag_operators }))
  const removeTag = (i: number) =>
    setDraft((d) => {
      const tags = d.tags.filter((_, j) => j !== i)
      // 删除第 i 个标签时去掉它与前一个之间的运算符（第一个则去掉它后面的）
      const ops = d.tag_operators.filter((_, j) => j !== (i === 0 ? 0 : i - 1))
      return { ...d, tags, tag_operators: ops }
    })
  const flipOp = (i: number) => setDraft((d) => ({ ...d, tag_operators: d.tag_operators.map((o, j) => (j === i ? (o === "and" ? "or" : "and") : o)) }))
  const addExclude = (t: string) => setDraft((d) => (d.exclude_tags.includes(t) ? d : { ...d, exclude_tags: [...d.exclude_tags, t] }))
  const removeExclude = (t: string) => setDraft((d) => ({ ...d, exclude_tags: d.exclude_tags.filter((x) => x !== t) }))

  return (
    <Card className={cn("flex flex-col", dirty && "ring-1 ring-accent/40")}>
      <div className="flex flex-wrap items-center gap-2 border-b px-4 py-3 sm:px-5">
        <h2 className="text-sm font-semibold">{ROLE_LABEL[role]}</h2>
        <span className="text-xs text-muted-foreground">{ROLE_HINT[role]}</span>
        {restricted(base) ? <Badge variant="warning">有限制</Badge> : <Badge variant="soft">全部可见</Badge>}
        {dirty && (
          <Badge variant="accent-soft" className="ml-auto">
            未保存
          </Badge>
        )}
      </div>

      <div className="grid gap-5 px-4 py-4 sm:px-5">
        <section className="grid gap-2">
          <h3 className="text-[13px] font-medium">标签模式</h3>
          <div role="radiogroup" aria-label={`${ROLE_LABEL[role]}标签模式`} className="inline-flex w-full rounded-md border bg-muted/40 p-0.5 sm:w-auto">
            {(
              [
                ["all", "全部"],
                ["selected", "仅所选标签"],
              ] as const
            ).map(([m, label]) => (
              <button
                key={m}
                type="button"
                role="radio"
                aria-checked={draft.tag_mode === m}
                onClick={() => setMode(m)}
                className={cn(
                  "h-8 flex-1 rounded px-4 text-[13px] transition-colors sm:flex-none",
                  draft.tag_mode === m ? "bg-card font-medium text-foreground" : "text-muted-foreground hover:text-foreground",
                )}
              >
                {label}
              </button>
            ))}
          </div>
        </section>

        {draft.tag_mode === "selected" && (
          <section className="grid gap-2">
            <div className="flex items-baseline justify-between gap-2">
              <h3 className="flex items-center gap-1.5 text-[13px] font-medium">
                <Tags className="h-3.5 w-3.5 text-muted-foreground" />
                标签表达式
              </h3>
              <span className="text-[11px] tabular-nums text-muted-foreground">
                {draft.tags.length}/{MAX_SCOPE_TAGS}
              </span>
            </div>
            <p className="text-xs text-muted-foreground">点击两个标签之间的 AND / OR 切换。新加入的标签默认以 OR 连接。</p>
            <div className="flex min-h-11 flex-wrap items-center gap-1.5 rounded-md border bg-background/50 p-2">
              {draft.tags.length === 0 && <span className="px-1 text-[13px] text-muted-foreground">尚未添加标签（该模式下至少需要一个）</span>}
              {draft.tags.map((t, i) => (
                <span key={`${i}-${t}`} className="inline-flex items-center gap-1.5">
                  {i > 0 && (
                    <button
                      type="button"
                      onClick={() => flipOp(i - 1)}
                      title="切换 AND / OR"
                      aria-label={`第 ${i} 与第 ${i + 1} 个标签之间的运算符：${draft.tag_operators[i - 1] === "and" ? "AND" : "OR"}，点击切换`}
                      className={cn(
                        "h-6 rounded px-1.5 font-mono text-[11px] font-semibold transition-colors",
                        draft.tag_operators[i - 1] === "and" ? "bg-primary-soft text-primary-text hover:bg-primary/20" : "bg-accent-soft text-accent-text hover:bg-accent/20",
                      )}
                    >
                      {draft.tag_operators[i - 1] === "and" ? "AND" : "OR"}
                    </button>
                  )}
                  <Chip label={t} onRemove={() => removeTag(i)} />
                </span>
              ))}
            </div>
            <TagInput placeholder="搜索并添加标签…" onAdd={addTag} disabled={draft.tags.length >= MAX_SCOPE_TAGS} />
            <div className="rounded-md bg-muted/50 px-3 py-2 text-[13px]">
              <span className="text-muted-foreground">预览：</span>
              <span className="wrap-anywhere font-mono">{preview || "—"}</span>
            </div>
          </section>
        )}

        <section className="grid gap-2">
          <div className="flex items-baseline justify-between gap-2">
            <h3 className="flex items-center gap-1.5 text-[13px] font-medium">
              <Ban className="h-3.5 w-3.5 text-muted-foreground" />
              排除标签
            </h3>
            <span className="text-[11px] tabular-nums text-muted-foreground">
              {draft.exclude_tags.length}/{MAX_SCOPE_TAGS}
            </span>
          </div>
          <p className="text-xs text-muted-foreground">带有其中任意一个标签的条目将被隐藏。</p>
          {draft.exclude_tags.length > 0 && (
            <div className="flex flex-wrap gap-1.5">
              {draft.exclude_tags.map((t) => (
                <Chip key={t} label={t} tone="destructive" onRemove={() => removeExclude(t)} />
              ))}
            </div>
          )}
          <TagInput placeholder="搜索并添加排除标签…" onAdd={addExclude} disabled={draft.exclude_tags.length >= MAX_SCOPE_TAGS} />
        </section>
      </div>

      <div className="mt-auto flex flex-wrap items-center justify-end gap-2 rounded-b-[inherit] border-t bg-muted/20 px-4 py-3 sm:px-5">
        {dirty && invalid && <span className="mr-auto text-xs text-destructive-text">{invalid}</span>}
        <Button variant="outline" size="sm" onClick={() => setDraft(base)} disabled={!dirty || save.isPending}>
          <RotateCcw />
          撤销
        </Button>
        <Button size="sm" onClick={() => save.mutate()} disabled={!dirty || !!invalid || save.isPending}>
          {save.isPending ? <Loader2 className="animate-spin" /> : <Save />}
          保存
        </Button>
      </div>
    </Card>
  )
}

function Chip({ label, onRemove, tone = "default" }: { label: string; onRemove: () => void; tone?: "default" | "destructive" }) {
  return (
    <span
      className={cn(
        "inline-flex h-7 max-w-full items-center gap-1 rounded-full border pl-2.5 pr-1 text-[13px]",
        tone === "destructive" ? "border-destructive/30 bg-destructive/8 text-destructive-text" : "bg-card",
      )}
    >
      <span className="truncate">{label}</span>
      <button type="button" onClick={onRemove} aria-label={`移除 ${label}`} className="inline-flex h-5 w-5 shrink-0 items-center justify-center rounded-full hover:bg-muted">
        <X className="h-3 w-3" />
      </button>
    </span>
  )
}

function useDebounced<T>(value: T, ms: number) {
  const [v, setV] = useState(value)
  useEffect(() => {
    const t = setTimeout(() => setV(value), ms)
    return () => clearTimeout(t)
  }, [value, ms])
  return v
}

/** 标签输入 + 自动补全（/admin/subject-tags，带条目数）；回车添加高亮项或输入的文本 */
function TagInput({ placeholder, onAdd, disabled }: { placeholder: string; onAdd: (t: string) => void; disabled?: boolean }) {
  const [text, setText] = useState("")
  const [open, setOpen] = useState(false)
  const [active, setActive] = useState(-1)
  const listId = useId()
  const inputRef = useRef<HTMLInputElement>(null)
  const term = useDebounced(text.trim(), 200)
  const q = useQuery({ queryKey: ["admin", "subject-tags", term], queryFn: () => adminApi.subjectTags(term), enabled: open, staleTime: 60_000 })
  const items = q.data?.items ?? []
  const tooLong = [...text.trim()].length > MAX_SCOPE_TAG_CHARS

  const commit = (t: string) => {
    const v = t.trim()
    if (!v || [...v].length > MAX_SCOPE_TAG_CHARS) return
    onAdd(v)
    setText("")
    setActive(-1)
    inputRef.current?.focus()
  }

  return (
    <div className="relative">
      <div className="flex gap-2">
        <input
          ref={inputRef}
          type="text"
          role="combobox"
          aria-expanded={open && items.length > 0}
          aria-controls={listId}
          aria-autocomplete="list"
          aria-activedescendant={active >= 0 ? `${listId}-${active}` : undefined}
          aria-invalid={tooLong || undefined}
          value={text}
          disabled={disabled}
          placeholder={disabled ? "已达上限" : placeholder}
          onChange={(e) => {
            setText(e.target.value)
            setOpen(true)
            setActive(-1)
          }}
          onFocus={() => setOpen(true)}
          onBlur={() => setTimeout(() => setOpen(false), 120)}
          onKeyDown={(e) => {
            if (e.key === "ArrowDown") {
              e.preventDefault()
              setOpen(true)
              setActive((a) => Math.min(items.length - 1, a + 1))
            } else if (e.key === "ArrowUp") {
              e.preventDefault()
              setActive((a) => Math.max(-1, a - 1))
            } else if (e.key === "Enter") {
              e.preventDefault()
              commit(active >= 0 && items[active] ? items[active].name : text)
            } else if (e.key === "Escape") {
              setOpen(false)
            }
          }}
          className="flex h-9 w-full min-w-0 rounded-md border border-input bg-card px-3 text-sm transition-colors placeholder:text-muted-foreground/80 focus-visible:border-primary focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary/25 disabled:cursor-not-allowed disabled:opacity-50 aria-[invalid=true]:border-destructive"
        />
        <Button variant="outline" size="sm" onClick={() => commit(text)} disabled={disabled || !text.trim() || tooLong} aria-label="添加标签">
          <Plus />
          <span className="hidden sm:inline">添加</span>
        </Button>
      </div>
      {open && !disabled && (items.length > 0 || q.isFetching) && (
        <ul
          id={listId}
          role="listbox"
          className="absolute inset-x-0 top-full z-20 mt-1 max-h-64 overflow-y-auto rounded-md border bg-popover p-1 text-popover-foreground"
        >
          {items.length === 0 && (
            <li className="flex items-center gap-2 px-2.5 py-2 text-[13px] text-muted-foreground">
              <Loader2 className="h-3.5 w-3.5 animate-spin" />
              搜索中…
            </li>
          )}
          {items.map((t, i) => (
            <li
              key={t.name}
              id={`${listId}-${i}`}
              role="option"
              aria-selected={i === active}
              onMouseDown={(e) => {
                e.preventDefault()
                commit(t.name)
              }}
              onMouseEnter={() => setActive(i)}
              className={cn("flex cursor-pointer items-center justify-between gap-3 rounded px-2.5 py-1.5 text-[13px]", i === active ? "bg-muted" : "hover:bg-muted/60")}
            >
              <span className="truncate">{t.name}</span>
              <span className="shrink-0 text-[11px] tabular-nums text-muted-foreground">{t.count.toLocaleString()} 部</span>
            </li>
          ))}
        </ul>
      )}
      {tooLong && <p className="mt-1 text-xs text-destructive-text">标签最多 {MAX_SCOPE_TAG_CHARS} 字</p>}
    </div>
  )
}
