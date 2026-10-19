// 打开种子的状态机（纯函数，便于测试；usePlayResolve 使用）。
//
// POST /torrents/hash/{ih}/play：
//   - status "ready"：已有元数据，按真实标题 / 文件判定；allow 时带磁力（及已映射的番剧集数 meta）
//   - status "pending"：新种子，服务器正在获取元数据（不给磁力：判定前浏览器不能开始 P2P）
//     → 订阅 GET /torrents/hash/{ih}/resolve/events：
//       redirect {path}          有文件已映射到某番某集 → 跳到单集页
//       ready {}                 未映射且允许播放 → 重新请求 play 拿磁力
//       denied {action, reason}  不允许 → 无权播放页（之后再访问由 play 直接返回拒绝）
//       timeout {}               60 秒内没拿到元数据 → 「暂时无法获取种子信息」+ 重试

export type GateAction = "login" | "vip" | "block"

export type ResolveStep =
  /** 显示播放器 */
  | { kind: "play" }
  /** 等待服务器获取种子信息（打开 SSE） */
  | { kind: "pending" }
  /** 跳转到单集页（并重新请求 play，拿到磁力后在新页面就地播放） */
  | { kind: "redirect"; path: string }
  /** 重新请求 play */
  | { kind: "replay" }
  /** 无权播放 */
  | { kind: "gate"; action: GateAction; message: string }
  /** 超时，可重试 */
  | { kind: "timeout" }

export interface PlayLike {
  status?: string
  access: { action: string; reason: string }
}

/** play 响应 → 下一步 */
export function stepOfPlay(p: PlayLike): ResolveStep {
  if (p.status === "pending") return { kind: "pending" }
  if (p.access.action !== "allow") return gateStep(p.access.action, p.access.reason)
  return { kind: "play" }
}

/** resolve/events 的一个事件 → 下一步；未知事件返回 null（忽略） */
export function stepOfEvent(type: string, data: unknown): ResolveStep | null {
  const d = (data && typeof data === "object" ? data : {}) as Record<string, unknown>
  switch (type) {
    case "redirect":
      return typeof d.path === "string" && d.path.startsWith("/") ? { kind: "redirect", path: d.path } : { kind: "replay" }
    case "ready":
      return { kind: "replay" }
    case "denied":
      return gateStep(String(d.action ?? "block"), String(d.reason ?? ""))
    case "timeout":
      return { kind: "timeout" }
  }
  return null
}

export function gateStep(action: string, reason: string): ResolveStep {
  if (action === "login" || action === "vip") return { kind: "gate", action, message: reason }
  return { kind: "gate", action: "block", message: reason }
}

/**
 * 播放页（/watch/hash/{ih}）拿到已映射的结果时应跳到的单集页；
 * fileIndex：URL 指定的文件（与映射文件不同时不跳转，避免换文件）。
 */
export function episodeRedirect(
  meta: { subject_id: number; bgm_id?: string | number | null; episode_key: string; file_index: number } | null | undefined,
  ih: string,
  fileIndex: number | null,
): string | null {
  if (!meta || !meta.episode_key) return null
  if (fileIndex !== null && fileIndex !== meta.file_index) return null
  const bgm = meta.bgm_id && Number(meta.bgm_id) > 0 ? String(meta.bgm_id) : ""
  const [provider, id] = bgm ? ["bgm", bgm] : ["id", String(meta.subject_id)]
  return `/subject/${provider}/${encodeURIComponent(id)}/episode/${encodeURIComponent(meta.episode_key)}/hash/${encodeURIComponent(ih)}/file/${meta.file_index}`
}
