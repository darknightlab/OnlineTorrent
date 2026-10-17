import { useState } from "react"
import { keepPreviousData, useQuery } from "@tanstack/react-query"
import { ActionBadge } from "@/components/badges"
import { Badge } from "@/components/ui/badge"
import { Pager } from "@/components/states"
import { api, toList } from "@/lib/api"
import type { AuditEntry, PlayRecord } from "@/lib/types"
import { formatDateTime } from "@/lib/format"
import { AdminHeader, DataView, Mono, type Column } from "./components"

const AUDIT_LABEL: Record<string, string> = {
  "settings.update": "修改设置",
  "qb.save": "保存 qB 服务器",
  "qb.delete": "删除 qB 服务器",
  "qb.test": "测试 qB 服务器",
  "user.update": "修改用户",
  "user.redeem": "兑换大会员",
  "invite.create": "生成邀请码",
  "invite.update": "修改邀请码",
  "redeem.create": "生成兑换码",
  "rule.save": "保存规则",
  "rule.create": "新建规则",
  "rule.delete": "删除规则",
  "review.resolve": "处理审核",
  "review.decide": "处理审核",
  "auth.login": "登录",
  "play.block": "拦截播放",
}

export function Plays() {
  const [page, setPage] = useState(1)
  const q = useQuery({ queryKey: ["admin", "plays", page], queryFn: () => api.admin.plays(page), placeholderData: keepPreviousData })
  const list = toList(q.data)
  const columns: Column<PlayRecord>[] = [
    {
      key: "name",
      header: "资源",
      mobile: "title",
      cell: (p) => (
        <div className="min-w-0 max-w-xl">
          <p className="line-clamp-2 wrap-anywhere text-[13px]">{p.name || <span className="text-muted-foreground">（未知名称）</span>}</p>
          <Mono className="text-[11px] text-muted-foreground">{p.infohash}</Mono>
        </div>
      ),
    },
    { key: "user", header: "用户", cell: (p) => p.user ?? <span className="text-muted-foreground">游客</span> },
    { key: "ip", header: "IP", cell: (p) => <Mono>{p.ip ?? "-"}</Mono>, mobile: "hidden" },
    { key: "action", header: "结果", cell: (p) => <ActionBadge action={p.action} /> },
    { key: "seed", header: "做种", cell: (p) => (p.seeded_by.length ? p.seeded_by.map((s) => <Badge key={s} variant="soft" className="mr-1">{s}</Badge>) : "-") },
    { key: "time", header: "时间", cell: (p) => <span className="whitespace-nowrap tabular-nums text-muted-foreground">{formatDateTime(p.created_at)}</span> },
  ]
  return (
    <div>
      <AdminHeader title="播放记录" description="每次 /play 请求与推送结果" />
      <DataView columns={columns} rows={q.data ? list.items : undefined} loading={q.isPending} error={q.error} onRetry={() => q.refetch()} rowKey={(p) => p.id} />
      <Pager page={list.page} pageCount={list.page_count} onChange={setPage} />
    </div>
  )
}

export function Audit() {
  const [page, setPage] = useState(1)
  const q = useQuery({ queryKey: ["admin", "audit", page], queryFn: () => api.admin.audit(page), placeholderData: keepPreviousData })
  const list = toList(q.data)
  const columns: Column<AuditEntry>[] = [
    { key: "action", header: "操作", mobile: "title", cell: (a) =>
        AUDIT_LABEL[a.action] ? (
          <span className="inline-flex flex-wrap items-baseline gap-x-1.5">
            <span className="text-[13px] font-medium">{AUDIT_LABEL[a.action]}</span>
            <Mono className="text-[11px] text-muted-foreground">{a.action}</Mono>
          </span>
        ) : (
          <Mono className="text-[13px] font-medium text-foreground">{a.action}</Mono>
        ),
    },
    { key: "actor", header: "操作者", cell: (a) => a.actor ?? <span className="text-muted-foreground">系统</span> },
    { key: "target", header: "对象", cell: (a) => <Mono>{a.target}</Mono> },
    { key: "detail", header: "详情", cell: (a) => <span className="text-[13px]">{a.detail}</span> },
    { key: "time", header: "时间", cell: (a) => <span className="whitespace-nowrap tabular-nums text-muted-foreground">{formatDateTime(a.created_at)}</span> },
  ]
  return (
    <div>
      <AdminHeader title="审计日志" description="管理操作与策略拦截记录" />
      <DataView columns={columns} rows={q.data ? list.items : undefined} loading={q.isPending} error={q.error} onRetry={() => q.refetch()} rowKey={(a) => a.id} />
      <Pager page={list.page} pageCount={list.page_count} onChange={setPage} />
    </div>
  )
}
