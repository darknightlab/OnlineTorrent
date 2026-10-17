import { lazy, Suspense } from "react"
import { BrowserRouter, Route, Routes } from "react-router-dom"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { ThemeProvider } from "@/lib/theme"
import { isApiError } from "@/lib/api"
import { Shell } from "@/components/layout/Shell"
import { Toaster } from "@/components/ui/toast"
import { PageLoading } from "@/components/states"
import Home from "@/pages/Home"
import AnimePage from "@/pages/Anime"
import SearchPage from "@/pages/Search"
import { LoginPage, RegisterPage } from "@/pages/Auth"
import MePage from "@/pages/Me"
import NotFound from "@/pages/NotFound"

// 播放页（webtorrent）与后台按需加载
const WatchPage = lazy(() => import("@/pages/watch/Watch"))
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
              <Route path="anime/:key" element={<AnimePage />} />
              <Route path="search" element={<SearchPage />} />
              <Route
                path="watch"
                element={
                  <Suspense fallback={<PageLoading label="正在加载播放器…" />}>
                    <WatchPage />
                  </Suspense>
                }
              />
              <Route path="login" element={<LoginPage />} />
              <Route path="register" element={<RegisterPage />} />
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
