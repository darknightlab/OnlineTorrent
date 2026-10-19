// 边下边供 WebSeed 的端到端检查（dev 服务器 + 测试 qB + headless Chromium）。
//   QB_URL=… QB_USER=… QB_PASS=… OT_USER=… OT_PASS=… node e2e/webseed/progressive.mjs [infohash] [seconds]
// 浏览器屏蔽所有域名解析（tracker、STUN、种子自带的 url-list WebSeed；本站用 IP 访问）并禁用 WebRTC（UDP），只剩本站 WebSeed 一个来源；
// qB 下载限速，保持「下载中」。每 5 s 记录：qB 进度、qB 上传给本站 BT 连接的字节、播放器日志、视频状态。
import { spawn } from "node:child_process"
import fs from "node:fs"

const IH = process.argv[2] || "08ada5a7a6183aae1e09d831df6748d566095a10" // Sintel (CC-BY, webtorrent.io)
const SECS = Number(process.argv[3] || 150)
const BASE = process.env.OT_BASE || "http://127.0.0.1:8080"
const QB = process.env.QB_URL || "https://qbittorrent-ams.kawaii.cam"
const QB_LIMIT = Number(process.env.QB_DL_LIMIT || 150_000)
const CHROMIUM = process.env.CHROMIUM || "/nix/var/nix/profiles/runtime/bin/chromium"
const sleep = (ms) => new Promise((r) => setTimeout(r, ms))
const procs = []
const killAll = () => {
  procs.forEach((p) => { try { p.kill("SIGKILL") } catch {} })
  try { fs.rmSync(prof, { recursive: true, force: true }) } catch {}
}
process.on("exit", killAll)
for (const sig of ["SIGTERM", "SIGINT"]) process.on(sig, () => { killAll(); process.exit(130) })

// ---- qB WebAPI
let cookie = ""
async function qb(path, body) {
  for (let i = 0; ; i++) {
    try {
      return await qbOnce(path, body)
    } catch (e) {
      if (i >= 2) throw e
      await sleep(1000)
    }
  }
}
async function qbOnce(path, body) {
  const r = await fetch(QB + "/api/v2/" + path, { method: body ? "POST" : "GET", headers: { Referer: QB, Cookie: cookie, ...(body ? { "content-type": "application/x-www-form-urlencoded" } : {}) }, body: body ? new URLSearchParams(body) : undefined })
  return r
}
async function qbLogin() {
  const r = await qb("auth/login", { username: process.env.QB_USER || "canoziia", password: process.env.QB_PASS || "" })
  cookie = (r.headers.get("set-cookie") || "").split(";")[0]
}
async function qbJSON(path) {
  let r = await qb(path)
  if (r.status === 403) { await qbLogin(); r = await qb(path) }
  return r.ok ? r.json() : null
}

// ---- chromium
const dbg = 19000 + Math.floor(Math.random() * 11000)
const prof = `/tmp/webseed-e2e-profile-${process.pid}`
fs.rmSync(prof, { recursive: true, force: true })
procs.push(spawn(CHROMIUM, ["--headless=new", "--no-sandbox", "--disable-gpu", `--remote-debugging-port=${dbg}`, `--user-data-dir=${prof}`, "--autoplay-policy=no-user-gesture-required",
  "--no-proxy-server", "--force-webrtc-ip-handling-policy=disable_non_proxied_udp", "--host-resolver-rules=MAP * ~NOTFOUND, EXCLUDE 127.0.0.1", "about:blank"], { stdio: "ignore" }))
let target
for (let i = 0; i < 80 && !target; i++) { await sleep(200); try { target = (await (await fetch(`http://127.0.0.1:${dbg}/json`)).json()).find((x) => x.type === "page") } catch {} }
const ws = new WebSocket(target.webSocketDebuggerUrl)
await new Promise((r) => (ws.onopen = r))
let id = 0
const pend = new Map()
const consoleErr = []
ws.onmessage = (m) => { const d = JSON.parse(m.data); if (d.id && pend.has(d.id)) { pend.get(d.id)(d); pend.delete(d.id) } else if (d.method === "Runtime.consoleAPICalled" && d.params.type === "error") consoleErr.push(d.params.args.map((a) => a.value ?? a.description).join(" ").slice(0, 160)) }
const send = (method, params = {}) => new Promise((r) => { const i = ++id; pend.set(i, r); ws.send(JSON.stringify({ id: i, method, params })) })
const ev = async (code) => {
  const r = await Promise.race([send("Runtime.evaluate", { expression: `(async () => { ${code} })()`, awaitPromise: true, returnByValue: true }), sleep(10_000).then(() => null)])
  return r?.result?.result?.value
}
await send("Page.enable")
await send("Runtime.enable")
await send("Network.enable")
// count webseed responses seen by the page / service worker
const wsResp = { 200: 0, 206: 0, 503: 0, other: 0, bytes: 0 }
const reqUrl = new Map()
ws.addEventListener("message", (m) => {
  const d = JSON.parse(m.data)
  if (d.method === "Network.responseReceived" && d.params.response.url.includes("/webseed/hash/")) {
    const s = d.params.response.status
    wsResp[s in wsResp ? s : "other"]++
    reqUrl.set(d.params.requestId, true)
  } else if (d.method === "Network.loadingFinished" && reqUrl.has(d.params.requestId)) {
    wsResp.bytes += d.params.encodedDataLength
  }
})

