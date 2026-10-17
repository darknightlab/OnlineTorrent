import type { Engine } from "./types"

/** mock 模式下默认使用模拟引擎（加 ?real=1 可强制真实 webtorrent） */
export async function loadEngine(): Promise<Engine> {
  if (import.meta.env.VITE_MOCK === "1" && !new URLSearchParams(location.search).has("real")) {
    return (await import("../mocks/engine")).mockEngine
  }
  return (await import("./webtorrent-engine")).webtorrentEngine
}
