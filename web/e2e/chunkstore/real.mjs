// 对 dev 后端的真实检查：vite（5174，代理到 127.0.0.1:8080）+ headless Chromium。
//   OT_USER=… OT_PASS=… node e2e/chunkstore/real.mjs [infohash]
// 登录 → 打开 /watch/hash/<ih> → 等待开始播放 → 检查 OPFS 里的分块文件 → 站内离开再返回 / 整页刷新 → 不应重新下载。
import { spawn } from "node:child_process"
import fs from "node:fs"
import path from "node:path"
import { fileURLToPath } from "node:url"

const here = path.dirname(fileURLToPath(import.meta.url))
const web = path.resolve(here, "../..")
const IH = process.argv[2] || "656393d2e72f0f5e167015b02900d14388af79d9"
const PORT = 5174
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

const vite = spawn(process.execPath, [path.join(web, "node_modules/vite/bin/vite.js"), "--port", String(PORT), "--host", "127.0.0.1", "--strictPort"], { cwd: web, stdio: ["ignore", "pipe", "pipe"] })
procs.push(vite)
await new Promise((res, rej) => {
  vite.stdout.on("data", (d) => /ready in/.test(String(d)) && res())
  vite.on("exit", (c) => rej(new Error("vite exited " + c)))
})
const dbg = 9300 + Math.floor(Math.random() * 600)
const prof = "/tmp/chunkstore-real-profile"
fs.rmSync(prof, { recursive: true, force: true })
procs.push(spawn(CHROMIUM, ["--headless=new", "--no-sandbox", "--disable-gpu", `--remote-debugging-port=${dbg}`, `--user-data-dir=${prof}`, "--autoplay-policy=no-user-gesture-required", "about:blank"], { stdio: "ignore" }))
let target
for (let i = 0; i < 80 && !target; i++) { await sleep(200); try { target = (await (await fetch(`http://127.0.0.1:${dbg}/json`)).json()).find((x) => x.type === "page") } catch {} }
const ws = new WebSocket(target.webSocketDebuggerUrl)
await new Promise((r) => (ws.onopen = r))
let id = 0
const pend = new Map()
ws.onmessage = (m) => { const d = JSON.parse(m.data); if (d.id && pend.has(d.id)) { pend.get(d.id)(d); pend.delete(d.id) } }
const send = (method, params = {}) => new Promise((r) => { const i = ++id; pend.set(i, r); ws.send(JSON.stringify({ id: i, method, params })) })
const ev = async (code) => {
  const d = await send("Runtime.evaluate", { expression: `(async () => { ${code} })()`, awaitPromise: true, returnByValue: true })
  if (d.result?.exceptionDetails) throw new Error(d.result.exceptionDetails.exception?.description || JSON.stringify(d.result.exceptionDetails))
  return d.result?.result?.value
}
const base = `http://127.0.0.1:${PORT}`
const nav = async (url) => { await send("Page.navigate", { url }); await sleep(1500) }
const until = async (code, ms) => { const t0 = Date.now(); while (Date.now() - t0 < ms) { if (await ev(code).catch(() => false)) return Date.now() - t0; await sleep(300) } return -1 }
await send("Page.enable")
await send("Runtime.enable")

const state = `
  const { pieceCache } = await import("/src/player/store/cache.ts")
  const e = pieceCache.entries.get(${JSON.stringify(IH)})
  const t = e?.live?.torrent
  const v = document.querySelector("video")
  let files = 0, bytes = 0, meta = null
  try {
    const d = await (await (await navigator.storage.getDirectory()).getDirectoryHandle("ot-pieces")).getDirectoryHandle(${JSON.stringify(IH)})
    for await (const [n, h] of d.entries()) { if (/^\\d+$/.test(n)) { files++; bytes += (await h.getFile()).size } else if (n === "meta.json") meta = JSON.parse(await (await h.getFile()).text()) }
  } catch {}
  const logs = [...document.querySelectorAll("p")].map(p => p.textContent).filter(s => /缓存|分块|种子|下载|WebSeed|节点|失败|不兼容/.test(s || "")).slice(-14)
  return { live: !!t, ready: t?.ready, progress: t?.progress, received: t?.received, numPieces: t?.pieces?.length, video: v ? { rs: v.readyState, t: +v.currentTime.toFixed(2), paused: v.paused, err: v.error?.message ?? null } : null, opfs: { files, bytes, meta }, budget: pieceCache.budget, logs }`

