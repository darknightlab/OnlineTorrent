// 播放器外观（e2e/edge/run.mjs 的 EXTRA）：亮 / 暗主题 × 1280 / 1024 宽：
//  - 圆角：放大截取播放器左上 / 右下角，曲线外侧 1 px 的环上不能有明显变暗（双重抗锯齿的黑边）
// 两侧黑线见 sweep.mjs。截图保存在运行目录（corner-*.png）。
import fs from "node:fs"
import { decodePNG } from "../subtitles/checks.mjs"

const lum = (px, i, bpp) => 0.2126 * px[i * bpp] + 0.7152 * px[i * bpp + 1] + 0.0722 * px[i * bpp + 2]

export default async function ({ ev, send, sleep, RUN }) {
  const r = {}
  const DPR = Number(process.env.DPR || 1)
  await ev(`document.querySelector("video")?.play().catch(() => {}); return 1`)
  for (const theme of ["light", "dark"]) {
    for (const width of (process.env.WIDTHS || "1280,1024").split(",").map(Number)) {
      await send("Emulation.setEmulatedMedia", { features: [{ name: "prefers-color-scheme", value: theme }] })
      await send("Emulation.setDeviceMetricsOverride", { width, height: 900, deviceScaleFactor: DPR, mobile: false })
      await sleep(1500)
      const box = await ev(`window.scrollTo(0, 0)
        const v = document.querySelector("video"); const el = v?.closest(".ot-player-frame")
        if (!el) return null
        // the visible video area = the aspect box the video sits in (the video itself may overscan by 1px)
        const b = el.getBoundingClientRect(); const vb = (v.closest(".aspect-video") ?? v).getBoundingClientRect()
        const cs = getComputedStyle(el)
        return { x: b.x, y: b.y, w: b.width, h: b.height, radius: parseFloat(cs.borderTopLeftRadius) || parseFloat((cs.clipPath.match(/round ([\\d.]+)px/) || [])[1]) || 12,
                 page: getComputedStyle(document.body).backgroundColor, fit: getComputedStyle(v).objectFit, vx: vb.x, vw: vb.width, vy: vb.y, vh: vb.height, vr: v.videoWidth / v.videoHeight }`)
      if (!box) {
        r[`${theme} ${width}: player frame found`] = false
        continue
      }
      const label = `${theme} ${width}${DPR !== 1 ? ` @${DPR}x` : ""}`
      // corners: 3× zoom of a 20×20 css-px square at the top-left and bottom-right
      const ZOOM = 3
      let worst = 0
      for (const [cx, cy, sx, sy] of [[box.x, box.y, 1, 1], [box.x + box.w, box.y + box.h, -1, -1]]) {
        const clip = { x: sx > 0 ? cx - 4 : cx - 16, y: sy > 0 ? cy - 4 : cy - 16, width: 20, height: 20, scale: ZOOM }
        const shot = await send("Page.captureScreenshot", { format: "png", clip, captureBeyondViewport: true })
        const png = Buffer.from(shot.result.data, "base64")
        fs.writeFileSync(`${RUN}/corner-${theme}-${width}-${sx > 0 ? "tl" : "br"}.png`, png)
        const { w, h, bpp, px } = decodePNG(png)
        const S = w / clip.width // output pixels per css px (clip scale × device pixel ratio)
        // page background luminance: the far corner of the clip (outside the player)
        const bgL = lum(px, sx > 0 ? 0 : w * h - 1, bpp)
        // circle centre of the rounded corner, in clip pixels
        const ccx = (sx > 0 ? cx + box.radius - clip.x : cx - box.radius - clip.x) * S
        const ccy = (sy > 0 ? cy + box.radius - clip.y : cy - box.radius - clip.y) * S
        for (let y = 0; y < h; y++)
          for (let x = 0; x < w; x++) {
            // only the corner quadrant, ring just outside the curve (r+0.5 … r+1.5 css px)
            const dx = (x + 0.5 - ccx) * sx * -1, dy = (y + 0.5 - ccy) * sy * -1
            if (dx < 0 || dy < 0) continue
            const d = Math.hypot(dx, dy) / S
            if (d < box.radius + 0.5 || d > box.radius + 1.5) continue
            worst = Math.max(worst, Math.abs(bgL - lum(px, y * w + x, bpp)))
          }
      }
      console.log(`${label}: frame ${box.w.toFixed(2)}×${box.h.toFixed(2)} at x=${box.x.toFixed(2)}; corner ring max deviation from page bg ${worst.toFixed(0)} (0–255); video ${box.vw.toFixed(2)}px, object-fit ${box.fit}, ratio ${box.vr.toFixed(4)} vs box ${(box.vw / box.vh).toFixed(4)}`)
      r[`${label}: no dark fringe outside the rounded corners`] = worst < 40
    }
  }
  await send("Emulation.clearDeviceMetricsOverride")
  return r
}
