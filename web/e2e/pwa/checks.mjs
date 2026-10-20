// PWA（e2e/edge/run.mjs 的 EXTRA，需 FRONT_DIR）：可安装、Service Worker 接管、第一次访问即可播放（基础检查）、
// 更新流程（播放中换上新构建 → 提示「新版本已准备好」→ 流不中断 → 点「刷新」→ 新版本）、离线打开应用壳。
//   构建 A（VITE_BUILD_ID=A）复制到 FRONT_DIR，构建 B（VITE_BUILD_ID=B）放在 PWA_NEXT_DIR
//   PWA_COLD=1 FRONT_DIR=… PWA_NEXT_DIR=… EXTRA=e2e/pwa/checks.mjs node e2e/edge/run.mjs 20
import fs from "node:fs"
import path from "node:path"

export default async function ({ ev, send, sleep, SITE, IH, RUN }) {
  const r = {}
  const FRONT = process.env.FRONT_DIR
  const NEXT = process.env.PWA_NEXT_DIR
  // installability: manifest + a service worker controlling the page
  const ctl = await ev(`const reg = await navigator.serviceWorker.getRegistration(); return { controlled: !!navigator.serviceWorker.controller, script: navigator.serviceWorker.controller?.scriptURL ?? null, active: reg?.active?.scriptURL ?? null, build: document.documentElement.dataset.build ?? null }`)
  const man = await send("Page.getAppManifest")
  const inst = await send("Page.getInstallabilityErrors")
  const errors = inst.result?.installabilityErrors ?? inst.error
  console.log("sw:", JSON.stringify(ctl), "manifest:", man.result?.url, "errors:", JSON.stringify(man.result?.errors ?? []), "installability:", JSON.stringify(errors))
  r["page controlled by /sw.js (claimed on first visit)"] = ctl.controlled && /\/sw\.js$/.test(ctl.script)
  r["manifest parsed without errors"] = !!man.result?.url && !(man.result?.errors ?? []).length
  r["installable (no installability errors)"] = Array.isArray(errors) && errors.length === 0
  r["running build A"] = ctl.build === "A"

  // update while playing
  const playing = () => ev(`const v = document.querySelector("video"); return v ? { t: v.currentTime, paused: v.paused, rs: v.readyState } : null`)
  await ev(`document.querySelector("video")?.play().catch(() => {}); return 1`)
  await sleep(2000)
  const before = await playing()
  for (const f of fs.readdirSync(FRONT)) fs.rmSync(path.join(FRONT, f), { recursive: true, force: true })
  fs.cpSync(NEXT, FRONT, { recursive: true })
  await ev(`const reg = await navigator.serviceWorker.getRegistration(); await reg.update(); return 1`)
  let prompt = false
  for (let i = 0; i < 40 && !prompt; i++) {
    await sleep(500)
    prompt = await ev(`return [...document.querySelectorAll("[role=status]")].some((e) => e.textContent.includes("新版本已准备好"))`)
  }
  const promptInfo = await ev(`const el = [...document.querySelectorAll("[role=status]")].find((e) => e.textContent.includes("新版本已准备好")); if (!el) return null; const b = el.getBoundingClientRect(); return { text: el.textContent.trim(), buttons: [...el.querySelectorAll("button")].map((x) => x.textContent.trim()), shadow: getComputedStyle(el).boxShadow, bottom: innerHeight - b.bottom }`)
  const waiting = await ev(`const reg = await navigator.serviceWorker.getRegistration(); return { waiting: !!reg.waiting, controller: navigator.serviceWorker.controller?.scriptURL }`)
  console.log("prompt:", JSON.stringify(promptInfo), JSON.stringify(waiting))
  await send("Page.captureScreenshot", { format: "png" }).then((s) => fs.writeFileSync(`${RUN}/pwa-prompt.png`, Buffer.from(s.result.data, "base64")))
  // phone (above the bottom tab bar), dark theme
  await send("Emulation.setDeviceMetricsOverride", { width: 390, height: 844, deviceScaleFactor: 2, mobile: true })
  await send("Emulation.setEmulatedMedia", { features: [{ name: "prefers-color-scheme", value: "dark" }] })
  await sleep(800)
  const phone = await ev(`const el = [...document.querySelectorAll("[role=status]")].find((e) => e.textContent.includes("新版本已准备好")); const tab = document.querySelector('nav[aria-label="底部导航"]'); return el && tab ? { promptBottom: el.getBoundingClientRect().bottom, tabTop: tab.getBoundingClientRect().top, width: el.getBoundingClientRect().width } : null`)
  await send("Page.captureScreenshot", { format: "png" }).then((s) => fs.writeFileSync(`${RUN}/pwa-prompt-phone.png`, Buffer.from(s.result.data, "base64")))
  await send("Emulation.clearDeviceMetricsOverride")
  await send("Emulation.setEmulatedMedia", { features: [] })
  console.log("phone:", JSON.stringify(phone))
  r["phone: prompt above the bottom tab bar"] = !!phone && phone.promptBottom <= phone.tabTop
  r["update detected: 「新版本已准备好」 with 稍后 / 刷新"] = prompt && JSON.stringify(promptInfo?.buttons) === JSON.stringify(["稍后", "刷新"])
  r["prompt has no shadow"] = promptInfo?.shadow === "none"
  r["new worker waits (old one still controls)"] = waiting.waiting
  // the stream keeps going while the update waits (seek forward too: needs new /webtorrent/ requests)
  await ev(`const v = document.querySelector("video"); v.currentTime = Math.min(v.duration - 20, v.currentTime + 30); v.play().catch(() => {}); return 1`)
  await sleep(1500)
  const mid = await playing()
  await sleep(6000)
  const after = await playing()
  console.log("playback while the update waits:", JSON.stringify({ before, mid, after }))
  r["stream not broken while the update waits (plays on, also after a seek)"] = after && !after.paused && after.t - mid.t > 3 && after.rs >= 3

  // 刷新 → the new version controls and runs
  await ev(`[...document.querySelectorAll("[role=status] button")].find((b) => b.textContent.trim() === "刷新")?.click(); return 1`)
  let build = null
  for (let i = 0; i < 40; i++) {
    await sleep(500)
    build = await ev(`return document.readyState === "complete" ? (document.documentElement.dataset.build ?? null) : null`).catch(() => null)
    if (build === "B") break
  }
  await sleep(3000)
  const after2 = await ev(`const reg = await navigator.serviceWorker.getRegistration(); return { build: document.documentElement.dataset.build, waiting: !!reg?.waiting, controlled: !!navigator.serviceWorker.controller, prompt: document.body.textContent.includes("新版本已准备好") }`)
  console.log("after 刷新:", JSON.stringify(after2))
  r["刷新 → build B, no prompt, still controlled"] = after2.build === "B" && !after2.waiting && after2.controlled && !after2.prompt
  await sleep(8000)
  const p2 = await playing()
  console.log("playback after 刷新:", JSON.stringify(p2))
  r["playback works again after the update"] = !!p2 && p2.rs >= 2 && p2.t > 0

  // offline: the app shell from the precache
  await send("Network.emulateNetworkConditions", { offline: true, latency: 0, downloadThroughput: -1, uploadThroughput: -1 })
  await send("Page.navigate", { url: `${SITE}/subject/bgm/1` })
  await sleep(3000)
  const off = await ev(`return { build: document.documentElement.dataset.build, header: !!document.querySelector("header"), text: document.body.innerText.slice(0, 80) }`)
  await send("Network.emulateNetworkConditions", { offline: false, latency: 0, downloadThroughput: -1, uploadThroughput: -1 })
  console.log("offline:", JSON.stringify(off))
  r["offline: app shell loads (SPA route from the precache)"] = off?.build === "B" && off.header
  void IH
  return r
}
