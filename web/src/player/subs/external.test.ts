import { test } from "node:test"
import assert from "node:assert/strict"
import { decodeSubtitle, externalLabels, externalSubsFor, externalToAss, fontFiles, langsFromHint, timedTextToAss } from "./external.ts"

const F = (...paths: string[]) => paths.map((path) => ({ path, length: 100 }))
const pick = (files: { path: string }[], v: number) => externalSubsFor(files as never, v).map((s) => files[s.fileIndex].path)

test("single video: every subtitle file in the torrent belongs to it", () => {
  const files = F("Movie/Movie.2023.1080p.mkv", "Movie/Movie.2023.1080p.chs.ass", "Movie/Subs/English.srt", "Movie/Subs/forced.vtt", "Movie/readme.txt", "Movie/Fonts/a.ttf")
  assert.deepEqual(pick(files, 0), ["Movie/Movie.2023.1080p.chs.ass", "Movie/Subs/English.srt", "Movie/Subs/forced.vtt"])
  const s = externalSubsFor(files, 0)
  assert.deepEqual(s.map((x) => [x.name, x.langs, x.format]), [["chs", ["zh-Hans"], "ass"], ["English", ["en"], "srt"], ["forced", [], "vtt"]])
  assert.deepEqual(fontFiles(files), [5])
})

test("batch: same stem + language suffixes; other episodes and other stems are excluded", () => {
  const files = F(
    "[Grp] Show [01-12]/[Grp] Show - 01 [1080p].mkv",
    "[Grp] Show [01-12]/[Grp] Show - 01 [1080p].sc.ass",
    "[Grp] Show [01-12]/[Grp] Show - 01 [1080p].tc.ass",
    "[Grp] Show [01-12]/[Grp] Show - 01 [1080p].简体.ass",
    "[Grp] Show [01-12]/[Grp] Show - 01 [1080p].ja.srt",
    "[Grp] Show [01-12]/[Grp] Show - 01 [1080p].ass",
    "[Grp] Show [01-12]/[Grp] Show - 02 [1080p].mkv",
    "[Grp] Show [01-12]/[Grp] Show - 02 [1080p].sc.ass", // other episode
    "[Grp] Show [01-12]/[Grp] Show - 01 [720p].sc.ass", // different stem
    "[Grp] Show [01-12]/[Grp] Show - 01 [1080p] v2.ass", // different stem (v2 is no language)
    "[Grp] Show [01-12]/Subs/[Grp] Show - 01 [1080p].chs&jpn.ass", // sub folder
    "[Grp] Show [01-12]/[SC]/[Grp] Show - 01 [1080p].ass", // language folder
    "[Grp] Show [01-12]/[Grp] Show - 01 [1080p]_TC.ass", // underscore + language
  )
  assert.deepEqual(pick(files, 0), [
    "[Grp] Show [01-12]/[Grp] Show - 01 [1080p].sc.ass",
    "[Grp] Show [01-12]/[Grp] Show - 01 [1080p].tc.ass",
    "[Grp] Show [01-12]/[Grp] Show - 01 [1080p].简体.ass",
    "[Grp] Show [01-12]/[Grp] Show - 01 [1080p].ja.srt",
    "[Grp] Show [01-12]/[Grp] Show - 01 [1080p].ass",
    "[Grp] Show [01-12]/Subs/[Grp] Show - 01 [1080p].chs&jpn.ass",
    "[Grp] Show [01-12]/[SC]/[Grp] Show - 01 [1080p].ass",
    "[Grp] Show [01-12]/[Grp] Show - 01 [1080p]_TC.ass",
  ])
  assert.deepEqual(pick(files, 6), ["[Grp] Show [01-12]/[Grp] Show - 02 [1080p].sc.ass"])
  const langs = externalSubsFor(files, 0).map((s) => s.langs)
  assert.deepEqual(langs, [["zh-Hans"], ["zh-Hant"], ["zh-Hans"], ["ja"], [], ["zh-Hans", "ja"], ["zh-Hans"], ["zh-Hant"]])
})

