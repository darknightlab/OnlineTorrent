// node --test src/lib/rank.test.ts（npm test）——评分机制；默认规则的场景见 rank.scenarios.test.ts
import { test } from "node:test"
import assert from "node:assert/strict"
import { readFileSync } from "node:fs"
import { DEFAULT_RANK_RULES as R, describeScore, rankFiles, scoreFile, seederScore, sizeScore, type RankInput, type Viewer } from "./rank.ts"
import type { RankRules, SwarmStats } from "./types.ts"

const GUEST: Viewer = { canSeed: false, canWebseed: false }
const MEMBER: Viewer = { canSeed: true, canWebseed: true }
const WEBSEED_ONLY: Viewer = { canSeed: false, canWebseed: true }
const MB = 1024 ** 2
const GB = 1024 ** 3

/** 只看可用性的规则（无关键词 / 大小加分），便于验证计算 */
const AV: RankRules = {
  availability: { server_complete: 30, wt_seeder: 5, wt_cap: 30, bt_seeder: 1, bt_cap: 20, curve: "linear", dead: -100 },
  keywords: [],
  teams: [],
  size: { from_mb: 0, from_score: 0, to_mb: 0, to_score: 0, extend: false },
}

const sw = (p: Partial<SwarmStats>): SwarmStats => ({
  bt_seeders: null,
  bt_leechers: null,
  wt_seeders: null,
  wt_leechers: null,
  server_complete: false,
  checked_at: "2026-10-19T00:00:00Z",
  ...p,
})

const file = (p: Partial<RankInput> = {}): RankInput => ({
  torrent_title: "[Team] Show - 02",
  path: "Show - 02.mkv",
  team: "Team",
  size_bytes: 0,
  published_at: "2026-10-18T00:00:00Z",
  access: { action: "allow", reason: "" },
  swarm: null,
  ...p,
})

const score = (f: RankInput, v: Viewer = MEMBER, rules = AV) => scoreFile(f, rules, v).score

test("Go and TS defaults are identical (internal/rank/testdata/default.json)", () => {
  const golden = JSON.parse(readFileSync(new URL("../../../internal/rank/testdata/default.json", import.meta.url), "utf8"))
  assert.deepEqual(R, golden)
})

test("linear availability: server, caps, BT only for seeders", () => {
  assert.equal(score(file({ swarm: sw({ server_complete: true, bt_seeders: 0, wt_seeders: 0 }) })), 30)
  assert.equal(score(file({ swarm: sw({ wt_seeders: 2 }) })), 10)
  assert.equal(score(file({ swarm: sw({ wt_seeders: 50 }) })), 30, "WebRTC cap")
  assert.equal(score(file({ swarm: sw({ bt_seeders: 500 }) })), 20, "BT cap")
  assert.equal(score(file({ swarm: sw({ bt_seeders: 23, wt_seeders: 1 }) }), GUEST), 5)
  assert.equal(score(file({ swarm: sw({ bt_seeders: 23, wt_seeders: 1 }) }), WEBSEED_ONLY), 5)
})

test("log curve: coefficient × log2(1 + n), capped", () => {
  assert.equal(seederScore(1, 2, 6, "log"), 2)
  assert.equal(seederScore(3, 2, 6, "log"), 4)
  assert.equal(seederScore(7, 2, 6, "log"), 6)
  assert.equal(seederScore(500, 2, 6, "log"), 6)
})

test("tanh curve: coefficient × tanh(n / scale)", () => {
  // defaults: BT 10·tanh(n/20), WebRTC 20·tanh(n/20)
  assert.equal(seederScore(3, 10, 10, "tanh", 20), 1.5)
  assert.equal(seederScore(23, 10, 10, "tanh", 20), 8.2)
  assert.equal(seederScore(500, 10, 10, "tanh", 20), 10)
  assert.equal(seederScore(1, 20, 20, "tanh", 20), 1)
  assert.equal(seederScore(20, 20, 20, "tanh", 20), 15.2)
  assert.equal(seederScore(5, 20, 3, "tanh", 20), 3, "cap still applies")
  assert.equal(seederScore(5, 20, 20, "tanh", 0), 0, "no scale → 0")
  assert.equal(seederScore(0, 2, 6, "log"), 0)
  assert.equal(seederScore(4, 2, 100, "linear"), 8)
  // rules saved before `curve` existed behave linearly
  const legacy = { ...AV, availability: { ...AV.availability, curve: undefined as unknown as "linear" } }
  assert.equal(score(file({ swarm: sw({ wt_seeders: 2 }) }), MEMBER, legacy), 10)
})

test("size line: 0 up to 200 MB, −16 at 1000 MB, unbounded beyond", () => {
  const s = R.size
  assert.equal(sizeScore(100 * MB, s), 0)
  assert.equal(sizeScore(200 * MB, s), 0)
  assert.equal(sizeScore(300 * MB, s), -2, "2 points per 100 MB")
  assert.equal(sizeScore(1000 * MB, s), -16)
  assert.equal(sizeScore(2 * GB, s), -37)
  assert.equal(sizeScore(0, s), 0, "unknown size")
  // not extended: flat beyond to_mb
  assert.equal(sizeScore(2 * GB, { ...s, extend: false }), -16)
  // legacy shape {max, full_below_mb, zero_above_mb} = small-file bonus
  const legacy = { max: 16, full_below_mb: 200, zero_above_mb: 1000 } as unknown as RankRules["size"]
  assert.equal(sizeScore(100 * MB, legacy), 16)
  assert.equal(sizeScore(600 * MB, legacy), 8)
  assert.equal(sizeScore(5 * GB, legacy), 0)
  assert.equal(sizeScore(100 * MB, undefined), 0)
})

