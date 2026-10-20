import { test } from "node:test"
import assert from "node:assert/strict"
import { buildTimeMap, byteToTimeFraction } from "./timemap.ts"
import { segmentsGradient } from "./pieces.ts"

test("buildTimeMap: sorted by offset, time made monotonic, duplicate offsets merged, decimated", () => {
  const m = buildTimeMap([[300, 30], [100, 10], [200, 25], [200, 20], [250, 18], [NaN, 1]])!
  assert.deepEqual(m.bytes, [100, 200, 250, 300])
  assert.deepEqual(m.times, [10, 25, 25, 30])
  assert.equal(buildTimeMap([[1, 1]]), null)
  const big = buildTimeMap(Array.from({ length: 10_000 }, (_, i): [number, number] => [i * 10, i]), 100)!
  assert.equal(big.bytes.length, 101)
  assert.equal(big.bytes.at(-1), 99_990)
})

test("byteToTimeFraction: VBR — a late byte range maps to a later time span", () => {
  // 1000-byte file, 100 s: the first half of the bytes holds 80 s (low bitrate), the rest 20 s
  // (high bitrate); a 50-byte header before the first sample
  const m = buildTimeMap([[50, 0], [500, 80], [1000, 100]])!
  const f = byteToTimeFraction(m, 1000, 100)
  assert.equal(f(0), 0)
  assert.equal(f(0.05), 0) // header → 0
  assert.ok(Math.abs(f(0.275) - 0.4) < 1e-9) // halfway through the low-bitrate part
  assert.ok(Math.abs(f(0.75) - 0.9) < 1e-9)
  assert.equal(f(1), 1)
  // monotonic
  let prev = -1
  for (let x = 0; x <= 1; x += 0.01) {
    const y = f(x)
    assert.ok(y >= prev)
    prev = y
  }
  // moov at the end: bytes after the last sample map to the rest of the duration
  const tail = byteToTimeFraction(buildTimeMap([[0, 0], [900, 95]])!, 1000, 100)
  assert.ok(Math.abs(tail(0.95) - 0.975) < 1e-9)
})

test("segmentsGradient draws byte segments at their time span", () => {
  const segs = Uint8Array.from([0, 0, 0, 2]) // last quarter of the bytes downloaded
  assert.equal(segmentsGradient(segs), "linear-gradient(to right, transparent 0% 75%, var(--ot-piece-done) 75% 100%)")
  const m = buildTimeMap([[0, 0], [500, 80], [1000, 100]])!
  // the last quarter of the bytes is the last 10 % of the time
  assert.equal(segmentsGradient(segs, byteToTimeFraction(m, 1000, 100)), "linear-gradient(to right, transparent 0% 90%, var(--ot-piece-done) 90% 100%)")
})
