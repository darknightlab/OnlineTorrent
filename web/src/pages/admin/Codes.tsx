import { useState } from "react"
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { Copy, Link2, Loader2, Pencil, Plus } from "lucide-react"
import { Button } from "@/components/ui/button"
import { Dialog, DialogContent, DialogFooter } from "@/components/ui/dialog"
import { Card } from "@/components/ui/card"
import { Field, Input, Select } from "@/components/ui/input"
import { Badge } from "@/components/ui/badge"
import { Switch } from "@/components/ui/switch"
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs"
import { toast } from "@/components/ui/toast"
import { errorMessage } from "@/components/states"
import { toList } from "@/lib/api"
import { adminApi } from "./api"
import type { InviteCode, InviteCreate, InvitePatch, RedeemCode } from "./types"
import { formatDate } from "@/lib/format"
import { copyText } from "@/lib/utils"
import { AdminHeader, DataView, Mono, type Column } from "./components"

async function copy(text: string, what: string) {
  if (await copyText(text)) toast.success(`${what}已复制`)
  else toast.error("复制失败")
}

function CodeCell({ code }: { code: string }) {
  return (
    <span className="inline-flex items-center gap-1">
      <Mono className="text-[13px] font-medium text-foreground">{code}</Mono>
      <button
        type="button"
        aria-label={`复制 ${code}`}
        onClick={() => copy(code, "代码")}
        className="inline-flex h-8 w-8 items-center justify-center rounded text-muted-foreground hover:bg-muted hover:text-foreground"
      >
        <Copy className="h-3.5 w-3.5" />
      </button>
    </span>
  )
}

/** 后端未返回 url 时按当前站点拼接 */
const inviteURL = (c: InviteCode) => c.url || `${location.origin}/register/invite/${encodeURIComponent(c.code)}`

function inviteStatus(c: InviteCode) {
  if (c.disabled) return <Badge variant="muted">已停用</Badge>
  if (c.expires_at && new Date(c.expires_at).getTime() < Date.now()) return <Badge variant="destructive">已过期</Badge>
  if (c.max_uses > 0 && c.uses >= c.max_uses) return <Badge variant="muted">已用完</Badge>
  return <Badge variant="soft">可用</Badge>
}

export default function Codes() {
  return (
    <div>
      <AdminHeader title="邀请码 / 兑换码" description="邀请码用于邀请注册（可附带大会员）；兑换码用于开通大会员" />
      <Tabs defaultValue="invites">
        <TabsList className="mb-4">
          <TabsTrigger value="invites">邀请码</TabsTrigger>
          <TabsTrigger value="redeem">兑换码</TabsTrigger>
        </TabsList>
        <TabsContent value="invites">
          <Invites />
        </TabsContent>
        <TabsContent value="redeem">
          <Redeem />
        </TabsContent>
      </Tabs>
    </div>
  )
}

const INVITE_DEFAULT: InviteCreate = { max_uses: 1, role: "member", vip_days: 0, expires_in_days: 30, note: "" }

