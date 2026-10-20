import { test } from "node:test"
import assert from "node:assert/strict"
import { buildMenu, chooseMenu, type ExternalTrack } from "./set.ts"
import type { SubTrack } from "./mkv.ts"

const emb = (name: string, language = "chi"): SubTrack => ({ number: 3, codec: "ass", header: "", language, languageIETF: "", name, isDefault: false, forced: false, encodings: [] })
const ext = (name: string, langs: string[]): ExternalTrack => ({ fileIndex: 1, path: `x/a.${name}.ass`, format: "ass", name, langs, load: async () => "" })

test("menu: embedded first, then external with readable labels", () => {
  const m = buildMenu([emb("简日双语")], [ext("sc", ["zh-Hans"]), ext("default", [])])
  assert.deepEqual(m.map((x) => [x.kind, x.label]), [["embedded", "简日双语"], ["external", "简体（外挂）"], ["external", "a.default.ass"]])
})

test("auto-pick: equal match → external; embedded matching better → embedded; nothing → off", () => {
  const zh = ["zh-CN"]
  // both monolingual 简体: tie → external
  assert.equal(chooseMenu(buildMenu([emb("简体")], [ext("sc", ["zh-Hans"])]), null, zh), 1)
  // the embedded track carrying the MKV default flag doesn't beat an equally matching external file
  assert.equal(chooseMenu(buildMenu([{ ...emb("简体中文"), isDefault: true }], [ext("sc", ["zh-Hans"])]), null, zh), 1)
  // external is bilingual (简日), embedded monolingual 简体: embedded matches better
  assert.equal(chooseMenu(buildMenu([emb("简体")], [ext("jpsc", ["zh-Hans", "ja"])]), null, zh), 0)
  // embedded only says "chi" (simplified/traditional unknown), external is 简体: external
  assert.equal(chooseMenu(buildMenu([emb("", "chi")], [ext("tc", ["zh-Hant"]), ext("sc", ["zh-Hans"])]), null, zh), 2)
  // no language match at all
  assert.equal(chooseMenu(buildMenu([emb("English", "eng")], [ext("sc", ["zh-Hans"])]), null, ["fr-FR"]), -1)
  // session preference: same name across episodes
  assert.equal(chooseMenu(buildMenu([emb("简体")], [ext("tc", ["zh-Hant"])]), { mode: "track", name: "tc", langs: ["zh-Hant"] }, zh), 1)
})
