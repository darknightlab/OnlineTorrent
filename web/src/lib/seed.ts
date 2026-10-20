// 服务器做种请求：qB 只下载正在播放的文件（POST /torrents/hash/{ih}/seed {file}）。
// 每个播放会话记下已请求过的文件：切到同一种子的另一个文件时再请求一次，同一文件不重复请求；
// 请求失败（网络错误等）的文件下次切回时重试；服务器明确拒绝（权限 / 超限）的不再重试。

export type SeedOutcome = "seeded" | "no_server" | "retry" | "refused"

export class SeedRequests {
  private done = new Set<number>()
  private inflight = new Set<number>()

  /** 是否应为该文件发起请求（会标记为进行中） */
  begin(file: number): boolean {
    if (file < 0 || this.done.has(file) || this.inflight.has(file)) return false
    this.inflight.add(file)
    return true
  }

  /** 请求结束：retry 的文件之后可再次请求，其余记为已处理 */
  end(file: number, outcome: SeedOutcome) {
    this.inflight.delete(file)
    if (outcome !== "retry") this.done.add(file)
  }

  /** HTTP 状态 → 是否值得以后重试（4xx 是服务器的明确答复，不重试；429 / 5xx / 网络错误重试） */
  static outcomeOfError(status: number | undefined): SeedOutcome {
    if (status === undefined || status === 429 || status >= 500) return "retry"
    return "refused"
  }
}
