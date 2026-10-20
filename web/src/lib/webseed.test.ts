// node --test src/lib/webseed.test.ts（npm test）
import { test } from "node:test"
import assert from "node:assert/strict"
import { applyRanges, bitsFromRanges, countPieces, hasPiece, resetToRanges, retryDelay, WebseedSubscription, type EventSourceLike, type SubState } from "./webseed.ts"

test("ranges ↔ bitfield", () => {
  const b = bitsFromRanges(20, [0, 2, 7, 7, 18, 30])
  assert.deepEqual([...b], [0b11100001, 0b00000000, 0b00110000])
  assert.equal(countPieces(b, 20), 6)
  assert.deepEqual(applyRanges(b, 20, [2, 4], true), [3, 4]) // 2 already set
  assert.deepEqual(applyRanges(b, 20, [0, 0, 19, 19], false), [0, 19])
  assert.ok(!hasPiece(b, 0) && hasPiece(b, 1) && hasPiece(b, 18) && !hasPiece(b, 19))
  const r = resetToRanges(b, 20, [1, 1, 10, 11])
  assert.deepEqual(r.added, [10, 11])
  assert.deepEqual(r.removed, [2, 3, 4, 7, 18])
  assert.deepEqual(applyRanges(b, 20, [5], true), []) // odd length: ignored tail
})

test("retry delays: 15 s, 30 s, 60 s, … capped at 5 min", () => {
  assert.deepEqual([0, 1, 2, 3, 4, 5, 6, 50].map(retryDelay), [15e3, 30e3, 60e3, 120e3, 240e3, 300e3, 300e3, 300e3])
})

/** fake EventSource + fake timers */
class FakeES implements EventSourceLike {
  readyState = 0
  closed = false
  onerror: ((e: unknown) => void) | null = null
  private ls = new Map<string, ((e: { data?: unknown }) => void)[]>()
  addEventListener(type: string, cb: (e: { data?: unknown }) => void) {
    this.ls.set(type, [...(this.ls.get(type) ?? []), cb])
  }
  emit(type: string, data: unknown) {
    for (const cb of this.ls.get(type) ?? []) cb({ data: JSON.stringify(data) })
  }
  close() {
    this.closed = true
    this.readyState = 2
  }
}

function harness() {
  const opened: FakeES[] = []
  const timers: { cb: () => void; ms: number; id: number }[] = []
  let nextId = 1
  const log: string[] = []
  const states: SubState[] = []
  let seedCalls = 0
  const sub = new WebseedSubscription(
    {
      open: () => {
        const es = new FakeES()
        opened.push(es)
        return es
      },
      setTimeout: (cb, ms) => {
        const t = { cb, ms, id: nextId++ }
        timers.push(t)
        return t.id
      },
      clearTimeout: (id) => {
        const i = timers.findIndex((t) => t.id === id)
        if (i >= 0) timers.splice(i, 1)
      },
    },
    {
      seed: (e) => log.push(`seed ${e.url} ${e.pieces} ${JSON.stringify(e.have)} ${e.complete} ${e.token ?? "-"} ${e.exp ?? "-"}`),
      have: (u, r) => log.push(`have ${u} ${r}`),
      lost: (u, r) => log.push(`lost ${u} ${r}`),
      complete: (u) => log.push(`complete ${u}`),
      token: (u, t, exp) => log.push(`token ${u} ${t} ${exp}`),
      done: () => log.push("done"),
      waiting: (p) => log.push(`waiting ${p}`),
      unavailable: async (reason) => {
        log.push(`unavailable ${reason}`)
        if (reason === "not_seeded") seedCalls++
      },
      state: (s) => states.push(s),
    },
  )
  const fire = () => {
    const t = timers.shift()
    assert.ok(t, "a retry timer is pending")
    t.cb()
    return t.ms
  }
  return { sub, opened, timers, log, states, fire, seeds: () => seedCalls }
}

const tick = () => new Promise((r) => setTimeout(r, 0))