try {
  await nav(base + "/")
  const login = await ev(`const r = await fetch("/api/v1/auth/login", { method: "POST", headers: { "content-type": "application/json", "x-requested-with": "fetch" }, body: JSON.stringify({ identifier: ${JSON.stringify(process.env.OT_USER || "canoziia")}, password: ${JSON.stringify(process.env.OT_PASS || "devpass-123")} }) }); return r.status`)
  check(login === 200, "login", login)

  // 1. 第一次打开
  await nav(`${base}/watch/hash/${IH}`)
  const tPlay = await until(`const v = document.querySelector("video"); return !!v && v.readyState >= 2 && v.currentTime > 0.3`, 90_000)
  const tDone = await until(`const { pieceCache } = await import("/src/player/store/cache.ts"); return pieceCache.entries.get(${JSON.stringify(IH)})?.live?.torrent?.done === true`, 120_000)
  await sleep(1500)
  const s1 = await ev(state)
  console.log("## first visit:", JSON.stringify({ playStartMs: tPlay, doneMs: tDone, ...s1 }, null, 1))
  check(tPlay >= 0, "playback started (currentTime advancing)", s1.video)
  check(s1.opfs.files > 0 && s1.opfs.files === s1.numPieces && s1.opfs.meta?.infoHash === IH, "OPFS holds one file per piece + meta.json", { files: s1.opfs.files, numPieces: s1.numPieces })

  // 2. 站内离开再返回（SPA：会话关闭但保留数据，再次打开从缓存续传）
  await ev(`history.pushState({}, "", "/"); dispatchEvent(new PopStateEvent("popstate")); return true`)
  await sleep(1500)
  const away = await ev(state)
  check(!away.live && away.opfs.files === s1.opfs.files, "after leaving: session closed, data kept", { live: away.live, files: away.opfs.files })
  await ev(`history.back(); return true`)
  const tPlay2 = await until(`const v = document.querySelector("video"); return !!v && v.readyState >= 2 && v.currentTime > 0.3`, 60_000)
  await sleep(1500)
  const s2 = await ev(state)
  console.log("## back (SPA):", JSON.stringify({ playStartMs: tPlay2, ...s2 }, null, 1))
  check(tPlay2 >= 0 && s2.progress === 1 && s2.received < s2.opfs.meta.pieceLength, "re-enter (SPA): 100% from cache, no piece data downloaded", { playStartMs: tPlay2, progress: s2.progress, received: s2.received })

  // 3. 整页刷新
  await nav(`${base}/watch/hash/${IH}`)
  const tPlay3 = await until(`const v = document.querySelector("video"); return !!v && v.readyState >= 2 && v.currentTime > 0.3`, 60_000)
  await sleep(1500)
  const s3 = await ev(state)
  console.log("## reload:", JSON.stringify({ playStartMs: tPlay3, progress: s3.progress, received: s3.received, files: s3.opfs.files }))
  check(tPlay3 >= 0 && s3.progress === 1 && s3.received < s3.opfs.meta.pieceLength, "full reload: 100% from cache, no piece data downloaded (received = wire protocol bytes only)", { playStartMs: tPlay3, progress: s3.progress, received: s3.received })
} catch (e) {
  console.error("ERROR", e)
  failures++
} finally {
  cleanup()
}
console.log(failures ? `\n${failures} check(s) FAILED` : "\nall checks passed")
process.exit(failures ? 1 : 0)
