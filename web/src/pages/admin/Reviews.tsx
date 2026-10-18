import { useState } from "react"
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { Bot, Check, Flag, Loader2 } from "lucide-react"
import { Button } from "@/components/ui/button"
import { Card } from "@/components/ui/card"
import { Select, Input } from "@/components/ui/input"
import { Badge } from "@/components/ui/badge"
import { Tabs, TabsList, TabsTrigger } from "@/components/ui/tabs"
import { toast } from "@/components/ui/toast"
import { ActionBadge } from "@/components/badges"
import { EmptyState, ErrorState, Spinner, errorMessage } from "@/components/states"
import { api, toList } from "@/lib/api"
import type { Review, ReviewAction, RuleLabel } from "@/lib/types"
import { timeAgo } from "@/lib/format"
import { watchHref } from "@/lib/utils"
import { AdminHeader, Mono } from "./components"

export default function Reviews() {
  const [status, setStatus] = useState("pending")
  const q = useQuery({ queryKey: ["admin", "reviews", status], queryFn: () => api.admin.reviews(status) })
  const items = toList(q.data).items
  return (
    <div>
      <AdminHeader title="审核队列" description="用户举报与自动标记的可疑资源；裁决会写入 infohash 级规则" />
      <Tabs value={status} onValueChange={setStatus}>
        <TabsList className="mb-4">
          <TabsTrigger value="pending">待处理</TabsTrigger>
          <TabsTrigger value="done">已处理</TabsTrigger>
        </TabsList>
      </Tabs>
      {q.error ? (
        <Card>
          <ErrorState error={q.error} onRetry={() => q.refetch()} />
        </Card>
      ) : q.isPending ? (
        <div className="flex h-40 items-center justify-center">
          <Spinner />
        </div>
      ) : items.length === 0 ? (
        <Card>
          <EmptyState icon={Check} title={status === "pending" ? "审核队列已清空" : "暂无记录"} />
        </Card>
      ) : (
        <ul className="grid gap-3">
          {items.map((r) => (
            <ReviewCard key={r.id} r={r} />
          ))}
        </ul>
      )}
    </div>
  )
}

function ReviewCard({ r }: { r: Review }) {
  const qc = useQueryClient()
  const [action, setAction] = useState<ReviewAction>(r.action ?? "block")
  const [label, setLabel] = useState<RuleLabel>(r.label ?? "")
  const [note, setNote] = useState(r.note ?? "")
  const m = useMutation({
    mutationFn: () => api.admin.decideReview(r.id, { action, label, note }),
    onSuccess: () => {
      toast.success("已裁决")
      void qc.invalidateQueries({ queryKey: ["admin", "reviews"] })
      void qc.invalidateQueries({ queryKey: ["admin", "stats"] })
    },
    onError: (e) => toast.error(errorMessage(e)),
  })
  return (
    <li>
      <Card className="p-4">
        <div className="flex flex-wrap items-start justify-between gap-2">
          <div className="min-w-0 flex-1">
            <a href={watchHref(r.infohash)} target="_blank" rel="noreferrer" className="wrap-anywhere text-sm font-medium hover:text-primary-text hover:underline">
              {r.title}
            </a>
            <p className="mt-0.5">
              <Mono className="text-muted-foreground">{r.infohash}</Mono>
            </p>
          </div>
          <span className="flex shrink-0 items-center gap-2 text-xs text-muted-foreground">
            {r.source === "report" ? (
              <Badge variant="accent-soft">
                <Flag />
                举报
              </Badge>
            ) : (
              <Badge variant="muted">
                <Bot />
                自动
              </Badge>
            )}
            {timeAgo(r.created_at)}
          </span>
        </div>
        <p className="mt-2 rounded-md bg-muted/60 px-3 py-2 text-[13px]">
          <span className="text-muted-foreground">{r.source === "report" ? `${r.reporter_name ?? "匿名用户"} 举报：` : "自动标记："}</span>
          {r.reason}
        </p>
        {r.status === "pending" ? (
          <div className="mt-3 grid gap-2 sm:grid-cols-[140px_120px_minmax(0,1fr)_auto]">
            <Select aria-label="裁决动作" value={action} onChange={(e) => setAction(e.target.value as ReviewAction)}>
              <option value="allow">允许</option>
              <option value="login">需登录</option>
              <option value="vip">需大会员</option>
              <option value="block">屏蔽</option>
              <option value="dismiss">驳回（不处理）</option>
            </Select>
            <Select aria-label="标签" value={label} onChange={(e) => setLabel(e.target.value as RuleLabel)}>
              <option value="">无标签</option>
              <option value="political">政治</option>
              <option value="porn">色情</option>
              <option value="copyright">版权</option>
              <option value="other">其他</option>
            </Select>
            <Input aria-label="备注" placeholder="备注（可选）" value={note} onChange={(e) => setNote(e.target.value)} />
            <Button onClick={() => m.mutate()} disabled={m.isPending}>
              {m.isPending ? <Loader2 className="animate-spin" /> : <Check />}
              裁决
            </Button>
          </div>
        ) : (
          <div className="mt-3 flex items-center gap-2 text-[13px] text-muted-foreground">
            裁决：{r.action === "dismiss" ? <Badge variant="muted">已驳回</Badge> : r.action ? <ActionBadge action={r.action} /> : <Badge variant="muted">{r.status}</Badge>} {r.note}
          </div>
        )}
      </Card>
    </li>
  )
}
