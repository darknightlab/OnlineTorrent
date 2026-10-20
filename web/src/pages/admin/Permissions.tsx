import { useEffect, useMemo, useState } from "react"
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { ChevronRight, Info, Link2, Loader2, Lock, RotateCcw, Save, Undo2 } from "lucide-react"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Card } from "@/components/ui/card"
import { toast } from "@/components/ui/toast"
import { ErrorState, Spinner, errorMessage } from "@/components/states"
import { api } from "@/lib/api"
import { qk } from "@/lib/queries"
import type { GrantRole, PermissionItem, PermissionsResponse } from "@/lib/types"
import { cn } from "@/lib/utils"
import { AdminHeader } from "./components"

const KEY = ["admin", "permissions"]
const ROLES: GrantRole[] = ["guest", "member", "vip"]
const ROLE_LABEL: Record<GrantRole | "admin", string> = { guest: "游客", member: "会员", vip: "大会员", admin: "管理员" }

/**
 * 等级有序（guest < member < vip < admin），授权给低等级即包含所有更高等级，
 * 因此每个接口的有效授权可归一为「最低授权等级」：null 表示仅管理员。
 */
type Min = GrantRole | null

const rank = (r: GrantRole) => ROLES.indexOf(r)
const editable = (i: PermissionItem) => i.access === "configurable" || i.access === "user"

function minOf(i: PermissionItem, roles: Iterable<string>): Min {
  for (const r of ROLES) {
    if (i.access === "user" && r === "guest") continue
    if ([...roles].includes(r)) return r
  }
  return null
}
const baseMin = (i: PermissionItem) => minOf(i, ROLES.filter((r) => i.granted?.[r]))
const defaultMin = (i: PermissionItem) => minOf(i, i.default_roles ?? [])

type CellState =
  | { kind: "on" } // 显式授权（可取消）
  | { kind: "off" } // 未授权（可勾选）
  | { kind: "inherited"; from: GrantRole } // 由更低等级继承，禁用
  | { kind: "fixed-on"; note?: string } // 固定允许
  | { kind: "fixed-off"; note: string } // 固定不允许

function cellOf(i: PermissionItem, min: Min, r: GrantRole | "admin"): CellState {
  if (r === "admin") return { kind: "fixed-on" }
  if (i.access === "public") return { kind: "fixed-on" }
  if (i.access === "admin") return { kind: "fixed-off", note: "—" }
  if (i.access === "user" && r === "guest") return { kind: "fixed-off", note: "需登录" }
  if (min === null || rank(min) > rank(r)) return { kind: "off" }
  if (min === r) return { kind: "on" }
  return { kind: "inherited", from: min }
}

/** 点击某格后的新最低等级：勾选 → 从该等级起；取消（该格为最低等级）→ 仅从更高一级起 */
function toggled(i: PermissionItem, min: Min, r: GrantRole): Min {
  const c = cellOf(i, min, r)
  if (c.kind === "off") return r
  if (c.kind === "on") return ROLES[rank(r) + 1] ?? null
  return min
}

function summary(i: PermissionItem, min: Min) {
  if (i.access === "public") return "所有人"
  if (i.access === "admin" || min === null) return "仅管理员"
  return `${ROLE_LABEL[min]}及以上`
}

