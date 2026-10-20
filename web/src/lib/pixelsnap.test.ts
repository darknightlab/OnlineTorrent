import { test } from "node:test"
import assert from "node:assert/strict"
import { snapBox } from "./pixelsnap.ts"

const onGrid = (x: number, dpr: number) => Math.abs(x * dpr - Math.round(x * dpr)) < 1e-6

test("snapped box edges and height land on whole device pixels, losing < 1 device px per side", () => {
  for (const dpr of [1, 1.25, 1.5, 2, 1.1]) {
    for (const [left, width] of [[51.188, 541.625], [68.3, 809.41], [0, 1001], [64, 732], [59.5, 691]]) {
      const s = snapBox(left, width, dpr)
      const x0 = left + s.marginLeft
      const x1 = x0 + s.width
      assert.ok(onGrid(x0, dpr) && onGrid(x1, dpr), `${left}/${width}@${dpr}: ${x0}–${x1}`)
      assert.ok(onGrid(s.height, dpr))
      assert.ok(s.marginLeft >= 0 && s.marginLeft * dpr < 1 + 1e-9)
      assert.ok((left + width - x1) * dpr >= -1e-9 && (left + width - x1) * dpr < 1 + 1e-9)
      // never wider than 16:9 (contain fills the width → no side bars); 16:9 within a device pixel
      assert.ok(s.width / s.height <= 16 / 9 + 1e-9)
      assert.ok(Math.abs(s.width / (16 / 9) - s.height) * dpr < 1)
    }
  }
  // already on the grid: unchanged
  assert.deepEqual(snapBox(64, 732, 1), { marginLeft: 0, width: 732, height: 412 })
})
