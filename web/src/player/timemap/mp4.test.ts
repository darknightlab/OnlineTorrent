import { test } from "node:test"
import assert from "node:assert/strict"
import { mp4Points, parseMoov } from "./mp4.ts"

// ---- a tiny ISO-BMFF writer ----
const be32 = (n: number) => Uint8Array.of((n >>> 24) & 255, (n >>> 16) & 255, (n >>> 8) & 255, n & 255)
const cat = (...p: Uint8Array[]) => {
  const out = new Uint8Array(p.reduce((s, x) => s + x.length, 0))
  let o = 0
  for (const x of p) {
    out.set(x, o)
    o += x.length
  }
  return out
}
const box = (type: string, ...data: Uint8Array[]) => {
  const d = cat(...data)
  return cat(be32(8 + d.length), new TextEncoder().encode(type), d)
}
const full = (type: string, version: number, ...data: Uint8Array[]) => box(type, Uint8Array.of(version, 0, 0, 0), ...data)
const u32s = (...n: number[]) => cat(...n.map(be32))

/**
 * A video track: timescale 1000, 10 samples; stts: 4 × 100 ms then 6 × 50 ms;
 * stsc: chunks 1–2 hold 3 samples, from chunk 3 on 2 samples (→ chunks of 3,3,2,2);
 * edit list starts at media time 100 (one sample of B-frame delay).
 */
function trak(handler: string, offsets: number[], co64 = false) {
  const mdhd = full("mdhd", 0, u32s(0, 0, 1000, 700), Uint8Array.of(0, 0, 0, 0))
  const hdlr = full("hdlr", 0, u32s(0), new TextEncoder().encode(handler), u32s(0, 0, 0), Uint8Array.of(0))
  const stts = full("stts", 0, u32s(2, 4, 100, 6, 50))
  const stsc = full("stsc", 0, u32s(2, 1, 3, 1, 3, 2, 1))
  const chunks = co64 ? full("co64", 0, u32s(offsets.length), ...offsets.map((o) => u32s(Math.floor(o / 2 ** 32), o >>> 0))) : full("stco", 0, u32s(offsets.length, ...offsets))
  const stbl = box("stbl", stts, stsc, full("stsz", 0, u32s(0, 10), ...Array(10).fill(be32(1000))), chunks)
  const edts = box("edts", full("elst", 0, u32s(1, 700, 100, 0x10000)))
  return box("trak", edts, box("mdia", mdhd, hdlr, box("minf", stbl)))
}

test("moov sample tables → one point per chunk (video track, edit-list shift)", () => {
  const moov = box("moov", trak("soun", [5000, 9000]), trak("vide", [1000, 4000, 7000, 8000]))
  const r = parseMoov(moov)!
  // chunk start samples 0, 3, 6, 8 → dts 0, 300, 400+2*50=500, 600 ms; minus the 100 ms edit
  assert.deepEqual(r.points, [[1000, 0], [4000, 0.2], [7000, 0.4], [8000, 0.5]])
  assert.equal(r.duration, 0.7)
  // 64-bit chunk offsets
  const big = parseMoov(box("moov", trak("vide", [2 ** 33, 2 ** 33 + 10, 2 ** 33 + 20, 2 ** 33 + 30], true)))!
  assert.equal(big.points[3][0], 2 ** 33 + 30)
})

test("moov found after mdat (header-only reads of the top-level boxes)", async () => {
  const ftyp = box("ftyp", new TextEncoder().encode("isom"), u32s(0))
  const mdatBody = new Uint8Array(50_000)
  const moov = box("moov", trak("vide", [100, 200, 300, 400]))
  const file = cat(ftyp, box("mdat", mdatBody), moov)
  const reads: [number, number][] = []
  const r = await mp4Points(async (s, e) => {
    reads.push([s, e])
    return file.slice(s, e)
  }, file.length)
  assert.equal(r?.points.length, 4)
  // never read the mdat payload
  assert.ok(reads.every(([s, e]) => e - s <= 16 || s === ftyp.length + 8 + mdatBody.length), JSON.stringify(reads))
  assert.equal(await mp4Points(async (s, e) => new Uint8Array(e - s), 100), null) // not an mp4
})
