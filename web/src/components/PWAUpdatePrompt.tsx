import { useRegisterSW } from "virtual:pwa-register/react"
import { RefreshCw } from "lucide-react"
import { Button } from "@/components/ui/button"

/**
 * 注册站点的 Service Worker（src/sw/sw.js：WebTorrent 流 + 应用壳）并提示更新。
 * 新版本安装后在后台等待（旧版本继续服务，正在播放的流不受影响），用户点「刷新」才接管并重新加载；
 * 每小时检查一次更新。位置同 Toaster：手机在底部 Tab 栏之上，宽屏在右下角。
 */
export function PWAUpdatePrompt() {
  const {
    needRefresh: [needRefresh, setNeedRefresh],
    updateServiceWorker,
  } = useRegisterSW({
    immediate: true, // 尽早接管：第一次打开就播放时不必等 load
    onRegisteredSW: (_url, registration) => {
      if (registration) window.setInterval(() => void registration.update().catch(() => {}), 60 * 60 * 1000)
    },
  })
  const refresh = () => {
    // 浏览器要等旧 Service Worker 手上的请求都结束才让新版本接管，而正在播放的视频是一条一直开着的
    // /webtorrent/ 流：先停掉页面里的媒体（流随之取消），新版本接管后自动重新加载
    for (const m of document.querySelectorAll<HTMLMediaElement>("video, audio")) {
      m.pause()
      m.removeAttribute("src")
      m.load()
    }
    void updateServiceWorker(true)
    // 万一还有别的请求拖住：过一会儿直接重新加载（新版本在旧页面关闭后接管）
    window.setTimeout(() => window.location.reload(), 4000)
  }
  if (!needRefresh) return null
  return (
    <div className="pointer-events-none fixed inset-x-0 bottom-[calc(4rem+env(safe-area-inset-bottom))] z-[60] flex justify-center px-4 sm:bottom-6 sm:left-auto sm:right-6">
      <div role="status" aria-live="polite" className="pointer-events-auto flex w-full max-w-sm items-center gap-2.5 rounded-lg border bg-popover py-2 pl-3.5 pr-2 text-sm text-popover-foreground">
        <RefreshCw className="h-4 w-4 shrink-0 text-primary-text" />
        <span className="min-w-0 flex-1 font-medium">新版本已准备好</span>
        <Button variant="ghost" size="xs" onClick={() => setNeedRefresh(false)}>
          稍后
        </Button>
        <Button size="xs" onClick={refresh}>
          刷新
        </Button>
      </div>
    </div>
  )
}
