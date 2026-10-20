import { test } from "node:test"
import assert from "node:assert/strict"
import { classifyResponse, WebSeedHealth, type HealthOptions } from "./webseedhealth.ts"

function harness(extra: Partial<HealthOptions> = {}) {
  let now = 0
  let timers: { at: number; cb: () => void; id: number }[] = []
  let id = 0
  const events: string[] = []
  const h = new WebSeedHealth({
    now: () => now,
    setTimer: (cb, ms) => {
      timers.push({ at: now + ms, cb, id: ++id })
      return id
    },
    clearTimer: (t) => (timers = timers.filter((x) => x.id !== t)),
    onDisable: (n, ms, k) => events.push(`disable ${n} ${ms / 1000}s ${k}`),
    onEnable: (why) => events.push(`enable ${why}`),
    onRecovered: () => events.push("recovered"),
    onTokenRefresh: () => events.push("token?"),
    ...extra,
  })
  const advance = (ms: number) => {
    now += ms
    for (;;) {
      const due = timers.filter((t) => t.at <= now).sort((a, b) => a.at - b.at)[0]
      if (!due) break
      timers = timers.filter((t) => t !== due)
      due.cb()
    }
  }
  return { h, events, advance, timers: () => timers.length }
}

test("classifyResponse: expected 503s from a progressive edge are not failures", () => {
  assert.equal(classifyResponse(206), "ok")
  assert.equal(classifyResponse(503, "pieces not downloaded yet\n"), "expected")
  assert.equal(classifyResponse(503, "busy\n"), "expected")
  assert.equal(classifyResponse(429, "rate limited"), "expected")
  assert.equal(classifyResponse(503, "edge not ready\n"), "unavailable")
  assert.equal(classifyResponse(503, "read failed\n"), "unavailable")
  assert.equal(classifyResponse(503, ""), "unavailable")
  assert.equal(classifyResponse(502, "qBittorrent unavailable"), "server")
  assert.equal(classifyResponse(500), "server")
  assert.equal(classifyResponse(401), "auth")
  assert.equal(classifyResponse(403), "auth")
  assert.equal(classifyResponse(404, "torrent not on this server"), "unavailable")
})

test("3 failures within 30 s disable; backoff 15 → 30 → 60 … capped at 5 min; success resets", () => {
  const { h, events, advance } = harness()
  h.failure("network")
  advance(1000)
  h.failure("server")
  assert.equal(h.state, "ok")
  advance(1000)
  h.failure("network")
  assert.equal(h.state, "disabled")
  assert.deepEqual(events, ["disable 3 15s network"])
  // failures of in-flight requests while disabled are ignored
  h.failure("network")
  advance(14_999)
  assert.equal(h.state, "disabled")
  advance(1)
  assert.equal(h.state, "ok")
  // still failing → next backoffs double, then cap
  const seen: string[] = []
  for (let i = 0; i < 6; i++) {
    h.failure("network")
    h.failure("network")
    h.failure("network")
    seen.push(events.at(-1)!)
    advance(h.backoffMs(100)) // past any backoff
  }
  assert.deepEqual(seen, ["disable 3 30s network", "disable 3 60s network", "disable 3 120s network", "disable 3 240s network", "disable 3 300s network", "disable 3 300s network"])
  // a success after re-enabling: logged once as recovered, backoff back to 15 s
  events.length = 0
  h.success()
  h.success()
  assert.deepEqual(events, ["recovered"])
  h.failure("network")
  h.failure("network")
  h.failure("network")
  assert.deepEqual(events, ["recovered", "disable 3 15s network"])
})

test("failures spread over more than 30 s, or interrupted by a success, don't disable", () => {
  const { h, advance } = harness()
  h.failure("network")
  advance(20_000)
  h.failure("network")
  advance(11_000) // the first one is now older than 30 s
  h.failure("network")
  assert.equal(h.state, "ok")
  h.success()
  h.failure("server")
  h.failure("server")
  assert.equal(h.state, "ok")
})

test("SSE seed / token / ready re-enables immediately (timer cancelled)", () => {
  const { h, events, advance, timers } = harness()
  for (let i = 0; i < 3; i++) h.failure("unavailable")
  assert.equal(timers(), 1)
  advance(2000)
  h.refreshed("ready")
  assert.equal(h.state, "ok")
  assert.equal(timers(), 0)
  assert.deepEqual(events, ["disable 3 15s unavailable", "enable sse"])
  // refreshed while healthy: nothing
  h.refreshed("token")
  assert.equal(events.length, 2)
})

test("401/403: refresh the token first; stale in-flight 401s are not counted; still failing → disable", () => {
  const { h, events, advance } = harness()
  h.failure("auth")
  assert.deepEqual(events, ["token?"])
  // more 401s from requests made with the old token, before the new one arrives
  h.failure("auth")
  h.failure("auth")
  h.failure("auth")
  assert.equal(h.state, "ok")
  // new token arrives, still rejected → counted
  advance(1000)
  h.refreshed("token")
  h.failure("auth")
  h.failure("auth")
  h.failure("auth")
  assert.equal(h.state, "disabled")
  assert.deepEqual(events, ["token?", "disable 3 15s auth"])
  // a later 401 (after the refresh interval) asks for a token again instead of counting
  advance(60_000)
  h.failure("auth")
  assert.equal(events.at(-1), "token?")
})

test("401 with no token arriving: counted once the grace period is over", () => {
  const { h, advance } = harness()
  h.failure("auth") // asks
  advance(10_000)
  h.failure("auth")
  h.failure("auth")
  h.failure("auth")
  assert.equal(h.state, "disabled")
})

test("hash failures: 2xx responses (garbage data) don't reset them; a verified piece does", () => {
  const { h, events, advance } = harness()
  // a server returning wrong data: every request is a 206, every piece fails verification
  for (let i = 0; i < 3; i++) {
    h.success("transport")
    h.failure("hash")
  }
  assert.deepEqual(events, ["disable 3 15s hash"])
  advance(15_000)
  h.success("transport") // still garbage after the retry: not "recovered"
  assert.deepEqual(events, ["disable 3 15s hash", "enable retry"])
  h.success("verified")
  assert.deepEqual(events, ["disable 3 15s hash", "enable retry", "recovered"])
  h.failure("hash")
  h.failure("hash")
  h.success("verified")
  h.failure("hash")
  assert.equal(h.state, "ok")
  // transport successes do clear network failures
  h.success("verified")
  h.failure("network")
  h.failure("network")
  h.success("transport")
  h.failure("network")
  assert.equal(h.state, "ok")
})
