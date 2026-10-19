// node --test src/lib/pieces.test.ts（npm test）
import { test } from "node:test"
import assert from "node:assert/strict"
import { MAX_SEGMENTS, segmentCount, segmentsGradient } from "./pieces.ts"

test("segmentCount: about one segment per piece, capped", () => {
  assert.equal(segmentCount(0, 16384), 1)
  assert.equal(segmentCount(100, 0), 1)
  assert.equal(segmentCount(10 * 16384, 16384), 11)
  assert.equal(segmentCount(1e12, 16384), MAX_SEGMENTS)
})

test("segmentsGradient: nothing downloaded → none", () => {
  assert.equal(segmentsGradient(new Uint8Array(0)), "none")
  assert.equal(segmentsGradient(new Uint8Array(5)), "none")
})

test("segmentsGradient: merges runs into hard stops", () => {
  assert.equal(
    segmentsGradient(Uint8Array.from([2, 2, 1, 0])),
    "linear-gradient(to right, var(--ot-piece-done) 0% 50%, var(--ot-piece-partial) 50% 75%, transparent 75% 100%)",
  )
  assert.equal(segmentsGradient(Uint8Array.from([2, 2, 2])), "linear-gradient(to right, var(--ot-piece-done) 0% 100%)")
  assert.equal(segmentsGradient(Uint8Array.from([0, 0, 2])), "linear-gradient(to right, transparent 0% 66.667%, var(--ot-piece-done) 66.667% 100%)")
})
