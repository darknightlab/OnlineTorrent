import { test } from "node:test"
import assert from "node:assert/strict"
import { deflateSync } from "node:zlib"
import { ID } from "./ebml.ts"
import { parseMkvHeader, readAttachment, readCuePoints } from "./mkv.ts"
import { SubtitleSource, srtToAss } from "./source.ts"
import type { RawBlock } from "./tap.ts"

// ---- a tiny EBML writer ----
const vint = (n: number) => {
  for (let len = 1; len <= 8; len++) {
    if (n < 2 ** (7 * len) - 1) {
      const b = new Uint8Array(len)
      let v = n
      for (let i = len - 1; i >= 0; i--) {
        b[i] = v & 0xff
        v = Math.floor(v / 256)
      }
      b[0] |= 0x80 >> (len - 1)
      return b
    }
  }
  throw new Error("too big")
}
const idBytes = (id: number) => {
  const out: number[] = []
  for (let v = id; v > 0; v = Math.floor(v / 256)) out.unshift(v & 0xff)
  return Uint8Array.from(out)
}
const cat = (...parts: Uint8Array[]) => {
  const out = new Uint8Array(parts.reduce((s, p) => s + p.length, 0))
  let o = 0
  for (const p of parts) {
    out.set(p, o)
    o += p.length
  }
  return out
}
const el = (id: number, ...data: Uint8Array[]) => {
  const d = cat(...data)
  return cat(idBytes(id), vint(d.length), d)
}
const uint = (n: number) => {
  const out: number[] = []
  for (let v = n; v > 0 || !out.length; v = Math.floor(v / 256)) out.unshift(v & 0xff)
  return Uint8Array.from(out)
}
const str = (s: string) => new TextEncoder().encode(s)
const block = (track: number, rel: number, data: Uint8Array) => cat(vint(track), Uint8Array.of((rel >> 8) & 0xff, rel & 0xff, 0x80), data)

const ASS_HEADER = "[Script Info]\nScriptType: v4.00+\n\n[V4+ Styles]\nFormat: Name, Fontname\nStyle: Default,Fake\n\n[Events]\nFormat: Layer, Start, End, Style, Name, MarginL, MarginR, MarginV, Effect, Text\n"
const FONT = Uint8Array.from({ length: 5000 }, (_, i) => i & 0xff)

function makeMkv() {
  const zlibEnc = el(ID.ContentEncodings, el(ID.ContentEncoding, el(ID.ContentEncodingScope, uint(3)), el(ID.ContentCompression, el(ID.ContentCompAlgo, uint(0)))))
  const tracks = el(
    ID.Tracks,
    el(ID.TrackEntry, el(ID.TrackNumber, uint(1)), el(ID.TrackType, uint(1)), el(ID.CodecID, str("V_MPEG4/ISO/AVC"))),
    el(ID.TrackEntry, el(ID.TrackNumber, uint(2)), el(ID.TrackType, uint(17)), el(ID.CodecID, str("S_TEXT/ASS")), el(ID.Language, str("chi")), el(ID.Name, str("简日双语")),
      el(ID.CodecPrivate, deflateSync(str(ASS_HEADER))), zlibEnc),
    el(ID.TrackEntry, el(ID.TrackNumber, uint(3)), el(ID.TrackType, uint(17)), el(ID.CodecID, str("S_TEXT/UTF8")), el(ID.Language, str("eng")), el(ID.FlagDefault, uint(0))),
  )
  const attachments = el(ID.Attachments, el(ID.AttachedFile, el(ID.FileName, str("Fake.ttf")), el(ID.FileMimeType, str("application/x-truetype-font")), el(ID.FileData, FONT)))
  const video = (n: number) => el(ID.SimpleBlock, block(1, 0, new Uint8Array(n).fill(0x1f))) // contains 0x1f bytes to tempt the resync
  const ass = (track: number, rel: number, dur: number, text: string) => el(ID.BlockGroup, el(ID.Block, block(track, rel, deflateSync(str(text)))), el(ID.BlockDuration, uint(dur)))
  const srt = (rel: number, dur: number, text: string) => el(ID.BlockGroup, el(ID.BlockDuration, uint(dur)), el(ID.Block, block(3, rel, str(text)))) // duration before the block
  const clusters = [
    el(ID.Cluster, el(ID.Timestamp, uint(0)), video(40_000), ass(2, 500, 2000, "1,0,Default,,0,0,0,,第一句"), video(30_000), srt(800, 1500, "<i>Hello</i>\nworld")),
    // ffmpeg writes a CRC-32 element first in every cluster
    el(ID.Cluster, el(0xbf, Uint8Array.of(1, 2, 3, 4)), el(ID.Timestamp, uint(10_000)), video(50_000), ass(2, 100, 3000, "2,0,Default,,0,0,0,,第二句"), video(20_000)),
    el(ID.Cluster, el(ID.Timestamp, uint(20_000)), ass(2, 0, 1000, "3,0,Default,,0,0,0,,第三句"), video(60_000), srt(200, 900, "Bye")),
  ]
  const info = el(ID.Info, el(ID.TimestampScale, uint(1_000_000)))
  const segBody = cat(info, tracks, attachments, ...clusters)
  const ebml = el(ID.EBML, el(0x4282, str("matroska")))
  const file = cat(ebml, el(ID.Segment, segBody))
  // where cluster 2 starts (for the mid-file test)
  const c2 = file.length - clusters[2].length - clusters[1].length
  return { file, c2 }
}

