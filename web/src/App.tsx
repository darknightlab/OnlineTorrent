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
const WatchPage = lazy(() => import("@/pages/watch/Watch"))
const LegacyWatchPage = lazy(() => import("@/pages/watch/Watch").then((m) => ({ default: m.LegacyWatchPage })))
const EpisodePage = lazy(() => import("@/pages/Episode"))

const playerFallback = <PageLoading label="正在加载播放器…" />
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
              <Route
                path="subject/:provider/:id/episode/:n"
                element={
                  <Suspense fallback={playerFallback}>
                    <EpisodePage />
                  </Suspense>
                }
              />
              <Route path="search" element={<LegacySearchPage />} />
              <Route path="search/:keyword" element={<SearchPage />} />
              {/* /watch/hash/:ih 与 /watch/hash/:ih/file/:index 共用一个路由，切换文件不重建播放器 */}
              <Route
                path="watch/hash/:ih/*"
                element={
                  <Suspense fallback={playerFallback}>
                    <WatchPage />
                  </Suspense>
                }
              />
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