function Invites() {
  const qc = useQueryClient()
  const KEY = ["admin", "invites"]
  const q = useQuery({ queryKey: KEY, queryFn: adminApi.invites })
  const [f, setF] = useState<InviteCreate>(INVITE_DEFAULT)
  const [editing, setEditing] = useState<InviteCode | null>(null)
  const create = useMutation({
    mutationFn: () => adminApi.createInvite(f),
    onSuccess: async (inv) => {
      void qc.invalidateQueries({ queryKey: KEY })
      if (inv?.code && (await copyText(inviteURL(inv)))) toast.success("邀请码已生成，链接已复制")
      else toast.success("邀请码已生成")
    },
    onError: (e) => toast.error(errorMessage(e)),
  })
  const toggle = useMutation({
    mutationFn: (c: InviteCode) => adminApi.updateInvite(c.id, { disabled: !c.disabled }),
    onSuccess: () => qc.invalidateQueries({ queryKey: KEY }),
    onError: (e) => toast.error(errorMessage(e)),
  })
  const num = (k: keyof InviteCreate) => (e: React.ChangeEvent<HTMLInputElement>) => setF((s) => ({ ...s, [k]: Math.max(0, Number(e.target.value) || 0) }))

  const columns: Column<InviteCode>[] = [
    { key: "code", header: "邀请码", mobile: "title", cell: (c) => <CodeCell code={c.code} /> },
    {
      key: "grant",
      header: "注册为",
      cell: (c) => (
        <span className="inline-flex items-center gap-1">
          {c.role === "vip" ? <Badge variant="warning">大会员</Badge> : <Badge variant="soft">会员</Badge>}
          {c.vip_days > 0 && <span className="text-xs text-muted-foreground">+{c.vip_days} 天</span>}
        </span>
      ),
    },
    { key: "uses", header: "使用", cell: (c) => <span className="tabular-nums">{c.uses} / {c.max_uses > 0 ? c.max_uses : "∞"}</span> },
    { key: "expires", header: "过期", cell: (c) => <span className="tabular-nums text-muted-foreground">{c.expires_at ? formatDate(c.expires_at) : "永不"}</span> },
    { key: "status", header: "状态", cell: inviteStatus },
    { key: "note", header: "备注", cell: (c) => c.note || <span className="text-muted-foreground">-</span> },
  ]
  return (
    <>
      <Card className="mb-4 p-3.5 sm:p-4">
        <form
          className="grid grid-cols-2 items-end gap-3 sm:grid-cols-3 xl:flex xl:flex-wrap"
          onSubmit={(e) => {
            e.preventDefault()
            create.mutate()
          }}
        >
          <Field label="可用次数（0=不限）" htmlFor="inv-max" className="xl:w-36">
            <Input id="inv-max" type="number" min={0} value={f.max_uses} onChange={num("max_uses")} />
          </Field>
          <Field label="注册角色" htmlFor="inv-role" className="xl:w-32">
            <Select id="inv-role" value={f.role} onChange={(e) => setF((s) => ({ ...s, role: e.target.value as InviteCreate["role"] }))}>
              <option value="member">会员</option>
              <option value="vip">大会员</option>
            </Select>
          </Field>
          <Field label="赠送大会员天数" htmlFor="inv-vip" className="xl:w-32">
            <Input id="inv-vip" type="number" min={0} value={f.vip_days} onChange={num("vip_days")} />
          </Field>
          <Field label="有效天数（0=永久）" htmlFor="inv-exp" className="xl:w-36">
            <Input id="inv-exp" type="number" min={0} value={f.expires_in_days} onChange={num("expires_in_days")} />
          </Field>
          <Field label="备注" htmlFor="inv-note" className="col-span-2 min-w-40 sm:col-span-1 xl:flex-1">
            <Input id="inv-note" value={f.note} onChange={(e) => setF((s) => ({ ...s, note: e.target.value }))} placeholder="可选" />
          </Field>
          <Button type="submit" disabled={create.isPending} className="col-span-2 sm:col-span-1">
            {create.isPending ? <Loader2 className="animate-spin" /> : <Plus />}
            生成邀请码
          </Button>
        </form>
      </Card>
      <DataView
        columns={columns}
        rows={q.data ? toList(q.data).items : undefined}
        loading={q.isPending}
        error={q.error}
        onRetry={() => q.refetch()}
        rowKey={(c) => c.id}
        empty="还没有邀请码"
        actions={(c) => (
          <>
            <Button variant="outline" size="sm" onClick={() => setEditing(c)}>
              <Pencil />
              编辑
            </Button>
            <Button variant="outline" size="sm" onClick={() => copy(inviteURL(c), "邀请链接")}>
              <Link2 />
              复制链接
            </Button>
            <span className="inline-flex h-9 items-center gap-2 pl-1 text-xs text-muted-foreground">
              <Switch checked={!c.disabled} onCheckedChange={() => toggle.mutate(c)} aria-label={`${c.disabled ? "启用" : "停用"}邀请码 ${c.code}`} />
            </span>
          </>
        )}
      />
      {editing && <InviteEditDialog invite={editing} onClose={() => setEditing(null)} onSaved={() => qc.invalidateQueries({ queryKey: KEY })} />}
    </>
  )
}