const reader = (file: Uint8Array) => async (s: number, e: number) => file.slice(s, Math.min(e, file.length))

function feed(src: SubtitleSource, file: Uint8Array, start: number, seed: number) {
  const tap = src.tap(start)
  let rnd = seed
  for (let p = start; p < file.length; ) {
    rnd = (rnd * 1103515245 + 12345) % 2 ** 31
    const n = 1 + (rnd % 9000)
    tap.push(file.subarray(p, p + n))
    p += n
  }
  return tap
}

test("header: subtitle tracks (zlib CodecPrivate), attachments, timestamp scale", async () => {
  const { file } = makeMkv()
  const h = await parseMkvHeader(reader(file), file.length)
  assert.equal(h.timestampScale, 1_000_000)
  assert.deepEqual(h.subtitles.map((s) => [s.number, s.codec, s.name, s.language, s.isDefault]), [
    [2, "ass", "简日双语", "chi", true],
    [3, "srt", "", "eng", false],
  ])
  assert.equal(h.subtitles[0].header, ASS_HEADER)
  assert.equal(h.trackTypes.get(1), 1)
  assert.equal(h.attachments.length, 1)
  assert.deepEqual(await readAttachment(reader(file), h.attachments[0]), FONT)
})

test("tap: subtitle blocks from the video's byte stream (random chunking), deduped across reads", async () => {
  const { file } = makeMkv()
  const src = new SubtitleSource(reader(file), file.length)
  await src.header
  const got: string[] = []
  src.onCue((tr, c) => got.push(`${tr}@${c.start}+${c.duration}:${c.text}`))
  for (const seed of [1, 7, 42]) feed(src, file, 0, seed)
  await new Promise((r) => setTimeout(r, 20))
  assert.deepEqual(got, [
    "2@500+2000:1,0,Default,,0,0,0,,第一句",
    "3@800+1500:1,0,Default,,0,0,0,,{\\i1}Hello{\\i0}\\Nworld",
    "2@10100+3000:2,0,Default,,0,0,0,,第二句",
    "2@20000+1000:3,0,Default,,0,0,0,,第三句",
    "3@20200+900:2,0,Default,,0,0,0,,Bye",
  ])
  assert.equal(src.cuesOf(2).length, 3)
  assert.deepEqual(await src.fonts(), [FONT])
})

test("tap: a stream starting mid-file (seek) resyncs on the next cluster", async () => {
  const { file, c2 } = makeMkv()
  const src = new SubtitleSource(reader(file), file.length)
  await src.header
  // start inside cluster 1's first video block
  feed(src, file, c2 - 60_000, 3)
  await new Promise((r) => setTimeout(r, 20))
  assert.deepEqual(src.cuesOf(2).map((c) => c.start), [10100, 20000])
  assert.deepEqual(src.cuesOf(3).map((c) => c.start), [20200])
})

