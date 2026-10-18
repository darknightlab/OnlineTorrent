// node --test src/player/store/policy.test.ts（npm test）
import { test } from "node:test"
import assert from "node:assert/strict"
import {
  GiB,
  MIN_BUDGET,
  MiB,
  bitfieldFromPieces,
  computeBudget,
  edgePieces,
  infoHashFromMagnet,
  isProtected,
  pickVictim,
  protectionFor,
  readerRange,
  shrinkOnQuota,
  type CacheEntry,
  type LiveState,
  type Victim,
} from "./policy.ts"

const pieces = (from: number, to: number, size = 1) => new Map(Array.from({ length: to - from + 1 }, (_, k) => [from + k, size] as [number, number]))
const live = (readers: [number, number, number][], pinned: number[] = []): LiveState => ({
  readers: readers.map(([piece, from, to]) => ({ piece, from, to })),
  pinned: new Set(pinned),
})

/** 反复淘汰直到没有可淘汰对象，返回淘汰序列 */
function drain(entries: CacheEntry[], legacy: { key: string; bytes: number }[] = []) {
  const out: Victim[] = []
  const lg = [...legacy]
  const es = entries.map((e) => ({ ...e, pieces: new Map(e.pieces) }))
  for (let v = pickVictim(lg, es); v; v = pickVictim(lg, es)) {
    out.push(v)
    if (v.kind === "legacy") lg.splice(lg.findIndex((l) => l.key === v.key), 1)
    else if (v.kind === "torrent") es.find((e) => e.key === v.key)!.pieces.clear()
    else es.find((e) => e.key === v.key)!.pieces.delete(v.index)
    assert.ok(out.length < 10_000)
  }
  return out
}

test("budget = min(max_push, 25% quota)", () => {
  assert.equal(computeBudget(4 * GiB, 100 * GiB), 4 * GiB)
  assert.equal(computeBudget(4 * GiB, 8 * GiB), 2 * GiB)
  assert.equal(computeBudget(undefined, 8 * GiB), 2 * GiB)
  assert.equal(computeBudget(3 * GiB, undefined), 3 * GiB)
  assert.equal(computeBudget(0, 0), 2 * GiB)
  assert.equal(computeBudget(1, 1), MIN_BUDGET)
  assert.equal(shrinkOnQuota(100 * MiB, 200 * MiB, 10 * MiB), 90 * MiB)
  assert.equal(shrinkOnQuota(300 * MiB, 200 * MiB, 10 * MiB), 180 * MiB)
  assert.equal(shrinkOnQuota(5 * MiB, 200 * MiB, 10 * MiB), 10 * MiB)
})

test("protection scales with the budget, at least one/two pieces", () => {
  const big = protectionFor(4 * GiB, 4 * MiB)
  assert.equal(big.aheadBytes, 32 * MiB)
  assert.equal(big.behindBytes, 8 * MiB)
  assert.equal(big.edgeBytes, 8 * MiB)
  assert.equal(protectionFor(4 * GiB, 4 * MiB, 2 * MiB).aheadBytes, 120 * MiB) // 60 s × 2 MB/s
  const small = protectionFor(6 * MiB, 256 * 1024)
  assert.ok(small.aheadBytes + small.behindBytes + 2 * small.edgeBytes < 6 * MiB)
  const huge = protectionFor(64 * MiB, 16 * MiB)
  assert.equal(huge.aheadBytes, 32 * MiB)
  assert.equal(huge.behindBytes, 16 * MiB)
  assert.deepEqual(edgePieces(10, 100, MiB, 3 * MiB).sort((a, b) => a - b), [10, 11, 12, 98, 99, 100])
  assert.deepEqual(edgePieces(5, 5, MiB, 8 * MiB), [5, 5].slice(0, 2))
  const r = readerRange(10 * MiB + 5, MiB, { aheadBytes: 3 * MiB, behindBytes: MiB, edgeBytes: MiB }, 0, 11)
  assert.deepEqual(r, { piece: 10, from: 9, to: 11 })
})

