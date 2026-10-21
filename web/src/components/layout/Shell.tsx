import { useEffect, useRef } from "react"
import { Outlet, useLocation } from "react-router-dom"
import { Header } from "./Header"
import { MobileTabBar } from "./MobileTabBar"
import { MobileTopBar, mobileTopBarVisible } from "./MobileTopBar"
import { Footer } from "./Footer"
import { cn, infohashOfPath } from "@/lib/utils"
import { isAdminPath, useIsAdmin } from "@/lib/queries"
export { Container } from "./Container"

export function Shell() {
  const { pathname } = useLocation()
  // 管理区布局只对管理员生效（其他人在 /admin 看到的是普通 404 页）
  const isAdmin = useIsAdmin() && isAdminPath(pathname)
  // 播放页 / 单集页不显示页脚
  const isWatch = pathname.startsWith("/watch") || /^\/subject\/[^/]+\/[^/]+\/episode\//.test(pathname)
  const prev = useRef(pathname)

  useEffect(() => {
    // 同一种子内切换文件、播放页切到单集页（同一 infohash，播放器不重建）不回到顶部
    const a = infohashOfPath(prev.current)
    prev.current = pathname
    if (a && a === infohashOfPath(pathname)) return
    window.scrollTo({ top: 0 })
  }, [pathname])

  return (
    <div className="flex min-h-dvh flex-col">
      <a
        href="#main"
        className="sr-only focus:not-sr-only focus:fixed focus:left-2 focus:top-2 focus:z-[70] focus:rounded-md focus:bg-primary focus:px-3 focus:py-2 focus:text-primary-foreground"
      >
        跳到主要内容
      </a>
      <Header />
      {!isAdmin && <MobileTopBar />}
      <main
        id="main"
        className={cn(
          "flex-1",
          // 移动端没有顶栏：避开刘海/状态栏
          !isAdmin && !mobileTopBarVisible(pathname) && "pt-safe sm:pt-0",
          // 为移动端底部 Tab 栏留出空间
          !isAdmin && "pb-[calc(3.25rem+env(safe-area-inset-bottom))] sm:pb-0",
        )}
      >
        <Outlet />
      </main>
      {!isAdmin && !isWatch && <Footer />}
      {!isAdmin && <MobileTabBar />}
    </div>
  )
}
