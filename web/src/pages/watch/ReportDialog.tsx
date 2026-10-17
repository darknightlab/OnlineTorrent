import { useState } from "react"
import { useMutation } from "@tanstack/react-query"
import { Loader2 } from "lucide-react"
import { Dialog, DialogContent, DialogFooter } from "@/components/ui/dialog"
import { Button } from "@/components/ui/button"
import { Textarea } from "@/components/ui/input"
import { toast } from "@/components/ui/toast"
import { errorMessage } from "@/components/states"
import { api } from "@/lib/api"
import { cn } from "@/lib/utils"

const REASONS = ["政治敏感", "色情低俗", "侵犯版权", "内容与标题不符", "无法播放 / 损坏", "其他"]

export function ReportDialog({ open, onOpenChange, infohash, title }: { open: boolean; onOpenChange: (v: boolean) => void; infohash: string; title: string }) {
  const [reason, setReason] = useState(REASONS[0])
  const [detail, setDetail] = useState("")
  const m = useMutation({
    mutationFn: () => api.report({ infohash, reason: detail.trim() ? `${reason}：${detail.trim()}` : reason }),
    onSuccess: () => {
      toast.success("举报已提交，感谢反馈")
      setDetail("")
      onOpenChange(false)
    },
    onError: (e) => toast.error(errorMessage(e)),
  })
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent title="举报该资源" description={<span className="line-clamp-2 wrap-anywhere">{title}</span>}>
        <div role="radiogroup" aria-label="举报原因" className="grid grid-cols-2 gap-2">
          {REASONS.map((r) => (
            <button
              key={r}
              type="button"
              role="radio"
              aria-checked={reason === r}
              onClick={() => setReason(r)}
              className={cn(
                "h-10 rounded-md border px-3 text-left text-[13px] transition-colors",
                reason === r ? "border-primary bg-primary-soft font-medium text-primary-text" : "hover:bg-muted",
              )}
            >
              {r}
            </button>
          ))}
        </div>
        <Textarea aria-label="补充说明" placeholder="补充说明（可选）" value={detail} onChange={(e) => setDetail(e.target.value)} maxLength={500} />
        <DialogFooter>
          <Button variant="outline" onClick={() => onOpenChange(false)}>
            取消
          </Button>
          <Button onClick={() => m.mutate()} disabled={m.isPending}>
            {m.isPending && <Loader2 className="animate-spin" />}
            提交举报
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
