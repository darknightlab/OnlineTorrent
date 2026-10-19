// 后台「推荐规则」：单集页资源排序 / 自动播放的评分规则（GET/PUT /admin/rank-rules，公开于 /site.rank_rules；评分见 lib/rank.ts）
import { useEffect, useMemo, useState } from "react"
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { Loader2, Plus, RotateCcw, Save, Trash2, Undo2 } from "lucide-react"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Card } from "@/components/ui/card"
import { Input } from "@/components/ui/input"
import { toast } from "@/components/ui/toast"
import { ErrorState, Spinner, errorMessage } from "@/components/states"
import { api } from "@/lib/api"
import { scoreFile, type Viewer } from "@/lib/rank"
import type { RankRules, RankRulesResponse, SwarmStats } from "@/lib/types"
import { cn } from "@/lib/utils"
import { AdminHeader } from "./components"

const KEY = ["admin", "rank-rules"]
const MAX_KEYWORDS = 30
const MAX_TEAMS = 200

/** 与服务端一致的正则子集检查（Go RE2 ∩ JS）：这里只做浏览器能否编译 + 明显不兼容语法的提示，最终以服务端为准 */
function patternError(p: string): string | null {
  if (!p.trim()) return "不能为空"
  if (/\(\?(?!:)/.test(p)) return "只支持 (?: 非捕获分组"
  if (/\\[AzQECpP]|\\x\{|\[\[:/.test(p)) return "包含浏览器不兼容的语法"
  if (/\\[1-9]/.test(p)) return "不支持反向引用"
  try {
    new RegExp(p, "i")
  } catch (e) {
    return (e as Error).message
  }
  return null
}

const AVAIL: { key: keyof RankRules["availability"]; label: string; hint: string }[] = [
  { key: "server_complete", label: "服务器已缓存", hint: "某台 qB 已完整下载（可 WebRTC 做种 / WebSeed）" },
  { key: "wt_seeder", label: "每个 WebTorrent 做种者", hint: "wss tracker 统计的做种人数" },
  { key: "wt_cap", label: "WebTorrent 加分上限", hint: "≥ 0" },
  { key: "bt_seeder", label: "每个 BT 做种者", hint: "仅对能让服务器做种的用户（接口权限 seed）" },
  { key: "bt_cap", label: "BT 加分上限", hint: "≥ 0" },
  { key: "dead", label: "无人做种", hint: "游客类用户：WebRTC 做种 0 且服务器未缓存；有做种 / WebSeed 权限的用户：所有来源都为 0。未统计过的不扣分" },
]

export default function RankRulesPage() {
  const qc = useQueryClient()
  const q = useQuery({ queryKey: KEY, queryFn: api.admin.rankRules })
  const [draft, setDraft] = useState<RankRules | null>(null)
  useEffect(() => {
    if (q.data) setDraft(structuredClone(q.data.rules))
  }, [q.data])
  const dirty = !!q.data && !!draft && JSON.stringify(draft) !== JSON.stringify(q.data.rules)
  const errors = useMemo(() => (draft ? draft.keywords.map((k) => patternError(k.pattern)) : []), [draft])
  const save = useMutation({
    mutationFn: () => api.admin.saveRankRules(draft!),
    onSuccess: (d: RankRulesResponse) => {
      qc.setQueryData(KEY, d)
      void qc.invalidateQueries({ queryKey: ["site"] })
      toast.success("推荐规则已保存")
    },
    onError: (e) => toast.error(errorMessage(e)),
  })

  if (q.error) return <ErrorState error={q.error} onRetry={() => q.refetch()} />
  if (!q.data || !draft)
    return (
      <div className="flex h-40 items-center justify-center">
        <Spinner />
      </div>
    )
  const set = (f: (r: RankRules) => void) =>
    setDraft((d) => {
      const n = structuredClone(d!)
      f(n)
      return n
    })
  const invalid = errors.some(Boolean) || draft.keywords.some((k) => !k.label.trim()) || draft.teams.some((t) => !t.name.trim())

  return (
    <div className="pb-20">
      <AdminHeader
        title="推荐规则"
        description="单集页按总分从高到低排序资源并自动播放第一个（当前账号不可播放的始终排最后）；同分时较新的在前。评分在浏览器中进行，取决于用户权限与做种人数。"
        actions={
          <Button variant="outline" size="sm" onClick={() => setDraft(structuredClone(q.data.defaults))}>
            <RotateCcw />
            恢复默认
          </Button>
        }
      />
      <div className="grid gap-4">
        <Card className="divide-y">
          <h2 className="px-4 py-3 text-sm font-semibold sm:px-5">可用性（做种人数）</h2>
          <div className="grid gap-x-6 gap-y-3 px-4 py-3 sm:grid-cols-2 sm:px-5 xl:grid-cols-3">
            {AVAIL.map((a) => (
              <NumberField
                key={a.key}
                id={`avail-${a.key}`}
                label={a.label}
                hint={a.hint}
                value={draft.availability[a.key]}
                min={a.key.endsWith("_cap") ? 0 : -1000}
                onChange={(v) => set((r) => void (r.availability[a.key] = v))}
              />
            ))}
          </div>
        </Card>

        <Card className="divide-y">
          <div className="flex items-center justify-between gap-2 px-4 py-3 sm:px-5">
            <div>
              <h2 className="text-sm font-semibold">关键词</h2>
              <p className="text-xs text-muted-foreground">正则（不区分大小写）匹配「种子标题 + 文件路径」，命中即加分；可为负数</p>
            </div>
            <Button
              variant="soft"
              size="sm"
              disabled={draft.keywords.length >= MAX_KEYWORDS}
              onClick={() => set((r) => void r.keywords.push({ label: "", pattern: "", score: 1 }))}
            >
              <Plus />
              添加
            </Button>
          </div>
          {draft.keywords.length === 0 && <p className="px-4 py-4 text-[13px] text-muted-foreground sm:px-5">没有关键词规则</p>}
          {draft.keywords.map((k, i) => (
            <div key={i} className="grid grid-cols-[minmax(0,1fr)_5.5rem_auto] items-start gap-2 px-4 py-3 sm:grid-cols-[9rem_minmax(0,1fr)_6rem_auto] sm:px-5">
              <Input aria-label="名称" placeholder="名称，如 HEVC" value={k.label} maxLength={40} onChange={(e) => set((r) => void (r.keywords[i].label = e.target.value))} />
              <div className="col-span-3 row-start-2 min-w-0 sm:col-span-1 sm:row-start-auto">
                <Input
                  aria-label="正则"
                  placeholder="正则，如 hevc|[hx]\.?265"
                  className={cn("font-mono text-xs", errors[i] && "border-destructive")}
                  value={k.pattern}
                  maxLength={200}
                  onChange={(e) => set((r) => void (r.keywords[i].pattern = e.target.value))}
                />
                {errors[i] && <p className="mt-1 text-xs text-destructive-text">{errors[i]}</p>}
              </div>
              <ScoreInput aria-label="分值" className="col-start-2 row-start-1 sm:col-start-auto sm:row-start-auto" value={k.score} onChange={(v) => set((r) => void (r.keywords[i].score = v))} />
              <Button variant="ghost" size="icon" aria-label="删除" className="col-start-3 row-start-1 sm:col-start-auto sm:row-start-auto" onClick={() => set((r) => void r.keywords.splice(i, 1))}>
                <Trash2 />
              </Button>
            </div>
          ))}
        </Card>

        <Card className="divide-y">
          <div className="flex items-center justify-between gap-2 px-4 py-3 sm:px-5">
            <div>
              <h2 className="text-sm font-semibold">字幕组</h2>
              <p className="text-xs text-muted-foreground">按字幕组名称精确匹配（区分大小写）</p>
            </div>
            <Button variant="soft" size="sm" disabled={draft.teams.length >= MAX_TEAMS} onClick={() => set((r) => void r.teams.push({ name: "", score: 5 }))}>
              <Plus />
              添加
            </Button>
          </div>
          {draft.teams.length === 0 && <p className="px-4 py-4 text-[13px] text-muted-foreground sm:px-5">没有字幕组规则</p>}
          {draft.teams.map((t, i) => (
            <div key={i} className="grid grid-cols-[minmax(0,1fr)_6rem_auto] gap-2 px-4 py-3 sm:px-5">
              <Input aria-label="字幕组名称" placeholder="字幕组名称" value={t.name} maxLength={80} onChange={(e) => set((r) => void (r.teams[i].name = e.target.value))} />
              <ScoreInput aria-label="分值" value={t.score} onChange={(v) => set((r) => void (r.teams[i].score = v))} />
              <Button variant="ghost" size="icon" aria-label="删除" onClick={() => set((r) => void r.teams.splice(i, 1))}>
                <Trash2 />
              </Button>
            </div>
          ))}
        </Card>

        <Card className="divide-y">
          <h2 className="px-4 py-3 text-sm font-semibold sm:px-5">文件大小</h2>
          <div className="px-4 py-3 sm:px-5">
            <NumberField
              id="oversize"
              label="超过自动选种大小上限"
              hint="文件大于站点设置「时间线自动选种大小上限」（play.max_auto_size_mb）时"
              value={draft.oversize}
              min={-1000}
              onChange={(v) => set((r) => void (r.oversize = v))}
            />
          </div>
        </Card>

        <TryIt rules={draft} />
      </div>

      <div
        className={cn(
          "fixed inset-x-0 bottom-0 z-30 border-t bg-header pb-safe backdrop-blur-md transition-transform lg:left-[max(14rem,calc((100vw-1440px)/2+14rem))]",
          dirty ? "translate-y-0" : "invisible translate-y-full",
        )}
      >
        <div className="flex h-14 items-center justify-between gap-3 px-3 sm:px-5 lg:px-8">
          <span className="text-[13px] text-muted-foreground">{invalid ? "有无效的规则" : "规则已修改"}</span>
          <div className="flex gap-2">
            <Button variant="outline" size="sm" onClick={() => setDraft(structuredClone(q.data.rules))}>
              <Undo2 />
              撤销
            </Button>
            <Button size="sm" onClick={() => save.mutate()} disabled={save.isPending || invalid}>
              {save.isPending ? <Loader2 className="animate-spin" /> : <Save />}
              保存
            </Button>
          </div>
        </div>
      </div>
    </div>
  )
}

function ScoreInput({ value, onChange, className, ...rest }: { value: number; onChange: (v: number) => void } & Omit<React.InputHTMLAttributes<HTMLInputElement>, "value" | "onChange">) {
  return (
    <Input
      {...rest}
      type="number"
      inputMode="numeric"
      min={-1000}
      max={1000}
      step={1}
      className={cn("tabular-nums", className)}
      value={String(value)}
      onChange={(e) => onChange(e.target.value === "" || e.target.value === "-" ? 0 : Math.trunc(Number(e.target.value)) || 0)}
    />
  )
}

function NumberField({ id, label, hint, value, min, onChange }: { id: string; label: string; hint: string; value: number; min: number; onChange: (v: number) => void }) {
  return (
    <div className="grid gap-1">
      <label htmlFor={id} className="text-[13px] font-medium">
        {label}
      </label>
      <Input
        id={id}
        type="number"
        inputMode="numeric"
        min={min}
        max={1000}
        step={1}
        className="max-w-36 tabular-nums"
        value={String(value)}
        onChange={(e) => onChange(Math.trunc(Number(e.target.value)) || 0)}
      />
      <p className="text-xs leading-relaxed text-muted-foreground">{hint}</p>
    </div>
  )
}

const VIEWERS: { label: string; v: Viewer }[] = [
  { label: "游客", v: { canSeed: false, canWebseed: false } },
  { label: "会员（可做种 / WebSeed）", v: { canSeed: true, canWebseed: true } },
]

/** 试算：输入标题与做种人数，按草稿规则给出分数 */
function TryIt({ rules }: { rules: RankRules }) {
  const [title, setTitle] = useState("[Team] Show - 02 [WebRip 1080p HEVC-10bit AAC][简繁内封]")
  const [bt, setBt] = useState("23")
  const [wt, setWt] = useState("0")
  const [server, setServer] = useState(false)
  const swarm: SwarmStats = {
    bt_seeders: bt === "" ? null : Number(bt),
    bt_leechers: 0,
    wt_seeders: wt === "" ? null : Number(wt),
    wt_leechers: 0,
    server_complete: server,
    checked_at: bt === "" && wt === "" ? null : new Date().toISOString(),
  }
  return (
    <Card className="divide-y">
      <h2 className="px-4 py-3 text-sm font-semibold sm:px-5">试算</h2>
      <div className="grid gap-3 px-4 py-3 sm:px-5">
        <Input aria-label="标题 / 文件名" value={title} onChange={(e) => setTitle(e.target.value)} />
        <div className="flex flex-wrap items-center gap-3 text-[13px]">
          <label className="flex items-center gap-1.5">
            BT 做种
            <Input type="number" min={0} className="w-20" value={bt} onChange={(e) => setBt(e.target.value)} placeholder="未知" />
          </label>
          <label className="flex items-center gap-1.5">
            WebRTC 做种
            <Input type="number" min={0} className="w-20" value={wt} onChange={(e) => setWt(e.target.value)} placeholder="未知" />
          </label>
          <label className="flex items-center gap-1.5">
            <input type="checkbox" checked={server} onChange={(e) => setServer(e.target.checked)} />
            服务器已缓存
          </label>
        </div>
        <ul className="grid gap-1.5">
          {VIEWERS.map(({ label, v }) => {
            const s = scoreFile({ torrent_title: title, path: "", team: "", size_bytes: 0, published_at: "", access: { action: "allow", reason: "" }, swarm }, rules, v, 0)
            return (
              <li key={label} className="flex flex-wrap items-center gap-1.5 text-[13px]">
                <span className="w-44 shrink-0 text-muted-foreground">{label}</span>
                <span className={cn("w-14 font-semibold tabular-nums", s.score < 0 && "text-destructive-text")}>{s.score}</span>
                {s.parts.map((p, i) => (
                  <Badge key={i} variant={p.score < 0 ? "destructive" : "soft"}>
                    {p.label} {p.score > 0 ? "+" : ""}
                    {p.score}
                  </Badge>
                ))}
              </li>
            )
          })}
        </ul>
      </div>
    </Card>
  )
}