test("batch: episode 1 doesn't take episode 10's subs; sibling folders yes, far folders no", () => {
  const files = F("S/Show - 1.mkv", "S/Show - 10.mkv", "S/Show - 10.sc.ass", "S/Show - 1 sc.ass", "S/Show - 10 sc.ass")
  assert.deepEqual(pick(files, 0), ["S/Show - 1 sc.ass"])
  assert.deepEqual(pick(files, 1), ["S/Show - 10.sc.ass", "S/Show - 10 sc.ass"])
  const tree = F("Pack/Video/Ep 01.mkv", "Pack/Video/Ep 02.mkv", "Pack/Subs/Ep 01.chs.ass", "Other/Subs/Ep 01.chs.ass")
  assert.deepEqual(pick(tree, 0), ["Pack/Subs/Ep 01.chs.ass"])
  assert.deepEqual(pick(F("A/x.mkv", "A/x.ass", "B/y.mkv"), 2), [])
  assert.deepEqual(pick(F("A/notes.txt", "A/x.ass"), 0), []) // not a video
})

test("labels and language hints", () => {
  assert.deepEqual(langsFromHint("default"), [])
  assert.deepEqual(langsFromHint("zh-Hant"), ["zh-Hant"])
  assert.deepEqual(langsFromHint("en"), ["en"])
  assert.deepEqual(langsFromHint("v2"), [])
  const subs = externalSubsFor(F("x/a.mkv", "x/b.mkv", "x/a.sc.ass", "x/a.chs.srt", "x/a.ass"), 0)
  assert.deepEqual(externalLabels(subs), ["简体（外挂） sc", "简体（外挂） chs", "a.ass"])
})

test("encodings: BOMs, UTF-8, GB18030 vs Big5", () => {
  const text = "第1句 这是简体中文的字幕，我们来看一下。"
  assert.equal(decodeSubtitle(Uint8Array.of(0xef, 0xbb, 0xbf, ...new TextEncoder().encode(text))), text)
  const u16 = new Uint8Array(2 + text.length * 2)
  u16.set([0xff, 0xfe])
  for (let i = 0; i < text.length; i++) {
    u16[2 + 2 * i] = text.charCodeAt(i) & 255
    u16[3 + 2 * i] = text.charCodeAt(i) >> 8
  }
  assert.equal(decodeSubtitle(u16), text)
  assert.equal(decodeSubtitle(new TextEncoder().encode(text)), text)
  // legacy: "我們的時間" in Big5, "我们的时间" in GBK (bytes typed out: no encoder for these in TextEncoder)
  const big5 = Uint8Array.of(0xa7, 0xda, 0xad, 0xcc, 0xaa, 0xba, 0xae, 0xc9, 0xb6, 0xa1)
  assert.equal(decodeSubtitle(big5), "我們的時間")
  const gbk = Uint8Array.of(0xce, 0xd2, 0xc3, 0xc7, 0xb5, 0xc4, 0xca, 0xb1, 0xbc, 0xe4)
  assert.equal(decodeSubtitle(gbk), "我们的时间")
})

test("SRT / VTT → full ASS; ASS passes through", () => {
  const srt = "1\r\n00:00:01,500 --> 00:00:03,000\r\n<i>Hello</i>\r\nworld\r\n\r\n2\r\n00:01:02,000 --> 00:01:04,250\r\nBye\r\n"
  const a = timedTextToAss(srt)
  assert.match(a, /\[Script Info\][\s\S]*\[Events\]/)
  assert.match(a, /Dialogue: 0,0:00:01\.50,0:00:03\.00,Default,,0,0,0,,\{\\i1\}Hello\{\\i0\}\\Nworld/)
  assert.match(a, /Dialogue: 0,0:01:02\.00,0:01:04\.25,Default,,0,0,0,,Bye/)
  const vtt = "WEBVTT\n\nNOTE x\n\n00:05.000 --> 00:06.000 align:start\n<v Bob>Hi, <c.loud>there</c>\n"
  assert.match(timedTextToAss(vtt), /Dialogue: 0,0:00:05\.00,0:00:06\.00,Default,,0,0,0,,Hi, there/)
  const ass = "\uFEFF[Script Info]\nTitle: x\n"
  assert.equal(externalToAss("ass", new TextEncoder().encode(ass)), "[Script Info]\nTitle: x\n")
})
