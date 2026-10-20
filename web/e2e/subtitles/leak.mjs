// 字幕渲染器不能在离开播放页后残留（EXTRA=e2e/subtitles/leak.mjs，媒体同 checks.mjs）：
// 显示一句字幕后，站内导航到首页（SPA，不刷新），页面上不能再有字幕画布 / 洋红色字；
// 网页全屏（播放器被移到 <body>）时离开同样不能残留。
import checks from "./checks.mjs"

export default async function (ctx) {
  const { ev, send, sleep, RUN } = ctx
  const r = {}
  const count = async (name) => {
    const s = await send("Page.captureScreenshot", { format: "png" })
    const fs = await import("node:fs")
    fs.writeFileSync(`${RUN}/${name}.png`, Buffer.from(s.result.data, "base64"))
    const dom = await ev(`return { canvases: document.querySelectorAll("canvas.ot-subtitles").length,
      outside: [...document.querySelectorAll("canvas")].filter((c) => !c.closest(".art-video-player")).length, path: location.pathname }`)
    return dom
  }
  const shownAt = async () => {
    for (let i = 0; i < 60; i++) {
      const s = await ev(`const v = document.querySelector("video"); return v ? v.currentTime : -1`)
      const ph = (s - 2) % 6
      if (ph > 0.6 && ph < 4) return s
      await sleep(250)
    }
    return -1
  }
  const leave = async (label, fullscreenWeb) => {
    await ev(`document.querySelector("video")?.play().catch(() => {}); return 1`)
    if (fullscreenWeb) {
      await send("Runtime.evaluate", { expression: `document.querySelector(".art-control-fullscreenWeb")?.click()`, userGesture: true })
      await sleep(500)
    }
    await shownAt()
    await sleep(500)
    const before = await count(`${label}-before`)
    // SPA navigation (no reload): the site logo link
    await ev(`const a = document.querySelector('header a[href="/"]'); a ? a.click() : history.back(); return 1`)
    await sleep(2500)
    const after = await count(`${label}-after`)
    console.log(`${label}: before ${JSON.stringify(before)} after ${JSON.stringify(after)}`)
    r[`${label}: subtitles were shown before leaving`] = before.canvases === 1
    r[`${label}: no subtitle canvas left after leaving the play page`] = after.canvases === 0 && after.path === "/"
    r[`${label}: no canvas outside a player`] = after.outside === 0
  }
  void checks
  await leave("normal", false)
  // back to the watch page, this time leave from web fullscreen
  await ev(`history.back(); return 1`)
  await sleep(6000)
  await leave("web-fullscreen", true)
  return r
}
