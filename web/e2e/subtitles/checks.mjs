// 内封字幕的浏览器检查（由 e2e/edge/run.mjs 在播放开始后调用，EXTRA=e2e/subtitles/checks.mjs）。
// 媒体：带两条 ASS（简体 = 洋红色字、繁体 = 青色字，置顶）+ 一条 SRT + CJK 字体附件的 MKV，
// 每 6 秒一句（2–7 s、8–13 s …），见 e2e/subtitles/make.sh。
//   EXPECT=zh-Hans（默认，浏览器 zh-CN）：默认显示简体；拖动后仍有字幕；切换到繁体；关闭；全屏时仍显示
//   EXPECT=en（浏览器 en-US）：默认选 English（SRT 轨道）
//   EXPECT=off（浏览器 fr-FR，没有法语轨道）：默认不显示，也不加载 JASSUB
import fs from "node:fs"
import zlib from "node:zlib"

/** 最小 PNG 解码（8 位 RGB / RGBA，非隔行） */
export function decodePNG(buf) {
  let pos = 8
  let w = 0, h = 0, ct = 0
  const idat = []
  while (pos < buf.length) {
    const len = buf.readUInt32BE(pos)
    const type = buf.toString("ascii", pos + 4, pos + 8)
    const data = buf.subarray(pos + 8, pos + 8 + len)
    if (type === "IHDR") {
      w = data.readUInt32BE(0)
      h = data.readUInt32BE(4)
      ct = data[9]
    } else if (type === "IDAT") idat.push(data)
    pos += 12 + len
  }
  const bpp = ct === 6 ? 4 : 3
  const raw = zlib.inflateSync(Buffer.concat(idat))
  const out = Buffer.alloc(w * h * bpp)
  const stride = w * bpp
  for (let y = 0; y < h; y++) {
    const f = raw[y * (stride + 1)]
    const line = raw.subarray(y * (stride + 1) + 1, (y + 1) * (stride + 1))
    for (let x = 0; x < stride; x++) {
      const a = x >= bpp ? out[y * stride + x - bpp] : 0
      const b = y > 0 ? out[(y - 1) * stride + x] : 0
      const c = x >= bpp && y > 0 ? out[(y - 1) * stride + x - bpp] : 0
      let v = line[x]
      if (f === 1) v += a
      else if (f === 2) v += b
      else if (f === 3) v += (a + b) >> 1
      else if (f === 4) {
        const p = a + b - c
        const pa = Math.abs(p - a), pb = Math.abs(p - b), pc = Math.abs(p - c)
        v += pa <= pb && pa <= pc ? a : pb <= pc ? b : c
      }
      out[y * stride + x] = v & 0xff
    }
  }
  return { w, h, bpp, px: out }
}