await qbLogin()
await send("Page.navigate", { url: BASE + "/" })
await sleep(1500)
const login = await ev(`return (await fetch("/api/v1/auth/login", { method: "POST", headers: { "content-type": "application/json", "x-requested-with": "fetch" }, body: JSON.stringify({ identifier: ${JSON.stringify(process.env.OT_USER || "canoziia")}, password: ${JSON.stringify(process.env.OT_PASS || "devpass-123")} }) })).status`)
console.log("login", login)
// optionally pre-add the .torrent to qB (dev server category) with a download limit, so it stays
// mid-download (qB would otherwise fetch small test torrents in seconds); the site's own seed
// request is then a no-op ("already exists")
if (process.env.PREADD_TORRENT) {
  const fd = new FormData()
  fd.set("torrents", new Blob([fs.readFileSync(process.env.PREADD_TORRENT)]), "t.torrent")
  fd.set("category", process.env.QB_CATEGORY || "projectk")
  fd.set("dlLimit", String(QB_LIMIT))
  fd.set("sequentialDownload", "true")
  fd.set("firstLastPiecePrio", "true")
  const r = await fetch(QB + "/api/v2/torrents/add", { method: "POST", headers: { Referer: QB, Cookie: cookie }, body: fd })
  console.log("pre-added to qB with limit", QB_LIMIT, "B/s:", r.status)
  await sleep(3000)
}
const before = await qbJSON(`torrents/info?hashes=${IH}`)
console.log("qB has torrent before opening:", before?.length ? `${before[0].state} ${(before[0].progress * 100).toFixed(1)}%` : "no")

const t0 = Date.now()
await send("Page.navigate", { url: `${BASE}/watch/hash/${IH}` })
let limited = false
const samples = []
let playAt = -1
while (Date.now() - t0 < SECS * 1000) {
  await sleep(5000)
  const sec = Math.round((Date.now() - t0) / 1000)
  const info = (await qbJSON(`torrents/info?hashes=${IH}`))?.[0]
  if (info && !limited) {
    const r = await qb("torrents/setDownloadLimit", { hashes: IH, limit: String(QB_LIMIT) })
    limited = r.ok
    console.log(`  +${sec}s qB has the torrent (${info.category}); download limit ${QB_LIMIT} B/s: ${r.status}`)
  }
  let ourPeer = null
  if (info) {
    const peers = (await qbJSON(`sync/torrentPeers?hash=${IH}&rid=0`))?.peers ?? {}
    for (const [addr, p] of Object.entries(peers)) if (/PK|131\.143\.240\.2:|192\.168\.4\.2:/.test(`${p.client} ${addr}`)) ourPeer = { addr, client: p.client, uploaded: p.uploaded, up_speed: p.up_speed, flags: p.flags }
  }
  const page = await ev(`
    const v = document.querySelector("video, audio")
    const tab = [...document.querySelectorAll("[role=tab]")].find((x) => /日志/.test(x.textContent))
    if (tab && tab.getAttribute("aria-selected") !== "true") { tab.dispatchEvent(new MouseEvent("mousedown", { bubbles: true })); tab.click() }
    return { v: v ? { rs: v.readyState, t: +v.currentTime.toFixed(1), buf: v.buffered.length ? +v.buffered.end(v.buffered.length - 1).toFixed(1) : 0, err: v.error?.message ?? null } : null,
             logs: [...document.querySelectorAll(".font-mono p")].map((p) => p.textContent.replace(/^\\d\\d:\\d\\d:\\d\\d/, "")) }`)
  if (playAt < 0 && page?.v?.rs >= 3 && page.v.t > 0.2) playAt = sec
  const s = { sec, qb: info ? { state: info.state, progress: +(info.progress * 100).toFixed(1), dl: info.dlspeed, up: info.upspeed } : null, ourPeer, video: page?.v, webseed: { ...wsResp } }
  samples.push(s)
  console.log(JSON.stringify(s))
}
const final = await ev(`return [...document.querySelectorAll(".font-mono p")].map((p) => p.textContent)`)
console.log("\nplayer log:\n  " + (final ?? []).filter((l) => /WebSeed|种子|节点|下载|失败|不兼容/.test(l)).join("\n  "))
console.log("\nconsole errors:", consoleErr.slice(0, 5))
const last = samples[samples.length - 1]
const ok = playAt >= 0 && wsResp[206] > 0 && samples.some((s) => s.qb && s.qb.progress < 100 && s.webseed[206] > 0)
console.log(`\nplayback started at +${playAt}s; webseed responses ${JSON.stringify(wsResp)}; qB progress at end ${last?.qb?.progress}%`)
console.log(ok ? "OK: webseed served bytes while qB was still downloading, playback works" : "FAIL")
process.exit(ok ? 0 : 1)
