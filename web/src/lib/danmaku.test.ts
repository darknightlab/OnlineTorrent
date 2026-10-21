// node --test src/lib/danmaku.test.ts（npm test）
import { test } from "node:test"
import assert from "node:assert/strict"
import { toDanmu } from "./danmaku.ts"

test("server danmaku → plugin format", () => {
  assert.deepEqual(
    toDanmu([
      [1.5, 1, 0xffffff, "滚动"],
      [2, 5, 0xff0000, "顶部"],
      [3, 4, 0x00ff, "底部"],
      [4, 9, 0, "未知模式按滚动"],
      [5, 1, 0, ""],
      [-1, 1, 0, "负时间"],
    ]),
    [
      { text: "滚动", time: 1.5, mode: 0, color: "#ffffff" },
      { text: "顶部", time: 2, mode: 1, color: "#ff0000" },
      { text: "底部", time: 3, mode: 2, color: "#0000ff" },
      { text: "未知模式按滚动", time: 4, mode: 0, color: "#000000" },
    ],
  )
})