export default async function ({ ev, send, sleep, consoleAll, RUN, requests }) {
  const expect = process.env.EXPECT || "zh-Hans"
  const shot = async (name) => {
    const r = await send("Page.captureScreenshot", { format: "png" })
    const png = Buffer.from(r.result.data, "base64")
    fs.writeFileSync(`${RUN}/${name}.png`, png)
    const { w, h, bpp, px } = decodePNG(png)
    let magenta = 0, cyan = 0
    for (let i = 0; i < w * h; i++) {
      const r = px[i * bpp], g = px[i * bpp + 1], b = px[i * bpp + 2]
      if (r > 200 && b > 200 && g < 90) magenta++
      if (g > 200 && b > 200 && r < 90) cyan++
    }
    return { magenta, cyan }
  }
  const state = () => ev(`
    const v = document.querySelector("video")
    const sub = document.querySelector(".art-control-subtitle")
    const panel = document.querySelector(".art-control-panel")
    const items = [...document.querySelectorAll(".art-control-subtitle .art-selector-item")].map((x) => ({ t: x.textContent, cur: x.classList.contains("art-current") }))
    return { t: v ? +v.currentTime.toFixed(1) : -1, paused: v?.paused, rs: v?.readyState, sub: !!sub, leftOfPanel: !!sub && !!panel && sub.nextElementSibling === panel,
             canvas: !!document.querySelector("canvas.ot-subtitles"), items }`)
  // wait until the playhead is inside a cue (s ∈ [k*6+2.5, k*6+6.5]) and take a screenshot
  const inCue = async (name, minT = 0) => {
    for (let i = 0; i < 80; i++) {
      const s = await state()
      const ph = (s.t - 2) % 6
      if (s.t >= minT && ph > 0.6 && ph < 4.4) {
        await sleep(400) // a rendered frame
        return { ...(await shot(name)), t: s.t }
      }
      await sleep(250)
    }
    return { magenta: -1, cyan: -1, t: -1 }
  }
  const pick = (label) => ev(`
    const it = [...document.querySelectorAll(".art-control-subtitle .art-selector-item")].find((x) => x.textContent.trim() === ${JSON.stringify(label)})
    if (!it) return false
    it.click()
    return true`)
  const r = {}
  await ev(`document.querySelector("video")?.play().catch(() => {}); return 1`)
  let s = await state()
  for (let i = 0; i < 40 && !s.sub && expect !== "none"; i++) {
    await sleep(250)
    s = await state()
  }
  console.log("subtitle UI:", JSON.stringify(s))
  r["「字幕」 button shown immediately left of 「资源」"] = s.sub && s.leftOfPanel
  r["track list: 简体中文 / 繁體中文 / English / 关闭"] = JSON.stringify(s.items.map((x) => x.t)) === JSON.stringify(["简体中文", "繁體中文", "English", "关闭"])
  if (expect === "en") {
    const a = await inCue("en")
    console.log("en:", JSON.stringify(a), JSON.stringify(s.items))
    r["English browser → the English (SRT) track"] = s.items.find((x) => x.cur)?.t === "English" && a.magenta < 50 && a.cyan < 50
    r["renderer loaded for the SRT track"] = requests.some((u) => /\/assets\/(render|worker)-/.test(u))
    return r
  }
  if (expect === "off") {
    const a = await inCue("off")
    console.log("off:", JSON.stringify(a))
    r["no subtitles when no track matches the browser language"] = a.t >= 0 && a.magenta < 50 && a.cyan < 50 && s.items.find((x) => x.cur)?.t === "关闭"
    r["JASSUB never loaded when subtitles are off"] = !requests.some((u) => /jassub|\.wasm|\/assets\/(render|worker)-/.test(u))
    return r
  }
  r["default track = browser language (简体中文 for zh-CN)"] = s.items.find((x) => x.cur)?.t === "简体中文"
  const a = await inCue("default")
  console.log("default track:", JSON.stringify(a))
  r["styled ASS rendered (magenta, top) at a cue time"] = a.magenta > 250 && a.cyan < 50
  // seek far ahead: cues come from the clusters the video reads after the seek
  await ev(`const v = document.querySelector("video"); v.currentTime = 62.6; v.play().catch(() => {}); return 1`)
  const b = await inCue("after-seek", 62)
  console.log("after seek:", JSON.stringify(b))
  r["subtitles after seeking to 62 s"] = b.magenta > 250
  // switch track
  r["menu: pick 繁體中文"] = await pick("繁體中文")
  const c = await inCue("cht", b.t)
  console.log("繁體:", JSON.stringify(c))
  r["switched to 繁體 (cyan) without reload"] = c.cyan > 250 && c.magenta < 50
  // fullscreen keeps the canvas (and the subtitles)
  await send("Runtime.evaluate", { expression: `document.querySelector(".art-control-fullscreen")?.click()`, userGesture: true })
  await sleep(800)
  const fs1 = await ev(`return { fs: !!document.fullscreenElement, inside: !!document.fullscreenElement?.querySelector("canvas.ot-subtitles") }`)
  const d = await inCue("fullscreen", c.t)
  console.log("fullscreen:", JSON.stringify(fs1), JSON.stringify(d))
  r["fullscreen keeps subtitles"] = fs1.fs && fs1.inside && d.cyan > 250
  await send("Runtime.evaluate", { expression: `document.exitFullscreen?.()`, userGesture: true })
  await sleep(500)
  r["menu: pick 关闭"] = await pick("关闭")
  const e = await inCue("closed", d.t)
  console.log("closed:", JSON.stringify(e))
  r["关闭 hides subtitles"] = e.magenta < 50 && e.cyan < 50
  r["font attachment loaded"] = consoleAll.some((l) => /字幕字体/.test(l))
  console.log("console (subtitles):\n  " + consoleAll.filter((l) => /字幕/.test(l)).slice(0, 10).join("\n  "))
  return r
}
