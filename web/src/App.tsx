import { lazy, Suspense } from "react"
import { BrowserRouter, Route, Routes } from "react-router-dom"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { ThemeProvider } from "@/lib/theme"
import { isApiError } from "@/lib/api"
import { Shell } from "@/components/layout/Shell"
import { Toaster } from "@/components/ui/toast"
import { PageLoading } from "@/components/states"
import Home from "@/pages/Home"
import SubjectPage, { LegacyAnimeRedirect } from "@/pages/Subject"
import SearchPage, { LegacySearchPage } from "@/pages/Search"
import { LoginPage, RegisterPage, RegisterRoute } from "@/pages/Auth"
import MePage from "@/pages/Me"
import NotFound from "@/pages/NotFound"

// 播放页 / 单集页（webtorrent）与后台按需加载
const PlaybackPage = lazy(() => import("@/pages/watch/Playback"))
const LegacyWatchPage = lazy(() => import("@/pages/watch/Watch").then((m) => ({ default: m.LegacyWatchPage })))

const playerFallback = <PageLoading label="正在加载播放器…" />
/**
 * 播放页与单集页的所有路由共用同一个元素：React Router 不给路由元素加 key，同类型元素在这些路由之间导航时
 * 不会重新挂载，因此切换（如播放页 → 单集页、单集页切换文件 / 资源）时可以保留同一个播放器与 WebTorrent 会话。
 */
const playback = (
  <Suspense fallback={playerFallback}>
    <PlaybackPage />
  </Suspense>
)
const PLAYBACK_PATHS = [
  "subject/:provider/:id/episode/:key",
  "subject/:provider/:id/episode/:key/hash/:ih",
  "subject/:provider/:id/episode/:key/hash/:ih/file/:index",
  "watch/hash/:ih",
  "watch/hash/:ih/file/:index",
]
const AdminApp = lazy(() => import("@/pages/admin/AdminApp"))

const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      refetchOnWindowFocus: false,
      retry: (count, err) => (isApiError(err) && err.status > 0 && err.status < 500 ? false : count < 2),
    },
  },
})

export default function App() {
  return (
    <ThemeProvider>
      <QueryClientProvider client={queryClient}>
        <BrowserRouter>
          <Routes>
            <Route element={<Shell />}>
              <Route index element={<Home />} />
              <Route path="subject/:provider/:id" element={<SubjectPage />} />
              {PLAYBACK_PATHS.map((p) => (
                <Route key={p} path={p} element={playback} />
              ))}
              <Route path="search" element={<LegacySearchPage />} />
              <Route path="search/:keyword" element={<SearchPage />} />
              {/* 旧链接：/watch?uri=|ih=|t=，无参数为粘贴磁力页 */}
              <Route
                path="watch"
                element={
                  <Suspense fallback={playerFallback}>
                    <LegacyWatchPage />
                  </Suspense>
                }
              />
              <Route path="anime/:key" element={<LegacyAnimeRedirect />} />
              <Route path="login" element={<LoginPage />} />
              <Route path="register" element={<RegisterRoute />} />
              <Route path="register/invite/:code" element={<RegisterPage />} />
              <Route path="me" element={<MePage />} />
              <Route
                path="admin/*"
                element={
                  <Suspense fallback={<PageLoading label="正在加载后台…" />}>
                    <AdminApp />
                  </Suspense>
                }
              />
              <Route path="*" element={<NotFound />} />
            </Route>
          </Routes>
        </BrowserRouter>
        <Toaster />
      </QueryClientProvider>
    </ThemeProvider>
  )
}