export default function PermissionsPage() {
  const qc = useQueryClient()
  const q = useQuery({ queryKey: KEY, queryFn: api.admin.permissions })
  const items = useMemo(() => q.data?.items ?? [], [q.data])
  const base = useMemo(() => Object.fromEntries(items.filter(editable).map((i) => [i.key, baseMin(i)])) as Record<string, Min>, [items])
  const [draft, setDraft] = useState<Record<string, Min>>({})
  useEffect(() => setDraft(base), [base])

  // 联动组：切换其中一个接口，同组的其余接口一起切换
  const bundles = useMemo(() => {
    const m = new Map<string, string[]>()
    for (const i of items) if (i.bundle) m.set(i.bundle, [...(m.get(i.bundle) ?? []), i.key])
    return m
  }, [items])
  const set = (key: string, min: Min) =>
    setDraft((d) => {
      const item = items.find((i) => i.key === key)
      const keys = item?.bundle ? (bundles.get(item.bundle) ?? [key]) : [key]
      return { ...d, ...Object.fromEntries(keys.map((k) => [k, min])) }
    })

  const dirty = useMemo(() => Object.keys(base).filter((k) => draft[k] !== undefined && draft[k] !== base[k]), [base, draft])
  const groups = useMemo(() => {
    const m = new Map<string, PermissionItem[]>()
    for (const i of items) m.set(i.group || "其他", [...(m.get(i.group || "其他") ?? []), i])
    // 组内可配置接口排在前（稳定排序，保持后端注册顺序）
    return [...m.entries()].map(([g, list]) => [g, [...list].sort((a, b) => Number(editable(b)) - Number(editable(a)))] as const)
  }, [items])
  const configurable = items.filter(editable).length

  const save = useMutation({
    mutationFn: () => api.admin.savePermissions({ grants: Object.fromEntries(dirty.map((k) => [k, draft[k] ? [draft[k]!] : []])) }),
    onSuccess: (data: PermissionsResponse) => {
      qc.setQueryData(KEY, data)
      void qc.invalidateQueries({ queryKey: qk.me })
      toast.success(`已保存 ${dirty.length} 项接口权限`)
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
      <AdminHeader title="接口权限" description={`按等级控制每个接口的访问权限，共 ${items.length} 个接口，其中 ${configurable} 个可配置`} />

      <div role="note" className="mb-4 flex gap-2.5 rounded-lg border border-accent/25 bg-accent-soft px-3 py-2.5 text-[13px] leading-relaxed sm:mb-5">
        <Info className="mt-0.5 h-4 w-4 shrink-0 text-accent-text" />
        <div>
          <p>
            <strong className="font-semibold">勾选某一等级即自动包含更高等级</strong>
            <span className="text-muted-foreground">（游客 &lt; 会员 &lt; 大会员 &lt; 管理员）</span>，被包含的等级显示为「继承」。
          </p>
          <p className="mt-0.5 text-muted-foreground">管理员始终拥有全部权限；「公开」接口对所有人开放，「需登录」接口不能授予游客。保存后立即生效。</p>
        </div>
      </div>

      {groups.length > 1 && (
        <nav aria-label="接口分组" className="-mx-3 mb-4 flex gap-2 overflow-x-auto px-3 scrollbar-none sm:mx-0 sm:flex-wrap sm:px-0">
          {groups.map(([g, list]) => (
            <a key={g} href={`#perm-${g}`} className="inline-flex h-9 shrink-0 items-center gap-1.5 rounded-full border bg-card px-3.5 text-[13px] hover:border-primary hover:text-primary-text">
              {g}
              <span className="text-[11px] tabular-nums text-muted-foreground">{list.length}</span>
            </a>
          ))}
        </nav>
      )}

      <div className="grid gap-4">
        {groups.map(([g, list]) => (
          <Group key={g} name={g} items={list} draft={draft} base={base} onSet={set} />
        ))}
      </div>

      <div
        className={cn(
          "fixed inset-x-0 bottom-0 z-30 border-t bg-header pb-safe backdrop-blur-md transition-transform lg:left-[max(14rem,calc((100vw-1440px)/2+14rem))]",
          dirty.length ? "translate-y-0" : "translate-y-full",
        )}
        aria-hidden={!dirty.length}
      >
        <div className="flex h-14 items-center justify-between gap-3 px-3 sm:px-5 lg:px-8">
          <span className="text-[13px] text-muted-foreground">
            已修改 <span className="font-semibold tabular-nums text-foreground">{dirty.length}</span> 个接口
          </span>
          <div className="flex gap-2">
            <Button variant="outline" size="sm" onClick={() => setDraft(base)} tabIndex={dirty.length ? 0 : -1}>
              <RotateCcw />
              撤销
            </Button>
            <Button size="sm" onClick={() => save.mutate()} disabled={save.isPending} tabIndex={dirty.length ? 0 : -1}>
              {save.isPending ? <Loader2 className="animate-spin" /> : <Save />}
              保存
            </Button>
          </div>
        </div>
      </div>
    </div>
  )
}

function Group({
  name,
  items,
  draft,
  base,
  onSet,
}: {
  name: string
  items: PermissionItem[]
  draft: Record<string, Min>
  base: Record<string, Min>
  onSet: (key: string, min: Min) => void
}) {
  // 全部为固定权限的分组（如「后台」）默认折叠，减少干扰
  const fixedOnly = !items.some(editable)
  const [open, setOpen] = useState(!fixedOnly)
  const changed = items.filter((i) => editable(i) && draft[i.key] !== base[i.key]).length

  return (
    <Card id={`perm-${name}`} className="scroll-mt-32 overflow-hidden">
      <button
        type="button"
        onClick={() => setOpen((v) => !v)}
        aria-expanded={open}
        className="flex w-full items-center gap-2 px-4 py-3 text-left hover:bg-muted/40 sm:px-5"
      >
        <ChevronRight className={cn("h-4 w-4 shrink-0 text-muted-foreground transition-transform", open && "rotate-90")} />
        <h2 className="text-sm font-semibold">{name}</h2>
        <span className="text-xs tabular-nums text-muted-foreground">{items.length} 个接口</span>
        {fixedOnly && (
          <Badge variant="muted" className="ml-1">
            <Lock />
            {items.every((i) => i.access === "admin") ? "仅管理员 · 不可配置" : "不可配置"}
          </Badge>
        )}
        {changed > 0 && (
          <Badge variant="accent-soft" className="ml-auto">
            已修改 {changed}
          </Badge>
        )}
      </button>

      {open && (
        <>
          {/* 桌面：矩阵 */}
          <div role="table" aria-label={`${name}接口权限`} className="hidden border-t sm:block">
            <div role="row" className="grid grid-cols-[minmax(0,1fr)_repeat(4,5.5rem)] items-center border-b bg-muted/40 px-5 text-xs font-medium text-muted-foreground lg:grid-cols-[minmax(0,1fr)_repeat(4,6.5rem)]">
              <span role="columnheader" className="py-2">
                接口
              </span>
              {(["guest", "member", "vip", "admin"] as const).map((r) => (
                <span key={r} role="columnheader" className="py-2 text-center">
                  {ROLE_LABEL[r]}
                </span>
              ))}
            </div>
            <div role="rowgroup" className="divide-y">
              {items.map((i) => (
                <DesktopRow key={i.key} item={i} min={draft[i.key] ?? null} baseMin={base[i.key] ?? null} onSet={(m) => onSet(i.key, m)} />
              ))}
            </div>
          </div>

          {/* 移动端：卡片 */}
          <ul className="divide-y border-t sm:hidden">
            {items.map((i) => (
              <MobileRow key={i.key} item={i} min={draft[i.key] ?? null} baseMin={base[i.key] ?? null} onSet={(m) => onSet(i.key, m)} />
            ))}
          </ul>
        </>
      )}
    </Card>
  )
}

const methodCls: Record<string, string> = {
  GET: "bg-accent-soft text-accent-text",
  POST: "bg-primary-soft text-primary-text",
  PUT: "bg-warning/15 text-warning-text",
  DELETE: "bg-destructive/12 text-destructive-text",
}

function RouteLabel({ item, dirty, onReset }: { item: PermissionItem; dirty: boolean; onReset?: () => void }) {
  return (
    <div className="min-w-0">
      <div className="flex flex-wrap items-center gap-1.5">
        <span className="text-[13px] font-medium leading-snug">{item.label || item.key}</span>
        {item.access === "public" && <Badge variant="soft">公开</Badge>}
        {item.access === "user" && <Badge variant="outline">需登录</Badge>}
        {item.bundle && (
          <span
            className="inline-flex items-center gap-0.5 rounded bg-accent-soft px-1 text-[11px] text-accent-text"
            title={`与「${item.bundle_label ?? item.bundle}」的其他接口联动：必须一起开关`}
          >
            <Link2 className="h-3 w-3" />
            联动 · {item.bundle_label ?? item.bundle}
          </span>
        )}
        {dirty && <span className="h-1.5 w-1.5 rounded-full bg-accent" aria-label="已修改" title="已修改" />}
        {onReset && (
          <button type="button" onClick={onReset} className="inline-flex items-center gap-0.5 rounded px-1 text-[11px] text-muted-foreground hover:bg-muted hover:text-foreground" title="恢复为默认权限">
            <Undo2 className="h-3 w-3" />
            默认
          </button>
        )}
      </div>
      <div className="mt-0.5 flex min-w-0 items-center gap-1.5">
        <span className={cn("shrink-0 rounded px-1 font-mono text-[10px] font-semibold leading-4", methodCls[item.method] ?? "bg-muted text-muted-foreground")}>{item.method}</span>
        <span className="truncate font-mono text-[11px] text-muted-foreground" title={item.path}>
          {item.path}
        </span>
      </div>
    </div>
  )
}

interface RowProps {
  item: PermissionItem
  min: Min
  baseMin: Min
  onSet: (m: Min) => void
}

function useRow({ item, min, baseMin, onSet }: RowProps) {
  const isEditable = editable(item)
  const dirty = isEditable && min !== baseMin
  const def = isEditable ? defaultMin(item) : null
  const onReset = isEditable && item.default_roles && min !== def ? () => onSet(def) : undefined
  return { dirty, onReset }
}

function DesktopRow(props: RowProps) {
  const { item, min, onSet } = props
  const { dirty, onReset } = useRow(props)
  return (
    <div role="row" className={cn("grid grid-cols-[minmax(0,1fr)_repeat(4,5.5rem)] items-center px-5 py-2.5 lg:grid-cols-[minmax(0,1fr)_repeat(4,6.5rem)]", dirty && "bg-accent-soft/50")}>
      <div role="cell" className="min-w-0 pr-3">
        <RouteLabel item={item} dirty={dirty} onReset={onReset} />
      </div>
      {(["guest", "member", "vip", "admin"] as const).map((r) => (
        <div role="cell" key={r} className="flex justify-center">
          <Cell state={cellOf(item, min, r)} label={`${item.label} · ${ROLE_LABEL[r]}`} onToggle={r === "admin" ? undefined : () => onSet(toggled(item, min, r))} />
        </div>
      ))}
    </div>
  )
}

function MobileRow(props: RowProps) {
  const { item, min, onSet } = props
  const { dirty, onReset } = useRow(props)
  return (
    <li className={cn("px-4 py-3", dirty && "bg-accent-soft/50")}>
      <RouteLabel item={item} dirty={dirty} onReset={onReset} />
      {editable(item) ? (
        <div className="mt-2.5 grid grid-cols-3 gap-2">
          {ROLES.map((r) => (
            <div key={r} className="flex flex-col items-center gap-1 rounded-md border bg-background/50 py-2">
              <span className="text-[11px] text-muted-foreground">{ROLE_LABEL[r]}</span>
              <Cell state={cellOf(item, min, r)} label={`${item.label} · ${ROLE_LABEL[r]}`} onToggle={() => onSet(toggled(item, min, r))} />
            </div>
          ))}
        </div>
      ) : (
        <p className="mt-1.5 flex items-center gap-1 text-[11px] text-muted-foreground">
          <Lock className="h-3 w-3" />
          {summary(item, min)}（不可配置）
        </p>
      )}
    </li>
  )
}

/** 小号开关：可编辑 / 继承（已勾选禁用）/ 固定 */
function Cell({ state, label, onToggle }: { state: CellState; label: string; onToggle?: () => void }) {
  if (state.kind === "fixed-off")
    return (
      <span className={cn("inline-flex h-[1.875rem] items-start pt-0.5 text-[11px]", state.note === "—" ? "text-muted-foreground/50" : "text-muted-foreground")}>{state.note}</span>
    )
  const checked = state.kind !== "off"
  const interactive = (state.kind === "on" || state.kind === "off") && !!onToggle
  const caption = state.kind === "inherited" ? "继承" : state.kind === "fixed-on" ? state.note : undefined
  return (
    <span className="inline-flex flex-col items-center gap-0.5">
      <button
        type="button"
        role="switch"
        aria-checked={checked}
        aria-label={label}
        aria-disabled={!interactive}
        disabled={!interactive}
        title={state.kind === "inherited" ? `由「${ROLE_LABEL[state.from]}」授权继承` : state.kind === "fixed-on" ? "始终允许" : undefined}
        onClick={interactive ? onToggle : undefined}
        className={cn(
          "relative inline-flex h-5 w-9 shrink-0 items-center rounded-full transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2 focus-visible:ring-offset-background",
          state.kind === "on" && "bg-primary",
          state.kind === "off" && "bg-input hover:bg-muted-foreground/40",
          state.kind === "inherited" && "cursor-not-allowed bg-primary/35",
          state.kind === "fixed-on" && "cursor-not-allowed bg-muted-foreground/30",
        )}
      >
        <span
          className={cn(
            "inline-block h-4 w-4 rounded-full bg-white transition-transform",
            checked ? "translate-x-[18px]" : "translate-x-0.5",
          )}
        />
      </button>
      {/* 始终占位，保证同一行开关垂直对齐 */}
      <span className="h-3 text-[10px] leading-3 text-muted-foreground">{caption}</span>
    </span>
  )
}