/** UTC 时间 → 本地日期 YYYY-MM-DD（date 输入框用） */
function toDateInput(iso: string | null) {
  if (!iso) return ""
  const d = new Date(iso)
  const p = (n: number) => String(n).padStart(2, "0")
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())}`
}

/** 从「永不过期」切回有期限时的默认日期：30 天后 */
function defaultExpiry() {
  return toDateInput(new Date(Date.now() + 30 * 86400_000).toISOString())
}

/** 编辑邀请码：次数、注册角色、赠送天数、过期日期、备注、启用（代码本身与已用次数不可改） */
function InviteEditDialog({ invite, onClose, onSaved }: { invite: InviteCode; onClose: () => void; onSaved: () => void }) {
  const [f, setF] = useState({
    max_uses: invite.max_uses,
    role: invite.role,
    vip_days: invite.vip_days,
    expires: toDateInput(invite.expires_at),
    note: invite.note,
    enabled: !invite.disabled,
  })
  const m = useMutation({
    mutationFn: () => {
      const b: InvitePatch = { max_uses: f.max_uses, role: f.role, vip_days: f.vip_days, note: f.note, disabled: !f.enabled }
      // 日期未改动时不提交，避免把精确到秒的过期时间改成当天结束
      if (f.expires !== toDateInput(invite.expires_at)) b.expires_at = f.expires
      return adminApi.updateInvite(invite.id, b)
    },
    onSuccess: () => {
      toast.success("已保存")
      onSaved()
      onClose()
    },
    onError: (e) => toast.error(errorMessage(e)),
  })
  const num = (k: "max_uses" | "vip_days") => (e: React.ChangeEvent<HTMLInputElement>) => setF((s) => ({ ...s, [k]: Math.max(0, Number(e.target.value) || 0) }))
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent title={`编辑邀请码 ${invite.code}`} description={`已使用 ${invite.uses} 次`}>
        <form
          className="grid gap-3"
          onSubmit={(e) => {
            e.preventDefault()
            m.mutate()
          }}
        >
          <div className="grid gap-3 sm:grid-cols-2">
            <Field label="可用次数（0=不限）" htmlFor="ie-max" hint={f.max_uses > 0 && f.max_uses <= invite.uses ? "不大于已用次数，保存后即用完" : undefined}>
              <Input id="ie-max" type="number" min={0} value={f.max_uses} onChange={num("max_uses")} />
            </Field>
            <Field label="注册角色" htmlFor="ie-role">
              <Select id="ie-role" value={f.role} onChange={(e) => setF((s) => ({ ...s, role: e.target.value as InviteCode["role"] }))}>
                <option value="member">会员</option>
                <option value="vip">大会员</option>
              </Select>
            </Field>
            <Field label="赠送大会员天数" htmlFor="ie-vip">
              <Input id="ie-vip" type="number" min={0} value={f.vip_days} onChange={num("vip_days")} />
            </Field>
            <Field label="过期日期" htmlFor="ie-exp">
              {/* 日期输入框在 Chrome 等浏览器里没有清空按钮，用开关切换「永不过期」 */}
              <div className="flex items-center gap-2">
                <Input
                  id="ie-exp"
                  type="date"
                  disabled={f.expires === ""}
                  value={f.expires}
                  onChange={(e) => setF((s) => ({ ...s, expires: e.target.value }))}
                  className="min-w-0 flex-1"
                />
                <label className="inline-flex shrink-0 items-center gap-1.5 text-[13px] text-muted-foreground">
                  <Switch
                    checked={f.expires === ""}
                    onCheckedChange={(never) => setF((s) => ({ ...s, expires: never ? "" : toDateInput(invite.expires_at) || defaultExpiry() }))}
                    aria-label="永不过期"
                  />
                  永不
                </label>
              </div>
            </Field>
          </div>
          <Field label="备注" htmlFor="ie-note">
            <Input id="ie-note" value={f.note} onChange={(e) => setF((s) => ({ ...s, note: e.target.value }))} placeholder="可选" />
          </Field>
          <label className="flex items-center justify-between rounded-md border px-3 py-2.5 text-sm">
            启用该邀请码
            <Switch checked={f.enabled} onCheckedChange={(v) => setF((s) => ({ ...s, enabled: v }))} aria-label="启用该邀请码" />
          </label>
          <DialogFooter className="mt-1">
            <Button type="button" variant="outline" onClick={onClose}>
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

function Redeem() {
  const qc = useQueryClient()
  const KEY = ["admin", "redeem"]
  const q = useQuery({ queryKey: KEY, queryFn: adminApi.redeemCodes })
  const [count, setCount] = useState(5)
  const [days, setDays] = useState(30)
  const [note, setNote] = useState("")
  const [created, setCreated] = useState<RedeemCode[]>([])
  const m = useMutation({
    mutationFn: () => adminApi.createRedeemCodes({ count, vip_days: days, note }),
    onSuccess: (r) => {
      const items = toList(r).items
      setCreated(items)
      toast.success(`已生成 ${items.length} 个兑换码`)
      void qc.invalidateQueries({ queryKey: KEY })
    },
    onError: (e) => toast.error(errorMessage(e)),
  })
  const columns: Column<RedeemCode>[] = [
    { key: "code", header: "兑换码", mobile: "title", cell: (c) => <CodeCell code={c.code} /> },
    { key: "days", header: "大会员", cell: (c) => `${c.vip_days} 天` },
    {
      key: "status",
      header: "状态",
      cell: (c) =>
        c.used_by_name ? (
          <span className="text-[13px]">
            <Badge variant="muted">已使用</Badge> <span className="text-muted-foreground">{c.used_by_name} · {formatDate(c.used_at)}</span>
          </span>
        ) : (
          <Badge variant="soft">未使用</Badge>
        ),
    },
    { key: "note", header: "备注", cell: (c) => c.note || <span className="text-muted-foreground">-</span> },
    { key: "created", header: "创建", cell: (c) => <span className="tabular-nums text-muted-foreground">{formatDate(c.created_at)}</span> },
  ]
  return (
    <>
      <Card className="mb-4 p-3.5 sm:p-4">
        <form
          className="grid grid-cols-2 items-end gap-3 sm:flex sm:flex-wrap"
          onSubmit={(e) => {
            e.preventDefault()
            m.mutate()
          }}
        >
          <Field label="数量" htmlFor="rd-count" className="sm:w-24">
            <Input id="rd-count" type="number" min={1} max={100} value={count} onChange={(e) => setCount(Number(e.target.value))} />
          </Field>
          <Field label="大会员天数" htmlFor="rd-days" className="sm:w-28">
            <Input id="rd-days" type="number" min={1} value={days} onChange={(e) => setDays(Number(e.target.value))} />
          </Field>
          <Field label="备注" htmlFor="rd-note" className="col-span-2 min-w-40 sm:flex-1">
            <Input id="rd-note" value={note} onChange={(e) => setNote(e.target.value)} placeholder="可选" />
          </Field>
          <Button type="submit" disabled={m.isPending} className="col-span-2 sm:w-auto">
            {m.isPending ? <Loader2 className="animate-spin" /> : <Plus />}
            批量生成
          </Button>
        </form>
        {created.length > 0 && (
          <div className="mt-3 rounded-md border bg-muted/50 p-3">
            <div className="mb-2 flex items-center justify-between gap-2 text-[13px]">
              <span>本次生成 {created.length} 个</span>
              <Button variant="outline" size="xs" onClick={() => copy(created.map((c) => c.code).join("\n"), "全部兑换码")}>
                <Copy />
                复制全部
              </Button>
            </div>
            <pre className="max-h-32 overflow-auto font-mono text-xs leading-relaxed">{created.map((c) => c.code).join("\n")}</pre>
          </div>
        )}
      </Card>
      <DataView columns={columns} rows={q.data ? toList(q.data).items : undefined} loading={q.isPending} error={q.error} onRetry={() => q.refetch()} rowKey={(c) => c.id} empty="还没有兑换码" />
    </>
  )
}
