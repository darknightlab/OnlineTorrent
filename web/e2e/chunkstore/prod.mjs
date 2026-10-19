// 生产构建（Go 服务器内嵌的 web/dist）上的检查：只用 DOM 与 OPFS，不依赖源码模块。
//   OT_BASE=http://127.0.0.1:8080 OT_USER=… OT_PASS=… node e2e/chunkstore/prod.mjs [infohash]
// 登录 → /watch/hash/<ih> 开始播放（readyState ≥ 3）且日志里没有「不兼容」→ OPFS 有分块文件 →
// 站内离开再返回、整页刷新：从缓存播放（分块文件数不变、没有比离开时更新的文件 = 没有重新下载）。
import { spawn } from "node:child_process"
import fs from "node:fs"

const IH = process.argv[2] || "656393d2e72f0f5e167015b02900d14388af79d9"
const BASE = process.env.OT_BASE || "http://127.0.0.1:8080"
const CHROMIUM = process.env.CHROMIUM || "/nix/var/nix/profiles/runtime/bin/chromium"
const sleep = (ms) => new Promise((r) => setTimeout(r, ms))
const procs = []
const cleanup = () => procs.forEach((p) => { try { p.kill("SIGKILL") } catch {} })
process.on("exit", cleanup)
let failures = 0
const check = (ok, what, extra) => {
  console.log(`${ok ? "  ok  " : "  FAIL"} ${what}${extra !== undefined ? "  " + JSON.stringify(extra) : ""}`)
  if (!ok) failures++
}

const dbg = 19000 + Math.floor(Math.random() * 11000)
const prof = `/tmp/chunkstore-prod-profile-${process.pid}`
fs.rmSync(prof, { recursive: true, force: true })
procs.push(spawn(CHROMIUM, ["--headless=new", "--no-sandbox", "--disable-gpu", `--remote-debugging-port=${dbg}`, `--user-data-dir=${prof}`, "--autoplay-policy=no-user-gesture-required", "about:blank"], { stdio: "ignore" }))
let target
for (let i = 0; i < 80 && !target; i++) { await sleep(200); try { target = (await (await fetch(`http://127.0.0.1:${dbg}/json`)).json()).find((x) => x.type === "page") } catch {} }
const ws = new WebSocket(target.webSocketDebuggerUrl)
await new Promise((r) => (ws.onopen = r))
let id = 0
const pend = new Map()
const consoleErrors = []
ws.onmessage = (m) => {
  const d = JSON.parse(m.data)
  if (d.id && pend.has(d.id)) { pend.get(d.id)(d); pend.delete(d.id) }
  else if (d.method === "Runtime.consoleAPICalled" && d.params.type === "error") consoleErrors.push(d.params.args.map((a) => a.value ?? a.description).join(" ").slice(0, 200))
}
const send = (method, params = {}) => new Promise((r) => { const i = ++id; pend.set(i, r); ws.send(JSON.stringify({ id: i, method, params })) })
const ev = async (code) => {
  const d = await send("Runtime.evaluate", { expression: `(async () => { ${code} })()`, awaitPromise: true, returnByValue: true })
  if (d.result?.exceptionDetails) throw new Error(d.result.exceptionDetails.exception?.description || JSON.stringify(d.result.exceptionDetails))
  return d.result?.result?.value
}
const until = async (code, ms) => { const t0 = Date.now(); while (Date.now() - t0 < ms) { if (await ev(code).catch(() => false)) return Date.now() - t0; await sleep(250) } return -1 }
await send("Page.enable")
await send("Runtime.enable")

