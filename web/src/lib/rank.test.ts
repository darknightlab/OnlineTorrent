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
  size: { max: 0, full_below_mb: 0, zero_above_mb: 0 },
  oversize: -10,
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

const score = (f: RankInput, v: Viewer = MEMBER, rules = AV) => scoreFile(f, rules, v, 2 * GB).score

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
  assert.equal(seederScore(0, 2, 6, "log"), 0)
  assert.equal(seederScore(4, 2, 100, "linear"), 8)
  // rules saved before `curve` existed behave linearly
  const legacy = { ...AV, availability: { ...AV.availability, curve: undefined as unknown as "linear" } }
  assert.equal(score(file({ swarm: sw({ wt_seeders: 2 }) }), MEMBER, legacy), 10)
})

test("size bonus: full below, zero above, linear between; unknown size 0", () => {
  const s = R.size
  assert.equal(sizeScore(100 * MB, s), 6)
  assert.equal(sizeScore(300 * MB, s), 6)
  assert.equal(sizeScore(900 * MB, s), 3)
  assert.equal(sizeScore(1500 * MB, s), 0)
  assert.equal(sizeScore(5 * GB, s), 0)
  assert.equal(sizeScore(0, s), 0)
  assert.equal(sizeScore(100 * MB, { max: 0, full_below_mb: 300, zero_above_mb: 1500 }), 0)
  assert.equal(sizeScore(100 * MB, undefined), 0)
})

test("guest −100: no WebRTC seeder and not on the server", () => {
  const s = scoreFile(file({ swarm: sw({ bt_seeders: 23, wt_seeders: 0 }) }), AV, GUEST, 2 * GB)
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
  assert.equal(scoreFile(file(), R, GUEST, 0).known, false)
})

test("keyword groups: only the best match per group counts", () => {
  const kw = (title: string) => scoreFile(file({ torrent_title: title, path: "a.mp4" }), R, MEMBER, 0)
  // 简繁 → 简体 25 only (not 25 + 12)
  assert.equal(kw("[Team] Show - 02 [简繁内封]").score, 25)
  assert.equal(kw("[Team] Show - 02 [CHT]").score, 12)
  assert.equal(kw("[Team] Show - 02 [1080p][HEVC][CHS]").score, 37)
  assert.equal(kw("Show 2160p 1080p").score, 10, "resolution group: max")
  assert.equal(kw("Screen [A1080F2E]").score, 0, "no false matches inside words / CRCs")
  const parts = kw("[Team] Show [简繁内封][1080p]").parts.map((p) => p.label)
  assert.deepEqual(parts, ["简体中文", "1080p"])
})

test("teams, oversize, invalid patterns", () => {
  const rules = { ...AV, teams: [{ name: "Team", score: 5 }, { name: "Other", score: 9 }] }
  assert.equal(scoreFile(file(), rules, MEMBER, 0).score, 5)
  assert.equal(scoreFile(file({ team: "team" }), rules, MEMBER, 0).score, 0, "exact team name")
  assert.equal(score(file({ size_bytes: 3 * GB })), -10)
  assert.equal(scoreFile(file({ size_bytes: 3 * GB }), AV, MEMBER, 0).score, 0, "no limit")
  assert.equal(scoreFile(file(), { ...AV, keywords: [{ label: "bad", pattern: "(", score: 5 }] }, MEMBER, 0).score, 0)
})

test("ranking: blocked last, score, then newest", () => {
  const items = [
    file({ path: "old.mkv", published_at: "2026-10-01T00:00:00Z" }),
    file({ path: "new.mkv", published_at: "2026-10-10T00:00:00Z" }),
    file({ path: "blocked.mkv", access: { action: "login", reason: "" }, swarm: sw({ server_complete: true, wt_seeders: 9 }) }),
    file({ path: "seeded.mkv", published_at: "2026-09-01T00:00:00Z", swarm: sw({ wt_seeders: 1 }) }),
    file({ path: "dead.mkv", published_at: "2026-10-18T00:00:00Z", swarm: sw({ bt_seeders: 0, wt_seeders: 0 }) }),
  ]
  const order = rankFiles(items, AV, MEMBER, 2 * GB).map((s) => s.file.path)
  assert.deepEqual(order, ["seeded.mkv", "new.mkv", "old.mkv", "dead.mkv", "blocked.mkv"])
  assert.match(describeScore(rankFiles(items, AV, MEMBER, 2 * GB)[0]), /推荐分 5：WebRTC 做种 1 \+5/)
  assert.match(describeScore(rankFiles(items, AV, MEMBER, 0).at(-1)!), /不可播放/)
})
