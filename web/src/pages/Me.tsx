import { useCallback, useEffect, useState, type FormEvent } from "react"
import { Link, Navigate, useNavigate } from "react-router-dom"
import { useMutation } from "@tanstack/react-query"
import { Crown, HardDrive, KeyRound, Loader2, LogOut, Shield, Ticket, Trash2 } from "lucide-react"
import { Container } from "@/components/layout/Container"
import { RoleBadge } from "@/components/badges"
import { PageLoading, errorMessage } from "@/components/states"
import { Button } from "@/components/ui/button"
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card"
import { Field, Input } from "@/components/ui/input"
import { toast } from "@/components/ui/toast"
import { api } from "@/lib/api"
import { PERM, useCan, useLogout, useSetMe, useSiteOrFallback, useUser } from "@/lib/queries"
import { formatBytes, formatDate } from "@/lib/format"
import type { CacheStats } from "@/player/store/cache"
import { useDocumentTitle } from "@/lib/hooks"

export default function MePage() {
  useDocumentTitle("个人中心")
  const { user, loading } = useUser()
  const logout = useLogout()
  const navigate = useNavigate()
  const canRedeem = useCan(PERM.redeem)
  const canPassword = useCan(PERM.password)
  if (loading) return <PageLoading />
  if (!user) return <Navigate replace to="/login?next=/me" />

  const vipActive = user.role === "admin" || (user.vip_until && new Date(user.vip_until).getTime() > Date.now())
  return (
    <Container className="max-w-3xl space-y-4 py-4 sm:py-6">
      <Card className="overflow-hidden">
        <div className="h-16 bg-gradient-to-r from-primary/70 to-accent/70 sm:h-20" />
        <div className="px-4 pb-4 sm:px-5">
          <div className="flex items-start justify-between gap-3">
            <span className="-mt-8 inline-flex h-16 w-16 shrink-0 items-center justify-center rounded-full border-4 border-card bg-primary text-2xl font-bold uppercase text-white sm:-mt-10 sm:h-20 sm:w-20 sm:text-3xl">
              {user.username.slice(0, 1)}
            </span>
            {user.role === "admin" && (
              <Button asChild variant="outline" size="sm" className="mt-3">
                <Link to="/admin">
                  <Shield />
                  后台管理
                </Link>
              </Button>
            )}
          </div>
          <div className="mt-2 min-w-0">
            <div className="flex items-center gap-2">
              <h1 className="truncate text-lg font-bold">{user.username}</h1>
              <RoleBadge role={user.role} />
            </div>
            <p className="truncate text-[13px] text-muted-foreground">{user.email}</p>
          </div>
        </div>
        <dl className="grid grid-cols-2 divide-x border-t text-center">
          <div className="px-3 py-3">
            <dt className="text-xs text-muted-foreground">大会员</dt>
            <dd className="mt-0.5 text-sm font-semibold">
              {user.role === "admin" ? "永久（管理员）" : vipActive ? `${formatDate(user.vip_until)} 到期` : "未开通"}
            </dd>
          </div>
          <div className="px-3 py-3">
            <dt className="text-xs text-muted-foreground">注册时间</dt>
            <dd className="mt-0.5 text-sm font-semibold">{formatDate(user.created_at)}</dd>
          </div>
        </dl>
      </Card>

      {(canRedeem || canPassword) && (
        <div className={canRedeem && canPassword ? "grid gap-4 md:grid-cols-2" : "grid gap-4"}>
          {canRedeem && <RedeemCard />}
          {canPassword && <PasswordCard />}
        </div>
      )}

      <CacheCard />

      <Button
        variant="outline"
        className="w-full text-destructive-text hover:bg-destructive/10"
        onClick={() => logout.mutate(undefined, { onSettled: () => navigate("/") })}
      >
        <LogOut />
        退出登录
      </Button>
    </Container>
  )
}

