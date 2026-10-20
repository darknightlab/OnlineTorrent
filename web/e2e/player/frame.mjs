// 播放框与字幕画布（e2e/edge/run.mjs 的 EXTRA，媒体用带 ASS 的 MKV：/tmp/subs-e2e/test.mkv）：
// 双列布局（宽屏）里显示字幕时：
//  - 视频与字幕画布的祖先上没有圆角裁剪 / clip-path / mask / filter / opacity（合成器会把它们画进中间纹理，
//    硬件覆盖层显示的视频——Windows 上硬件解码的 10-bit HEVC——因此整块变黑）；
//  - 画布透明、与视频内容区重合，视频照常可见；圆角外没有黑边；网页全屏铺满窗口；页头盖在播放器上面；
//  - 「字幕」控件没有悬停提示；手机上同样正常。
import fs from "node:fs"
import { decodePNG } from "../subtitles/checks.mjs"

const lum = (px, i, bpp) => 0.2126 * px[i * bpp] + 0.7152 * px[i * bpp + 1] + 0.0722 * px[i * bpp + 2]

export default async function ({ ev, send, sleep, RUN }) {
  const r = {}
  const shotPNG = async (name, clip) => {
    const s = await send("Page.captureScreenshot", { format: "png", ...(clip ? { clip: { ...clip, scale: clip.scale ?? 1 } } : {}) })
    const png = Buffer.from(s.result.data, "base64")
    fs.writeFileSync(`${RUN}/${name}.png`, png)
    return decodePNG(png)
  }
  const layout = async (width, height = 900, mobile = false) => {
    await send("Emulation.setDeviceMetricsOverride", { width, height, deviceScaleFactor: mobile ? 2 : 1, mobile })
    await sleep(1500)
  }
  // a frame with a cue on screen (test.mkv: a cue every 6 s, 2–7 s, 8–13 s …), paused
  const atCue = async () => {
    await ev(`const v = document.querySelector("video"); v.pause(); v.currentTime = 50; await new Promise((r) => v.addEventListener("seeked", r, { once: true })); return 1`)
    await sleep(2500)
  }
  const inspect = () =>
    ev(`const v = document.querySelector("video"); const c = document.querySelector("canvas.ot-subtitles")
      const bad = []
      for (const el of [v, c]) {
        for (let a = el?.parentElement; a && a !== document.documentElement; a = a.parentElement) {
          const s = getComputedStyle(a)
          const rounded = s.overflow !== "visible" && parseFloat(s.borderTopLeftRadius) > 0
          if (s.clipPath !== "none" || s.maskImage !== "none" || s.filter !== "none" || Number(s.opacity) < 1 || rounded)
            bad.push((a.className || a.tagName).toString().slice(0, 40) + ": " + [s.clipPath !== "none" && "clip-path", s.maskImage !== "none" && "mask", s.filter !== "none" && "filter", Number(s.opacity) < 1 && "opacity", rounded && "rounded overflow"].filter(Boolean).join(","))
        }
      }
      const vb = v.getBoundingClientRect(); const cb = c?.getBoundingClientRect()
      // the video's content box (object-fit: contain)
      const ar = v.videoWidth / v.videoHeight, w = Math.min(vb.width, vb.height * ar), h = w / ar
      const content = { x: vb.x + (vb.width - w) / 2, y: vb.y + (vb.height - h) / 2, w, h }
      return { bad: [...new Set(bad)], canvas: cb ? { x: cb.x, y: cb.y, w: cb.width, h: cb.height, bg: getComputedStyle(c).backgroundColor } : null, content, video: { x: vb.x, y: vb.y, w: vb.width, h: vb.height } }`)
  const fits = (i) => i.canvas && Math.abs(i.canvas.x - i.content.x) < 1.5 && Math.abs(i.canvas.y - i.content.y) < 1.5 && Math.abs(i.canvas.w - i.content.w) < 1.5 && Math.abs(i.canvas.h - i.content.h) < 1.5
  // share of very dark pixels in the video area, outside the bottom third (subtitles / controls)
  const darkShare = async (name, v) => {
    const img = await shotPNG(name, { x: v.x, y: v.y, width: v.w, height: v.h * 0.6 })
    let d = 0
    for (let i = 0; i < img.w * img.h; i++) if (lum(img.px, i, img.bpp) < 20) d++
    return d / (img.w * img.h)
  }

  // ---- two columns (lg+), subtitles on
  for (const width of [1366, 1600]) {
    await layout(width)
    await atCue()
    const i = await inspect()
    const dark = await darkShare(`frame-${width}`, i.video)
    console.log(`${width}: ${JSON.stringify(i)} dark ${(dark * 100).toFixed(1)}%`)
    r[`${width}: no clip-path / rounded clip / mask / filter / opacity above the video and the subtitle canvas`] = i.bad.length === 0
    r[`${width}: subtitle canvas transparent, on the video's content box`] = fits(i) && i.canvas.bg === "rgba(0, 0, 0, 0)"
    r[`${width}: video visible under the subtitles`] = dark < 0.2
  }

  // corners: the ring just outside the rounded corner matches the page background (light + dark)
  await layout(1366)
  for (const theme of ["light", "dark"]) {
    await send("Emulation.setEmulatedMedia", { features: [{ name: "prefers-color-scheme", value: theme }] })
    await sleep(800)
    const b = await ev(`window.scrollTo(0, 0); const f = document.querySelector(".ot-player-frame").getBoundingClientRect(); return { x: f.x, y: f.y, w: f.width, h: f.height }`)
    const R = 12
    let worst = 0
    for (const [cx, cy, sx, sy] of [[b.x, b.y, 1, 1], [b.x + b.w, b.y + b.h, -1, -1]]) {
      const clip = { x: sx > 0 ? cx - 4 : cx - 16, y: sy > 0 ? cy - 4 : cy - 16, width: 20, height: 20, scale: 3 }
      const img = await shotPNG(`corner-${theme}-${sx > 0 ? "tl" : "br"}`, clip)
      const S = img.w / clip.width
      const bgL = lum(img.px, sx > 0 ? 0 : img.w * img.h - 1, img.bpp)
      const ccx = (sx > 0 ? cx + R - clip.x : cx - R - clip.x) * S
      const ccy = (sy > 0 ? cy + R - clip.y : cy - R - clip.y) * S
      for (let y = 0; y < img.h; y++)
        for (let x = 0; x < img.w; x++) {
          const dx = (x + 0.5 - ccx) * -sx, dy = (y + 0.5 - ccy) * -sy
          if (dx < 0 || dy < 0) continue
          const d = Math.hypot(dx, dy) / S
          if (d < R + 0.5 || d > R + 1.5) continue
          worst = Math.max(worst, Math.abs(bgL - lum(img.px, y * img.w + x, img.bpp)))
        }
    }
    console.log(`corners ${theme}: ring deviation ${worst.toFixed(0)}`)
    r[`corners (${theme}): rounded, no dark fringe`] = worst < 40
  }
  await send("Emulation.setEmulatedMedia", { features: [] })

  // header above the player when scrolled under it
  const hdr = await ev(`document.body.style.minHeight = "300vh"; const f = document.querySelector(".ot-player-frame"); const h = document.querySelector("header"); window.scrollTo(0, f.getBoundingClientRect().top + scrollY - 20); await new Promise((r) => setTimeout(r, 300))
    const hb = h.getBoundingClientRect(); const fb = f.getBoundingClientRect()
    const x = fb.x + fb.width / 2, y = hb.bottom - 5
    const top = document.elementFromPoint(x, y); return { overlap: fb.top < hb.bottom, inHeader: h.contains(top), what: top?.className?.toString().slice(0, 40) }`)
  console.log("header:", JSON.stringify(hdr))
  r["header stays above the player when scrolled"] = hdr.overlap && hdr.inHeader
  await ev(`document.body.style.minHeight = ""; window.scrollTo(0, 0); return 1`)

  // web fullscreen covers the window
  await send("Runtime.evaluate", { expression: `document.querySelector(".art-control-fullscreenWeb")?.click()`, userGesture: true })
  await sleep(1200)
  const fsw = await ev(`const p = document.querySelector(".art-video-player"); const b = p.getBoundingClientRect(); const pts = [[3, 3], [innerWidth - 3, innerHeight - 3], [innerWidth - 3, 3], [3, innerHeight / 2]]
    return { web: p.classList.contains("art-fullscreen-web"), rect: [b.x, b.y, b.width, b.height], covered: pts.every(([x, y]) => p.contains(document.elementFromPoint(x, y))), vw: innerWidth, vh: innerHeight }`)
  const full = await shotPNG("webfullscreen")
  // 16:9 video in a 1366×900 window: the letterbox bar at the top is the player's black, not the page
  const edge = lum(full.px, 10 * full.w + Math.floor(full.w / 2), full.bpp)
  console.log("web fullscreen:", JSON.stringify(fsw), "top bar luminance", edge.toFixed(0))
  r["web fullscreen covers the whole window"] = fsw.web && fsw.covered && fsw.rect[2] >= fsw.vw - 1 && fsw.rect[3] >= fsw.vh - 1 && edge < 30
  await send("Runtime.evaluate", { expression: `document.querySelector(".art-control-fullscreenWeb")?.click()`, userGesture: true })
  await sleep(800)

  // tooltips: none on 字幕; 倍速 / 资源 reported
  const tips = await ev(`const q = (n) => document.querySelector(".art-control-" + n); return { subtitle: q("subtitle")?.getAttribute("aria-label") ?? null, rate: q("rate")?.getAttribute("aria-label") ?? null, panel: document.querySelector(".art-control-panel, .art-control-sources")?.getAttribute("aria-label") ?? null }`)
  // hover 倍速: its menu and its hint both open above the control
  const rateBox = await ev(`const b = document.querySelector(".art-control-rate").getBoundingClientRect(); return { x: b.x + b.width / 2, y: b.y + b.height / 2 }`)
  await send("Input.dispatchMouseEvent", { type: "mouseMoved", x: rateBox.x, y: rateBox.y })
  await sleep(600)
  const rateMenu = await ev(`const l = document.querySelector(".art-control-rate .art-selector-list"); const s = l && getComputedStyle(l); const b = l?.getBoundingClientRect(); const c = document.querySelector(".art-control-rate").getBoundingClientRect(); return { menuVisible: !!l && s.display !== "none" && s.visibility !== "hidden" && b.height > 0, menuBottom: b?.bottom, controlTop: c.top }`)
  await shotPNG("hover-rate", { x: rateBox.x - 120, y: rateBox.y - 260, width: 240, height: 290 })
  const subBox = await ev(`const b = document.querySelector(".art-control-subtitle").getBoundingClientRect(); return { x: b.x + b.width / 2, y: b.y + b.height / 2 }`)
  await send("Input.dispatchMouseEvent", { type: "mouseMoved", x: subBox.x, y: subBox.y })
  await sleep(600)
  await shotPNG("hover-subtitle", { x: subBox.x - 120, y: subBox.y - 260, width: 240, height: 290 })
  console.log("tooltips:", JSON.stringify(tips), "rate menu on hover:", JSON.stringify(rateMenu))
  r["「字幕」 has no tooltip"] = tips.subtitle === null
  r["「倍速」 has no tooltip (it covered its own menu)"] = tips.rate === null

  // phone
  await layout(390, 844, true)
  await atCue()
  const m = await inspect()
  const mDark = await darkShare("frame-phone", m.video)
  const corners = await ev(`return getComputedStyle(document.querySelector(".ot-frame-corners")).display`)
  console.log(`phone: ${JSON.stringify(m)} dark ${(mDark * 100).toFixed(1)}% corners ${corners}`)
  r["phone: canvas on the video's content box, video visible, no corner mask"] = fits(m) && mDark < 0.2 && corners === "none"
  await send("Emulation.clearDeviceMetricsOverride")
  return r
}
