// node --test src/lib/scope.test.ts（npm test）
import { test } from "node:test"
import assert from "node:assert/strict"
import { expressionString, scopeVisible, tagGroups, validateScope, type ScopeLike } from "./scope.ts"

const sel = (tags: string[], ops: ("and" | "or")[], exclude: string[] = []): ScopeLike => ({ tag_mode: "selected", tags, tag_operators: ops, exclude_tags: exclude })

test("AND binds tighter than OR", () => {
  assert.deepEqual(tagGroups(["原创", "校园", "战斗"], ["and", "or"]), [["原创", "校园"], ["战斗"]])
  assert.equal(expressionString(["原创", "校园", "战斗"], ["and", "or"]), "(原创 AND 校园) OR 战斗")
  assert.equal(expressionString(["原创", "校园", "战斗"], ["or", "and"]), "原创 OR (校园 AND 战斗)")
  assert.equal(expressionString(["原创", "校园"], ["and"]), "原创 AND 校园")
  assert.equal(expressionString(["原创"], []), "原创")
  assert.equal(expressionString([], []), "")
  const s = sel(["原创", "校园", "战斗"], ["and", "or"])
  assert.equal(scopeVisible(s, ["原创", "校园"]), true)
  assert.equal(scopeVisible(s, ["战斗"]), true)
  assert.equal(scopeVisible(s, ["原创"]), false)
  assert.equal(scopeVisible(s, []), false)
})

test("exclude tags", () => {
  const all: ScopeLike = { tag_mode: "all", tags: [], tag_operators: [], exclude_tags: ["里番"] }
  assert.equal(scopeVisible(all, ["原创"]), true)
  assert.equal(scopeVisible(all, ["原创", "里番"]), false)
  assert.equal(scopeVisible(sel(["原创"], [], ["里番"]), ["原创", "里番"]), false)
  assert.equal(scopeVisible(null, ["x"]), true)
})

test("validation", () => {
  assert.equal(validateScope(sel([], [])), "「仅所选标签」模式至少需要一个标签")
  assert.match(validateScope(sel(["a", "b"], []))!, /运算符数量/)
  assert.match(validateScope(sel(["长".repeat(61)], []))!, /过长/)
  assert.equal(validateScope(sel(["长".repeat(60)], [])), null)
  assert.equal(validateScope({ tag_mode: "all", tags: [], tag_operators: [], exclude_tags: [] }), null)
})
