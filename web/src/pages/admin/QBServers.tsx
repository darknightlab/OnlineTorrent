import { useState, type FormEvent } from "react"
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { Loader2, Pencil, Plus, Plug, Trash2 } from "lucide-react"
import { Button } from "@/components/ui/button"
import { Dialog, DialogContent, DialogFooter } from "@/components/ui/dialog"
import { Field, Input } from "@/components/ui/input"
import { Switch } from "@/components/ui/switch"
import { toast } from "@/components/ui/toast"
import { errorMessage } from "@/components/states"
import { api, toList } from "@/lib/api"
import type { QBServer, QBServerInput } from "@/lib/types"
import { timeAgo } from "@/lib/format"
import { AdminHeader, DataView, Mono, StatusDot, type Column } from "./components"

const EMPTY: QBServerInput = { name: "", url: "", username: "", password: "", api_key: "", category: "projectk", enabled: true, seeding_time_limit_min: 1440, ratio_limit: -2, max_active: 0, add_wss_trackers: true }
const KEY = ["admin", "qb"]

export default function QBServers() {
  const qc = useQueryClient()
  const q = useQuery({ queryKey: KEY, queryFn: api.admin.qbServers })
  const [editing, setEditing] = useState<QBServer | "new" | null>(null)
  const [testing, setTesting] = useState<number | null>(null)
  const invalidate = () => qc.invalidateQueries({ queryKey: KEY })

  const toggle = useMutation({
    mutationFn: (s: QBServer) => api.admin.updateQB(s.id, { enabled: !s.enabled }),
    onSuccess: invalidate,
    onError: (e) => toast.error(errorMessage(e)),
  })
  const del = useMutation({
    mutationFn: (id: number) => api.admin.deleteQB(id),
    onSuccess: () => {
      toast.success("已删除")
      void invalidate()
    },
    onError: (e) => toast.error(errorMessage(e)),
  })

  async function test(s: QBServer) {
    setTesting(s.id)
    try {
      const r = await api.admin.testQB(s.id)
      toast.success(`${s.name} 连接正常${r.version ? `（${r.version}）` : ""}`)
    } catch (e) {
      toast.error(`${s.name}：${errorMessage(e)}`)
    } finally {
      setTesting(null)
      void invalidate()
    }
  }

  const columns: Column<QBServer>[] = [
    {
      key: "name",
      header: "名称",
      mobile: "title",
      cell: (s) => (
        <div className="flex items-center gap-2">
          <StatusDot ok={s.enabled ? s.online : null} label="" />
          <span className="font-medium">{s.name}</span>
        </div>
      ),
    },
    { key: "url", header: "地址", cell: (s) => <Mono>{s.url}</Mono>, className: "max-w-64" },
    { key: "category", header: "分类", cell: (s) => s.category || "-" },
    {
      key: "status",
      header: "状态",
      cell: (s) => (
        <div className="max-w-64">
          <span className={s.enabled ? (s.online ? "text-primary-text" : "text-destructive-text") : "text-muted-foreground"}>
            {s.enabled ? (s.online ? `在线 ${s.version}` : "离线") : "已停用"}
          </span>
          {s.last_error && <p className="line-clamp-2 text-xs text-muted-foreground">{s.last_error}</p>}
          {s.last_check_at && <p className="text-[11px] text-muted-foreground">检查于 {timeAgo(s.last_check_at)}</p>}
        </div>
      ),
    },
    { key: "limits", header: "做种限制", cell: (s) => <span className="text-xs tabular-nums text-muted-foreground">{s.seeding_time_limit_min} 分 · 比率 {s.ratio_limit} · 并发 {s.max_active}{s.add_wss_trackers ? " · 补 wss" : ""}</span> },
    { key: "enabled", header: "启用", cell: (s) => <Switch checked={s.enabled} onCheckedChange={() => toggle.mutate(s)} aria-label={`启用 ${s.name}`} /> },
  ]

  return (
    <div>
      <AdminHeader
        title="qB 服务器"
        description="WebRTC 做种的 qBittorrent 池（需 5.3+ / libtorrent 2.1 且开启 WebTorrent）"
        actions={
          <Button onClick={() => setEditing("new")}>
            <Plus />
            添加服务器
          </Button>
        }
      />
      <DataView
        columns={columns}
        rows={q.data ? toList(q.data).items : undefined}
        loading={q.isPending}
        error={q.error}
        onRetry={() => q.refetch()}
        rowKey={(s) => s.id}
        empty="还没有添加 qB 服务器"
        actions={(s) => (
          <>
            <Button variant="outline" size="sm" onClick={() => test(s)} disabled={testing === s.id}>
              {testing === s.id ? <Loader2 className="animate-spin" /> : <Plug />}
              测试
            </Button>
            <Button variant="ghost" size="icon-sm" aria-label={`编辑 ${s.name}`} onClick={() => setEditing(s)}>
              <Pencil />
            </Button>
            <Button
              variant="ghost"
              size="icon-sm"
              aria-label={`删除 ${s.name}`}
              className="text-destructive-text hover:bg-destructive/10"
              onClick={() => confirm(`确定删除服务器「${s.name}」？`) && del.mutate(s.id)}
            >
              <Trash2 />
            </Button>
          </>
        )}
      />
      {editing && <QBDialog server={editing === "new" ? null : editing} onClose={() => setEditing(null)} onSaved={invalidate} />}
    </div>
  )
}

