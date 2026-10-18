import { Link, useLocation } from "react-router-dom"
import { ThemeCycleButton } from "./ThemeToggle"
import { Search } from "lucide-react"

/**
 * 移动端专用顶栏（桌面端用 Header）：
 * - 首页：搜索框样式的入口，点击进入 /search
 * - 我的：标题 + 右侧主题按钮；登录 / 注册：只有右侧主题按钮（日 / 月 / 跟随系统 循环）
 * - 其它页面不显示（播放页播放器直接顶到最上方）
 */
export function mobileTopBarVisible(pathname: string) {
  return pathname === "/" || pathname === "/me" || pathname === "/login" || pathname.startsWith("/register")
}

export function MobileTopBar() {
  const { pathname } = useLocation()
  const isHome = pathname === "/"
  if (!mobileTopBarVisible(pathname)) return null

  return (
    <div className="bg-background pt-safe md:hidden">
      <div className={isHome ? "flex items-center px-3 pt-3" : "flex h-12 items-center justify-end px-2"}>
        {isHome ? (
          // 外观同搜索框，点击进入搜索页（搜索页会自动聚焦输入框）
          <Link
            to="/search"
            className="flex h-9 min-w-0 flex-1 items-center justify-between rounded-lg border bg-muted/60 pl-3 pr-3 text-sm text-muted-foreground"
          >
            <span className="truncate">搜索种子或粘贴磁力链接</span>
            <Search className="h-4 w-4 shrink-0" />
          </Link>
        ) : (
          <>
            {/* 我的 / 登录 / 注册：不显示标题，只保留右侧主题按钮 */}
            <ThemeCycleButton />
          </>
        )}
      </div>
    </div>
  )
}
