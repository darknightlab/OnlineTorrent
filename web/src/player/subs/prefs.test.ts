import { test } from "node:test"
import assert from "node:assert/strict"
import { chooseTrack, loadPref, prefFor, savePref } from "./prefs.ts"

const t = (name: string, language = "und") => ({ name, language, languageIETF: "" })

test("explicit choice persists across files; otherwise the browser language decides", () => {
  const ep1 = [t("简体中文", "chi"), t("繁體中文", "chi"), t("Signs", "eng")]
  const ep2 = [t("CHS", "chi"), t("CHT", "chi")]
  const zh = ["zh-CN"]
  assert.equal(chooseTrack(ep1, null, zh), 0)
  assert.equal(chooseTrack(ep1, null, ["fr"]), -1)
  // picked 繁體 on episode 1 → episode 2 gets its 繁体 track (different name, same language)
  const p = prefFor(ep1, 1)
  assert.deepEqual(p, { mode: "track", name: "繁體中文", langs: ["zh-Hant"] })
  assert.equal(chooseTrack(ep2, p, zh), 1)
  // same name wins
  assert.equal(chooseTrack([t("Signs", "eng"), t("繁體中文")], { mode: "track", name: "繁體中文", langs: [] }, zh), 1)
  // picked "off": stays off
  assert.equal(chooseTrack(ep2, prefFor(ep1, -1), zh), -1)
  // picked a language the next file doesn't have: browser default
  assert.equal(chooseTrack(ep2, prefFor([t("Signs", "eng")], 0), zh), 0)
})

test("pref storage round trip and garbage", () => {
  const m = new Map<string, string>()
  const st = { getItem: (k: string) => m.get(k) ?? null, setItem: (k: string, v: string) => void m.set(k, v) }
  assert.equal(loadPref(st), null)
  savePref({ mode: "track", name: "CHS", langs: ["zh-Hans"] }, st)
  assert.deepEqual(loadPref(st), { mode: "track", name: "CHS", langs: ["zh-Hans"] })
  savePref({ mode: "off" }, st)
  assert.deepEqual(loadPref(st), { mode: "off" })
  m.set("ot.subtitle.pref", "{bad")
  assert.equal(loadPref(st), null)
})
