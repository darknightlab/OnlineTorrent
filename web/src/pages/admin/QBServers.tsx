import { useState, type FormEvent } from "react"
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { KeyRound, Loader2, Pencil, Plus, Plug, RadioTower, Trash2 } from "lucide-react"
import { Button } from "@/components/ui/button"
import { Dialog, DialogContent, DialogFooter } from "@/components/ui/dialog"
import { Field, Input } from "@/components/ui/input"
import { Switch } from "@/components/ui/switch"
import { toast } from "@/components/ui/toast"
import { errorMessage } from "@/components/states"
import { toList } from "@/lib/api"
import { adminApi } from "./api"
import type { QBServer, QBServerInput } from "./types"
import { formatBytes, timeAgo } from "@/lib/format"
import { AdminHeader, DataView, Mono, StatusDot, type Column } from "./components"

const EMPTY: QBServerInput = { name: "", url: "", username: "", password: "", api_key: "", category: "projectk", enabled: true, seeding_time_limit_min: 1440, ratio_limit: -2, max_active: 0, add_wss_trackers: true, edge_url: "", edge_api: false }
const KEY = ["admin", "qb"]

export default function QBServers() {
  const qc = useQueryClient()
  const q = useQuery({ queryKey: KEY, queryFn: adminApi.qbServers })
  const [editing, setEditing] = useState<QBServer | "new" | null>(null)
  const [testing, setTesting] = useState<number | null>(null)
  const [edgeOf, setEdgeOf] = useState<QBServer | null>(null)
  const invalidate = () => qc.invalidateQueries({ queryKey: KEY })

  const toggle = useMutation({
    mutationFn: (s: QBServer) => adminApi.updateQB(s.id, { enabled: !s.enabled }),
    onSuccess: invalidate,
    onError: (e) => toast.error(errorMessage(e)),
  })
  const del = useMutation({
    mutationFn: (id: number) => adminApi.deleteQB(id),
    onSuccess: () => {
      toast.success("已删除")
      void invalidate()
    },
    onError: (e) => toast.error(errorMessage(e)),
  })

  async function test(s: QBServer) {
    setTesting(s.id)
    try {
      const r = await adminApi.testQB(s.id)
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
    {
      key: "edge",
      header: "边缘节点",
      cell: (s) => <EdgeCell s={s} />,
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
            <Button variant="ghost" size="icon-sm" aria-label={`${s.name} 的边缘节点`} title="边缘节点" onClick={() => setEdgeOf(s)}>
              <RadioTower />
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
      {edgeOf && <EdgeDialog server={edgeOf} onClose={() => setEdgeOf(null)} onChanged={invalidate} />}
    </div>
  )
}

function EdgeCell({ s }: { s: QBServer }) {
  if (!s.edge_bound) return <span className="text-xs text-muted-foreground">未绑定</span>
  const e = s.edge
  if (!e?.connected) return <span className="text-xs text-destructive-text">未连接{s.edge_api ? "（qB API 不可用）" : ""}</span>
  return (
    <div className="max-w-64 text-xs">
      <span className="text-primary-text">已连接</span>
      <span className="text-muted-foreground">
        {e.rtt_ms ? ` · ${Math.round(e.rtt_ms)} ms` : ""} · 已提供 {formatBytes(e.bytes_served)}
      </span>
      <p className="text-[11px] text-muted-foreground">
        {e.since && `连接于 ${timeAgo(e.since)}`}
        {e.version && ` · ${e.version}`}
        {e.host && ` · ${e.host}`}
      </p>
      {!s.edge_url && <p className="text-[11px] text-destructive-text">未设置公开地址，浏览器不会使用它</p>}
    </div>
  )
}

function EdgeDialog({ server, onClose, onChanged }: { server: QBServer; onClose: () => void; onChanged: () => void }) {
  const [issued, setIssued] = useState<{ token: string; connect_url: string } | null>(null)
  const gen = useMutation({
    mutationFn: () => adminApi.edgeToken(server.id),
    onSuccess: (r) => {
      setIssued(r)
      onChanged()
    },
    onError: (e) => toast.error(errorMessage(e)),
  })
  const unbind = useMutation({
    mutationFn: () => adminApi.edgeUnbind(server.id),
    onSuccess: () => {
      toast.success("已解除绑定，边缘节点已断开")
      onChanged()
      onClose()
    },
    onError: (e) => toast.error(errorMessage(e)),
  })
  const rotate = useMutation({
    mutationFn: () => adminApi.rotateEdgeKey(),
    onSuccess: () => toast.success("已更换签名密钥（旧密钥签发的访问令牌在过期前仍有效）"),
    onError: (e) => toast.error(errorMessage(e)),
  })
  const e = server.edge
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent
        title={`${server.name} · 边缘节点`}
        description="边缘节点（otd edge）运行在 qB 所在主机上，直接读取 qB 的下载目录，向浏览器提供 WebSeed（含未下完文件中已校验的分块）；它主动连接本站，不需要开放入站端口给本站。"
      >
        <div className="grid gap-3 text-sm">
          <div className="rounded-md border px-3 py-2.5">
            {!server.edge_bound ? (
              <span className="text-muted-foreground">尚未生成令牌</span>
            ) : e?.connected ? (
              <div className="grid gap-0.5">
                <span className="text-primary-text">已连接{e.since && `（${timeAgo(e.since)}）`}</span>
                <span className="text-xs text-muted-foreground">
                  版本 {e.version || "-"} · 主机 {e.host || "-"} · 来源 {e.remote || "-"} · 延迟 {e.rtt_ms ? `${Math.round(e.rtt_ms)} ms` : "-"}
                </span>
                <span className="text-xs text-muted-foreground">
                  已提供 {formatBytes(e.bytes_served)} · {e.requests} 次请求 · 监听 {e.listen || "-"}
                  {e.stats_error && ` · 统计不可用：${e.stats_error}`}
                </span>
              </div>
            ) : (
              <span className="text-destructive-text">已生成令牌，边缘节点未连接</span>
            )}
          </div>
          <p className="text-xs text-muted-foreground">
            公开地址：{server.edge_url ? <Mono>{server.edge_url}</Mono> : "未设置（在「编辑」中填写，否则浏览器不会使用该边缘节点）"}
            {server.edge_api && " · qB API 经边缘节点访问"}
          </p>
          {issued && (
            <div className="grid gap-2 rounded-md border border-primary/40 bg-primary/5 p-3">
              <p className="text-xs">令牌只显示这一次，请写入边缘节点的环境变量（旧令牌已失效）：</p>
              <Field label="OTD_EDGE_TOKEN" htmlFor="edge-tok">
                <Input id="edge-tok" readOnly value={issued.token} className="font-mono text-xs" onFocus={(ev) => ev.currentTarget.select()} />
              </Field>
              <Field label="OTD_MAIN" htmlFor="edge-main">
                <Input id="edge-main" readOnly value={issued.connect_url} className="font-mono text-xs" onFocus={(ev) => ev.currentTarget.select()} />
              </Field>
            </div>
          )}
          <DialogFooter className="mt-1 flex-wrap">
            <Button variant="ghost" onClick={() => confirm("更换所有边缘节点共用的访问令牌签名密钥？") && rotate.mutate()} disabled={rotate.isPending}>
              更换签名密钥
            </Button>
            {server.edge_bound && (
              <Button variant="outline" className="text-destructive-text" onClick={() => confirm("解除绑定？边缘节点将断开且无法重连。") && unbind.mutate()} disabled={unbind.isPending}>
                解除绑定
              </Button>
            )}
            <Button
              onClick={() => (!server.edge_bound || confirm("重新生成令牌？当前边缘节点会断开，需要换上新令牌。")) && gen.mutate()}
              disabled={gen.isPending}
            >
              {gen.isPending ? <Loader2 className="animate-spin" /> : <KeyRound />}
              {server.edge_bound ? "重新生成令牌" : "生成令牌"}
            </Button>
          </DialogFooter>
        </div>
      </DialogContent>
    </Dialog>
  )
}

function QBDialog({ server, onClose, onSaved }: { server: QBServer | null; onClose: () => void; onSaved: () => void }) {
  const [f, setF] = useState<QBServerInput>(() => (server ? { ...EMPTY, ...server, password: "", api_key: "" } : EMPTY))
  const m = useMutation({
    mutationFn: () => {
      const body: Partial<QBServerInput> = { ...f }
      if (!body.password) delete body.password
      if (!body.api_key) delete body.api_key
      return server ? adminApi.updateQB(server.id, body) : adminApi.createQB(body as QBServerInput)
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
          <Field label="边缘节点公开地址" htmlFor="qb-edge-url" hint="可选。该服务器的边缘节点经 Cloudflare 对外的地址，浏览器直接从这里获取 WebSeed">
            <Input id="qb-edge-url" type="url" value={f.edge_url} onChange={set("edge_url")} placeholder="https://edge-ams.projectk.org" />
          </Field>
          <label className="flex items-center justify-between gap-3 rounded-md border px-3 py-2.5 text-sm">
            <span>
              qB API 经边缘节点访问
              <span className="block text-xs text-muted-foreground">开启后不再直连上面的 WebUI 地址（qB 不必对外开放）；边缘节点断开时该服务器离线</span>
            </span>
            <Switch checked={f.edge_api} onCheckedChange={(v) => setF((s) => ({ ...s, edge_api: v }))} aria-label="qB API 经边缘节点访问" />
          </label>
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
