// 起播（e2e/edge/run.mjs 的 EXTRA）：首帧时间、文件头尾 piece 的请求顺序。
// 视频是种子里的第一个文件（piece 0 起）；MKV 的 Cues / MP4 的 moov 在文件尾，浏览器起播前就要读，
// 所以尾部 piece 必须和头部一起最先请求。
// 起播闸门的规则（webtorrent-engine edgesPending）：首尾中有来源能提供的先取；没人有的不等；可播放就不等。
//   完整的边缘节点：PIECE_LENGTH=4194304 THROTTLE=1600000 START_FRACTION=1 MEDIA=<大 MKV> … → 尾部在前 3 个请求里
//   边缘节点还在下载（8 %）、尾部还没有：PARTIAL_TAIL=1 QB_RATE=4000000 MEDIA=<MKV，Cues 在末尾> … → 先取头部，不等尾部
//   MP4（moov 在开头）：MEDIA=/tmp/f/media/v.mp4 PARTIAL_TAIL=1 … → canplay 后放开
//   EXTRA=e2e/startup/checks.mjs node e2e/edge/run.mjs 40
export default async function ({ ev, consoleAll, edgeLog, t0, PL, videoLength }) {
  const r = {}
  const TTFF_MAX = Number(process.env.TTFF_MAX || 10_000)
  const tail = Math.floor((videoLength - 1) / PL)
  const firstPlay = await ev(`return window.__otFirstPlay ?? null`)
  const pieceOf = (range) => {
    const m = /bytes=(\d+)-(\d*)/.exec(range)
    return m ? Math.floor(Number(m[1]) / PL) : null
  }
  const reqs = edgeLog.map((e) => ({ piece: pieceOf(e.range), t: e.t - t0, done: e.done ? e.done - t0 : null })).filter((e) => e.piece !== null && e.piece <= tail)
  const order = []
  for (const q of reqs) if (!order.includes(q.piece)) order.push(q.piece)
  const at = (p, k) => {
    const xs = reqs.filter((q) => q.piece === p)
    if (!xs.length) return null
    return k === "req" ? Math.min(...xs.map((q) => q.t)) : Math.max(...xs.map((q) => q.done ?? Infinity))
  }
  const info = {
    ttff: firstPlay === null ? null : Math.round(firstPlay),
    firstEdgeRequest: reqs.length ? reqs[0].t : null,
    headRequested: at(0, "req"),
    headDone: at(0, "done"),
    tailRequested: at(tail, "req"),
    tailDone: at(tail, "done"),
    firstPieces: order.slice(0, 10).map((p) => (p === tail ? `TAIL(${p})` : p)),
    requestsBeforeFirstFrame: reqs.filter((q) => firstPlay !== null && q.t < firstPlay).length,
  }
  if (process.env.SHOW_LOG) console.log("LOG\n  " + consoleAll.filter((l) => /\[player\]|\[webtorrent\]|WebSeed|service worker|Service/i.test(l)).slice(0, 40).join("\n  "))
  const watchdog = consoleAll.filter((l) => /长时间无法读取视频/.test(l))
  console.log("startup:", JSON.stringify(info), watchdog.length ? `watchdog: ${watchdog.length}` : "")
  if (process.env.PARTIAL_TAIL) {
    // the tail isn't on any source yet: the head goes first, playback never waits for the tail
    r["head piece requested among the first 2 pieces (tail not available)"] = order.slice(0, 2).includes(0)
  } else r["tail piece requested among the first 3 pieces"] = order.slice(0, 3).includes(tail)
  r[`first frame within ${TTFF_MAX / 1000} s`] = info.ttff !== null && info.ttff <= TTFF_MAX
  r["no stall-watchdog reload"] = watchdog.length === 0
  return r
}
