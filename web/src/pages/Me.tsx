import { useState, type FormEvent } from "react"
import { Link, Navigate, useNavigate } from "react-router-dom"
import { useMutation } from "@tanstack/react-query"
import { Crown, KeyRound, Loader2, LogOut, Palette, Shield, Ticket } from "lucide-react"
import { Container } from "@/components/layout/Container"
import { ThemeSegmented } from "@/components/layout/ThemeToggle"
import { RoleBadge } from "@/components/badges"
import { PageLoading, errorMessage } from "@/components/states"
import { Button } from "@/components/ui/button"
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card"
import { Field, Input } from "@/components/ui/input"
import { toast } from "@/components/ui/toast"
import { api } from "@/lib/api"
import { PERM, useCan, useLogout, useSetMe, useUser } from "@/lib/queries"
import { formatDate } from "@/lib/format"
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

      <Card>
        <CardHeader className="pb-3">
          <CardTitle className="flex items-center gap-2">
            <Palette className="h-4 w-4 text-primary-text" />
            外观
          </CardTitle>
        </CardHeader>
        <CardContent>
          <ThemeSegmented />
        </CardContent>
      </Card>

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

