// 外挂字幕之二（e2e/edge/run.mjs 的 EXTRA）：种子里只有一个视频（MP4，没有内封字幕）+ Subs/简体.srt（GBK 编码）
// + Fonts/ 里的字体。检查菜单、默认选择、渲染（同一帧显示 vs 关闭）、种子字体被加载。
//   MEDIA=/tmp/f/media/v.mp4 ADD_FILES="gbk.srt=>Subs/简体.srt|tiny.otf=>Fonts/tiny.otf" EXTRA=e2e/subtitles/external-srt.mjs BROWSER_LANG=zh-CN node e2e/edge/run.mjs 15
import fs from "node:fs"
import { decodePNG } from "./checks.mjs"

export default async function ({ ev, send, sleep, consoleAll, RUN }) {
  const r = {}
  const items = await ev(`return [...document.querySelectorAll(".art-control-subtitle .art-selector-item")].map((x) => ({ t: x.textContent.trim(), cur: x.classList.contains("art-current") }))`)
  console.log("menu:", JSON.stringify(items))
  r["MP4 + external SRT: menu 简体（外挂） / 关闭, selected"] = items.length === 2 && items[0].t === "简体（外挂）" && items[0].cur
  const shot = async (name) => {
    const b = await ev(`const v = document.querySelector("video").getBoundingClientRect(); return { x: v.x, y: v.y, w: v.width, h: v.height }`)
    const s = await send("Page.captureScreenshot", { format: "png", clip: { x: b.x, y: b.y, width: b.w, height: b.h, scale: 1 } })
    const png = Buffer.from(s.result.data, "base64")
    fs.writeFileSync(`${RUN}/${name}.png`, png)
    return decodePNG(png)
  }
  await ev(`const v = document.querySelector("video"); v.pause(); v.currentTime = 33; await new Promise((r) => v.addEventListener("seeked", r, { once: true })); return 1`)
  await sleep(3000)
  const on = await shot("srt-on")
  await ev(`[...document.querySelectorAll(".art-control-subtitle .art-selector-item")].find((x) => x.textContent.trim() === "关闭")?.click(); return 1`)
  await sleep(1500)
  const off = await shot("srt-off")
  let n = 0
  for (let i = 0; i < on.w * on.h; i++) if (Math.abs(on.px[i * on.bpp] - off.px[i * off.bpp]) + Math.abs(on.px[i * on.bpp + 1] - off.px[i * off.bpp + 1]) > 60) n++
  console.log("pixels differing from 关闭:", n)
  r["GBK SRT rendered (CJK, decoded)"] = n > 800
  const logs = consoleAll.filter((l) => /字幕/.test(l))
  console.log("logs:", JSON.stringify(logs.slice(0, 5)))
  r["the torrent's Fonts/ font loaded"] = logs.some((l) => /种子字体文件 1 个/.test(l))
  return r
}
