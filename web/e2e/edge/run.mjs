// 边缘节点端到端测试（全部本地，不碰 dev 服务器 / 真实 qB）：
//   主站 otd（新库，随机端口）+ `otd edge` + 假 qB WebUI（本脚本）+ 一个「下载中」的种子 + headless Chromium。
//
//   cd web && npm run build && (cd .. && go build -o /tmp/edge-e2e/otd ./cmd/otd)
//   node e2e/edge/run.mjs [seconds] [media.mp4]
//
// 检查：
//   - 浏览器在站点 origin（http://127.0.0.1:主站端口）播放，WebSeed 只来自边缘节点的另一个 origin
//     （http://edge.test:边缘端口，Chromium host-resolver 映射到 127.0.0.1）；主站 /webseed 0 字节；
//   - 边缘节点的请求带 Authorization: Bearer，不带 Cookie；令牌在播放中刷新（edge.token_minutes = 1）；
//   - 「qB」下载中（.!qB 稀疏文件逐块写入，下完改名）时边缘节点已提供字节，播放开始；
//   - 无令牌 401、外站 Origin 预检 403、后台显示边缘节点已连接与已提供流量。
// 浏览器屏蔽其他域名解析并禁用 WebRTC（UDP），WebSeed 是唯一来源。
import { spawn, execFileSync } from "node:child_process"
import crypto from "node:crypto"
import fs from "node:fs"
import http from "node:http"
import path from "node:path"

const SECS = Number(process.argv[2] || 75)
const MEDIA = process.argv[3] || process.env.MEDIA || "/tmp/f/media/v.mp4"
const OTD = process.env.OTD_BIN || "/tmp/edge-e2e/otd"
const CHROMIUM = process.env.CHROMIUM || "/nix/var/nix/profiles/runtime/bin/chromium"
const RATE = Number(process.env.QB_RATE || 450_000) // fake qB download speed, bytes/s
const START_FRACTION = 0.08 // already downloaded when the test starts
const sleep = (ms) => new Promise((r) => setTimeout(r, ms))
const rnd = () => 19000 + Math.floor(Math.random() * 11000)
const MAIN_PORT = rnd(), EDGE_PORT = rnd(), QB_PORT = rnd(), DBG_PORT = rnd()
const SITE = `http://127.0.0.1:${MAIN_PORT}`
const EDGE_URL = `http://edge.test:${EDGE_PORT}`

const RUN = `/tmp/edge-e2e/run-${process.pid}`
const procs = []
const killAll = () => {
  for (const p of procs) try { p.kill("SIGKILL") } catch {}
  try { fs.rmSync(`${RUN}/chrome`, { recursive: true, force: true }) } catch {}
}
process.on("exit", killAll)
for (const sig of ["SIGTERM", "SIGINT"]) process.on(sig, () => { killAll(); process.exit(130) })
fs.rmSync(RUN, { recursive: true, force: true })
fs.mkdirSync(RUN, { recursive: true })

// ---------------------------------------------------------------- torrent
const PL = 64 << 10
const ROOT = "[E2E] Edge Test - 01"
const media = fs.readFileSync(MEDIA)
const notes = Buffer.from("边缘节点端到端测试\n".repeat(40))
const EXT = path.extname(MEDIA) || ".mp4"
const files = [{ path: `${ROOT}${EXT}`, data: media }, { path: "notes.txt", data: notes }]
const all = Buffer.concat(files.map((f) => f.data))
const NP = Math.ceil(all.length / PL)
const hashes = []
for (let i = 0; i < NP; i++) hashes.push(crypto.createHash("sha1").update(all.subarray(i * PL, Math.min((i + 1) * PL, all.length))).digest())
function bencode(v) {
  if (typeof v === "number") return Buffer.from(`i${v}e`)
  if (Buffer.isBuffer(v)) return Buffer.concat([Buffer.from(`${v.length}:`), v])
  if (typeof v === "string") return bencode(Buffer.from(v))
  if (Array.isArray(v)) return Buffer.concat([Buffer.from("l"), ...v.map(bencode), Buffer.from("e")])
  const keys = Object.keys(v).sort()
  return Buffer.concat([Buffer.from("d"), ...keys.flatMap((k) => [bencode(k), bencode(v[k])]), Buffer.from("e")])
}
const info = { name: ROOT, "piece length": PL, pieces: Buffer.concat(hashes), files: files.map((f) => ({ length: f.data.length, path: [f.path] })) }
const IH = crypto.createHash("sha1").update(bencode(info)).digest("hex")
const torrent = bencode({ info, announce: "wss://tracker.invalid/announce" })
console.log(`torrent ${IH}: ${NP} pieces of ${PL >> 10} KiB, ${(all.length / 1e6).toFixed(1)} MB`)

