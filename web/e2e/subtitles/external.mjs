// 外挂字幕（e2e/edge/run.mjs 的 EXTRA）。种子按真实合集的布局构造：本集视频 + 本集 .sc.ass / .tc.ass
// + 下一集的视频与字幕（不能被挂上）。字幕文件取自线上（白月字幕组 No Game No Life 第 1、2 集，只读下载）。
//   MEDIA=/tmp/subs-e2e/test.mkv VIDEO_NAME="<ep01>.mkv" ADD_FILES="279.ass=><ep01>.sc.ass|280.ass=><ep01>.tc.ass|…"
//   EXTRA=e2e/subtitles/external.mjs BROWSER_LANG=zh-CN node e2e/edge/run.mjs 15
// 检查：菜单（内封 3 条 + 外挂 简体 / 繁体，没有第 2 集的）、zh-CN 默认选外挂简体（与内封「简体中文」同分 → 外挂）、
// 画面上确有字幕（同一帧：显示 vs 关闭的截图差异）、切到繁体、只下载了本集的字幕文件且在视频出画面之后。
import fs from "node:fs"
import { decodePNG } from "./checks.mjs"

export default async function ({ ev, send, sleep, consoleAll, RUN, requests }) {
  const r = {}
  const AT = Number(process.env.AT ?? 31.5)
  const shot = async (name) => {
    const b = await ev(`const v = document.querySelector("video").getBoundingClientRect(); return { x: v.x, y: v.y, w: v.width, h: v.height }`)
    const s = await send("Page.captureScreenshot", { format: "png", clip: { x: b.x, y: b.y, width: b.w, height: b.h, scale: 1 } })
    const png = Buffer.from(s.result.data, "base64")
    fs.writeFileSync(`${RUN}/${name}.png`, png)
    return decodePNG(png)
  }
  const diff = (a, b) => {
    let n = 0
    for (let i = 0; i < a.w * a.h; i++) {
      const d = Math.abs(a.px[i * a.bpp] - b.px[i * b.bpp]) + Math.abs(a.px[i * a.bpp + 1] - b.px[i * b.bpp + 1]) + Math.abs(a.px[i * a.bpp + 2] - b.px[i * b.bpp + 2])
      if (d > 60) n++
    }
    return n
  }
  const menu = () =>
    ev(`return [...document.querySelectorAll(".art-control-subtitle .art-selector-item")].map((x) => ({ t: x.textContent.trim(), cur: x.classList.contains("art-current") }))`)
  const pick = async (label) => {
    await ev(`const it = [...document.querySelectorAll(".art-control-subtitle .art-selector-item")].find((x) => x.textContent.trim() === ${JSON.stringify(label)}); it?.click(); return !!it`)
    await sleep(1500)
  }
  const items = await menu()
  console.log("menu:", JSON.stringify(items))
  r["menu: embedded tracks + 简体（外挂） + 繁体（外挂）, nothing from episode 02"] =
    items.some((x) => x.t === "简体中文") && items.some((x) => x.t === "简体（外挂）") && items.some((x) => x.t === "繁体（外挂）") && items.length === 6
  r["default for zh-CN: 简体（外挂） (ties with the embedded 简体 track → external)"] = items.find((x) => x.cur)?.t === "简体（外挂）"

  // the same frame with / without subtitles
  await ev(`const v = document.querySelector("video"); v.pause(); v.currentTime = ${AT}; await new Promise((r) => v.addEventListener("seeked", r, { once: true })); return 1`)
  await sleep(2500)
  const withSc = await shot("ext-sc")
  await pick("繁体（外挂）")
  await sleep(1000)
  const withTc = await shot("ext-tc")
  await pick("关闭")
  await sleep(1000)
  const off = await shot("ext-off")
  const dSc = diff(withSc, off)
  const dTc = diff(withTc, off)
  const dScTc = diff(withSc, withTc)
  console.log(`pixels differing from 关闭: 简体 ${dSc}, 繁体 ${dTc}; 简体 vs 繁体 ${dScTc}`)
  r["external ASS rendered on the video (简体)"] = dSc > 800
  r["switched to 繁体 (different glyphs)"] = dTc > 800 && dScTc > 100

  const subReqs = requests.filter((u) => /\.ass$/i.test(decodeURIComponent(u)))
  console.log("subtitle requests:", subReqs.map((u) => decodeURIComponent(u).split("/").pop()))
  r["only this episode's subtitle files were downloaded"] = subReqs.length >= 1 && subReqs.every((u) => decodeURIComponent(u).includes("[01]"))
  const firstVideo = requests.findIndex((u) => /\.mkv$/i.test(decodeURIComponent(u)) && u.includes("/webseed/"))
  const firstSub = requests.findIndex((u) => /\.ass$/i.test(decodeURIComponent(u)))
  r["subtitle files fetched after the video started"] = firstVideo >= 0 && (firstSub < 0 || firstSub > firstVideo)
  const logs = consoleAll.filter((l) => /字幕/.test(l))
  console.log("logs:", JSON.stringify(logs.slice(0, 6)))
  r["log: 内封 3 条、外挂 2 个"] = logs.some((l) => /内封 3 条、外挂 2 个，已选择「简体（外挂）」/.test(l))
  return r
}
