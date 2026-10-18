// 分块缓存端到端测试：headless Chromium（CDP）+ vite dev + 本地 ws tracker。
//   node e2e/chunkstore/run.mjs        （需要 CHROMIUM=/path/to/chromium，默认 /nix/var/nix/profiles/runtime/bin/chromium）
// 页面 A（http://localhost:PORT，?role=seed）做种 / 充当第二个下载者；页面 B（http://127.0.0.1:PORT，另一个 origin、
// 独立的 OPFS）用真实的 webtorrentEngine 通过 Service Worker 流读取。
import { spawn } from "node:child_process"
import fs from "node:fs"
import path from "node:path"
import { fileURLToPath } from "node:url"
import { Server } from "bittorrent-tracker"

const here = path.dirname(fileURLToPath(import.meta.url))
const web = path.resolve(here, "../..")
const PORT = Number(process.env.E2E_PORT || 5175)
const CHROMIUM = process.env.CHROMIUM || "/nix/var/nix/profiles/runtime/bin/chromium"
const MB = 1 << 20
const sleep = (ms) => new Promise((r) => setTimeout(r, ms))
const procs = []
const cleanup = () => procs.forEach((p) => { try { p.kill("SIGKILL") } catch {} })
process.on("exit", cleanup)
process.on("SIGINT", () => process.exit(130))

let failures = 0
const check = (ok, what, extra) => {
  console.log(`${ok ? "  ok  " : "  FAIL"} ${what}${extra !== undefined ? "  " + JSON.stringify(extra) : ""}`)
  if (!ok) failures++
}

// ---- tracker + vite + chromium
const tracker = new Server({ udp: false, http: false, ws: true, stats: false })
await new Promise((r) => tracker.listen(0, "127.0.0.1", r))
const tr = `ws://127.0.0.1:${tracker.ws.address().port}`
const vite = spawn(process.execPath, [path.join(web, "node_modules/vite/bin/vite.js"), "--port", String(PORT), "--host", "127.0.0.1", "--strictPort"], { cwd: web, stdio: ["ignore", "pipe", "pipe"] })
procs.push(vite)
await new Promise((res, rej) => {
  const t = setTimeout(() => rej(new Error("vite start timeout")), 30000)
  vite.stdout.on("data", (d) => /ready in|Local:/.test(String(d)) && (clearTimeout(t), res()))
  let err = ""
  vite.stderr.on("data", (d) => (err += d))
  vite.on("exit", (c) => rej(new Error("vite exited " + c + ": " + err)))
})
const dbgPort = 9300 + Math.floor(Math.random() * 600)
const prof = "/tmp/chunkstore-e2e-profile"
fs.rmSync(prof, { recursive: true, force: true })
const ch = spawn(CHROMIUM, ["--headless=new", "--no-sandbox", "--disable-gpu", `--remote-debugging-port=${dbgPort}`, `--user-data-dir=${prof}`, "--disable-features=WebRtcHideLocalIpsWithMdns", "about:blank"], { stdio: "ignore" })
procs.push(ch)
for (let i = 0; i < 80; i++) { await sleep(200); try { await fetch(`http://127.0.0.1:${dbgPort}/json/version`); break } catch {} }

async function page(url, tag) {
  const t = await (await fetch(`http://127.0.0.1:${dbgPort}/json/new?about:blank`, { method: "PUT" })).json()
  const ws = new WebSocket(t.webSocketDebuggerUrl)
  await new Promise((r) => (ws.onopen = r))
  let id = 0
  const pend = new Map()
  ws.onmessage = (m) => {
    const d = JSON.parse(m.data)
    if (d.id && pend.has(d.id)) { pend.get(d.id)(d); pend.delete(d.id) }
    else if (d.method === "Runtime.exceptionThrown") console.log(`  [${tag} exc]`, d.params.exceptionDetails?.exception?.description?.split("\n")[0])
    else if (d.method === "Runtime.consoleAPICalled" && ["error", "warning"].includes(d.params.type)) console.log(`  [${tag} ${d.params.type}]`, d.params.args.map((a) => a.value ?? a.description).join(" ").slice(0, 300))
  }
  const send = (method, params = {}) => new Promise((r) => { const i = ++id; pend.set(i, r); ws.send(JSON.stringify({ id: i, method, params })) })
  const ev = async (code) => {
    const d = await send("Runtime.evaluate", { expression: `(async () => { ${code} })()`, awaitPromise: true, returnByValue: true })
    if (d.result?.exceptionDetails) throw new Error(`[${tag}] ` + (d.result.exceptionDetails.exception?.description || JSON.stringify(d.result.exceptionDetails)))
    return d.result?.result?.value
  }
  await send("Runtime.enable")
  await send("Page.enable")
  await send("Page.navigate", { url })
  for (let i = 0; i < 100; i++) { await sleep(200); try { if (await ev("return !!window.lab?.ready")) break } catch {} }
  return { send, ev }
}

