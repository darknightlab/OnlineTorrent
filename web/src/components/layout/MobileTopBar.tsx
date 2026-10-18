import { useLocation } from "react-router-dom"
import { ThemeCycleButton } from "./ThemeToggle"
import { TorrentSearchBox } from "@/components/TorrentSearchBox"

/**
 * 移动端专用顶栏（桌面端用 Header）：
 * - 首页：搜索框（磁力/infohash 直接播放，普通关键词进入搜索）
 * - 我的：标题 + 右侧主题按钮；登录 / 注册：只有右侧主题按钮（日 / 月 / 跟随系统 循环）
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
    <div className="bg-background pt-safe md:hidden">
      <div className="flex h-12 items-center gap-2.5 px-3">
        {isHome ? (
          <TorrentSearchBox size="sm" className="min-w-0 flex-1" />
        ) : (
          <>
            {/* 登录 / 注册页不显示标题，只保留右侧主题按钮 */}
            <h1 className="flex-1 text-[17px] font-semibold">{title === "我的" ? title : null}</h1>
            <ThemeCycleButton />
          </>
        )}
      </div>
    </div>
  )
}
