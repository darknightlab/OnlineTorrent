// 播放器两侧黑线（e2e/edge/run.mjs 的 EXTRA）：逐个视口宽度（SWEEP=1000:1600:1，DPR=1 / 1.25，亮色主题）
// 暂停在明亮的一帧上，检查播放框左右边缘的每一列设备像素：比页面背景和画面内侧都暗得多的一列 = 黑线。
// 截图按设备像素对齐（不重采样）。报告出现黑线的宽度数。
import { decodePNG } from "../subtitles/checks.mjs"

const lum = (px, i, bpp) => 0.2126 * px[i * bpp] + 0.7152 * px[i * bpp + 1] + 0.0722 * px[i * bpp + 2]

export default async function ({ ev, send, sleep }) {
  const DPR = Number(process.env.DPR || 1)
  const [from, to, step] = (process.env.SWEEP || "1000:1600:1").split(":").map(Number)
  await send("Emulation.setEmulatedMedia", { features: [{ name: "prefers-color-scheme", value: process.env.THEME || "light" }] })
  // a bright frame, paused
  await ev(`const v = document.querySelector("video"); v.currentTime = Number(${JSON.stringify(process.env.FRAME_AT || "12")}); v.pause(); return new Promise((r) => v.addEventListener("seeked", () => r(1), { once: true }))`)
  await sleep(800)
  const bad = []
  const badRows = []
  let checked = 0
  for (let width = from; width <= to; width += step) {
    await send("Emulation.setDeviceMetricsOverride", { width, height: 900, deviceScaleFactor: DPR, mobile: false })
    const b = await ev(`await new Promise((r) => requestAnimationFrame(() => requestAnimationFrame(r)))
      window.scrollTo(0, 0)
      const v = document.querySelector("video"); const box = v?.closest(".aspect-video")
      if (!box) return null
      const r = box.getBoundingClientRect()
      return { x: r.x, y: r.y, w: r.width, h: r.height, vw: document.documentElement.clientWidth }`)
    if (!b || (b.x < 6 && b.x + b.w > b.vw - 6)) continue
    checked++
    const sides = []
    // edges at the viewport bounds (full-bleed layout below lg) can't be sampled on both sides
    for (const edge of [b.x, b.x + b.w].filter((e) => e >= 6 && e <= b.vw - 6)) {
      // 10 device px around the edge, aligned to the device-pixel grid
      const d0 = Math.floor(edge * DPR) - 5
      const clip = { x: d0 / DPR, y: b.y + b.h / 3, width: 10 / DPR, height: b.h / 3, scale: 1 }
      const shot = await send("Page.captureScreenshot", { format: "png", clip, captureBeyondViewport: true })
      const { w, h, bpp, px } = decodePNG(Buffer.from(shot.result.data, "base64"))
      const colLum = (c) => {
        let s = 0
        for (let y = 0; y < h; y++) s += lum(px, y * w + c, bpp)
        return s / h
      }
      const cols = Array.from({ length: w }, (_, c) => colLum(c))
      const left = edge === b.x
      const page = left ? cols[0] : cols[w - 1]
      const inside = left ? cols[w - 1] : cols[0]
      // a column darker than both the page and the picture by a visible margin
      const darkest = Math.min(...cols)
      sides.push({ dark: darkest < Math.min(page, inside) - 40, cols: cols.map((x) => Math.round(x)) })
    }
    // top / bottom: 10 device rows around each horizontal edge, middle third of the width
    const rows = []
    for (const edge of [b.y, b.y + b.h].filter((e) => e >= 6)) {
      const d0 = Math.floor(edge * DPR) - 5
      const shot = await send("Page.captureScreenshot", { format: "png", clip: { x: b.x + b.w / 3, y: d0 / DPR, width: b.w / 3, height: 10 / DPR, scale: 1 }, captureBeyondViewport: true })
      const { w, h, bpp, px } = decodePNG(Buffer.from(shot.result.data, "base64"))
      const rl = Array.from({ length: h }, (_, y) => { let t = 0; for (let x = 0; x < w; x++) t += lum(px, y * w + x, bpp); return t / w })
      const top = edge === b.y
      rows.push(Math.min(...rl) < Math.min(top ? rl[0] : rl[h - 1], top ? rl[h - 1] : rl[0]) - 40)
    }
    if (rows.some(Boolean)) badRows.push(width)
    if (sides.some((s) => s.dark)) bad.push({ width, x: +b.x.toFixed(3), w: +b.w.toFixed(3), cols: sides.map((s) => s.cols.join(" ")) })
  }
  await send("Emulation.clearDeviceMetricsOverride")
  console.log(`sweep ${from}–${to} px @${DPR}x: ${bad.length} of ${checked} widths with a dark line at the frame's sides`)
  console.log(`  top/bottom: ${badRows.length} widths with a dark row${badRows.length ? ` (${badRows.slice(0, 12).join(", ")}…)` : ""}`)
  for (const x of bad.slice(0, 8)) console.log("  ", JSON.stringify(x))
  return { [`no black side lines at any width ${from}–${to} @${DPR}x`]: bad.length === 0, [`no black top/bottom lines @${DPR}x`]: badRows.length === 0 }
}
