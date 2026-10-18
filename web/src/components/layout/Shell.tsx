import { useEffect, useRef } from "react"
import { Outlet, useLocation } from "react-router-dom"
import { Header } from "./Header"
import { MobileTabBar } from "./MobileTabBar"
import { Footer } from "./Footer"
import { cn } from "@/lib/utils"
export { Container } from "./Container"

export function Shell() {
  const { pathname } = useLocation()
  const isAdmin = pathname.startsWith("/admin")
  // 播放页 / 单集页不显示页脚
  const isWatch = pathname.startsWith("/watch") || /^\/subject\/[^/]+\/[^/]+\/episode\//.test(pathname)
  const prev = useRef(pathname)

  useEffect(() => {
    // 同一种子内切换文件（/watch/hash/{ih}[/file/{i}]）不回到顶部
    const same = (p: string) => p.match(/^\/watch\/hash\/([^/]+)/)?.[1]
    const a = same(prev.current)
    prev.current = pathname
    if (a && a === same(pathname)) return
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
      <main
        id="main"
        className={cn(
          "flex-1",
          // 移动端没有顶栏：避开刘海/状态栏
          !isAdmin && "pt-safe md:pt-0",
          // 为移动端底部 Tab 栏留出空间
          !isAdmin && "pb-[calc(3.75rem+env(safe-area-inset-bottom))] md:pb-0",
        )}
      >
        <Outlet />
      </main>
      {!isAdmin && !isWatch && <Footer />}
      {!isAdmin && <MobileTabBar />}
    </div>
  )
}