test("subscription: ready (per-URL seeds, edge tokens) → have/lost/token/complete → done ends without reconnecting", () => {
  const h = harness()
  h.sub.start()
  const es = h.opened[0]
  const M = "https://www.x/webseed/hash/ab/"
  const E = "https://edge.x/webseed/hash/ab/"
  es.emit("waiting", { progress: 0.25 })
  es.emit("ready", { pieces: 10, seeds: [{ url: M, have: [0, 3], complete: false }, { url: E, have: [0, 5], complete: false, token: "t1", exp: 100 }, { have: [] }] })
  es.emit("have", { url: E, ranges: [6, 7] })
  es.emit("lost", { url: M, ranges: [0, 0] })
  es.emit("have", { url: E, ranges: "garbage" })
  es.emit("token", { url: E, token: "t2", exp: 200 })
  es.emit("seed", { url: "https://edge2.x/w/", have: [0, 9], complete: true, token: "u1", exp: 300 })
  es.emit("complete", { url: E })
  assert.equal(h.sub.state, "open", "complete for one URL doesn't end the stream")
  es.emit("done", {})
  assert.deepEqual(h.log, [
    "waiting 0.25",
    `seed ${M} 10 [0,3] false - -`,
    `seed ${E} 10 [0,5] false t1 100`,
    `have ${E} 6,7`,
    `lost ${M} 0,0`,
    `token ${E} t2 200`,
    "seed https://edge2.x/w/ 10 [0,9] true u1 300",
    `complete ${E}`,
    "done",
  ])
  assert.equal(h.sub.state, "finished")
  assert.ok(es.closed)
  assert.equal(h.timers.length, 0)
  h.sub.kick()
  assert.equal(h.opened.length, 1)
})

test("subscription: errors reconnect with backoff; any progress event resets it", () => {
  const h = harness()
  h.sub.start()
  h.opened[0].onerror?.({})
  assert.equal(h.sub.state, "waiting-retry")
  assert.ok(h.opened[0].closed, "closed instead of letting the browser retry")
  assert.equal(h.fire(), 15_000)
  h.opened[1].onerror?.({})
  assert.equal(h.fire(), 30_000)
  h.opened[2].onerror?.({})
  assert.equal(h.fire(), 60_000)
  // connected and got data → next failure starts at 15 s again
  h.opened[3].emit("ready", { pieces: 4, seeds: [{ url: "u", have: [], complete: false }] })
  h.opened[3].onerror?.({})
  assert.equal(h.fire(), 15_000)
  // late events from a closed stream are ignored
  h.opened[3].emit("have", { url: "u", ranges: [1, 1] })
  assert.ok(!h.log.includes("have u 1,1"))
  assert.equal(h.opened.length, 5)
})

test("subscription: kick reconnects immediately while waiting; stop cancels", () => {
  const h = harness()
  h.sub.start()
  h.opened[0].onerror?.({})
  assert.equal(h.timers.length, 1)
  h.sub.kick() // visibilitychange → visible / online
  assert.equal(h.opened.length, 2)
  assert.equal(h.timers.length, 0)
  h.sub.kick() // connected: no-op
  assert.equal(h.opened.length, 2)
  h.sub.stop()
  assert.ok(h.opened[1].closed)
  assert.equal(h.sub.state, "stopped")
})

test("subscription: unavailable / timeout escalate the backoff (no reset) and call the handler first", async () => {
  const h = harness()
  h.sub.start()
  h.opened[0].emit("unavailable", { reason: "not_seeded" })
  await tick()
  assert.equal(h.seeds(), 1)
  assert.equal(h.fire(), 15_000)
  h.opened[1].emit("unavailable", { reason: "not_seeded" })
  await tick()
  assert.equal(h.fire(), 30_000)
  h.opened[2].emit("timeout", {})
  await tick()
  assert.equal(h.fire(), 60_000)
  assert.equal(h.seeds(), 2)
  assert.deepEqual(h.states.slice(0, 4), ["connecting", "open", "waiting-retry", "connecting"])
})

test("countPieces counts within a piece range", () => {
  const b = bitsFromRanges(20, [0, 4, 10, 12]) // pieces 0–4 and 10–12
  assert.equal(countPieces(b, 20), 8)
  assert.equal(countPieces(b, 20, 10, 15), 3)
  assert.equal(countPieces(b, 20, 3, 11), 4)
})