const PLAYING = `const v = document.querySelector("video"); return !!v && v.readyState >= 3 && v.currentTime > 0.2`
const STATE = `
  const v = document.querySelector("video")
  const tab = [...document.querySelectorAll("[role=tab]")].find((x) => /日志/.test(x.textContent))
  if (tab && tab.getAttribute("aria-selected") !== "true") { tab.dispatchEvent(new MouseEvent("mousedown", { bubbles: true })); tab.click() }
  await new Promise((r) => setTimeout(r, 300))
  const logs = [...document.querySelectorAll(".font-mono p")].map((p) => p.textContent)
  let files = 0, newest = 0, meta = null, torrent = false
  try {
    const d = await (await (await navigator.storage.getDirectory()).getDirectoryHandle("ot-pieces")).getDirectoryHandle(${JSON.stringify(IH)})
    for await (const [n, h] of d.entries()) {
      if (/^\\d+$/.test(n)) { files++; newest = Math.max(newest, (await h.getFile()).lastModified) }
      else if (n === "meta.json") meta = JSON.parse(await (await h.getFile()).text())
      else if (n === "torrent") torrent = true
    }
  } catch {}
  return { video: v ? { rs: v.readyState, t: +v.currentTime.toFixed(2), err: v.error?.message ?? null } : null, files, newest, numPieces: meta?.numPieces, torrent, now: Date.now(), logs }`
const short = (s) => ({ ...s, logs: s.logs.filter((l) => /缓存|分块|不兼容|失败|就绪|完成/.test(l)).slice(-6) })

try {
  await send("Page.navigate", { url: BASE + "/" })
  await sleep(1500)
  const login = await ev(`return (await fetch("/api/v1/auth/login", { method: "POST", headers: { "content-type": "application/json", "x-requested-with": "fetch" }, body: JSON.stringify({ identifier: ${JSON.stringify(process.env.OT_USER || "canoziia")}, password: ${JSON.stringify(process.env.OT_PASS || "devpass-123")} }) })).status`)
  check(login === 200, "login", login)

  await send("Page.navigate", { url: `${BASE}/watch/hash/${IH}` })
  const tPlay = await until(PLAYING, 120_000)
  // 等全部下载并写入 OPFS
  await until(`const d = await (await (await navigator.storage.getDirectory()).getDirectoryHandle("ot-pieces")).getDirectoryHandle(${JSON.stringify(IH)}); let n = 0; for await (const k of d.keys()) if (/^\\d+$/.test(k)) n++; const m = JSON.parse(await (await (await d.getFileHandle("meta.json")).getFile()).text()); return n === m.numPieces`, 120_000)
  const s1 = await ev(STATE)
  console.log("## first visit:", JSON.stringify({ playStartMs: tPlay, ...short(s1) }))
  check(tPlay >= 0 && s1.video?.rs >= 3, "playback started (readyState ≥ 3)", s1.video)
  check(!s1.logs.some((l) => /不兼容/.test(l)) && !consoleErrors.some((l) => /不兼容/.test(l)), "no 「播放器内核不兼容」", consoleErrors.slice(0, 3))
  check(s1.files > 0 && s1.files === s1.numPieces && s1.torrent, "OPFS ot-pieces/<ih> holds every piece file (+ meta.json, torrent)", { files: s1.files, numPieces: s1.numPieces })

  const leftAt = Date.now()
  await sleep(50)
  await ev(`history.pushState({}, "", "/"); dispatchEvent(new PopStateEvent("popstate")); return true`)
  await sleep(1500)
  await ev(`history.back(); return true`)
  const tPlay2 = await until(PLAYING, 60_000)
  await sleep(1500)
  const s2 = await ev(STATE)
  console.log("## back (in-app):", JSON.stringify({ playStartMs: tPlay2, ...short(s2) }))
  check(tPlay2 >= 0 && s2.files === s1.files && s2.newest < leftAt, "leave + re-enter (in-app): plays from cache, no piece re-downloaded", { playStartMs: tPlay2, files: s2.files, rewritten: s2.newest >= leftAt })

  const reloadAt = Date.now()
  await send("Page.navigate", { url: `${BASE}/watch/hash/${IH}` })
  const tPlay3 = await until(PLAYING, 60_000)
  await sleep(1500)
  const s3 = await ev(STATE)
  console.log("## reload:", JSON.stringify({ playStartMs: tPlay3, ...short(s3) }))
  check(tPlay3 >= 0 && s3.files === s1.files && s3.newest < reloadAt, "full reload: plays from cache, no piece re-downloaded", { playStartMs: tPlay3, files: s3.files, rewritten: s3.newest >= reloadAt })
} catch (e) {
  console.error("ERROR", e)
  failures++
} finally {
  cleanup()
}
console.log(failures ? `\n${failures} check(s) FAILED` : "\nall checks passed")
process.exit(failures ? 1 : 0)
