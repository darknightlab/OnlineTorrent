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
  if (!mobileTopBarVisible(pathname)) return null

  return (
    <div className="bg-background pt-safe md:hidden">
      <div className={isHome ? "flex items-center px-3 pt-3" : "flex h-12 items-center justify-end px-2"}>
        {isHome ? (
          <TorrentSearchBox size="sm" className="min-w-0 flex-1" />
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
