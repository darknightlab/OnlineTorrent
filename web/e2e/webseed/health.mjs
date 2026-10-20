// WebSeed 健康（e2e/edge/run.mjs 的 EXTRA）：用 CDP Fetch 拦截发往边缘节点的请求，模拟
//  A. 正常的「还没下载到」503（边缘节点的正文）→ 不停用；
//  B. 连接失败（ConnectionRefused，同线上「Connection failed」）→ 停用一次、日志一条，停用期间几乎不再请求；
//  C. 恢复连接 → 退避到时（15 s）重新启用，日志「已恢复」，继续从边缘节点取数据；
//  D. 401（当前令牌过期被拒）→ 重新连接 SSE 取新令牌，用新令牌继续，不停用；
//  E. 返回错误数据（206，长度正确）→ 分块校验失败归到该 WebSeed → 停用。
// 媒体要足够大（窗口缓存模式），否则数据早已取完、没有请求：
//   TORRENT=/tmp/tmap/f2b.torrent MEDIA=/tmp/tmap/f2b.mp4 START_FRACTION=1 CACHE_BYTES=150000000 EXTRA=e2e/webseed/health.mjs node e2e/edge/run.mjs 10
export default async function ({ ev, send, sleep, consoleAll, onCDP, EDGE_URL, SITE }) {
  const r = {}
  let mode = "pass" // pass | notready | refuse | unauth
  let gets = 0 // edge GETs seen by the interceptor (current phase)
  let badToken = null
  const sseOpens = []
  const off = onCDP((method, p) => {
    if (method === "Network.requestWillBeSent" && /\/webseed\/events/.test(p.request.url)) sseOpens.push(Date.now())
    if (method !== "Fetch.requestPaused") return
    const id = p.requestId
    const auth = Object.entries(p.request.headers).find(([k]) => k.toLowerCase() === "authorization")?.[1]
    if (mode === "unauth") {
      // an expired token: only the token in use when the phase starts is rejected
      badToken ??= auth
      if (auth !== badToken) return void send("Fetch.continueRequest", { requestId: id })
    }
    if (p.request.method !== "GET" || mode === "pass") return void send("Fetch.continueRequest", { requestId: id })
    gets++
    const cors = [{ name: "Access-Control-Allow-Origin", value: SITE }, { name: "Access-Control-Expose-Headers", value: "Content-Range, Content-Length, Accept-Ranges, Retry-After" }, { name: "Content-Type", value: "text/plain; charset=utf-8" }]
    if (mode === "refuse") return void send("Fetch.failRequest", { requestId: id, errorReason: "ConnectionRefused" })
    if (mode === "corrupt") {
      // 206 with the right length but garbage: the piece fails verification
      const m = /bytes=(\d+)-(\d+)/.exec(Object.entries(p.request.headers).find(([k]) => k.toLowerCase() === "range")?.[1] ?? "")
      if (!m) return void send("Fetch.continueRequest", { requestId: id })
      const len = Number(m[2]) - Number(m[1]) + 1
      const body = Buffer.alloc(len, 0x5a).toString("base64")
      return void send("Fetch.fulfillRequest", { requestId: id, responseCode: 206, responseHeaders: [...cors, { name: "Content-Range", value: `bytes ${m[1]}-${m[2]}/*` }, { name: "Content-Length", value: String(len) }], body })
    }
    const [code, body, extra] = mode === "notready" ? [503, "pieces not downloaded yet\n", [{ name: "Retry-After", value: "5" }]] : [401, "token expired\n", []]
    send("Fetch.fulfillRequest", { requestId: id, responseCode: code, responseHeaders: [...cors, ...extra], body: Buffer.from(body).toString("base64") })
  })
  await send("Fetch.enable", { patterns: [{ urlPattern: `${EDGE_URL}/webseed/*`, requestStage: "Request" }] })
  const logs = (from) => consoleAll.slice(from).filter((l) => /WebSeed/.test(l))
  // keep the player asking for new data: seek to fresh spots every few seconds
  let seekN = 0
  const seekNew = () => ev(`const v = document.querySelector("video"); v.currentTime = v.duration * ${0.1 + 0.07 * (seekN++ % 12)}; v.play().catch(() => {}); return 1`)
  const phase = async (m, secs) => {
    mode = m
    gets = 0
    const from = consoleAll.length
    const t0 = Date.now()
    while (Date.now() - t0 < secs * 1000) {
      await seekNew()
      await sleep(3000)
    }
    return { gets, logs: logs(from) }
  }

  const a = await phase("notready", 20)
  console.log("A not-ready 503s:", a.gets, "requests;", JSON.stringify(a.logs))
  r["A: expected 503s (pieces not downloaded yet) don't disable the WebSeed"] = a.gets >= 3 && !a.logs.some((l) => /已停用/.test(l))
  await phase("pass", 6)

  // B: connection refused
  mode = "refuse"
  gets = 0
  const fromB = consoleAll.length
  let disabledAt = 0
  for (let i = 0; i < 20 && !disabledAt; i++) {
    await seekNew()
    await sleep(1000)
    if (logs(fromB).some((l) => /已停用/.test(l))) disabledAt = Date.now()
  }
  const getsToDisable = gets
  gets = 0
  // while disabled: keep seeking for 12 s; there should be (almost) no more edge requests
  for (let i = 0; i < 4; i++) {
    await seekNew()
    await sleep(3000)
  }
  const getsWhileDisabled = gets
  const bLogs = logs(fromB)
  console.log("B refused:", getsToDisable, "requests until disabled;", getsWhileDisabled, "in 12 s while disabled;", JSON.stringify(bLogs))
  r["B: connection failures disable the WebSeed, logged once"] = !!disabledAt && bLogs.filter((l) => /已停用/.test(l)).length === 1 && bLogs.some((l) => /WebSeed 暂时不可用，已停用（\d+ 次失败），15 秒后重试/.test(l))
  r["B: no requests to the disabled edge"] = getsWhileDisabled <= 2

  // C: back online; the retry (15 s after disabling) re-enables it
  mode = "pass"
  const fromC = consoleAll.length
  let recovered = 0
  for (let i = 0; i < 25 && !recovered; i++) {
    await seekNew()
    await sleep(1000)
    if (logs(fromC).some((l) => /已恢复/.test(l))) recovered = Date.now()
  }
  console.log("C recovered", recovered ? `${((recovered - disabledAt) / 1000).toFixed(1)} s after disabling` : "never", JSON.stringify(logs(fromC)))
  r["C: re-enabled after the backoff and logged 已恢复"] = !!recovered && (recovered - disabledAt) / 1000 >= 14
  const t = await ev(`const v = document.querySelector("video"); const t0 = v.currentTime; v.play().catch(() => {}); await new Promise((r) => setTimeout(r, 5000)); return v.currentTime - t0`)
  r["C: playback continues from the edge"] = t > 2

  // D: the edge rejects the current token (expired) → SSE reconnect for a new one; requests with it succeed
  const sseBefore = sseOpens.length
  const fromD = consoleAll.length
  mode = "unauth"
  gets = 0
  for (let i = 0; i < 3; i++) {
    await seekNew()
    await sleep(3000)
  }
  const t2 = await ev(`const v = document.querySelector("video"); const t0 = v.currentTime; v.play().catch(() => {}); await new Promise((r) => setTimeout(r, 5000)); return v.currentTime - t0`)
  mode = "pass"
  console.log("D 401:", gets, "requests; SSE reconnects", sseOpens.length - sseBefore, JSON.stringify(logs(fromD)))
  r["D: 401 makes the player fetch a new token (SSE reconnect)"] = sseOpens.length > sseBefore
  r["D: an expired token doesn't disable the WebSeed"] = !logs(fromD).some((l) => /已停用/.test(l))
  r["D: playback continues with the new token"] = t2 > 2

  // E: corrupt data (hash failures attributed to this WebSeed) → disabled
  await sleep(20_000) // let any earlier backoff state settle
  const fromE = consoleAll.length
  mode = "corrupt"
  gets = 0
  let eDisabled = false
  for (let i = 0; i < 20 && !eDisabled; i++) {
    await seekNew()
    await sleep(1000)
    eDisabled = logs(fromE).some((l) => /已停用/.test(l))
  }
  mode = "pass"
  console.log("E corrupt:", gets, "requests;", JSON.stringify(logs(fromE)))
  r["E: pieces failing verification disable the WebSeed"] = eDisabled

  await send("Fetch.disable")
  off()
  return r
}
