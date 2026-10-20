import { test } from "node:test"
import assert from "node:assert/strict"
import { SeedRequests } from "./seed.ts"

test("seed requests: once per file, again for another file of the same torrent", () => {
  const s = new SeedRequests()
  assert.equal(s.begin(2), true)
  assert.equal(s.begin(2), false, "in flight")
  s.end(2, "seeded")
  assert.equal(s.begin(2), false, "already requested")
  assert.equal(s.begin(0), true, "switching to another file requests it")
  s.end(0, "no_server")
  assert.equal(s.begin(0), false, "no server now: not re-requested on every switch")
  assert.equal(s.begin(-1), false)
})

test("seed requests: transient failures are retried on the next switch, refusals are not", () => {
  const s = new SeedRequests()
  s.begin(1)
  s.end(1, SeedRequests.outcomeOfError(undefined)) // network
  assert.equal(s.begin(1), true)
  s.end(1, SeedRequests.outcomeOfError(503))
  assert.equal(s.begin(1), true)
  s.end(1, SeedRequests.outcomeOfError(413)) // too_large: a server answer
  assert.equal(s.begin(1), false)
  assert.equal(SeedRequests.outcomeOfError(429), "retry")
  assert.equal(SeedRequests.outcomeOfError(403), "refused")
})
