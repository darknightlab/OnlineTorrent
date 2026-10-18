import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { api } from "./api"
import type { MeResponse, Site } from "./types"

export const qk = {
  site: ["site"] as const,
  me: ["me"] as const,
  schedule: ["schedule"] as const,
  subject: (provider: string, id: string) => ["subject", provider, id] as const,
  episode: (provider: string, id: string, n: number) => ["subject", provider, id, "episode", n] as const,
  search: (q: string, page: number) => ["search", q, page] as const,
  torrentStatus: (ih: string) => ["torrents", "hash", ih, "status"] as const,
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
  subject: "GET /api/v1/subjects/{provider}/{id}",
  episode: "GET /api/v1/subjects/{provider}/{id}/episodes/{n}",
  search: "GET /api/v1/search/{keyword}",
  torrentStatus: "GET /api/v1/torrents/hash/{ih}",
  playHash: "POST /api/v1/torrents/hash/{ih}/play",
  playMoe: "POST /api/v1/torrents/moe/{id}/play",
  seed: "POST /api/v1/torrents/hash/{ih}/seed",
  webseed: "GET /webseed/hash/{ih}/{path...}",
  report: "POST /api/v1/torrents/hash/{ih}/report",
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
  return (me: MeResponse) => {
    qc.setQueryData(qk.me, me)
    // 身份变化后 access 等用户相关字段需要重新获取
    void qc.invalidateQueries({ predicate: (q) => q.queryKey[0] !== "me" && q.queryKey[0] !== "site" })
  }
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

/** 条目详情；provider/id 为空时不请求 */
export function useSubject(provider: string, id: string) {
  return useQuery({
    queryKey: qk.subject(provider, id),
    queryFn: ({ signal }) => api.subject(provider, id, signal),
    enabled: !!provider && !!id,
  })
}

/** 单集文件列表（含当前用户 access） */
export function useEpisode(provider: string, id: string, n: number) {
  return useQuery({
    queryKey: qk.episode(provider, id, n),
    queryFn: ({ signal }) => api.episode(provider, id, n, signal),
    enabled: !!provider && !!id && Number.isFinite(n) && n >= 0,
  })
}

export function useSearch(q: string, page: number) {
  return useQuery({
    queryKey: qk.search(q, page),
    queryFn: ({ signal }) => api.search(q, page, signal),
    enabled: q.trim().length > 0,
    // 翻页时保留上一页；换关键词时显示骨架
    placeholderData: (prev, prevQuery) => (prevQuery?.queryKey[1] === q ? prev : undefined),
    staleTime: 30_000,
  })
}