/** 浏览器里的视频缓存（OPFS，按 piece 存储，超出预算自动清理） */
function CacheCard() {
  const site = useSiteOrFallback()
  const maxPush = site.max_push_size_bytes
  const [stats, setStats] = useState<CacheStats | null>(null)
  const [busy, setBusy] = useState(false)
  const load = useCallback(async () => {
    const { pieceCache } = await import("@/player/store/cache")
    return pieceCache.statsFresh(maxPush)
  }, [maxPush])
  useEffect(() => {
    let cancelled = false
    load()
      .then((s) => !cancelled && setStats(s))
      .catch(() => !cancelled && setStats({ supported: false, used: 0, budget: 0, videos: 0 }))
    return () => {
      cancelled = true
    }
  }, [load])
  async function clear() {
    setBusy(true)
    try {
      const { pieceCache } = await import("@/player/store/cache")
      const skipped = await pieceCache.clearAll()
      setStats(await load())
      toast.success(skipped ? `缓存已清空（${skipped} 个正在播放的视频未清理）` : "缓存已清空")
    } catch (e) {
      toast.error(errorMessage(e))
    } finally {
      setBusy(false)
    }
  }
  const pct = stats && stats.budget ? Math.min(1, stats.used / stats.budget) : 0
  return (
    <Card>
      <CardHeader className="pb-3">
        <CardTitle className="flex items-center gap-2">
          <HardDrive className="h-4 w-4 text-primary-text" />
          缓存
        </CardTitle>
        <CardDescription>看过的视频保存在本浏览器中，再次播放无需重新下载；超出上限时自动清理较早的内容</CardDescription>
      </CardHeader>
      <CardContent className="space-y-3">
        {!stats ? (
          <p className="flex items-center gap-2 text-[13px] text-muted-foreground">
            <Loader2 className="h-4 w-4 animate-spin" />
            正在统计…
          </p>
        ) : !stats.supported ? (
          <p className="text-[13px] text-muted-foreground">当前浏览器不支持本地缓存</p>
        ) : (
          <>
            <div className="flex flex-wrap items-baseline justify-between gap-2 text-[13px]">
              <span className="tabular-nums">
                已用 <strong className="font-semibold">{formatBytes(stats.used)}</strong> / {formatBytes(stats.budget)}
              </span>
              <span className="text-muted-foreground">{stats.videos} 个视频</span>
            </div>
            <div className="h-1.5 overflow-hidden rounded-full bg-piece-empty" role="progressbar" aria-valuenow={Math.round(pct * 100)} aria-valuemin={0} aria-valuemax={100}>
              <div className="h-full rounded-full bg-primary" style={{ width: `${Math.max(1, pct * 100)}%` }} />
            </div>
            <Button variant="outline" size="sm" onClick={clear} disabled={busy || stats.used === 0}>
              {busy ? <Loader2 className="animate-spin" /> : <Trash2 />}
              清空缓存
            </Button>
          </>
        )}
      </CardContent>
    </Card>
  )
}

function RedeemCard() {
  const setMe = useSetMe()
  const [code, setCode] = useState("")
  const m = useMutation({
    mutationFn: api.redeem,
    onSuccess: (me) => {
      setMe(me)
      setCode("")
      toast.success(`兑换成功，大会员有效期至 ${formatDate(me.user?.vip_until)}`)
    },
    onError: (e) => toast.error(errorMessage(e)),
  })
  function submit(e: FormEvent) {
    e.preventDefault()
    if (code.trim()) m.mutate(code.trim())
  }
  return (
    <Card>
      <CardHeader className="pb-3">
        <CardTitle className="flex items-center gap-2">
          <Crown className="h-4 w-4 text-warning-text" />
          兑换大会员
        </CardTitle>
        <CardDescription>大会员可观看高码率、大体积等专属资源</CardDescription>
      </CardHeader>
      <CardContent>
        <form onSubmit={submit} className="flex gap-2">
          <div className="relative flex-1">
            <Ticket className="pointer-events-none absolute left-3 top-1/2 h-4 w-4 -translate-y-1/2 text-muted-foreground" />
            <Input aria-label="兑换码" placeholder="输入兑换码" value={code} onChange={(e) => setCode(e.target.value)} className="pl-9 font-mono uppercase" />
          </div>
          <Button type="submit" disabled={m.isPending || !code.trim()}>
            {m.isPending && <Loader2 className="animate-spin" />}
            兑换
          </Button>
        </form>
      </CardContent>
    </Card>
  )
}

function PasswordCard() {
  const [f, setF] = useState({ old: "", next: "", confirm: "" })
  const [err, setErr] = useState("")
  const m = useMutation({
    mutationFn: api.changePassword,
    onSuccess: () => {
      setF({ old: "", next: "", confirm: "" })
      toast.success("密码已修改")
    },
    onError: (e) => setErr(errorMessage(e)),
  })
  function submit(e: FormEvent) {
    e.preventDefault()
    setErr("")
    if (f.next.length < 8) return setErr("新密码至少 8 位")
    if (f.next !== f.confirm) return setErr("两次输入的新密码不一致")
    m.mutate({ old_password: f.old, new_password: f.next })
  }
  const set = (k: keyof typeof f) => (e: React.ChangeEvent<HTMLInputElement>) => setF((s) => ({ ...s, [k]: e.target.value }))
  return (
    <Card>
      <CardHeader className="pb-3">
        <CardTitle className="flex items-center gap-2">
          <KeyRound className="h-4 w-4 text-accent-text" />
          修改密码
        </CardTitle>
      </CardHeader>
      <CardContent>
        <form onSubmit={submit} className="grid gap-3">
          <input type="text" autoComplete="username" hidden readOnly />
          <Field label="原密码" htmlFor="old-pw">
            <Input id="old-pw" type="password" autoComplete="current-password" required value={f.old} onChange={set("old")} />
          </Field>
          <Field label="新密码" htmlFor="new-pw">
            <Input id="new-pw" type="password" autoComplete="new-password" required value={f.next} onChange={set("next")} />
          </Field>
          <Field label="确认新密码" htmlFor="confirm-pw" error={err || undefined}>
            <Input id="confirm-pw" type="password" autoComplete="new-password" required value={f.confirm} onChange={set("confirm")} />
          </Field>
          <Button type="submit" variant="secondary" disabled={m.isPending} className="justify-self-start">
            {m.isPending && <Loader2 className="animate-spin" />}
            保存新密码
          </Button>
        </form>
      </CardContent>
    </Card>
  )
}