test("guest −100: no WebRTC seeder and not on the server", () => {
  const s = scoreFile(file({ swarm: sw({ bt_seeders: 23, wt_seeders: 0 }) }), AV, GUEST)
  assert.equal(s.score, -100)
  assert.equal(s.dead, true)
  assert.equal(score(file({ swarm: sw({ bt_seeders: 0, wt_seeders: 0, server_complete: true }) }), GUEST), 30)
  assert.equal(score(file({ swarm: sw({ bt_seeders: 23, wt_seeders: 0 }) }), MEMBER), 20)
})

test("seed/webseed users: −100 only when every source is empty", () => {
  assert.equal(score(file({ swarm: sw({ bt_seeders: 0, wt_seeders: 0 }) })), -100)
  assert.equal(score(file({ swarm: sw({ bt_seeders: 0, wt_seeders: 0 }) }), WEBSEED_ONLY), -100)
  assert.equal(score(file({ swarm: sw({ bt_seeders: 1, wt_seeders: 0 }) })), 1)
  assert.equal(score(file({ swarm: sw({ bt_seeders: 0, wt_seeders: null }) })), -100)
})

test("unknown stats score 0", () => {
  assert.equal(score(file({ swarm: null })), 0)
  assert.equal(score(file({ swarm: null }), GUEST), 0)
  assert.equal(score(file({ swarm: sw({ checked_at: null, bt_seeders: 0, wt_seeders: 0 }) }), GUEST), 0)
  assert.equal(score(file({ swarm: sw({ bt_seeders: 0, wt_seeders: null }) }), GUEST), 0)
  assert.equal(scoreFile(file(), R, GUEST).known, false)
})

test("keyword groups: only the best match per group counts", () => {
  const kw = (title: string) => scoreFile(file({ torrent_title: title, path: "a.mp4" }), R, MEMBER)
  // 简繁 → 简体 25 only (not 25 + 20); 简日 / CHS_JP → 简体
  assert.equal(kw("[Team] Show - 02 [简繁内封]").score, 25)
  assert.equal(kw("[Team] Show - 02 [简日双语]").score, 25)
  assert.equal(kw("[Team] Show - 02 [CHS_JP]").score, 25)
  assert.equal(kw("[Nekomoe kissaten][Show][02][JPTC]").score, 20)
  assert.equal(kw("[Team] Show - 02 [CHT]").score, 20)
  assert.equal(kw("[Team] Show - 02 [1080p][HEVC][CHS]").score, 47)
  assert.equal(kw("Show 2160p 1080p").score, 30, "resolution group: max (4K +30)")
  assert.equal(kw("Show [4320p]").score, 30)
  assert.equal(kw("Show [8K]").score, 30)
  assert.equal(kw("Show [720p]").score, 10)
  assert.equal(kw("Show [480p]").score, 0, "below 720p: 0")
  // Japanese subtitles: +10, but not for 日常 / 日本 in titles
  for (const t of ["Show [JP]", "Show [JPN]", "Show [日字]", "Show 日文字幕", "Show [日语]", "Show [日語]", "Show [Japanese]"]) assert.equal(kw(t).score, 10, t)
  for (const t of ["日常 - 02", "Nichijou 日本語", "日本 Show", "Show [RAW]"]) assert.equal(kw(t).score, 0, t)
  assert.equal(kw("Screen [A1080F2E]").score, 0, "no false matches inside words / CRCs")
  const parts = kw("[Team] Show [简繁内封][1080p]").parts.map((p) => p.label)
  assert.deepEqual(parts, ["简体中文", "1080p"])
})

test("teams, invalid patterns", () => {
  const rules = { ...AV, teams: [{ name: "Team", score: 5 }, { name: "Other", score: 9 }] }
  assert.equal(scoreFile(file(), rules, MEMBER).score, 5)
  assert.equal(scoreFile(file({ team: "team" }), rules, MEMBER).score, 0, "exact team name")
  assert.equal(scoreFile(file(), { ...AV, keywords: [{ label: "bad", pattern: "(", score: 5 }] }, MEMBER).score, 0)
})

test("ranking: score, then newest, then input order (policy is filtered by the backend)", () => {
  const items = [
    file({ path: "old.mkv", published_at: "2026-10-01T00:00:00Z" }),
    file({ path: "new.mkv", published_at: "2026-10-10T00:00:00Z" }),
    file({ path: "seeded.mkv", published_at: "2026-09-01T00:00:00Z", swarm: sw({ wt_seeders: 1 }) }),
    file({ path: "dead.mkv", published_at: "2026-10-18T00:00:00Z", swarm: sw({ bt_seeders: 0, wt_seeders: 0 }) }),
    file({ path: "new2.mkv", published_at: "2026-10-10T00:00:00Z" }),
  ]
  const order = rankFiles(items, AV, MEMBER).map((s) => s.file.path)
  assert.deepEqual(order, ["seeded.mkv", "new.mkv", "new2.mkv", "old.mkv", "dead.mkv"])
  assert.match(describeScore(rankFiles(items, AV, MEMBER)[0]), /推荐分 5：WebRTC 做种 1 \+5/)
})