// on disk, as qB lays it out: <save>/<root>/<file>(.!qB); qB sees /Data/Torrent/…, the edge
// mounts it elsewhere (DATA_MAP)
const DATA = `${RUN}/data`
const QB_SAVE = "/Data/Torrent/qbittorrent/files/默认"
const localSave = path.join(DATA, "qbittorrent/files/默认")
fs.mkdirSync(path.join(localSave, ROOT), { recursive: true })
const videoPath = path.join(localSave, ROOT, `${ROOT}${EXT}`)
const notesPath = path.join(localSave, ROOT, "notes.txt")
const states = new Array(NP).fill(0)
{
  const fd = fs.openSync(videoPath + ".!qB", "w")
  fs.ftruncateSync(fd, media.length) // sparse, like qB with preallocation off
  fs.closeSync(fd)
}
const piecesOfVideo = Math.ceil(media.length / PL)
let videoDone = false
function writePiece(i) {
  // write the bytes of piece i into the files it covers
  let off = i * PL
  const end = Math.min(off + PL, all.length)
  let fileStart = 0
  for (const f of files) {
    const fEnd = fileStart + f.data.length
    const s = Math.max(off, fileStart), e = Math.min(end, fEnd)
    if (s < e) {
      const p = f === files[0] ? (videoDone ? videoPath : videoPath + ".!qB") : notesPath
      const fd = fs.openSync(p, fs.existsSync(p) ? "r+" : "w")
      fs.writeSync(fd, f.data, s - fileStart, e - s, s - fileStart)
      fs.closeSync(fd)
    }
    fileStart = fEnd
  }
  states[i] = 2
}
// notes.txt is in the last piece(s) → complete from the start; the video's first 8 %
for (let i = 0; i < NP; i++) if (i >= piecesOfVideo - 1) writePiece(i)
for (let i = 0; i < Math.floor(piecesOfVideo * START_FRACTION); i++) writePiece(i)
const done = () => states.every((s) => s === 2)
const progress = () => states.filter((s) => s === 2).length / NP

