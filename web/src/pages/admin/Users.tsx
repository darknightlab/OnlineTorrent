import { useState } from "react"
import { keepPreviousData, useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { Loader2, Pencil, Search } from "lucide-react"
import { Button } from "@/components/ui/button"
import { Dialog, DialogContent, DialogFooter } from "@/components/ui/dialog"
import { Field, Input, Select } from "@/components/ui/input"
import { Switch } from "@/components/ui/switch"
import { Badge } from "@/components/ui/badge"
import { toast } from "@/components/ui/toast"
import { RoleBadge } from "@/components/badges"
import { Pager, errorMessage } from "@/components/states"
import { api, toList } from "@/lib/api"
import type { AdminUser, User } from "@/lib/types"
import { formatDate, timeAgo } from "@/lib/format"
import { AdminHeader, DataView, type Column } from "./components"

export default function UsersPage() {
  const qc = useQueryClient()
  const [q, setQ] = useState("")
  const [kw, setKw] = useState("")
  const [page, setPage] = useState(1)
  const [editing, setEditing] = useState<AdminUser | null>(null)
  const query = useQuery({ queryKey: ["admin", "users", kw, page], queryFn: () => api.admin.users(kw, page), placeholderData: keepPreviousData })
  const list = toList(query.data)

  const columns: Column<AdminUser>[] = [
    {
      key: "user",
      header: "用户",
      mobile: "title",
      cell: (u) => (
        <div className="flex items-center gap-2.5">
          <span className="inline-flex h-8 w-8 shrink-0 items-center justify-center rounded-full bg-primary-soft text-xs font-semibold uppercase text-primary-text">{u.username.slice(0, 1)}</span>
          <div className="min-w-0">
            <p className="flex items-center gap-1.5 font-medium">
              {u.username}
              {u.disabled && <Badge variant="destructive">已禁用</Badge>}
            </p>
            <p className="truncate text-xs text-muted-foreground">{u.email}</p>
          </div>
        </div>
      ),
    },
    { key: "role", header: "角色", cell: (u) => <RoleBadge role={u.role} /> },
    { key: "vip", header: "大会员到期", cell: (u) => <span className="tabular-nums">{u.vip_until ? formatDate(u.vip_until) : "-"}</span> },
    { key: "created", header: "注册", cell: (u) => <span className="tabular-nums text-muted-foreground">{formatDate(u.created_at)}</span> },
    { key: "login", header: "最近登录", cell: (u) => <span className="text-muted-foreground">{u.last_login_at ? timeAgo(u.last_login_at) : "-"}</span> },
  ]

  return (
    <div>
      <AdminHeader title="用户" description={query.data ? `第 ${list.page} / ${list.page_count} 页` : undefined} />
      <form
        className="mb-4 flex max-w-md gap-2"
        onSubmit={(e) => {
          e.preventDefault()
          setKw(q.trim())
          setPage(1)
        }}
      >
        <div className="relative flex-1">
          <Search className="pointer-events-none absolute left-3 top-1/2 h-4 w-4 -translate-y-1/2 text-muted-foreground" />
          <Input aria-label="搜索用户" placeholder="用户名或邮箱" value={q} onChange={(e) => setQ(e.target.value)} className="pl-9" />
        </div>
        <Button type="submit" variant="secondary">
          搜索
        </Button>
      </form>
      <DataView
        columns={columns}
        rows={query.data ? list.items : undefined}
        loading={query.isPending}
        error={query.error}
        onRetry={() => query.refetch()}
        rowKey={(u) => u.id}
        empty="没有匹配的用户"
        actions={(u) => (
          <Button variant="outline" size="sm" onClick={() => setEditing(u)}>
            <Pencil />
            编辑
          </Button>
        )}
      />
      <Pager page={list.page} pageCount={list.page_count} onChange={setPage} />
      {editing && <UserDialog user={editing} onClose={() => setEditing(null)} onSaved={() => qc.invalidateQueries({ queryKey: ["admin", "users"] })} />}
    </div>
  )
}

function UserDialog({ user, onClose, onSaved }: { user: AdminUser; onClose: () => void; onSaved: () => void }) {
  const [role, setRole] = useState<User["role"]>(user.role)
  const [vip, setVip] = useState(user.vip_until ? user.vip_until.slice(0, 10) : "")
  const [disabled, setDisabled] = useState(user.disabled)
  const m = useMutation({
    mutationFn: () => api.admin.updateUser(user.id, { role, vip_until: vip ? new Date(`${vip}T23:59:59+08:00`).toISOString() : null, disabled }),
    onSuccess: () => {
      toast.success("已保存")
      onSaved()
      onClose()
    },
    onError: (e) => toast.error(errorMessage(e)),
  })
  const addDays = (d: number) => {
    const base = vip && new Date(vip).getTime() > Date.now() ? new Date(vip) : new Date()
    base.setDate(base.getDate() + d)
    setVip(base.toISOString().slice(0, 10))
  }
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent title={`编辑用户 ${user.username}`} description={user.email}>
        <div className="grid gap-4">
          <Field label="角色" htmlFor="u-role">
            <Select id="u-role" value={role} onChange={(e) => setRole(e.target.value as User["role"])}>
              <option value="member">会员 member</option>
              <option value="vip">大会员 vip</option>
              <option value="admin">管理员 admin</option>
            </Select>
          </Field>
          <Field label="大会员到期日" htmlFor="u-vip" hint="留空表示无大会员">
            <div className="flex flex-wrap gap-2">
              <Input id="u-vip" type="date" value={vip} onChange={(e) => setVip(e.target.value)} className="w-44" />
              <Button variant="outline" size="sm" className="h-10" onClick={() => addDays(30)}>
                +30 天
              </Button>
              <Button variant="outline" size="sm" className="h-10" onClick={() => addDays(365)}>
                +1 年
              </Button>
              {vip && (
                <Button variant="ghost" size="sm" className="h-10" onClick={() => setVip("")}>
                  清除
                </Button>
              )}
            </div>
          </Field>
          <label className="flex items-center justify-between rounded-md border px-3 py-2.5 text-sm">
            <span>
              禁用账号
              <span className="block text-xs text-muted-foreground">禁用后无法登录</span>
            </span>
            <Switch checked={disabled} onCheckedChange={setDisabled} aria-label="禁用账号" />
          </label>
        </div>
        <DialogFooter>
          <Button variant="outline" onClick={onClose}>
            取消
          </Button>
          <Button onClick={() => m.mutate()} disabled={m.isPending}>
            {m.isPending && <Loader2 className="animate-spin" />}
            保存
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