const qs = `?tr=${encodeURIComponent(tr)}`
const originB = `http://127.0.0.1:${PORT}`
const A = await page(`http://localhost:${PORT}/e2e/chunkstore/index.html${qs}&role=seed`, "A")
const B = await page(`${originB}/e2e/chunkstore/index.html${qs}&role=play`, "B")
const step = async (label, pg, code) => {
  const t0 = Date.now()
  const r = await pg.ev(code)
  console.log(`## ${label} (${Date.now() - t0} ms):`, JSON.stringify(r))
  return r
}

try {
  var deadline
  const seeds = {}
  for (const n of ["big", "small", "quota"]) seeds[n] = await A.ev(`return await lab.seed(${JSON.stringify(n)})`)
  console.log("seeded:", JSON.stringify(Object.fromEntries(Object.entries(seeds).map(([k, v]) => [k, { ih: v.ih, pieces: v.pieces }]))))
  deadline = setTimeout(() => { console.error("TIMEOUT"); cleanup(); process.exit(1) }, 300_000)
  const BUDGET = 6 * MB
  await B.ev(`localStorage.setItem("ot.cacheBudgetBytes", ${BUDGET})
    // 旧版 fsa-chunk-store 留下的目录与 chunks 文件（legacy，应最先清理）
    const root = await navigator.storage.getDirectory()
    const d = await root.getDirectoryHandle("Old Show - 12345678", { create: true })
    const w = await (await d.getFileHandle("Old Show.mkv", { create: true })).createWritable(); await w.write(new Uint8Array(1 << 20)); await w.close()
    const w2 = await (await root.getFileHandle("chunks", { create: true })).createWritable(); await w2.write(new Uint8Array(1000)); await w2.close()
    return true`)

  // ---- 1. 预算 6 MB 顺序读 30 MB
  const r1 = await step("1. play big (30 MB) with 6 MB budget, sequential paced read via SW", B, `
    const s = await lab.start(${JSON.stringify(seeds.big.magnet)})
    const t = lab.torrent(${JSON.stringify(seeds.big.ih)})
    const f = lab.FILES.big
    const r = await lab.read(s.fileURL(0), 0, f.len - 1, { pauseMs: 60, chunkBytes: 512 * 1024 })
    const ok = (await lab.sha(r.data)) === (await lab.sha(lab.rand(f.len, f.seed)))
    const o = await lab.opfs(${JSON.stringify(seeds.big.ih)})
    return { status: r.status, bytes: r.bytes, match: ok, budget: lab.pieceCache.budget, maxStored: r.maxStored, maxOpfs: r.maxOpfs, receivedMB: +(t.received / lab.MB).toFixed(2), opfsNow: o.dirs, evicted: lab.pieceCache.stats.evicted, quotaErrors: lab.pieceCache.stats.quotaErrors, selections: t._selections._items.length, logs: lab.slog.filter(l => /缓存|分块|窗口|附近/.test(l)) }`)
  check(r1.match && r1.bytes === 30 * MB + 12345, "bytes correct (sha)")
  check(r1.maxStored <= BUDGET, "accounted storage never exceeds the budget", { maxStored: r1.maxStored, budget: BUDGET })
  check(r1.maxOpfs <= BUDGET + 256 * 1024, "sampled OPFS bytes ≤ budget (+1 piece being deleted)", { maxOpfs: r1.maxOpfs })
  check(r1.receivedMB < 32, "no large re-download (received ≈ file size)", { receivedMB: r1.receivedMB })
  check(r1.evicted > 80, "pieces were evicted", { evicted: r1.evicted })
  const legacy = await B.ev(`return { rootNames: (await lab.opfs()).rootNames, logs: lab.slog.filter(l => /旧版/.test(l)) }`)
  check(legacy.rootNames.join() === "ot-pieces" && legacy.logs.length === 2, "legacy OPFS entries evicted first", legacy)

  // ---- 2. 重读被淘汰的区间
  const r2 = await step("2. re-read an evicted range (1..4 MB)", B, `
    const t = lab.torrent(${JSON.stringify(seeds.big.ih)}); const f = lab.FILES.big
    const had = t.bitfield.get(5); const r0 = t.received
    const r = await lab.read(lab.s.fileURL(0), 1 * lab.MB, 4 * lab.MB - 1)
    const ok = (await lab.sha(r.data)) === (await lab.sha(lab.rand(f.len, f.seed).subarray(1 * lab.MB, 4 * lab.MB)))
    return { hadPiece5Before: had, match: ok, redownloadedMB: +((t.received - r0) / lab.MB).toFixed(2), maxStored: r.maxStored }`)
  check(!r2.hadPiece5Before && r2.match && r2.redownloadedMB >= 3, "evicted range re-downloaded on demand, bytes correct", r2)
  check(r2.maxStored <= BUDGET, "still within budget", { maxStored: r2.maxStored })

  // ---- 3. 离开再进入：数据保留，启动 bitfield 续传
  const r3 = await step("3. leave (session.destroy) and re-enter big", B, `
    lab.s.destroy(); await new Promise(r => setTimeout(r, 800))
    const before = (await lab.opfs(${JSON.stringify(seeds.big.ih)})).dirs[${JSON.stringify(seeds.big.ih)}]
    const t0 = performance.now()
    const s = await lab.start(${JSON.stringify(seeds.big.magnet)})
    const t = lab.torrent(${JSON.stringify(seeds.big.ih)})
    await new Promise(r => t.ready ? r() : t.once('ready', r))
    let have = 0; for (let i = 0; i < t.pieces.length; i++) if (t.bitfield.get(i)) have++
    const res = { filesKeptAfterLeave: before?.files, verifiedAtReady: have, readyMs: Math.round(performance.now() - t0) }
    s.destroy(); await new Promise(r => setTimeout(r, 500))
    return res`)
  check(r3.filesKeptAfterLeave > 0 && r3.verifiedAtReady === r3.filesKeptAfterLeave, "data kept on leave; resumed from startup bitfield", r3)

  // ---- 4. 小文件：完整缓存后，做种者离开，再进入秒开 100%
  const r4 = await step("4. small file fully cached → seeder gone → re-enter is instant 100%", B, `
    const f = lab.FILES.small
    let s = await lab.start(${JSON.stringify(seeds.small.magnet)})
    let r = await lab.read(s.fileURL(0), 0, f.len - 1)
    const first = (await lab.sha(r.data)) === (await lab.sha(lab.rand(f.len, f.seed)))
    const t1 = lab.torrent(${JSON.stringify(seeds.small.ih)}); await new Promise(r => t1.done ? r() : t1.once('done', r))
    s.destroy(); await new Promise(r => setTimeout(r, 800))
    const bigAfter = (await lab.opfs()).dirs[${JSON.stringify(seeds.big.ih)}]
    return { first, bigDirAfterSmall: bigAfter ?? null, cacheLogs: lab.slog.filter(l => /较早|清理/.test(l)) }`)
  await A.ev(`await lab.unseed(${JSON.stringify(seeds.small.ih)}); return true`)
  const r4b = await step("4b. re-enter small (no seeder)", B, `
    const f = lab.FILES.small
    const t0 = performance.now()
    const s = await lab.start(${JSON.stringify(seeds.small.magnet)}, ${JSON.stringify(seeds.small.torrentFile)})
    const t = lab.torrent(${JSON.stringify(seeds.small.ih)})
    await new Promise(r => t.ready ? r() : t.once('ready', r))
    const readyMs = Math.round(performance.now() - t0)
    const progressAtReady = t.progress
    const r = await lab.read(s.fileURL(0), 0, f.len - 1)
    const ok = (await lab.sha(r.data)) === (await lab.sha(lab.rand(f.len, f.seed)))
    const res = { readyMs, progressAtReady, match: ok, received: t.received, wires: t.wires.length }
    s.destroy(); await new Promise(r => setTimeout(r, 500))
    return res`)
  check(r4.first, "small file read correctly")
  check(r4b.progressAtReady === 1 && r4b.match && r4b.received === 0, "re-enter: 100% from cache, nothing downloaded", r4b)

  // ---- 5. 第二个节点从 B 下载剩余的全部 piece（B 有一部分 piece，其余被淘汰过）
  const r5a = await step("5a. B opens big again and reads 10..14 MB (keeps a known set of pieces)", B, `
    const s = await lab.start(${JSON.stringify(seeds.big.magnet)})
    const t = lab.torrent(${JSON.stringify(seeds.big.ih)})
    await lab.read(s.fileURL(0), 10 * lab.MB, 14 * lab.MB - 1)
    await new Promise(r => setTimeout(r, 500))
    let have = 0; for (let i = 0; i < t.pieces.length; i++) if (t.bitfield.get(i)) have++
    return { bHas: have, of: t.pieces.length, evictedTotal: lab.pieceCache.stats.evicted }`)
  await A.ev(`await lab.unseed(${JSON.stringify(seeds.big.ih)}); return true`)
  const r5 = await step("5b. peer C (stock WebTorrent, page A) joins with only B as a source", A, `return await lab.leech(${JSON.stringify(seeds.big.magnet)}, 40000)`)
  const r5c = await B.ev(`const t = lab.torrent(${JSON.stringify(seeds.big.ih)}); let have = 0; for (let i = 0; i < t.pieces.length; i++) if (t.bitfield.get(i)) have++; return { bHasNow: have, uploadedMB: +(t.uploaded / lab.MB).toFixed(2) }`)
  console.log("   B after:", JSON.stringify(r5c))
  check(r5.have === r5c.bHasNow && r5.have > 0, "C downloaded every piece B has", { cHave: r5.have, bHas: r5c.bHasNow })
  check(r5.rejects < 20 && r5.closedWires === 0, "no reject loop, connection kept", { rejects: r5.rejects, closed: r5.closedWires })

  // ---- 6. QuotaExceeded：真实配额比预算小，种子存活、数据正确
  await B.ev(`lab.s.destroy(); await new Promise(r => setTimeout(r, 500)); localStorage.setItem("ot.cacheBudgetBytes", ${64 * MB}); await lab.pieceCache.refresh(); return true`)
  const usage = await B.ev(`return (await navigator.storage.estimate()).usage`)
  await B.send("Storage.overrideQuotaForOrigin", { origin: originB, quotaSize: usage + 4 * MB })
  const r6 = await step("6. quota = usage + 4 MB, budget 64 MB, 12 MB file downloaded whole", B, `
    const f = lab.FILES.quota
    const s = await lab.start(${JSON.stringify(seeds.quota.magnet)})
    const t = lab.torrent(${JSON.stringify(seeds.quota.ih)})
    const t0 = Date.now(); while (!t.done && !t.destroyed && !lab.slog.some(l => /附近/.test(l)) && Date.now() - t0 < 30000) await new Promise(r => setTimeout(r, 200))
    const r = await lab.read(s.fileURL(0), 0, f.len - 1)
    const ok = (await lab.sha(r.data)) === (await lab.sha(lab.rand(f.len, f.seed)))
    return { destroyed: t.destroyed, done: t.done, match: ok, quotaErrors: lab.pieceCache.stats.quotaErrors, memFallbacks: lab.pieceCache.stats.memFallbacks, budgetNowMB: +(lab.pieceCache.budget / lab.MB).toFixed(2), storedMB: +(lab.pieceCache.stored / lab.MB).toFixed(2), logs: lab.slog.filter(l => /配额|预算|内存|error/.test(l)).slice(-5) }`)
  check(!r6.destroyed && r6.match && r6.quotaErrors > 0, "QuotaExceeded: torrent alive, bytes correct, budget shrunk", r6)

  // ---- 7. 多 tab：同一 origin 的第二个页面打开同一种子 → 临时目录，关闭后清理
  await B.send("Storage.overrideQuotaForOrigin", { origin: originB })
  const B2 = await page(`${originB}/e2e/chunkstore/index.html${qs}&role=play`, "B2")
  const r7 = await step("7. second tab opens the torrent that tab B is playing", B2, `
    const s = await lab.start(${JSON.stringify(seeds.quota.magnet)})
    const f = lab.FILES.quota
    const r = await lab.read(s.fileURL(0), 0, 2 * lab.MB - 1)
    const ok = (await lab.sha(r.data)) === (await lab.sha(lab.rand(f.len, f.seed).subarray(0, 2 * lab.MB)))
    const dirsOpen = Object.keys((await lab.opfs()).dirs)
    s.destroy(); await new Promise(r => setTimeout(r, 800))
    const dirsAfter = Object.keys((await lab.opfs()).dirs)
    return { match: ok, dirsOpen, dirsAfter, logs: lab.slog.filter(l => /其他标签页/.test(l)) }`)
  check(r7.logs.length === 1, "second tab logs the temporary cache")
  const ih = seeds.quota.ih
  check(r7.match && r7.dirsOpen.some((d) => d.startsWith(ih + "~")) && !r7.dirsAfter.some((d) => d.startsWith(ih + "~")) && r7.dirsAfter.includes(ih), "second tab used a temporary dir, removed on close; tab B's dir untouched", r7)
} catch (e) {
  console.error("ERROR", e)
  failures++
} finally {
  clearTimeout(deadline)
  cleanup()
  tracker.close()
}
console.log(failures ? `\n${failures} check(s) FAILED` : "\nall checks passed")
process.exit(failures ? 1 : 0)
