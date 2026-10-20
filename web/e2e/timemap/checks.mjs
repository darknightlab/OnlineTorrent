// 进度条 piece 图层按时间画（e2e/edge/run.mjs 的 EXTRA）：拖到 SEEK（默认 95 %）播放一会儿后，
// 包含播放位置的已下载段应从拖动位置附近开始（±1 %），而不是按字节比例画在更早的位置。
//   TORRENT=<真实 .torrent> MEDIA=<其数据> START_FRACTION=1 EXTRA=e2e/timemap/checks.mjs node e2e/edge/run.mjs 20
export default async function ({ ev, sleep }) {
  const SEEK = Number(process.env.SEEK ?? 0.95)
  const r = {}
  const st = await ev(`const v = document.querySelector("video"); return { d: v?.duration, rs: v?.readyState }`)
  if (!(st?.d > 0)) return { "video has a duration": false }
  await ev(`const v = document.querySelector("video"); v.currentTime = v.duration * ${SEEK}; v.play().catch(() => {}); return 1`)
  await sleep(15_000)
  const m = await ev(`const v = document.querySelector("video"); v.pause()
    const el = document.querySelector(".ot-progress-pieces")
    return { t: v.currentTime / v.duration, bg: el?.style.backgroundImage ?? "" }`)
  const spans = [...m.bg.matchAll(/(transparent|var\(--ot-piece-(done|partial)\)) ([\d.]+)% ([\d.]+)%/g)].map((x) => ({ c: x[2] ?? "none", a: +x[3] / 100, b: +x[4] / 100 }))
  // the downloaded (done / partial) run around the playhead: merge adjacent coloured spans
  const colored = spans.filter((x) => x.c !== "none")
  let run = null
  for (const x of colored) {
    if (run && Math.abs(x.a - run.b) < 1e-6) run.b = x.b
    else if (x.a <= m.t + 0.002 && x.b >= SEEK - 0.02) run = { a: x.a, b: x.b }
    else if (!run || run.b < SEEK - 0.02) run = x.a <= m.t + 0.002 ? { a: x.a, b: x.b } : run
  }
  const covering = run && run.a <= m.t + 0.002 && run.b >= m.t - 0.002 ? run : colored.find((x) => x.a <= m.t + 0.002 && x.b >= m.t - 0.002) ?? null
  console.log(`seek ${SEEK * 100}% → playhead ${(m.t * 100).toFixed(2)}%; downloaded span around it: ${covering ? `${(covering.a * 100).toFixed(2)}%–${(covering.b * 100).toFixed(2)}%` : "none"}`)
  console.log(`spans (coloured): ${colored.map((x) => `${(x.a * 100).toFixed(1)}–${(x.b * 100).toFixed(1)}`).join(", ")}`)
  r["piece layer covers the playhead after seeking"] = !!covering
  r[`downloaded span starts within 1 % of the seek point (${SEEK * 100} %)`] = !!covering && Math.abs(covering.a - SEEK) <= 0.01
  return r
}
