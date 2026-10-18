import { useState } from "react"
import { NavLink, Route, Routes, useLocation } from "react-router-dom"
import {
  Activity,
  ClipboardCheck,
  Gauge,
  History,
  KeySquare,
  Menu,
  Rss,
  ScrollText,
  Server,
  Settings2,
  ShieldCheck,
  Ticket,
  Users,
} from "lucide-react"
import { Dialog, DialogTrigger, SheetContent } from "@/components/ui/dialog"
import { Button } from "@/components/ui/button"
import { PageLoading } from "@/components/states"
import { StatusPage } from "@/pages/NotFound"
import { useUser } from "@/lib/queries"
import { cn } from "@/lib/utils"
import { useDocumentTitle } from "@/lib/hooks"
import { Navigate } from "react-router-dom"
import Overview from "./Overview"
import SettingsPage from "./Settings"
import PermissionsPage from "./Permissions"
import QBServers from "./QBServers"
import SourcesPage from "./Sources"
import UsersPage from "./Users"
import Codes from "./Codes"
import Rules from "./Rules"
import Reviews from "./Reviews"
import { Audit, Plays } from "./Logs"

const NAV = [
  { to: "/admin", label: "概览", icon: Gauge, end: true },
  { to: "/admin/settings", label: "站点设置", icon: Settings2 },
  { to: "/admin/permissions", label: "接口权限", icon: KeySquare },
  { to: "/admin/qb", label: "qB 服务器", icon: Server },
  { to: "/admin/sources", label: "抓取源", icon: Rss },
  { to: "/admin/users", label: "用户", icon: Users },
  { to: "/admin/codes", label: "邀请码 / 兑换码", icon: Ticket },
  { to: "/admin/rules", label: "策略规则", icon: ShieldCheck },
  { to: "/admin/reviews", label: "审核队列", icon: ClipboardCheck },
  { to: "/admin/plays", label: "播放记录", icon: Activity },
  { to: "/admin/audit", label: "审计日志", icon: History },
]

function NavList({ onNavigate }: { onNavigate?: () => void }) {
  return (
    <nav aria-label="后台导航" className="grid gap-0.5 p-2">
      {NAV.map((n) => (
        <NavLink
          key={n.to}
          to={n.to}
          end={n.end}
          onClick={onNavigate}
          className={({ isActive }) =>
            cn(
              "flex h-10 items-center gap-2.5 rounded-md px-3 text-sm transition-colors",
              isActive ? "bg-primary-soft font-medium text-primary-text" : "text-foreground/75 hover:bg-muted hover:text-foreground",
            )
          }
        >
          <n.icon className="h-4 w-4 shrink-0" />
          {n.label}
        </NavLink>
      ))}
    </nav>
  )
}

export default function AdminApp() {
  useDocumentTitle("后台管理")
  const { user, loading } = useUser()
  const [open, setOpen] = useState(false)
  const { pathname } = useLocation()
  if (loading) return <PageLoading />
  if (!user) return <Navigate replace to={`/login?next=${encodeURIComponent(pathname)}`} />
  if (user.role !== "admin") return <StatusPage code="403" title="没有访问权限" description="后台管理仅限管理员访问。" />

  const current = NAV.filter((n) => (n.end ? pathname === n.to || pathname === `${n.to}/` : pathname.startsWith(n.to))).pop()

  return (
    <div className="mx-auto flex w-full max-w-[1440px]">
      <aside className="sticky top-14 hidden h-[calc(100dvh-3.5rem)] w-56 shrink-0 overflow-y-auto border-r lg:block">
        <div className="flex items-center gap-2 px-5 pb-1 pt-5 text-xs font-semibold uppercase tracking-wider text-muted-foreground">
          <ScrollText className="h-3.5 w-3.5" />
          后台管理
        </div>
        <NavList />
      </aside>
      <div className="min-w-0 flex-1">
        {/* 移动端：顶部二级栏 + 抽屉 */}
        <div className="sticky top-14 z-30 flex h-12 items-center gap-2 border-b bg-header px-2 backdrop-blur-md lg:hidden">
          <Dialog open={open} onOpenChange={setOpen}>
            <DialogTrigger asChild>
              <Button variant="ghost" size="icon" aria-label="打开后台菜单">
                <Menu className="!size-5" />
              </Button>
            </DialogTrigger>
            <SheetContent title="后台菜单">
              <div className="flex h-14 items-center border-b px-5 text-sm font-semibold">后台管理</div>
              <div className="flex-1 overflow-y-auto">
                <NavList onNavigate={() => setOpen(false)} />
              </div>
            </SheetContent>
          </Dialog>
          <span className="text-sm font-medium">{current?.label ?? "后台管理"}</span>
        </div>
        <div className="px-3 py-4 pb-[calc(1.5rem+env(safe-area-inset-bottom))] sm:px-5 sm:py-6 lg:px-8">
          <Routes>
            <Route index element={<Overview />} />
            <Route path="settings" element={<SettingsPage />} />
            <Route path="permissions" element={<PermissionsPage />} />
            <Route path="qb" element={<QBServers />} />
            <Route path="sources" element={<SourcesPage />} />
            <Route path="users" element={<UsersPage />} />
            <Route path="codes" element={<Codes />} />
            <Route path="rules" element={<Rules />} />
            <Route path="reviews" element={<Reviews />} />
            <Route path="plays" element={<Plays />} />
            <Route path="audit" element={<Audit />} />
            <Route path="*" element={<StatusPage code="404" title="页面不存在" description="后台没有这个页面。" />} />
          </Routes>
        </div>
      </div>
    </div>
  )
}
