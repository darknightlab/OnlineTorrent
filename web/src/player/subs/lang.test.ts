import { test } from "node:test"
import assert from "node:assert/strict"
import { browserLang, pickDefault, trackLabel, trackLangs } from "./lang.ts"

const t = (name: string, language = "und", languageIETF = "", isDefault = false) => ({ name, language, languageIETF, isDefault })

test("track language: names win over (often wrong) codes", () => {
  assert.deepEqual(trackLangs(t("简体中文", "chi")), ["zh-Hans"])
  assert.deepEqual(trackLangs(t("繁體中文", "chi")), ["zh-Hant"])
  assert.deepEqual(trackLangs(t("CHS", "jpn")), ["zh-Hans"]) // mislabelled code
  assert.deepEqual(trackLangs(t("[LoliHouse] CHT")), ["zh-Hant"])
  assert.deepEqual(trackLangs(t("SC")), ["zh-Hans"])
  assert.deepEqual(trackLangs(t("TC")), ["zh-Hant"])
  assert.deepEqual(trackLangs(t("GB")), ["zh-Hans"])
  assert.deepEqual(trackLangs(t("BIG5")), ["zh-Hant"])
  assert.deepEqual(trackLangs(t("简日双语")), ["zh-Hans", "ja"])
  assert.deepEqual(trackLangs(t("繁日雙語")), ["zh-Hant", "ja"])
  assert.deepEqual(trackLangs(t("日本語")), ["ja"])
  // fansub shorthand (Nekomoe kissaten & LoliHouse: "JPSC" / "JPTC")
  assert.deepEqual(trackLangs(t("JPSC", "chi", "zh-Hans")), ["zh-Hans", "ja"])
  assert.deepEqual(trackLangs(t("JPTC", "chi", "zh-Hant")), ["zh-Hant", "ja"])
  assert.deepEqual(trackLangs(t("CHS_JP")), ["zh-Hans", "ja"])
  assert.deepEqual(trackLangs(t("TCJP")), ["zh-Hant", "ja"])
  assert.deepEqual(trackLangs(t("JPCHS")), ["zh-Hans", "ja"])
  assert.deepEqual(trackLangs(t("English")), ["en"])
  assert.deepEqual(trackLangs(t("Signs & Songs")), [])
  // no recognisable name: LanguageIETF, then Language
  assert.deepEqual(trackLangs(t("", "chi", "zh-Hans")), ["zh-Hans"])
  assert.deepEqual(trackLangs(t("", "chi", "zh-TW")), ["zh-Hant"])
  assert.deepEqual(trackLangs(t("", "chi")), ["zh"])
  assert.deepEqual(trackLangs(t("", "jpn")), ["ja"])
  assert.deepEqual(trackLangs(t("", "und")), [])
  // words that only contain the letters are not codes
  assert.deepEqual(trackLangs(t("Scenes", "eng")), ["en"])
  assert.deepEqual(trackLangs(t("Japanese", "eng")), ["ja"])
})

test("browser language mapping", () => {
  assert.equal(browserLang("zh-CN"), "zh-Hans")
  assert.equal(browserLang("zh-SG"), "zh-Hans")
  assert.equal(browserLang("zh-Hans"), "zh-Hans")
  assert.equal(browserLang("zh-Hans-CN"), "zh-Hans")
  assert.equal(browserLang("zh-TW"), "zh-Hant")
  assert.equal(browserLang("zh-HK"), "zh-Hant")
  assert.equal(browserLang("zh-Hant-TW"), "zh-Hant")
  assert.equal(browserLang("zh"), "zh")
  assert.equal(browserLang("ja-JP"), "ja")
  assert.equal(browserLang("en-US"), "en")
  assert.equal(browserLang("fr"), "fr")
})

test("default track follows the browser language; nothing when no track matches", () => {
  const tracks = [t("简日双语", "chi"), t("繁日雙語", "chi"), t("简体中文", "chi"), t("繁體中文", "chi")]
  assert.equal(pickDefault(tracks, ["zh-CN", "zh", "en"]), 2) // monolingual 简体 preferred
  assert.equal(pickDefault(tracks, ["zh-TW"]), 3)
  assert.equal(pickDefault(tracks, ["zh-HK", "en"]), 3)
  assert.equal(pickDefault(tracks, ["ja"]), 0) // only bilingual tracks carry Japanese
  assert.equal(pickDefault(tracks, ["en-US", "en"]), -1) // no English: off
  assert.equal(pickDefault(tracks, ["de-DE", "en-US", "zh-CN"]), 2) // later browser languages count
  // a Chinese track with unknown script serves both
  assert.equal(pickDefault([t("", "chi")], ["zh-TW"]), 0)
  assert.equal(pickDefault([t("", "chi")], ["zh-CN"]), 0)
  // explicit script beats unknown
  assert.equal(pickDefault([t("", "chi"), t("", "chi", "zh-Hant")], ["zh-TW"]), 1)
  // browser only says "zh": any Chinese track (explicit preferred), MKV default flag breaks ties
  assert.equal(pickDefault([t("Signs"), t("繁體", "chi", "", false), t("简体", "chi", "", true)], ["zh"]), 2)
  assert.equal(pickDefault([], ["zh-CN"]), -1)
  assert.equal(pickDefault([t("Signs & Songs", "eng")], ["ja"]), -1)
})

test("default with fansub shorthand names", () => {
  const nekomoe = [t("JPSC", "chi", "zh-Hans", true), t("JPTC", "chi", "zh-Hant")]
  assert.equal(pickDefault(nekomoe, ["zh-CN"]), 0)
  assert.equal(pickDefault(nekomoe, ["zh-TW"]), 1)
  assert.equal(pickDefault(nekomoe, ["ja-JP"]), 0)
  assert.equal(pickDefault(nekomoe, ["en-US"]), -1)
})

test("labels", () => {
  assert.equal(trackLabel(t("CHS&JPN"), 0), "CHS&JPN")
  assert.equal(trackLabel(t("", "chi", "zh-Hant"), 0), "繁体")
  assert.equal(trackLabel(t(""), 2), "字幕 3")
})