test("order: legacy → idle torrent LRU → behind farthest → far ahead farthest", () => {
  const entries: CacheEntry[] = [
    { key: "old", lastUsed: 100, pieces: pieces(0, 2) },
    { key: "older", lastUsed: 50, pieces: pieces(0, 1) },
    { key: "other-tab", lastUsed: 1, pieces: pieces(0, 5), busy: true },
    // 正在播放：读者在 50，保护 [48, 55]，钉住首尾 0 和 99
    { key: "play", lastUsed: 999, pieces: pieces(0, 99), live: live([[50, 48, 55]], [0, 99]) },
  ]
  const seq = drain(entries, [{ key: "fsa dir", bytes: 7 }, { key: "chunks", bytes: 0 }])
  assert.deepEqual(seq.slice(0, 2).map((v) => v.kind + ":" + v.key), ["legacy:fsa dir", "legacy:chunks"])
  assert.deepEqual(seq.slice(2, 4).map((v) => v.kind + ":" + v.key), ["torrent:older", "torrent:old"])
  const ps = seq.slice(4) as Extract<Victim, { kind: "piece" }>[]
  assert.ok(ps.every((v) => v.key === "play"))
  // behind：1..47，最远（1）先
  const behind = ps.filter((v) => v.reason === "behind").map((v) => v.index)
  assert.deepEqual(behind, Array.from({ length: 47 }, (_, k) => 1 + k))
  assert.equal(ps.findIndex((v) => v.reason === "ahead"), 47, "all behind pieces go before any ahead piece")
  // ahead：98..56，最远先
  const ahead = ps.filter((v) => v.reason === "ahead").map((v) => v.index)
  assert.deepEqual(ahead, Array.from({ length: 43 }, (_, k) => 98 - k))
  // 从不淘汰：窗口 48..55、首尾 0 / 99、其他 tab 的种子
  const gone = new Set(ps.map((v) => v.index))
  for (const i of [0, 48, 50, 55, 99]) assert.ok(!gone.has(i), `piece ${i} must be protected`)
  assert.ok(!seq.some((v) => v.key === "other-tab"))
})

test("multiple readers: pieces between readers are 'ahead' of the lower one; nothing evictable → null", () => {
  const e: CacheEntry = { key: "p", lastUsed: 0, pieces: pieces(0, 40), live: live([[10, 10, 12], [30, 30, 32]]) }
  const seq = drain([e]) as Extract<Victim, { kind: "piece" }>[]
  assert.deepEqual(seq.filter((v) => v.reason === "behind").map((v) => v.index), [0, 1, 2, 3, 4, 5, 6, 7, 8, 9])
  // 13..29 距 10 的距离 3..19，33..40 距 30 的距离 3..10：按距离降序
  const ahead = seq.filter((v) => v.reason === "ahead")
  for (let k = 1; k < ahead.length; k++) assert.ok(ahead[k - 1].distance >= ahead[k].distance)
  assert.equal(ahead[0].index, 29)
  assert.equal(pickVictim([], [{ key: "x", lastUsed: 0, pieces: pieces(5, 6), live: live([[5, 4, 7]]) }]), null)
  assert.ok(isProtected(live([[5, 4, 7]], [20]), 20))
  // 播放中但还没有读位置：按位置 0 处理，最远的先走（钉住的除外）
  const noReader = drain([{ key: "n", lastUsed: 0, pieces: pieces(0, 3), live: live([], [0, 3]) }])
  assert.deepEqual(noReader.map((v) => (v as { index: number }).index), [2, 1])
})

test("bitfield / magnet helpers", () => {
  assert.deepEqual([...bitfieldFromPieces(10, [0, 7, 8, 9, 42])], [0b10000001, 0b11000000])
  assert.equal(bitfieldFromPieces(16, []).length, 2)
  assert.equal(infoHashFromMagnet("magnet:?xt=urn:btih:656393D2E72F0F5E167015B02900D14388AF79D9&dn=x"), "656393d2e72f0f5e167015b02900d14388af79d9")
  // base32 形式（同一个 hash）
  assert.equal(infoHashFromMagnet("magnet:?xt=urn:btih:MVRZHUXHF4HV4FTQCWYCSAGRIOEK66OZ"), "656393d2e72f0f5e167015b02900d14388af79d9")
  assert.equal(infoHashFromMagnet("magnet:?dn=x"), null)
})
