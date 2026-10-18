import { useState, type FormEvent, type ReactNode } from "react"
import { Link, Navigate, useNavigate, useParams, useSearchParams } from "react-router-dom"
import { useMutation } from "@tanstack/react-query"
import { Eye, EyeOff, Loader2, Lock } from "lucide-react"
import { Container } from "@/components/layout/Container"
import { LogoMark } from "@/components/layout/Logo"
import { ThemeSegmented } from "@/components/layout/ThemeToggle"
import { Turnstile } from "@/components/Turnstile"
import { Button } from "@/components/ui/button"
import { Card } from "@/components/ui/card"
import { Field, Input } from "@/components/ui/input"
import { toast } from "@/components/ui/toast"
import { errorMessage } from "@/components/states"
import { api } from "@/lib/api"
import { useSetMe, useSiteOrFallback, useUser } from "@/lib/queries"
import { useDocumentTitle } from "@/lib/hooks"

function safeNext(next: string | null) {
  return next && next.startsWith("/") && !next.startsWith("//") ? next : "/"
}

function AuthCard({ title, subtitle, children, footer }: { title: string; subtitle: ReactNode; children: ReactNode; footer: ReactNode }) {
  return (
    <Container className="flex min-h-[calc(100dvh-3.5rem-3.75rem)] items-center justify-center py-8 md:min-h-[calc(100dvh-3.5rem-6rem)]">
      <div className="w-full max-w-sm">
        <div className="mb-5 flex flex-col items-center text-center">
          <LogoMark className="h-11 w-11" />
          <h1 className="mt-3 text-xl font-bold">{title}</h1>
          <p className="mt-1 text-[13px] text-muted-foreground">{subtitle}</p>
        </div>
        <Card className="p-5 sm:p-6">{children}</Card>
        <p className="mt-4 text-center text-[13px] text-muted-foreground">{footer}</p>
        {/* 移动端没有顶栏，未登录用户在这里切换主题 */}
        <div className="mt-6 flex justify-center md:hidden">
          <ThemeSegmented />
        </div>
      </div>
    </Container>
  )
}

function PasswordInput(props: React.InputHTMLAttributes<HTMLInputElement>) {
  const [show, setShow] = useState(false)
  return (
    <div className="relative">
      <Input {...props} type={show ? "text" : "password"} className="pr-11" />
      <button
        type="button"
        onClick={() => setShow((s) => !s)}
        aria-label={show ? "隐藏密码" : "显示密码"}
        className="absolute right-0.5 top-1/2 inline-flex h-9 w-10 -translate-y-1/2 items-center justify-center rounded-md text-muted-foreground hover:text-foreground"
      >
        {show ? <EyeOff className="h-4 w-4" /> : <Eye className="h-4 w-4" />}
      </button>
    </div>
  )
}

export function LoginPage() {
  useDocumentTitle("登录")
  const [sp] = useSearchParams()
  const next = safeNext(sp.get("next"))
  const navigate = useNavigate()
  const setMe = useSetMe()
  const { user } = useUser()
  const [identifier, setIdentifier] = useState("")
  const [password, setPassword] = useState("")
  const m = useMutation({
    mutationFn: api.login,
    onSuccess: (me) => {
      setMe(me)
      toast.success(`欢迎回来，${me.user?.username ?? ""}`)
      navigate(next, { replace: true })
    },
  })
  if (user && !m.isPending && !m.isSuccess) return <Navigate replace to={next} />

  function submit(e: FormEvent) {
    e.preventDefault()
    m.mutate({ identifier: identifier.trim(), password })
  }
  return (
    <AuthCard
      title="登录 Project Kawaii"
      subtitle="登录后可观看会员资源、兑换大会员"
      footer={
        <>
          还没有账号？
          <Link to={`/register${next !== "/" ? `?next=${encodeURIComponent(next)}` : ""}`} className="font-medium text-primary-text hover:underline">
            注册
          </Link>
        </>
      }
    >
      <form onSubmit={submit} className="grid gap-4">
        <Field label="用户名或邮箱" htmlFor="identifier">
          <Input id="identifier" autoComplete="username" required value={identifier} onChange={(e) => setIdentifier(e.target.value)} autoFocus />
        </Field>
        <Field label="密码" htmlFor="password">
          <PasswordInput id="password" autoComplete="current-password" required value={password} onChange={(e) => setPassword(e.target.value)} />
        </Field>
        {m.error && (
          <p role="alert" className="rounded-md bg-destructive/10 px-3 py-2 text-[13px] text-destructive-text">
            {errorMessage(m.error)}
          </p>
        )}
        <Button type="submit" size="lg" disabled={m.isPending} className="w-full">
          {m.isPending && <Loader2 className="animate-spin" />}
          登录
        </Button>
      </form>
    </AuthCard>
  )
}

