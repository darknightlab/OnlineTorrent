// 只有中文 SRT 轨道（chs / cht）、没有字体附件的 MKV（e2e/edge/run.mjs 的 EXTRA）：
// zh-CN 默认选 chs；在字幕时间点（同一帧）显示 vs 关闭的截图必须有差别（字真的画出来了），拖动后、换 cht 后同样；
// 刷新页面后（数据来自本地缓存，浏览器把文件分成许多相邻的短请求来读）同样。
//   ffmpeg … -c:s srt -metadata:s:s:0 title=chs … → /tmp/subs-e2e/srt2.mkv（每 6 秒一句：2–7 s、8–13 s …）
//   MEDIA=/tmp/subs-e2e/srt2.mkv EXTRA=e2e/subtitles/srt-zh.mjs BROWSER_LANG=zh-CN node e2e/edge/run.mjs 20
import fs from "node:fs"
import { decodePNG } from "./checks.mjs"

export default async function ({ ev, send, sleep, RUN, consoleAll }) {
  const r = {}
  const shot = async (name) => {
    const b = await ev(`const v = document.querySelector("video").getBoundingClientRect(); return { x: v.x, y: v.y, w: v.width, h: v.height }`)
    const s = await send("Page.captureScreenshot", { format: "png", clip: { x: b.x, y: b.y, width: b.w, height: b.h, scale: 1 } })
    const png = Buffer.from(s.result.data, "base64")
    fs.writeFileSync(`${RUN}/${name}.png`, png)
    return decodePNG(png)
  }
  const diff = (a, b) => {
    let n = 0
    for (let i = 0; i < a.w * a.h; i++)
      if (Math.abs(a.px[i * a.bpp] - b.px[i * b.bpp]) + Math.abs(a.px[i * a.bpp + 1] - b.px[i * b.bpp + 1]) + Math.abs(a.px[i * a.bpp + 2] - b.px[i * b.bpp + 2]) > 60) n++
    return n
  }
  const pick = async (label) => {
    await ev(`const it = [...document.querySelectorAll(".art-control-subtitle .art-selector-item")].find((x) => x.textContent.trim() === ${JSON.stringify(label)}); it?.click(); return !!it`)
    await sleep(1500)
  }
  const seek = (t) => ev(`const v = document.querySelector("video"); v.pause(); v.currentTime = ${t}; await new Promise((r) => v.addEventListener("seeked", r, { once: true })); return 1`)
  const items = await ev(`return [...document.querySelectorAll(".art-control-subtitle .art-selector-item")].map((x) => ({ t: x.textContent.trim(), cur: x.classList.contains("art-current") }))`)
  console.log("menu:", JSON.stringify(items))
  r["SRT-only MKV: menu chs / cht / 关闭, chs selected for zh-CN"] = JSON.stringify(items.map((x) => x.t)) === JSON.stringify(["chs", "cht", "关闭"]) && items[0].cur

  const at = async (t, label) => {
    await seek(t)
    await sleep(2500)
    const on = await shot(`srt-${label}-on`)
    await pick("关闭")
    await sleep(800)
    const off = await shot(`srt-${label}-off`)
    return { on, off, d: diff(on, off) }
  }
  // early cue (8–13 s), then after a seek (62–67 s)
  const a = await at(9, "early")
  await pick("chs")
  const b = await at(63, "seek")
  await pick("cht")
  const c = await at(63.5, "cht")
  console.log(`pixels differing from 关闭: chs@9s ${a.d}, chs@63s ${b.d}, cht@63.5s ${c.d}; chs vs cht ${diff(b.on, c.on)}`)
  r["chs rendered at a cue time"] = a.d > 500
  r["chs rendered after seeking"] = b.d > 500
  r["cht rendered (different glyphs from chs)"] = c.d > 500 && diff(b.on, c.on) > 100
  console.log("logs:", JSON.stringify(consoleAll.filter((l) => /字幕/.test(l)).slice(0, 6)))

  // reload: the same profile, pieces from the OPFS cache
  await pick("chs")
  await send("Page.reload")
  let rs = 0
  for (let i = 0; i < 60 && rs < 3; i++) {
    await sleep(500)
    rs = (await ev(`return document.querySelector("video")?.readyState ?? 0`).catch(() => 0)) ?? 0
  }
  await ev(`document.querySelector("video")?.play().catch(() => {}); return 1`)
  await sleep(3000)
  const cur = await ev(`return [...document.querySelectorAll(".art-control-subtitle .art-selector-item")].find((x) => x.classList.contains("art-current"))?.textContent.trim() ?? null`)
  const d = await at(21, "reload")
  await pick(cur)
  const e = await at(75, "reload-seek")
  console.log(`after reload (cache): selected ${cur}; pixels differing from 关闭 @21s ${d.d}, after seeking @75s ${e.d}`)
  r["after reload (cached data): subtitles rendered"] = cur === "chs" && d.d > 500
  r["after reload (cached data): subtitles rendered after seeking"] = e.d > 500
  return r
}
