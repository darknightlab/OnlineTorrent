// node --test src/lib/danmaku.test.ts（npm test）
import { test } from "node:test"
import assert from "node:assert/strict"
import {
  DEFAULT_DANMAKU_SETTINGS,
  LaneAllocator,
  danmakuTextProblem,
  lowerBoundDanmu,
  parseDanmakuSettings,
  pluginOptions,
  toDanmu,
} from "./danmaku.ts"

test("server danmaku → sorted internal format", () => {
  assert.deepEqual(
    toDanmu([
      [4, 9, 0, "未知模式按滚动"],
      [1.5, 1, 0xffffff, "滚动"],
      [3, 4, 0x00ff, "底部"],
      [2, 5, 0xff0000, "顶部"],
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

test("own danmaku get a border", () => {
  assert.deepEqual(toDanmu([[2, 1, 0xffffff, "我的", 1]]), [{ text: "我的", time: 2, mode: 0, color: "#ffffff", border: true }])
})

test("lowerBoundDanmu binary search cursor", () => {
  const list = toDanmu([
    [1, 1, 0xffffff, "a"],
    [2.5, 1, 0xffffff, "b"],
    [2.5, 1, 0xffffff, "c"],
    [5, 1, 0xffffff, "d"],
  ])
  assert.equal(lowerBoundDanmu(list, 0), 0)
  assert.equal(lowerBoundDanmu(list, 1), 0)
  assert.equal(lowerBoundDanmu(list, 2.5), 1)
  assert.equal(lowerBoundDanmu(list, 3), 3)
  assert.equal(lowerBoundDanmu(list, 6), 4)
})

test("LaneAllocator: 2-point scroll anti-overlap and static top/bottom allocation", () => {
  const lanes = new LaneAllocator()
  // height 100px, fontSize 24 (lineHeight 30), area 0.75 -> maxBottom 75, usable 65 -> 2 lanes (top=10, top=40)
  lanes.configure(100, 24, 0.75, 10)

  // First scroll comment at t=0 takes lane 0 (top=10)
  assert.equal(lanes.allocate(0, 0, 100, 500, 5), 10)
  // Immediate second scroll comment at t=0 takes lane 1 (top=40)
  assert.equal(lanes.allocate(0, 0, 100, 500, 5), 40)
  // Third scroll comment at t=0: both lanes full -> null (dropped cleanly)
  assert.equal(lanes.allocate(0, 0, 100, 500, 5), null)

  // At t=1.1s, first comment (speed = 600/5 = 120px/s) has moved 132px >= 100 + 16 gap -> lane 0 is free for a same-width comment
  assert.equal(lanes.allocate(0, 1.1, 100, 500, 5), 10)
  // But a much wider/faster comment at t=1.1s in lane 1 would catch up before the previous exits -> rejected
  assert.equal(lanes.allocate(0, 1.1, 400, 500, 5), null)

  // Top static comments allocate from top (10, then 40, then null until 5s expires)
  assert.equal(lanes.allocate(1, 0, 80, 500, 5), 10)
  assert.equal(lanes.allocate(1, 1, 80, 500, 5), 40)
  assert.equal(lanes.allocate(1, 2, 80, 500, 5), null)
  assert.equal(lanes.allocate(1, 5, 80, 500, 5), 10)

  // Bottom static comments allocate from bottom (40, then 10)
  assert.equal(lanes.allocate(2, 0, 80, 500, 5), 40)
  assert.equal(lanes.allocate(2, 0, 80, 500, 5), 10)
  assert.equal(lanes.allocate(2, 0, 80, 500, 5), null)
})

test("settings: defaults, normalisation, plugin options", () => {
  assert.deepEqual(parseDanmakuSettings(null), DEFAULT_DANMAKU_SETTINGS)
  assert.deepEqual(parseDanmakuSettings({ visible: false, opacity: 5, size: "xl", speed: "fast", area: 0.3 }), {
    visible: false,
    opacity: 1,
    size: "m",
    speed: "fast",
    area: 0.75,
  })
  assert.equal(parseDanmakuSettings({ opacity: 0.01 }).opacity, 0.1)
  assert.deepEqual(pluginOptions({ visible: true, opacity: 0.5, size: "l", speed: "slow", area: 0.25 }, false), {
    visible: true,
    opacity: 0.5,
    fontSize: 30,
    speed: 8,
    margin: [10, "75%"],
  })
  assert.deepEqual(pluginOptions({ ...DEFAULT_DANMAKU_SETTINGS, area: 1 }, true).margin, [10, 10])
  assert.equal(pluginOptions(DEFAULT_DANMAKU_SETTINGS, true).fontSize, 15)
})

test("text check before sending (same rules as the server)", () => {
  assert.equal(danmakuTextProblem("   "), "")
  assert.equal(danmakuTextProblem(" 好 "), null)
  assert.equal(danmakuTextProblem("字".repeat(100)), null)
  assert.equal(danmakuTextProblem("字".repeat(101)), "最多 100 个字")
  assert.equal(danmakuTextProblem("😀".repeat(100)), null) // counted as characters, not UTF-16 units
  assert.equal(danmakuTextProblem("a\nb"), "不能包含换行或控制字符")
  assert.equal(danmakuTextProblem("a\u2028b"), "不能包含换行或控制字符")
})
