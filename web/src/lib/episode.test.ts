// node --test src/lib/episode.test.ts（npm test）
import { test } from "node:test"
import assert from "node:assert/strict"
import { canonicalEpisodeKey, compareEpisodeKeys, episodeLabel, episodeShort, formatEpisodeKey, parseEpisodeKey, type EpisodeKey } from "./episode.ts"

test("parse / format / labels", () => {
  const cases: [string, string, string, string][] = [
    // 输入, 规范键, 显示名, 按钮
    ["12", "12", "第 12 话", "12"],
    ["012", "12", "第 12 话", "12"],
    ["12.5", "12.5", "第 12.5 话", "12.5"],
    ["0", "0", "第 0 话", "0"],
    ["sp1", "sp1", "SP1", "SP1"],
    ["SP01", "sp1", "SP1", "SP1"],
    ["ncop", "ncop1", "NCOP1", "NCOP1"],
    ["ncop2", "ncop2", "NCOP2", "NCOP2"],
    ["nced1", "nced1", "NCED1", "NCED1"],
    ["pv3", "pv3", "PV3", "PV3"],
    ["cm1", "cm1", "CM1", "CM1"],
  ]
  for (const [raw, key, label, short] of cases) {
    const k = parseEpisodeKey(raw)
    assert.ok(k, raw)
    assert.equal(formatEpisodeKey(k), key, raw)
    assert.equal(episodeLabel(k), label, raw)
    assert.equal(episodeShort(k), short, raw)
    assert.deepEqual(parseEpisodeKey(key), k, `${raw} round trip`)
  }
  assert.equal(canonicalEpisodeKey("NCED"), "nced1")
})

test("invalid keys", () => {
  for (const bad of ["", "-1", "abc", "op1", "sp-1", "1e3", "12.", ".5", "5001", "sp1000", "ncop1x", "１２", "12/3"]) {
    assert.equal(parseEpisodeKey(bad), null, bad)
  }
  assert.equal(parseEpisodeKey(undefined), null)
})

test("order", () => {
  const keys = ["cm1", "ncop1", "12.5", "sp2", "2", "nced1", "sp1", "pv1", "0"].map((s) => parseEpisodeKey(s) as EpisodeKey)
  assert.deepEqual(keys.sort(compareEpisodeKeys).map(formatEpisodeKey), ["0", "2", "12.5", "sp1", "sp2", "ncop1", "nced1", "pv1", "cm1"])
})
