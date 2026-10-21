import { test } from "node:test"
import assert from "node:assert/strict"
import { phoneControls, centerSender, DialogPlayback } from "./player-controls.ts"

test("sm breakpoint and phone identity survive landscape rotation", () => {
  assert.equal(phoneControls(639, false), true)
  assert.equal(phoneControls(640, false), false)
  assert.equal(phoneControls(844, true), true)
  assert.equal(phoneControls(1280, false), false)
})
test("sender: desktop fullscreen any aspect, phone landscape fullscreen, sufficient measured space", () => {
  assert.equal(centerSender(false, false, 1280, 720, 600), false)
  assert.equal(centerSender(true, false, 720, 1280, 300), true)
  assert.equal(centerSender(true, true, 390, 844, 200), false)
  assert.equal(centerSender(true, true, 844, 390, 300), true)
  assert.equal(centerSender(true, false, 700, 400, 159), false)
})
function media(paused: boolean) {
  const v = { paused, ended: false, isConnected: true, plays: 0, pauses: 0,
    pause() { this.paused = true; this.pauses++ },
    play() { this.paused = false; this.plays++; return Promise.resolve() },
  }
  return v
}
test("dialog pause/resume once; originally paused stays paused", () => {
  for (const paused of [true, false]) {
    const v = media(paused), p = new DialogPlayback()
    p.open(v, "file1")
    p.open(v, "file1")
    assert.equal(v.paused, true)
    assert.equal(v.pauses, 1)
    p.close(v, "file1")
    p.close(v, "file1")
    assert.equal(v.paused, paused)
    assert.equal(v.plays, paused ? 0 : 1)
  }
})
test("navigation, disposal, disconnected or replaced video never autoplay", () => {
  for (const reason of ["source", "cancel", "disconnect", "element"]) {
    const v = media(false), p = new DialogPlayback()
    p.open(v, "file1")
    if (reason === "cancel") p.cancel()
    if (reason === "disconnect") v.isConnected = false
    const next = reason === "element" ? media(true) : v
    p.close(next, reason === "source" ? "file2" : "file1")
    assert.equal(v.plays, 0)
    assert.equal(next.plays, 0)
  }
})
