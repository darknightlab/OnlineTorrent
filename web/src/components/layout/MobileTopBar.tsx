import { Link, useLocation } from "react-router-dom"
import { LogoMark } from "./Logo"
import { ThemeCycleButton } from "./ThemeToggle"
import { TorrentSearchBox } from "@/components/TorrentSearchBox"

/**
 * 移动端专用顶栏（桌面端用 Header）：
 * - 首页：Logo + 搜索框（磁力/infohash 直接播放，普通关键词进入搜索）
 * - 我的 / 登录 / 注册：标题 + 右侧主题按钮（日 / 月 / 跟随系统 循环）
 * - 其它页面不显示（播放页播放器直接顶到最上方）
 */
export function mobileTopBarVisible(pathname: string) {
  return pathname === "/" || pathname === "/me" || pathname === "/login" || pathname.startsWith("/register")
}

export function MobileTopBar() {
  const { pathname } = useLocation()
  const isHome = pathname === "/"
  const title = pathname === "/me" ? "我的" : pathname === "/login" ? "登录" : pathname.startsWith("/register") ? "注册" : null
  if (!isHome && !title) return null
  return (
    <div className="sticky top-0 z-40 bg-header pt-safe backdrop-blur-md md:hidden">
      <div className="flex h-12 items-center gap-2.5 px-3">
        {isHome ? (
          <>
            <Link to="/" aria-label="Project Kawaii 首页" className="shrink-0">
              <LogoMark className="h-8 w-8" />
            </Link>
            <TorrentSearchBox size="sm" className="min-w-0 flex-1" />
            {/* 与左侧 Logo 等宽的留白，使搜索框居中对称 */}
            <span aria-hidden className="h-8 w-8 shrink-0" />
          </>
        ) : (
          <>
            <h1 className="flex-1 text-[17px] font-semibold">{title}</h1>
            <ThemeCycleButton />
          </>
        )}
      </div>
    </div>
  )
}
