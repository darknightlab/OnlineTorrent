import { useLocation, useNavigate } from "react-router-dom"
import { LogIn, LogOut, Shield, User as UserIcon, UserPlus } from "lucide-react"
import { Logo } from "./Logo"
import { ThemeMenuItems } from "./ThemeToggle"
import { Container } from "./Container"
import { TorrentSearchBox } from "@/components/TorrentSearchBox"
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu"
import { useLogout, useUser } from "@/lib/queries"
import { RoleBadge } from "@/components/badges"
import { cn } from "@/lib/utils"


export function Header() {
  const { pathname } = useLocation()
  // 搜索页自带大搜索框，顶栏不再重复显示
  const onSearch = /^\/search(\/|$)/.test(pathname)
  // 移动端不显示顶栏（用底部导航）；后台页面除外（后台的移动端菜单依赖顶栏）
  const isAdmin = pathname.startsWith("/admin")
  return (
    <header
      className={cn(
        "sticky top-0 z-40 border-b bg-header pt-safe backdrop-blur-md supports-[backdrop-filter]:bg-header",
        !isAdmin && "hidden sm:block",
      )}
    >
      <Container className="flex h-14 items-center gap-3">
        <Logo />
        <div className="ml-auto flex min-w-0 items-center gap-2">
          {!onSearch && <TorrentSearchBox size="sm" className="hidden w-56 sm:block lg:w-72" />}
          <UserMenu />
        </div>
      </Container>
    </header>
  )
}

function UserMenu() {
  const { user, loading } = useUser()
  const logout = useLogout()
  const navigate = useNavigate()
  // 三种状态（加载中 / 游客 / 已登录）占位一致：40×40，避免加载完成后顶栏位移
  if (loading)
    return (
      <span className="inline-flex h-10 w-10 shrink-0 items-center justify-center" aria-hidden>
        <span className="h-8 w-8 animate-pulse rounded-full bg-muted" />
      </span>
    )
  if (!user) {
    return (
      <DropdownMenu>
        <DropdownMenuTrigger asChild>
          <button
            type="button"
            aria-label="账户与设置"
            className="inline-flex h-10 w-10 shrink-0 items-center justify-center rounded-full text-muted-foreground hover:bg-muted hover:text-foreground"
          >
            <UserIcon className="h-5 w-5" />
          </button>
        </DropdownMenuTrigger>
        <DropdownMenuContent align="end" className="w-56">
          <DropdownMenuItem onSelect={() => navigate("/login")}>
            <LogIn />
            登录
          </DropdownMenuItem>
          <DropdownMenuItem onSelect={() => navigate("/register")}>
            <UserPlus />
            注册
          </DropdownMenuItem>
          <DropdownMenuSeparator />
          <ThemeMenuItems />
        </DropdownMenuContent>
      </DropdownMenu>
    )
  }
  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <button
          type="button"
          aria-label={`用户菜单：${user.username}`}
          className="inline-flex h-10 w-10 shrink-0 items-center justify-center rounded-full"
        >
          <span className="inline-flex h-8 w-8 items-center justify-center rounded-full bg-primary text-sm font-semibold uppercase text-primary-foreground ring-2 ring-primary-soft">
            {user.username.slice(0, 1)}
          </span>
        </button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end" className="w-56">
        <DropdownMenuLabel className="flex items-center gap-2">
          <span className="truncate text-sm font-medium text-foreground">{user.username}</span>
          <RoleBadge role={user.role} />
        </DropdownMenuLabel>
        <DropdownMenuSeparator />
        <DropdownMenuItem onSelect={() => navigate("/me")}>
          <UserIcon />
          个人中心
        </DropdownMenuItem>
        {user.role === "admin" && (
          <DropdownMenuItem onSelect={() => navigate("/admin")}>
            <Shield />
            后台管理
          </DropdownMenuItem>
        )}
        <DropdownMenuSeparator />
        <ThemeMenuItems />
        <DropdownMenuSeparator />
        <DropdownMenuItem onSelect={() => logout.mutate()}>
          <LogOut />
          退出登录
        </DropdownMenuItem>
      </DropdownMenuContent>
    </DropdownMenu>
  )
}
