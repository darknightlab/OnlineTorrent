// node --test src/lib/danmaku.test.ts（npm test）
import { test } from "node:test"
import assert from "node:assert/strict"
import { DEFAULT_DANMAKU_SETTINGS, danmakuTextProblem, parseDanmakuSettings, pluginOptions, toDanmu } from "./danmaku.ts"

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

test("own danmaku get a border", () => {
  assert.deepEqual(toDanmu([[2, 1, 0xffffff, "我的", 1]]), [{ text: "我的", time: 2, mode: 0, color: "#ffffff", border: true }])
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
