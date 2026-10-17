import { Link, useLocation, useNavigate } from "react-router-dom"
import { LogIn, LogOut, Shield, User as UserIcon } from "lucide-react"
import { Logo } from "./Logo"
import { ThemeToggle } from "./ThemeToggle"
import { Container } from "./Container"
import { TorrentSearchBox } from "@/components/TorrentSearchBox"
import { Button } from "@/components/ui/button"
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


export function Header() {
  // 搜索页自带大搜索框，顶栏不再重复显示（保留占位以免右侧按钮跳动）
  const onSearch = useLocation().pathname === "/search"
  return (
    <header className="sticky top-0 z-40 border-b bg-header pt-safe backdrop-blur-md supports-[backdrop-filter]:bg-header">
      <Container className="flex h-14 items-center gap-3 lg:gap-5">
        <Logo />
        <div className="mx-auto hidden min-w-0 flex-1 md:block md:max-w-md lg:max-w-xl">
          {!onSearch && <TorrentSearchBox size="sm" />}
        </div>
        <div className="ml-auto flex shrink-0 items-center gap-0.5 md:ml-0">
          <ThemeToggle />
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
  if (loading) return <div className="ml-1 h-8 w-8 animate-pulse rounded-full bg-muted" aria-hidden />
  if (!user) {
    return (
      <Button asChild size="sm" className="ml-1 hidden sm:inline-flex">
        <Link to="/login">
          <LogIn />
          登录
        </Link>
      </Button>
    )
  }
  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <button
          type="button"
          aria-label={`用户菜单：${user.username}`}
          className="ml-1 inline-flex h-10 w-10 items-center justify-center rounded-full"
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
        <DropdownMenuItem onSelect={() => logout.mutate()}>
          <LogOut />
          退出登录
        </DropdownMenuItem>
      </DropdownMenuContent>
    </DropdownMenu>
  )
}
