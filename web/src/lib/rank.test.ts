// node --test src/lib/rank.test.ts（npm test）
import { test } from "node:test"
import assert from "node:assert/strict"
import { DEFAULT_RANK_RULES as R, describeScore, rankFiles, scoreFile, type RankInput, type Viewer } from "./rank.ts"
import type { SwarmStats } from "./types.ts"

const GUEST: Viewer = { canSeed: false, canWebseed: false }
const MEMBER: Viewer = { canSeed: true, canWebseed: true }
const WEBSEED_ONLY: Viewer = { canSeed: false, canWebseed: true }
const GB = 1024 ** 3

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
  size_bytes: 500 * 1024 ** 2,
  published_at: "2026-10-18T00:00:00Z",
  access: { action: "allow", reason: "" },
  swarm: null,
  ...p,
})

const score = (f: RankInput, v: Viewer = MEMBER) => scoreFile(f, R, v, 2 * GB).score

test("availability: server, caps, BT only for seeders", () => {
  assert.equal(score(file({ swarm: sw({ server_complete: true, bt_seeders: 0, wt_seeders: 0 }) })), 30)
  assert.equal(score(file({ swarm: sw({ wt_seeders: 2 }) })), 10)
  assert.equal(score(file({ swarm: sw({ wt_seeders: 50 }) })), 30, "WebRTC cap +30")
  assert.equal(score(file({ swarm: sw({ bt_seeders: 7 }) })), 7)
  assert.equal(score(file({ swarm: sw({ bt_seeders: 500 }) })), 20, "BT cap +20")
  // BT seeders don't help users who can't make the server seed
  assert.equal(score(file({ swarm: sw({ bt_seeders: 23, wt_seeders: 1 }) }), GUEST), 5)
  assert.equal(score(file({ swarm: sw({ bt_seeders: 23, wt_seeders: 1 }) }), WEBSEED_ONLY), 5)
})

test("guest −100 rule: no WebRTC seeder and not on the server", () => {
  const s = scoreFile(file({ swarm: sw({ bt_seeders: 23, wt_seeders: 0 }) }), R, GUEST, 2 * GB)
  assert.equal(s.score, -100)
  assert.equal(s.dead, true)
  // server has it: fine for guests too
  assert.equal(score(file({ swarm: sw({ bt_seeders: 0, wt_seeders: 0, server_complete: true }) }), GUEST), 30)
  // members can make the server fetch it from BT
  assert.equal(score(file({ swarm: sw({ bt_seeders: 23, wt_seeders: 0 }) }), MEMBER), 20)
})

test("seed/webseed users: −100 only when every source is empty", () => {
  assert.equal(score(file({ swarm: sw({ bt_seeders: 0, wt_seeders: 0 }) })), -100)
  assert.equal(score(file({ swarm: sw({ bt_seeders: 0, wt_seeders: 0 }) }), WEBSEED_ONLY), -100)
  assert.equal(score(file({ swarm: sw({ bt_seeders: 1, wt_seeders: 0 }) })), 1)
  assert.equal(score(file({ swarm: sw({ bt_seeders: 0, wt_seeders: null }) })), -100, "unknown WebRTC count counts as 0 once BT is known")
})

test("unknown stats score 0 (never scraped / no tracker answered)", () => {
  assert.equal(score(file({ swarm: null })), 0)
  assert.equal(score(file({ swarm: null }), GUEST), 0)
  assert.equal(score(file({ swarm: sw({ checked_at: null, bt_seeders: 0, wt_seeders: 0 }) }), GUEST), 0)
  // guests: WebTorrent trackers never answered → no penalty
  assert.equal(score(file({ swarm: sw({ bt_seeders: 0, wt_seeders: null }) }), GUEST), 0)
  assert.equal(scoreFile(file(), R, GUEST, 0).known, false)
})

test("keywords, teams and size", () => {
  const f = file({ torrent_title: "[Team] Show - 02 [WebRip 1080p HEVC-10bit AAC][简繁内封]", path: "Show - 02 [1080p][x265].mkv" })
  // HEVC +3, 1080p +3, 简体 +2, 繁体 +1 — HEVC / 10-bit is not penalised any more
  assert.equal(score(f), 9)
  assert.equal(score(file({ torrent_title: "Show 2160p", path: "a.mp4" })), 1)
  assert.equal(score(file({ torrent_title: "[CHT] Show", path: "a.mp4" })), 1)
  assert.equal(score(file({ torrent_title: "Screen", path: "a.mp4" })), 0, "\\bsc\\b does not match inside words")
  const rules = { ...R, teams: [{ name: "Team", score: 5 }, { name: "Other", score: 9 }] }
  assert.equal(scoreFile(file(), rules, MEMBER, 0).score, 5)
  assert.equal(scoreFile(file({ team: "team" }), rules, MEMBER, 0).score, 0, "exact team name")
  assert.equal(score(file({ size_bytes: 3 * GB })), -10)
  assert.equal(scoreFile(file({ size_bytes: 3 * GB }), R, MEMBER, 0).score, 0, "no limit")
  // an invalid pattern is ignored, not thrown
  assert.equal(scoreFile(file(), { ...R, keywords: [{ label: "bad", pattern: "(", score: 5 }] }, MEMBER, 0).score, 0)
})

test("ranking: blocked last, score, then newest", () => {
  const items = [
    file({ path: "old.mkv", published_at: "2026-10-01T00:00:00Z" }),
    file({ path: "new.mkv", published_at: "2026-10-10T00:00:00Z" }),
    file({ path: "blocked.mkv", access: { action: "login", reason: "" }, swarm: sw({ server_complete: true, wt_seeders: 9 }) }),
    file({ path: "seeded.mkv", published_at: "2026-09-01T00:00:00Z", swarm: sw({ wt_seeders: 1 }) }),
    file({ path: "dead.mkv", published_at: "2026-10-18T00:00:00Z", swarm: sw({ bt_seeders: 0, wt_seeders: 0 }) }),
  ]
  const order = rankFiles(items, R, MEMBER, 2 * GB).map((s) => s.file.path)
  assert.deepEqual(order, ["seeded.mkv", "new.mkv", "old.mkv", "dead.mkv", "blocked.mkv"])
  const top = rankFiles(items, R, MEMBER, 2 * GB)[0]
  assert.match(describeScore(top), /推荐分 5：WebRTC 做种 1 \+5/)
  assert.match(describeScore(rankFiles(items, R, MEMBER, 0).at(-1)!), /不可播放/)
})

test("guest vs member pick (BT-only torrent vs WebRTC-seeded one)", () => {
  // like /subject/bgm/624085/episode/2: one torrent with many BT seeders but no WebRTC peer,
  // one with a WebRTC seeder
  const items = [
    file({ path: "bt-only.mp4", published_at: "2026-10-13T00:00:00Z", swarm: sw({ bt_seeders: 23, wt_seeders: 0 }) }),
    file({ path: "webrtc.mkv", published_at: "2026-10-16T00:00:00Z", swarm: sw({ bt_seeders: 3, wt_seeders: 1 }) }),
  ]
  assert.equal(rankFiles(items, R, GUEST, 2 * GB)[0].file.path, "webrtc.mkv")
  assert.equal(rankFiles(items, R, MEMBER, 2 * GB)[0].file.path, "bt-only.mp4") // 20 vs 3+5
})