test("tap: a file read as many short adjacent requests (cached replay) still yields every cue", async () => {
  const { file } = makeMkv()
  const mid = Math.floor(file.length * 0.4)
  // reference: one long stream from the same point
  const one = new SubtitleSource(reader(file), file.length)
  await one.header
  feed(one, file, mid, 3)
  // the browser reading local data: a chain of 4 KB range requests, each a new stream (shorter than a cluster)
  const many = new SubtitleSource(reader(file), file.length)
  await many.header
  for (let p = mid; p < file.length; p += 4096) many.tap(p).push(file.subarray(p, Math.min(file.length, p + 4096)))
  // overlapping / repeated requests don't disturb it
  for (let p = mid; p < file.length; p += 6000) many.tap(p).push(file.subarray(p, Math.min(file.length, p + 9000)))
  await new Promise((r) => setTimeout(r, 20))
  assert.ok(one.cuesOf(2).length > 0)
  assert.deepEqual(many.cuesOf(2), one.cuesOf(2))
  assert.deepEqual(many.cuesOf(3), one.cuesOf(3))
  // from the very start, also in short requests
  const head = new SubtitleSource(reader(file), file.length)
  await head.header
  for (let p = 0; p < file.length; p += 4096) head.tap(p).push(file.subarray(p, Math.min(file.length, p + 4096)))
  await new Promise((r) => setTimeout(r, 20))
  assert.equal(head.cuesOf(2).length, 3)
  assert.equal(head.cuesOf(3).length, 2)
})

test("tap: tracks learnt from the stream itself when the header isn't parsed yet", async () => {
  const { file } = makeMkv()
  let release: () => void = () => {}
  const gate = new Promise<void>((r) => (release = r))
  const slow = async (s: number, e: number) => {
    await gate
    return file.slice(s, e)
  }
  const blocks: RawBlock[] = []
  const src = new SubtitleSource(slow, file.length)
  const tap = (src as unknown as { newTap(start: number): unknown }).newTap(0) as { push(c: Uint8Array): void }
  // peek at what the tap classifies before the header is known
  ;(tap as unknown as { o: { onBlock: (b: RawBlock) => void } }).o.onBlock = (b) => blocks.push(b)
  tap.push(file)
  assert.deepEqual(blocks.map((b) => b.track), [2, 3, 2, 2, 3])
  release()
})

test("srt → ASS chunk", () => {
  assert.equal(srtToAss("<b>A</b> {x}\r\n<font color=\"#ff8000\">B</font>", 5), "5,0,Default,,0,0,0,,{\\b1}A{\\b0} (x)\\N{\\c&H0080ff&}B{\\c}")
})

test("Cues → time map points (video track preferred); tap records clusters for the no-Cues fallback", async () => {
  const { file } = makeMkv()
  // append Cues after the clusters, point a SeekHead at it: rebuild the file with SeekHead + Cues
  const h0 = await parseMkvHeader(reader(file), file.length)
  const segData = h0.segmentData
  // cluster positions relative to the segment data start (scan the file for cluster IDs)
  const rel: number[] = []
  for (let i = segData; i + 4 <= file.length; i++) if (file[i] === 0x1f && file[i + 1] === 0x43 && file[i + 2] === 0xb6 && file[i + 3] === 0x75) rel.push(i - segData)
  assert.equal(rel.length, 3)
  const cue = (t: number, track: number, pos: number) => el(ID.CuePoint, el(ID.CueTime, uint(t)), el(ID.CueTrackPositions, el(ID.CueTrack, uint(track)), el(ID.CueClusterPosition, uint(pos))))
  const cues = el(ID.Cues, cue(0, 1, rel[0]), cue(10_000, 2, rel[0]), cue(10_000, 1, rel[1]), cue(20_000, 1, rel[2]))
  const h = { ...h0, cuesPos: file.length }
  const withCues = cat(file, cues)
  const pts = await readCuePoints(reader(withCues), h)
  assert.deepEqual(pts, [[segData + rel[0], 0], [segData + rel[1], 10], [segData + rel[2], 20]])
  assert.equal(await readCuePoints(reader(file), { ...h0, cuesPos: -1 }), null)
  // fallback: clusters seen by the tap
  const src = new SubtitleSource(reader(file), file.length)
  await src.header
  feed(src, file, 0, 5)
  assert.deepEqual([...src.clusters], [[segData + rel[0], 0], [segData + rel[1], 10_000], [segData + rel[2], 20_000]])
})
