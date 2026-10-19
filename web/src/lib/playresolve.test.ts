// node --test src/lib/playresolve.test.ts（npm test）
import { test } from "node:test"
import assert from "node:assert/strict"
import { episodeRedirect, stepOfEvent, stepOfPlay } from "./playresolve.ts"

test("play responses", () => {
  assert.deepEqual(stepOfPlay({ status: "pending", access: { action: "pending", reason: "" } }), { kind: "pending" })
  assert.deepEqual(stepOfPlay({ status: "ready", access: { action: "allow", reason: "" } }), { kind: "play" })
  assert.deepEqual(stepOfPlay({ status: "ready", access: { action: "vip", reason: "仅限大会员" } }), { kind: "gate", action: "vip", message: "仅限大会员" })
  // older servers without status
  assert.deepEqual(stepOfPlay({ access: { action: "allow", reason: "" } }), { kind: "play" })
})

test("resolve events", () => {
  const p = "/subject/bgm/400602/episode/3/hash/" + "a".repeat(40) + "/file/0"
  assert.deepEqual(stepOfEvent("redirect", { path: p }), { kind: "redirect", path: p })
  // never navigate off-site
  assert.deepEqual(stepOfEvent("redirect", { path: "https://evil.example/" }), { kind: "replay" })
  assert.deepEqual(stepOfEvent("ready", {}), { kind: "replay" })
  assert.deepEqual(stepOfEvent("denied", { action: "block", reason: "该资源含有色情内容，已被屏蔽" }), {
    kind: "gate",
    action: "block",
    message: "该资源含有色情内容，已被屏蔽",
  })
  assert.deepEqual(stepOfEvent("denied", { action: "login", reason: "登录后即可观看" }), { kind: "gate", action: "login", message: "登录后即可观看" })
  assert.deepEqual(stepOfEvent("denied", null), { kind: "gate", action: "block", message: "" })
  assert.deepEqual(stepOfEvent("timeout", {}), { kind: "timeout" })
  assert.equal(stepOfEvent("message", {}), null)
})

test("known mapped torrent: watch page → episode page", () => {
  const ih = "b".repeat(40)
  const meta = { subject_id: 7, bgm_id: "400602", episode_key: "12.5", file_index: 2 }
  assert.equal(episodeRedirect(meta, ih, null), `/subject/bgm/400602/episode/12.5/hash/${ih}/file/2`)
  assert.equal(episodeRedirect(meta, ih, 2), `/subject/bgm/400602/episode/12.5/hash/${ih}/file/2`)
  // another file was asked for: stay
  assert.equal(episodeRedirect(meta, ih, 0), null)
  assert.equal(episodeRedirect({ ...meta, bgm_id: "" }, ih, null), `/subject/id/7/episode/12.5/hash/${ih}/file/2`)
  assert.equal(episodeRedirect(null, ih, null), null)
})