function QBDialog({ server, onClose, onSaved }: { server: QBServer | null; onClose: () => void; onSaved: () => void }) {
  const [f, setF] = useState<QBServerInput>(() => (server ? { ...EMPTY, ...server, password: "", api_key: "" } : EMPTY))
  const m = useMutation({
    mutationFn: () => {
      const body: Partial<QBServerInput> = { ...f }
      if (!body.password) delete body.password
      if (!body.api_key) delete body.api_key
      return server ? api.admin.updateQB(server.id, body) : api.admin.createQB(body as QBServerInput)
    },
    onSuccess: () => {
      toast.success(server ? "已保存" : "已添加")
      onSaved()
      onClose()
    },
    onError: (e) => toast.error(errorMessage(e)),
  })
  const set = <K extends keyof QBServerInput>(k: K) => (e: React.ChangeEvent<HTMLInputElement>) =>
    setF((s) => ({ ...s, [k]: typeof EMPTY[k] === "number" ? Number(e.target.value) : e.target.value }))
  function submit(e: FormEvent) {
    e.preventDefault()
    m.mutate()
  }
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent title={server ? `编辑 ${server.name}` : "添加 qB 服务器"} description="推荐使用 API Key（qB 5.2+），密码与 Key 仅写入，不会回显">
        <form onSubmit={submit} className="grid gap-3">
          <div className="grid gap-3 sm:grid-cols-2">
            <Field label="名称" htmlFor="qb-name">
              <Input id="qb-name" required value={f.name} onChange={set("name")} placeholder="ams" />
            </Field>
            <Field label="分类" htmlFor="qb-cat">
              <Input id="qb-cat" value={f.category} onChange={set("category")} />
            </Field>
          </div>
          <Field label="WebUI 地址" htmlFor="qb-url">
            <Input id="qb-url" required type="url" value={f.url} onChange={set("url")} placeholder="https://qbittorrent.example.com" />
          </Field>
          <Field label="API Key" htmlFor="qb-key" hint={server ? "留空则不修改" : "填写后可不填用户名密码"}>
            <Input id="qb-key" autoComplete="off" value={f.api_key} onChange={set("api_key")} placeholder="qbt_..." className="font-mono" />
          </Field>
          <div className="grid gap-3 sm:grid-cols-2">
            <Field label="用户名" htmlFor="qb-user">
              <Input id="qb-user" autoComplete="off" value={f.username} onChange={set("username")} />
            </Field>
            <Field label="密码" htmlFor="qb-pass" hint={server ? "留空则不修改" : undefined}>
              <Input id="qb-pass" type="password" autoComplete="new-password" value={f.password} onChange={set("password")} />
            </Field>
          </div>
          <div className="grid grid-cols-3 gap-3">
            <Field label="做种时长(分)" htmlFor="qb-time">
              <Input id="qb-time" type="number" min={-2} value={f.seeding_time_limit_min} onChange={set("seeding_time_limit_min")} />
            </Field>
            <Field label="分享率" htmlFor="qb-ratio">
              <Input id="qb-ratio" type="number" step="0.1" min={-2} value={f.ratio_limit} onChange={set("ratio_limit")} />
            </Field>
            <Field label="最大活动" htmlFor="qb-max">
              <Input id="qb-max" type="number" min={0} value={f.max_active} onChange={set("max_active")} />
            </Field>
          </div>
          <label className="flex items-center justify-between gap-3 rounded-md border px-3 py-2.5 text-sm">
            <span>
              添加后补 wss tracker
              <span className="block text-xs text-muted-foreground">qB 5.3 会过滤磁力中的 wss tracker，开启后通过 addTrackers 补上</span>
            </span>
            <Switch checked={f.add_wss_trackers} onCheckedChange={(v) => setF((s) => ({ ...s, add_wss_trackers: v }))} aria-label="添加后补 wss tracker" />
          </label>
          <label className="flex items-center justify-between rounded-md border px-3 py-2.5 text-sm">
            启用该服务器
            <Switch checked={f.enabled} onCheckedChange={(v) => setF((s) => ({ ...s, enabled: v }))} aria-label="启用该服务器" />
          </label>
          <DialogFooter className="mt-1">
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