// ---------------------------------------------------------------- fake qB WebUI
const qbLog = new Map() // path → count
let added = null // {category}
const prio = files.map(() => 1)
let downloading = false
const qb = http.createServer(async (req, res) => {
  const u = new URL(req.url, "http://qb")
  const p = u.pathname.replace(/^\/api\/v2\//, "")
  qbLog.set(p, (qbLog.get(p) ?? 0) + 1)
  const chunks = []
  for await (const c of req) chunks.push(c)
  const body = Buffer.concat(chunks).toString("latin1")
  const form = new URLSearchParams(req.headers["content-type"]?.includes("urlencoded") ? body : "")
  const q = (k) => u.searchParams.get(k) ?? form.get(k)
  const json = (v) => { res.setHeader("content-type", "application/json"); res.end(JSON.stringify(v)) }
  const have = () => added && (q("hash") ?? q("hashes") ?? "").toLowerCase().includes(IH)
  const nf = () => { res.statusCode = 404; res.end("Not Found") }
  switch (p) {
    case "auth/login":
      res.setHeader("set-cookie", "SID=fake; HttpOnly; path=/")
      return res.end("Ok.")
    case "app/version": return res.end("v5.3.0beta1")
    case "app/buildInfo": return json({ libtorrent: "2.1.1.0", qt: "6.8", bitness: 64 })
    case "torrents/add": {
      const cat = form.get("category") ?? /name="category"\r\n\r\n([^\r]*)/.exec(body)?.[1] ?? ""
      // magnet (urls=…btih:IH) or the .torrent itself (multipart, added stopped by the file-selection flow)
      if (body.toLowerCase().includes(IH) || body.includes(torrent.subarray(0, 64).toString("latin1"))) {
        if (!added || !cat.endsWith("-meta")) added = { category: cat }
        if (!cat.endsWith("-meta")) downloading = true
      }
      return res.end("Ok.")
    }
    case "torrents/setCategory":
      if (added) added.category = q("category")
      if (!q("category").endsWith("-meta")) downloading = true
      return res.end("")
    case "torrents/delete":
      if (have()) { added = null; downloading = false }
      return res.end("")
    case "torrents/info": {
      const cat = q("category")
      if (!added || (cat && cat !== added.category) || (q("hashes") && !q("hashes").includes(IH))) return json([])
      return json([{ hash: IH, name: ROOT, state: done() ? "uploading" : downloading ? "downloading" : "stoppedDL", progress: progress(), size: all.length, added_on: 1, has_metadata: true,
        num_seeds: 0, num_leechs: 0, upspeed: 0, dlspeed: downloading && !done() ? RATE : 0, category: added.category }])
    }
    case "torrents/files":
      if (!have()) return nf()
      return json(files.map((f, i) => ({ index: i, name: `${ROOT}/${f.path}`, size: f.data.length, progress: i === 0 ? (videoDone ? 1 : progress()) : 1, priority: prio[i] })))
    case "torrents/filePrio":
      for (const id of (q("id") ?? "").split("|")) prio[Number(id)] = Number(q("priority"))
      return res.end("")
    case "torrents/properties":
      if (!have()) return nf()
      return json({ save_path: QB_SAVE + "/", piece_size: PL })
    case "torrents/export":
      if (!have()) return nf()
      res.setHeader("content-type", "application/x-bittorrent")
      return res.end(torrent)
    case "torrents/pieceStates":
      if (!have()) return nf()
      return json(states)
    case "torrents/downloadFile":
      res.statusCode = 409
      return res.end("File not fully downloaded")
    default:
      return res.end("")
  }
})
await new Promise((r) => qb.listen(QB_PORT, "127.0.0.1", r))
// "download": sequential pieces at RATE once added to the seed category; rename when the video is complete
setInterval(() => {
  if (!downloading) return
  let budget = Math.max(1, Math.round((RATE / PL) / 5))
  for (let i = 0; i < NP && budget > 0; i++) if (states[i] !== 2) { writePiece(i); budget-- }
  if (!videoDone && states.slice(0, piecesOfVideo).every((s) => s === 2)) {
    fs.renameSync(videoPath + ".!qB", videoPath)
    videoDone = true
    console.log("  fake qB: download complete, renamed .!qB → final name")
  }
}, 200)

// ---------------------------------------------------------------- main otd + edge
const env = { ...process.env, GOMAXPROCS: "2", HTTP_PROXY: "", HTTPS_PROXY: "", http_proxy: "", https_proxy: "", PORT: "" }
execFileSync(OTD, ["user", "-db", `${RUN}/otd.db`, "-username", "e2eadmin", "-password", "e2e-pass-123", "-role", "admin"], { env, stdio: "ignore" })
const mainLog = fs.openSync(`${RUN}/main.log`, "w")
procs.push(spawn(OTD, ["serve", "-db", `${RUN}/otd.db`, "-meta-db", `${RUN}/meta.db`, "-addr", `127.0.0.1:${MAIN_PORT}`], { env, stdio: ["ignore", mainLog, mainLog] }))
for (let i = 0; i < 100; i++) { await sleep(200); try { if ((await fetch(SITE + "/api/v1/site")).ok) break } catch {} }

let cookie = ""
async function api(method, p, body) {
  const r = await fetch(SITE + "/api/v1" + p, { method, headers: { cookie, "x-requested-with": "fetch", "content-type": "application/json" }, body: body ? JSON.stringify(body) : undefined })
  const sc = r.headers.get("set-cookie")
  if (sc) cookie = sc.split(";")[0]
  const t = await r.text()
  if (!r.ok) throw new Error(`${method} ${p}: ${r.status} ${t}`)
  return t ? JSON.parse(t) : null
}
await api("POST", "/auth/login", { identifier: "e2eadmin", password: "e2e-pass-123" })
await api("PUT", "/admin/settings", { values: { "site.base_url": SITE, "edge.token_minutes": 1 } })
await api("POST", "/admin/qb-servers", { name: "fake-ams", url: "http://qb-only-via-edge.invalid", username: "u", password: "p", category: "projectk", enabled: true,
  seeding_time_limit_min: 1440, ratio_limit: -2, add_wss_trackers: false, edge_url: EDGE_URL, edge_api: true })
const sv = (await api("GET", "/admin/qb-servers")).items?.[0] ?? (await api("GET", "/admin/qb-servers"))[0]
const { token, connect_url } = await api("POST", `/admin/qb-servers/id/${sv.id}/edge-token`)
console.log("edge token issued:", token.slice(0, 9) + "…", "connect:", connect_url)

const edgeLog = fs.openSync(`${RUN}/edge.log`, "w")
procs.push(spawn(OTD, ["edge"], {
  env: { ...env, OTD_MAIN: `ws://127.0.0.1:${MAIN_PORT}/api/v1/edge/connect`, OTD_EDGE_TOKEN: token, QB_URL: `http://127.0.0.1:${QB_PORT}`, QB_USER: "u", QB_PASS: "p",
    DATA_ROOT: DATA, DATA_MAP: `/Data/Torrent=${DATA}`, EDGE_LISTEN: `127.0.0.1:${EDGE_PORT}` },
  stdio: ["ignore", edgeLog, edgeLog],
}))
let st
for (let i = 0; i < 60; i++) {
  await sleep(500)
  const ls = await api("GET", "/admin/qb-servers")
  st = (ls.items ?? ls)[0]
  if (st.edge?.connected && st.online) break
}
console.log("admin view:", JSON.stringify({ online: st.online, version: st.version, edge: st.edge }))
if (!st.edge?.connected || !st.online) throw new Error("edge did not connect / qB API via edge offline")

// ---------------------------------------------------------------- chromium
const prof = `${RUN}/chrome`
procs.push(spawn(CHROMIUM, ["--headless=new", "--no-sandbox", "--disable-gpu", `--remote-debugging-port=${DBG_PORT}`, `--user-data-dir=${prof}`, "--autoplay-policy=no-user-gesture-required",
  "--no-proxy-server", "--force-webrtc-ip-handling-policy=disable_non_proxied_udp", "--host-resolver-rules=MAP edge.test 127.0.0.1, MAP * ~NOTFOUND, EXCLUDE 127.0.0.1",
  `--lang=${process.env.BROWSER_LANG || "zh-CN"}`, `--accept-lang=${process.env.BROWSER_LANG || "zh-CN"}`, "--window-size=1280,900", "about:blank"], { stdio: "ignore" }))
let target
for (let i = 0; i < 80 && !target; i++) { await sleep(200); try { target = (await (await fetch(`http://127.0.0.1:${DBG_PORT}/json`)).json()).find((x) => x.type === "page") } catch {} }
const ws = new WebSocket(target.webSocketDebuggerUrl)
await new Promise((r) => (ws.onopen = r))
let id = 0
const pend = new Map()
const consoleErr = []
const consoleAll = []
const send = (method, params = {}) => new Promise((r) => { const i = ++id; pend.set(i, r); ws.send(JSON.stringify({ id: i, method, params })) })
const ev = async (code) => {
  const r = await Promise.race([send("Runtime.evaluate", { expression: `(async () => { ${code} })()`, awaitPromise: true, returnByValue: true }), sleep(10_000).then(() => null)])
  return r?.result?.result?.value
}
const stat = { edge: { 206: 0, 200: 0, 503: 0, 401: 0, other: 0, bytes: 0 }, main: { n: 0, bytes: 0 }, preflight: 0, auth: new Set(), noAuth: 0, cookieToEdge: 0 }
const reqs = new Map()
const allRequests = []
ws.onmessage = (m) => {
  const d = JSON.parse(m.data)
  if (d.id && pend.has(d.id)) { pend.get(d.id)(d); pend.delete(d.id); return }
  const p = d.params
  switch (d.method) {
    case "Runtime.consoleAPICalled":
      if (p.type === "error") consoleErr.push(p.args.map((a) => a.value ?? a.description).join(" ").slice(0, 200))
      consoleAll.push(p.args.map((a) => a.value ?? a.description).join(" ").slice(0, 300))
      break
    case "Network.requestWillBeSent": {
      allRequests.push(p.request.url)
      const url = p.request.url
      if (!url.includes("/webseed/hash/")) break
      const edge = url.startsWith(EDGE_URL)
      reqs.set(p.requestId, edge ? "edge" : "main")
      if (p.request.method === "OPTIONS") stat.preflight++
      else if (edge) {
        const a = Object.entries(p.request.headers).find(([k]) => k.toLowerCase() === "authorization")?.[1]
        if (a) stat.auth.add(a)
        else stat.noAuth++
      }
      break
    }
    case "Network.requestWillBeSentExtraInfo": // the headers actually sent (incl. cookies)
      if (reqs.get(p.requestId) === "edge" && Object.keys(p.headers).some((k) => k.toLowerCase() === "cookie")) stat.cookieToEdge++
      break
    case "Network.responseReceived": {
      const k = reqs.get(p.requestId)
      if (k === "edge" && p.type !== "Preflight") { const s = p.response.status; stat.edge[s in stat.edge ? s : "other"]++ }
      if (k === "main") stat.main.n++
      break
    }
    case "Network.loadingFinished": {
      const k = reqs.get(p.requestId)
      if (k === "edge") stat.edge.bytes += p.encodedDataLength
      if (k === "main") stat.main.bytes += p.encodedDataLength
      break
    }
  }
}
await send("Page.enable")
await send("Runtime.enable")
await send("Network.enable")
await send("Page.navigate", { url: SITE + "/" })
await sleep(1500)
console.log("browser login:", await ev(`return (await fetch("/api/v1/auth/login", { method: "POST", headers: { "content-type": "application/json", "x-requested-with": "fetch" }, body: JSON.stringify({ identifier: "e2eadmin", password: "e2e-pass-123" }) })).status`))

const t0 = Date.now()
await send("Page.navigate", { url: `${SITE}/watch/hash/${IH}` })
let playAt = -1
let servedMidDownload = false
const samples = []
while (Date.now() - t0 < SECS * 1000) {
  await sleep(4000)
  const sec = Math.round((Date.now() - t0) / 1000)
  if (sec > 20 && !downloading && !added?.category?.startsWith("projectk")) {
    // the page asks the server to seed on its own; nudge if it didn't (e.g. permissions)
    try { await api("POST", `/torrents/hash/${IH}/seed`) } catch (e) { console.log("seed:", e.message) }
  }
  const page = await ev(`
    const v = document.querySelector("video, audio")
    const tab = [...document.querySelectorAll("[role=tab]")].find((x) => /日志/.test(x.textContent))
    if (tab && tab.getAttribute("aria-selected") !== "true") { tab.dispatchEvent(new MouseEvent("mousedown", { bubbles: true })); tab.click() }
    return { v: v ? { rs: v.readyState, t: +v.currentTime.toFixed(1), buf: v.buffered.length ? +v.buffered.end(v.buffered.length - 1).toFixed(1) : 0, err: v.error?.message ?? null } : null }`)
  if (playAt < 0 && page?.v?.rs >= 3 && page.v.t > 0.2) playAt = sec
  const prog = +(progress() * 100).toFixed(1)
  if (prog < 100 && stat.edge[206] + stat.edge[200] > 0) servedMidDownload = true
  const s = { sec, qb: { added: added?.category ?? null, progress: prog, renamed: videoDone }, video: page?.v, edge: { ...stat.edge }, main: { ...stat.main }, tokens: stat.auth.size }
  samples.push(s)
  console.log(JSON.stringify(s))
}
const logs = await ev(`return [...document.querySelectorAll(".font-mono p")].map((p) => p.textContent)`)
console.log("\nplayer log:\n  " + (logs ?? []).filter((l) => /WebSeed|边缘|种子|失败|不兼容/.test(l)).slice(-15).join("\n  "))
console.log("console errors:", consoleErr.slice(0, 5))

// ---------------------------------------------------------------- direct checks against the edge
const edgeDirect = `http://127.0.0.1:${EDGE_PORT}/webseed/hash/${IH}/${encodeURIComponent(ROOT)}/${encodeURIComponent(ROOT + EXT)}`
const noTok = await fetch(edgeDirect, { headers: { Origin: SITE, Range: "bytes=0-9", Cookie: cookie } })
const foreign = await fetch(edgeDirect, { method: "OPTIONS", headers: { Origin: "https://evil.example", "Access-Control-Request-Method": "GET", "Access-Control-Request-Headers": "authorization,range" } })
const pre = await fetch(edgeDirect, { method: "OPTIONS", headers: { Origin: SITE, "Access-Control-Request-Method": "GET", "Access-Control-Request-Headers": "authorization,range,cache-control,user-agent" } })
const ls = await api("GET", "/admin/qb-servers")
const fin = (ls.items ?? ls)[0]
console.log(`\nedge without token (cookie only): ${noTok.status}; foreign-origin preflight: ${foreign.status}; site preflight: ${pre.status} allow-origin=${pre.headers.get("access-control-allow-origin")} credentials=${pre.headers.get("access-control-allow-credentials")}`)
console.log("admin edge status:", JSON.stringify(fin.edge))
console.log("fake qB calls:", JSON.stringify(Object.fromEntries(qbLog)))
console.log(`run dir: ${RUN} (main.log, edge.log)`)

const checks = {
  "playback started": playAt >= 0,
  "edge served bytes while qB was still downloading": servedMidDownload,
  "main server served no webseed data": stat.main.bytes === 0 && stat.main.n === 0,
  "every edge request had a bearer token": stat.noAuth === 0 && stat.auth.size > 0,
  "no cookies sent to the edge": stat.cookieToEdge === 0,
  "token refreshed during playback": stat.auth.size >= 2 || SECS < 60,
  "no 401 from the edge": stat.edge[401] === 0,
  "edge refuses requests without a token": noTok.status === 401,
  "edge refuses foreign-origin preflight": foreign.status === 403,
  "CORS preflights cached (not one per request)": stat.preflight <= 10,
  "preflight ok without credentials": pre.status === 204 && pre.headers.get("access-control-allow-origin") === SITE && !pre.headers.get("access-control-allow-credentials"),
  "admin shows bytes served": fin.edge?.connected && fin.edge.bytes_served > 0,
}
// extra checks (e.g. e2e/subtitles/checks.mjs): run against the same page
if (process.env.EXTRA) {
  const extra = await import(path.resolve(process.env.EXTRA))
  Object.assign(checks, await extra.default({ ev, send, sleep, consoleErr, consoleAll, RUN, requests: allRequests }))
}
console.log(`\nplayback started at +${playAt}s; edge ${JSON.stringify(stat.edge)}; preflights ${stat.preflight}; tokens seen ${stat.auth.size}`)
for (const [k, v] of Object.entries(checks)) console.log(`${v ? "  ok  " : "  FAIL"} ${k}`)
const ok = Object.values(checks).every(Boolean)
console.log(ok ? "OK" : "FAIL")
process.exit(ok ? 0 : 1)
