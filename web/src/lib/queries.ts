import { keepPreviousData, useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { api } from "./api"
import type { MeResponse, Site } from "./types"

export const qk = {
  site: ["site"] as const,
  me: ["me"] as const,
  schedule: ["schedule"] as const,
  anime: (key: string) => ["anime", key] as const,
  latest: (page: number) => ["torrents", "latest", page] as const,
  search: (q: string, page: number) => ["torrents", "search", q, page] as const,
}

/** 后端未就绪时的兜底站点配置（保留旧站 tracker / STUN） */
export const FALLBACK_SITE: Site = {
  name: "Project Kawaii",
  announcement_md: "",
  registration: "open",
  turnstile_site_key: "",
  invite_required: false,
  trackers: [
    "wss://tracker.projectk.org",
    "wss://tracker.dnlab.net:16443",
    "wss://tracker.btorrent.xyz",
    "wss://tracker.openwebtorrent.com",
  ],
  ice_servers: [
    { urls: ["stun:stun.miwifi.com:3478", "stun:stun.l.google.com:19302", "stun:global.stun.twilio.com:3478"] },
  ],
  max_auto_size_bytes: 2 * 1024 ** 3,
}

export function useSite() {
  return useQuery({ queryKey: qk.site, queryFn: ({ signal }) => api.site(signal), staleTime: 5 * 60_000 })
}

/** 站点配置，失败时返回兜底值 */
export function useSiteOrFallback(): Site {
  const { data } = useSite()
  return data ?? FALLBACK_SITE
}

export function useMe() {
  return useQuery({ queryKey: qk.me, queryFn: ({ signal }) => api.me(signal), staleTime: 60_000, retry: 1 })
}

export function useUser() {
  const q = useMe()
  return { user: q.data?.user ?? null, loading: q.isPending, error: q.error }
}

/** 常用接口权限键（与后端路由注册的 "METHOD /pattern" 一致） */
export const PERM = {
  schedule: "GET /api/v1/schedule",
  anime: "GET /api/v1/anime/{key}",
  latest: "GET /api/v1/torrents/latest",
  search: "GET /api/v1/torrents/search",
  play: "POST /api/v1/play",
  seed: "POST /api/v1/torrents/{ih}/seed",
  webseed: "POST /api/v1/torrents/{ih}/webseed",
  report: "POST /api/v1/report",
  password: "POST /api/v1/me/password",
  redeem: "POST /api/v1/me/redeem",
  metaRead: "GET /meta/v1/",
} as const

/** 当前身份是否可调用某接口；管理员放行所有接口 */
export function can(me: MeResponse | null | undefined, key: string): boolean {
  if (!me) return false
  if (me.user?.role === "admin") return true
  return (me.permissions ?? []).includes(key)
}

/** 权限判定 hook：/me 未加载完成时返回 false */
export function useCan(key: string): boolean {
  const { data } = useMe()
  return can(data, key)
}

export function useSetMe() {
  const qc = useQueryClient()
  return (me: MeResponse) => qc.setQueryData(qk.me, me)
}

export function useLogout() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: api.logout,
    onSettled: () => {
      qc.setQueryData(qk.me, { user: null, permissions: [] } satisfies MeResponse)
      void qc.invalidateQueries()
    },
  })
}

export function useSchedule() {
  return useQuery({ queryKey: qk.schedule, queryFn: ({ signal }) => api.schedule(signal), staleTime: 5 * 60_000 })
}

export function useAnime(key: string) {
  return useQuery({ queryKey: qk.anime(key), queryFn: ({ signal }) => api.anime(key, signal), enabled: !!key })
}

export function useLatest(page: number) {
  return useQuery({
    queryKey: qk.latest(page),
    queryFn: ({ signal }) => api.latest(page, signal),
    placeholderData: keepPreviousData,
    staleTime: 60_000,
  })
}

export function useSearch(q: string, page: number) {
  return useQuery({
    queryKey: qk.search(q, page),
    queryFn: ({ signal }) => api.search(q, page, signal),
    enabled: q.trim().length > 0,
    placeholderData: keepPreviousData,
  })
}
