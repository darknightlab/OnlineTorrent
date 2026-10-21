// Local edge harness: EXTRA=e2e/player/controls.mjs START_FRACTION=1 node e2e/edge/run.mjs 12 /tmp/f/media/v.mp4
// One Chromium, unique profile/random debug port supplied by the harness. Real playback
// comes from its local edge, never production. Checks idle shells separately by holding
// /play before any metadata can arrive.
import fs from "node:fs"
import assert from "node:assert/strict"
import { execFileSync } from "node:child_process"

export default async function ({ send, sleep, RUN, SITE, IH, onCDP }) {
  const out = process.env.CONTROLS_OUT || `${RUN}/controls`
  fs.mkdirSync(out, { recursive: true })
  const ev = async (code) => {
    const r = await send("Runtime.evaluate", { expression: `(async()=>{${code}})()`, awaitPromise: true, returnByValue: true, userGesture: true })
    if (r.result?.exceptionDetails) throw new Error(r.result.exceptionDetails.text + " " + r.result.exceptionDetails.exception?.description)
    return r.result?.result?.value
  }
  const shot = async (name) => {
    const r = await send("Page.captureScreenshot", { format: "png" })
    fs.writeFileSync(`${out}/${name}.png`, Buffer.from(r.result.data, "base64"))
  }
  const wait = async (code) => {
    for (let i = 0; i < 120; i++) { if (await ev(code)) return; await sleep(250) }
    throw new Error(`timeout: ${code}`)
  }
  const layout = async (width, height, phone) => {
    await send("Emulation.setDeviceMetricsOverride", { width, height, deviceScaleFactor: 1, mobile: phone })
    await sleep(500)
  }
  const phoneUA = "Mozilla/5.0 (Linux; Android 13; Pixel 7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Mobile Safari/537.36"
  const deskUA = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36"
  const metrics = () => ev(`
    const p=document.querySelector('.art-video-player');if(!p)return null;
    const box=e=>{const r=e.getBoundingClientRect();return {x:r.x,y:r.y,w:r.width,h:r.height}};
    const b=box(p), r=box(p.querySelector('.art-control-progress-inner'));
    const buttons=[...p.querySelectorAll('.art-controls .art-control')].filter(e=>!e.classList.contains('art-control-time'));
    const c=box(p.querySelector('.art-control-playAndPause'));
    const input=p.querySelector('.ot-danmaku-slot input');const slot=box(p.querySelector('.ot-danmaku-slot'));
    return { buttons:buttons.map(e=>e.className.split(' ').find(s=>s.startsWith('art-control-'))?.slice(12)),
      bottom:b.y+b.h,progress:r.y+r.h/2,buttonCenter:c.y+c.h/2,zoneCenter:(r.y+r.h/2+b.y+b.h)/2,
      centerVisible:p.classList.contains('ot-center-send'),slot, left:box(p.querySelector('.art-controls-left')), right:box(p.querySelector('.art-controls-right')),
      input:input?box(input):null, fullscreen:!!document.fullscreenElement,
      dialog:!!document.querySelector('[role=dialog]'),paused:p.querySelector('video').paused,
      tooltip:p.querySelector('.art-control-danmaku')?.getAttribute('aria-label'),
      buttonBoxes:buttons.map(e=>({name:e.className,box:box(e),icon:[...e.querySelectorAll('.art-icon')].filter(i=>getComputedStyle(i).display!=='none').map(box)})) };
  `)
  const show = async () => {
    await ev(`document.querySelector('video').pause();return 1`)
    await send("Input.dispatchMouseEvent", { type: "mouseMoved", x: 100, y: 150 })
    await sleep(250)
  }
  const clickDm = async () => { await ev(`document.querySelector('.art-control-danmaku').click();return 1`); await wait(`return !!document.querySelector('[role=dialog]')`) }
  const close = async () => { await ev(`document.querySelector('[role=dialog] button[aria-label=关闭]').click();return 1`); await sleep(300) }
  const desktop = ["playAndPause", "volume", "danmaku", "panel", "rate", "pip", "fullscreen"]
  const phone = ["playAndPause", "danmaku", "panel", "fullscreen"]
  const result = {}
  const check = (m, list) => {
    const actual = m.buttons.filter((b) => b !== "subtitle")
    assert.deepEqual(actual, list)
    assert.equal(m.tooltip, null)
    assert.ok(Math.abs(m.buttonCenter - m.zoneCenter) <= 1, `not centred: ${JSON.stringify(m)}`)
    if (m.input) { assert.ok(m.input.x >= m.left.x+m.left.w); assert.ok(m.input.x+m.input.w <= m.right.x) }
  }

  // Playing / fullscreen: desktop and a real mobile UA; a narrow desktop viewport also
  // uses phone controls, but a phone rotated wider than sm never regains volume/rate/PiP.
  for (const device of ["desktop", "phone"]) {
    const mobile = device === "phone"
    await send("Network.setUserAgentOverride", { userAgent: mobile ? phoneUA : deskUA })
    await layout(mobile ? 390 : 1280, mobile ? 844 : 800, mobile)
    await send("Page.navigate", { url: `${SITE}/watch/hash/${IH}` })
    await wait(`return (document.querySelector('video')?.currentTime??0)>0.5`)
    if (process.env.EXPECT_SUBTITLES) await wait(`return !!document.querySelector('.art-control-subtitle')`)
    await show()
    const m = result[device] = await metrics();check(m,mobile?phone:desktop)
    if (process.env.EXPECT_SUBTITLES) assert.ok(m.buttons.includes('subtitle'))
    // Progress track (centre and lower half + thumb radius) must hit .art-progress, never .art-controls.
    const hits = await ev(`
      const p = document.querySelector('.art-video-player');
      const r = p.querySelector('.art-control-progress-inner').getBoundingClientRect();
      const x = Math.round(r.left + r.width / 2), cy = r.top + r.height / 2;
      return [-6, -2, 0, 1, 2, 3, 6].map((dy) => !!document.elementFromPoint(x, Math.round(cy + dy))?.closest('.art-progress'));
    `)
    assert.deepEqual(hits, [true, true, true, true, true, true, true], `progress hit-test failed: ${JSON.stringify(hits)}`)
    await shot(`${device}-playing`)

    // Exact pause semantics: play→pause→resume, paused→pause→paused. Unmapped source must
    // not fetch/send under a guessed episode, but settings open normally.
    for (const wasPlaying of [true,false]) {
      await ev(`const v=document.querySelector('video');${wasPlaying ? 'await v.play()' : 'v.pause()'};return 1`)
      await clickDm()
      assert.equal(await ev(`return document.querySelector('video').paused`), true)
      assert.equal(await ev(`return !!document.querySelector('[role=dialog] input[aria-label=弹幕内容]')`), false)
      if(wasPlaying) await shot(`${device}-dialog`)
      await close()
      assert.equal(await ev(`return document.querySelector('video').paused`), !wasPlaying)
    }
    // Mobile landscape keeps phone controls; desktop portrait fullscreen also gets a sender.
    await layout(mobile?844:1280,mobile?390:720,mobile)
    await ev(`await document.querySelector('.art-video-player').requestFullscreen();return 1`)
    await wait(`return !!document.fullscreenElement && document.querySelector('.art-video-player').classList.contains('ot-center-send')`)
    await show()
    const f=result[`${device}-fs`]=await metrics();check(f,mobile?phone:desktop)
    await shot(`${device}-fullscreen`)
    await clickDm()
    assert.equal(await ev(`return document.querySelector('.art-video-player').contains(document.querySelector('[role=dialog]'))`), true)
    await close()
    if(!mobile) {
      await layout(720,1100,false)
      await wait(`return document.querySelector('.art-video-player').classList.contains('ot-center-send')`)
      result.desktopPortraitFs=await metrics()
      await layout(640,400,false)
      await wait(`return !document.querySelector('.art-video-player').classList.contains('ot-center-send')`)
      await clickDm()
      assert.ok(await ev(`return document.querySelector('[role=dialog]').textContent.includes('尚未关联')`))
      await close()
    }
    await ev(`await document.exitFullscreen();return 1`)
  }

  // A known, verified mapping: the local media is attached explicitly for this fixture,
  // never guessed from titles. No platform source ids: sending works without collected data.
  execFileSync("python3", ["-c", `
import sqlite3
c=sqlite3.connect(${JSON.stringify(RUN + "/meta.db")})
c.execute("INSERT OR IGNORE INTO works(id,key,title,title_cn,type,summary,updated_at) VALUES(999999,'bgm:999999','Controls test','Controls test','tv','test','x')")
c.execute("INSERT OR IGNORE INTO work_ids VALUES('bgm','999999','bgm:999999')")
c.execute("INSERT OR IGNORE INTO episodes(bgm_id,work_key,type,sort) VALUES(999999,'bgm:999999',0,1)")
c.execute("UPDATE torrent_files SET work_key='bgm:999999',episode=1,kind='episode',status='verified' WHERE infohash=? AND file_index=0",(${JSON.stringify(IH)},))
c.commit()
`])
  for (const [device,w,h,ua] of [["desktop",1280,720,deskUA],["phone",844,390,phoneUA]]) {
    await send('Network.setUserAgentOverride',{userAgent:ua})
    await layout(w,h,device==='phone')
    await send('Page.navigate',{url:`${SITE}/subject/bgm/999999/episode/1/hash/${IH}/file/0`})
    await wait(`return (document.querySelector('video')?.currentTime??0)>0.5`)
    await ev(`await document.querySelector('.art-video-player').requestFullscreen();return 1`)
    await wait(`return !!document.querySelector('.ot-danmaku-slot input')`)
    await show()
    const m=result[`${device}-mapped-fs`]=await metrics();check(m,device==='phone'?phone:desktop)
    assert.ok(m.input.w>0)
    assert.equal(await ev(`return document.querySelectorAll('.ot-danmaku-slot button[type=submit] svg').length`),1)
    assert.equal(await ev(`return document.querySelector('.ot-danmaku-slot button[type=submit]').textContent`),'')
    await shot(`${device}-fullscreen-send`)
    // Sender follows the controls; keyboard focus keeps the controls visible.
    await ev(`await document.querySelector('video').play();document.querySelector('.ot-danmaku-slot input').focus();return 1`)
    await send('Input.dispatchMouseEvent',{type:'mouseMoved',x:300,y:100})
    await sleep(3600)
    assert.ok(await ev(`return document.querySelector('.art-video-player').classList.contains('art-control-show')`))
    await ev(`document.querySelector('.ot-danmaku-slot input').blur();return 1`)
    await sleep(3600)
    assert.equal(await ev(`return document.querySelector('.art-video-player').classList.contains('art-control-show')`),false)
    await show()
    // Pause dialog always restores only its original state, including in fullscreen.
    await ev(`await document.querySelector('video').play();return 1`)
    await clickDm();assert.equal((await metrics()).paused,true)
    assert.equal(await ev(`return !!document.querySelector('[role=dialog] input[aria-label=弹幕内容]')`),false)
    await close();assert.equal((await metrics()).paused,false)
    await ev(`document.querySelector('video').pause();document.querySelector('.ot-danmaku-slot input').focus();return 1`)
    const t=await ev(`return document.querySelector('video').currentTime`)
    for (const [key,code,vk,text] of [[' ','Space',32,' '],['ArrowRight','ArrowRight',39],['f','KeyF',70,'f'],['m','KeyM',77,'m']]) {
      await send('Input.dispatchKeyEvent',{type:'keyDown',key,code,windowsVirtualKeyCode:vk,...(text?{text}:{})})
      await send('Input.dispatchKeyEvent',{type:'keyUp',key,code,windowsVirtualKeyCode:vk})
    }
    assert.equal((await metrics()).paused,true)
    assert.ok(Math.abs((await ev(`return document.querySelector('video').currentTime`))-t)<0.1)
    assert.ok((await metrics()).fullscreen)
    // Enter submits only once, including while the dialog/sender implementation is shared.
    await send('Input.insertText',{text:` controls-${device}`})
    let postCount=0
    const offPost=onCDP((method,p)=>{if(method==='Network.requestWillBeSent' && p.request.method==='POST' && p.request.url.endsWith('/danmaku'))postCount++})
    await send('Input.dispatchKeyEvent',{type:'keyDown',key:'Enter',code:'Enter',windowsVirtualKeyCode:13,text:'\r'})
    await send('Input.dispatchKeyEvent',{type:'keyUp',key:'Enter',code:'Enter',windowsVirtualKeyCode:13})
    await wait(`return document.querySelector('.ot-danmaku-slot input').value === ''`)
    assert.equal(postCount,1);offPost()
    await ev(`await document.querySelector('video').play();return 1`)
    await sleep(1100)
    assert.ok(await ev(`return [...document.querySelector('.art-danmuku').children].some(e=>e.style.border && e.textContent.includes('controls-${device}'))`))
    // Narrow fullscreen falls back to the dialog sender (not an unreachable hidden box).
    if(device==='desktop') {
      await layout(600,800,false)
      await wait(`return !document.querySelector('.art-video-player').classList.contains('ot-center-send')`)
      await clickDm()
      assert.ok(await ev(`return !!document.querySelector('[role=dialog] input[aria-label=弹幕内容]')`))
      assert.equal(await ev(`return document.querySelector('[role=dialog] button[type=submit]').textContent`),'发送')
      await shot('desktop-narrow-dialog-send')
      await close()
    }
    await ev(`await document.exitFullscreen();return 1`)
    // Changing route while a playing-video dialog is open must not resume a replacement.
    await ev(`await document.querySelector('video').play();return 1`)
    await clickDm()
    await send('Page.navigate',{url:`${SITE}/`})
    await sleep(300)
    assert.equal(await ev(`return !!document.querySelector('[role=dialog]')`),false)
  }

  // Hold new-hash /play to exercise the idle PlayerShell with no metadata at all.
  let held=[]
  const off=onCDP((method,p)=>{if(method==='Fetch.requestPaused')held.push(p.requestId)})
  await send('Fetch.enable',{patterns:[{urlPattern:'*/api/v1/torrents/hash/*/play*',requestStage:'Request'}]})
  for (const [device,w,h,ua] of [['desktop',1280,800,deskUA],['phone',390,844,phoneUA]]) {
    await send('Network.setUserAgentOverride',{userAgent:ua})
    await layout(w,h,device==='phone')
    await send('Page.navigate',{url:`${SITE}/watch/hash/${'af'.repeat(20)}`})
    await wait(`return !!document.querySelector('.ot-idle .art-control-danmaku')`)
    const m=result[`${device}-idle`]=await metrics();check(m,device==='phone'?phone:desktop)
    await shot(`${device}-idle`)
    await clickDm();await shot(`${device}-idle-dialog`);await close()
    await send('Page.navigate',{url:`${SITE}/subject/bgm/999999/episode/1/hash/${IH}/file/0`})
    await wait(`return !!document.querySelector('.ot-idle .art-control-danmaku')`)
    check(await metrics(),device==='phone'?phone:desktop)
    await clickDm();assert.ok(await ev(`return !!document.querySelector('[role=dialog]')`));await close()
    await layout(device==='phone'?844:1280,device==='phone'?390:720,device==='phone')
    await ev(`await document.querySelector('.art-video-player').requestFullscreen();return 1`)
    await sleep(500)
    const f=result[`${device}-idle-fs`]=await metrics();check(f,device==='phone'?phone:desktop)
    await shot(`${device}-idle-fullscreen`)
    await ev(`await document.exitFullscreen();return 1`)
    await send('Page.navigate',{url:`${SITE}/`});await sleep(200)
    for(const requestId of held.splice(0))await send('Fetch.failRequest',{requestId,errorReason:'Aborted'})
  }
  await send('Fetch.disable');off()
  fs.writeFileSync(`${out}/measurements.json`,JSON.stringify(result,null,2))
  console.log('CONTROLS',JSON.stringify(result))
  console.log('controls screenshots:',out)
  return {"exact idle/playing/phone/fullscreen buttons, pause-resume, send placement and geometry":true}
}
