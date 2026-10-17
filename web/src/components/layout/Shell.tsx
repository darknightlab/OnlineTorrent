import { useEffect } from "react"
import { Outlet, useLocation } from "react-router-dom"
import { Header } from "./Header"
import { MobileTabBar } from "./MobileTabBar"
import { Footer } from "./Footer"
import { cn } from "@/lib/utils"
export { Container } from "./Container"

export function Shell() {
  const { pathname } = useLocation()
  const isAdmin = pathname.startsWith("/admin")
  const isWatch = pathname.startsWith("/watch")

  useEffect(() => {
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