/** 旧链接 /register?invite=X → /register/invite/X */
export function RegisterRoute() {
  const [sp] = useSearchParams()
  const invite = sp.get("invite")?.trim()
  if (invite) {
    const rest = new URLSearchParams(sp)
    rest.delete("invite")
    const q = rest.toString()
    return <Navigate replace to={`/register/invite/${encodeURIComponent(invite)}${q ? `?${q}` : ""}`} />
  }
  return <RegisterPage />
}

/** /register 与 /register/invite/:code（预填邀请码） */
export function RegisterPage() {
  useDocumentTitle("注册")
  const site = useSiteOrFallback()
  const { code: inviteParam = "" } = useParams()
  const [sp] = useSearchParams()
  const next = safeNext(sp.get("next"))
  const navigate = useNavigate()
  const setMe = useSetMe()
  const [form, setForm] = useState({ username: "", email: "", password: "", confirm: "", invite_code: inviteParam })
  const [token, setToken] = useState("")
  const [resetKey, setResetKey] = useState(0)
  const [localError, setLocalError] = useState("")
  const needInvite = site.invite_required || site.registration === "invite"
  const m = useMutation({
    mutationFn: api.register,
    onSuccess: (me) => {
      setMe(me)
      toast.success("注册成功，已自动登录")
      navigate(next, { replace: true })
    },
    onError: () => setResetKey((k) => k + 1),
  })
  const set = (k: keyof typeof form) => (e: React.ChangeEvent<HTMLInputElement>) => setForm((f) => ({ ...f, [k]: e.target.value }))

  const footer = (
    <>
      已有账号？
      <Link to="/login" className="font-medium text-primary-text hover:underline">
        登录
      </Link>
    </>
  )

  if (site.registration === "closed") {
    return (
      <AuthCard title="暂停注册" subtitle="本站目前未开放注册" footer={footer}>
        <div className="flex flex-col items-center gap-2 py-4 text-center text-sm text-muted-foreground">
          <Lock className="h-8 w-8" />
          管理员已关闭新用户注册，请关注站点公告。
        </div>
      </AuthCard>
    )
  }

  function submit(e: FormEvent) {
    e.preventDefault()
    setLocalError("")
    if (form.password.length < 8) return setLocalError("密码至少 8 位")
    if (form.password !== form.confirm) return setLocalError("两次输入的密码不一致")
    if (site.turnstile_site_key && !token) return setLocalError("请完成人机验证")
    m.mutate({
      username: form.username.trim(),
      email: form.email.trim(),
      password: form.password,
      invite_code: form.invite_code.trim() || undefined,
      turnstile_token: token || undefined,
    })
  }
  const err = localError || (m.error ? errorMessage(m.error) : "")
  return (
    <AuthCard title="注册账号" subtitle={needInvite ? "当前为邀请注册，需要邀请码" : "注册即可观看会员资源"} footer={footer}>
      <form onSubmit={submit} className="grid gap-4">
        <Field label="用户名" htmlFor="username" hint="3-20 位字母、数字或下划线">
          <Input id="username" autoComplete="username" required minLength={3} maxLength={20} pattern="[A-Za-z0-9_]+" value={form.username} onChange={set("username")} />
        </Field>
        <Field label="邮箱" htmlFor="email">
          <Input id="email" type="email" autoComplete="email" required value={form.email} onChange={set("email")} />
        </Field>
        <Field label="密码" htmlFor="password">
          <PasswordInput id="password" autoComplete="new-password" required value={form.password} onChange={set("password")} />
        </Field>
        <Field label="确认密码" htmlFor="confirm">
          <PasswordInput id="confirm" autoComplete="new-password" required value={form.confirm} onChange={set("confirm")} />
        </Field>
        {(needInvite || form.invite_code) && (
          <Field label={needInvite ? "邀请码" : "邀请码（可选）"} htmlFor="invite" hint={inviteParam ? "已通过邀请链接自动填写" : undefined}>
            <Input id="invite" required={needInvite} value={form.invite_code} onChange={set("invite_code")} className="font-mono" />
          </Field>
        )}
        <Turnstile siteKey={site.turnstile_site_key} action="register" resetKey={resetKey} onToken={setToken} />
        {err && (
          <p role="alert" className="rounded-md bg-destructive/10 px-3 py-2 text-[13px] text-destructive-text">
            {err}
          </p>
        )}
        <Button type="submit" size="lg" disabled={m.isPending} className="w-full">
          {m.isPending && <Loader2 className="animate-spin" />}
          注册
        </Button>
      </form>
    </AuthCard>
  )
}
