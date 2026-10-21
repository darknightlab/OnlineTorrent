# Decision model calibration (LLM and Clef)

Torrent files that the rules can't map with certainty go to a **decision model**. There are two
backends, chosen by the setting `clef.provider` (settings group 决策模型):

- `openai` (default): an OpenAI-compatible LLM with structured output. Currently `gpt-6-luna`
  through the owner's gateway `https://sub2api.projectk.org/v1`.
- `clef`: Cloudflare Workers AI Clef (kept as the alternative; its calibration is in the second
  half of this page).

Design: [plan.md](../plan.md) 「规则收紧 + 决策模型」. API: [api.md](../api.md) 「后台」→「决策模型」.

## Names v5: attributed manami names and evidence normalisation (2026-10-21)

See [manami-names.md](manami-names.md) for release format, deterministic ID linking,
filtering, attribution, memory measurements and production pending-file replay.
Recorded v2 model answers: **255 applied / 0 wrong**, unchanged. Each evidence change
(long vowels, trailing sequel number, bracket removal, franchise-only alias prefix,
OVA parent names) was ablated independently; no change introduced a wrong auto-mapping.
Rules on the same full bangumi-data baseline: 197 → 196 certain, no new wrong; Tenipuri
is conservatively deferred because its short franchise name now names an older work.
Of 91 live production `no_name_evidence` files, **66 pass the name gate** offline; 25 remain
without deterministic shared-ID links. No model API calls or production writes.

## Date gate v4: late releases only matter for single-episode torrents (2026-10-21)

Owner decisions:
- **Early releases:** a release more than 7 days before the episode's airdate goes to review
  (日期不符) for every torrent. This includes the "not aired yet" rule for undated episodes.
- **Late releases:** a release more than 30 days after the airdate (undated: after the work's
  end) goes to review only for a **single-episode torrent**, meaning exactly one video file.
  - A torrent with 2 or more video files of any kind (episodes, SP, NCOP/NCED, PV, extras) is
    a batch or a later compilation, and may come any time after airing.
  - This replaces v3's late-release batch rule (≥ 3 files, unique fit, year match).
- **Both paths:** the gate applies to the rules as well as the decision model.
  - A certain rule mapping that fails it becomes a pending rule decision (`source = rule`,
    reason `date_mismatch: …`), as it already did without a release time.
  - Example: a forged "[LoliHouse] Show - 05" submitted before episode 5 aired → review.
- **Removed:** the Clef scorer's `old_entry` gate (entry ended long before publication). The
  single-episode late check covers it.

Offline replays:

| set | v3 | v4 |
|---|---|---|
| calibration v2 (240 torrents, 458 files): applied / wrong | 255 / 0 | **255 / 0** (coverage 95.5 %) |
| the 152 unresolved production files | 149 applied | **149 applied**, 2 not mapped, 1 rule decision |

The calibration replay takes the batch flag from the real torrent's video files, not from
the sampled task. The date gate's wrong catches still hold. The single-episode namesakes
(Collector 2001, 桃源暗鬼 S1) stay late singles, and the 2027 桃源暗鬼 第2クール is early.

## Post-checks v3: unresolved decisions (2026-10-21)

The 152 production files that went through the decision model and still weren't applied
automatically (25 torrents) were analysed one by one. Six changes to the checks *after* the
model answers were made; the model, prompt and candidates are unchanged. Code:
`clef.Refine` (`internal/clef/refine.go`), plus `meta` name / season / date helpers.

1. **Prequel numbering + "not aired yet"** (归还者 S2 `[14]`).
   - A main episode without an airdate counts as not aired when an earlier episode of the
     same work airs more than 7 days after the release. bangumi lists future episodes
     without dates, and episode 14 had become "valid".
   - For a stated season ≥ 2, a number the entry doesn't list, or one that can't have aired
     yet, is retried as the number minus the direct prequel's episode count (14 − 12 = 2).
     It is only used if that episode is listed and passes the date gate.
   - The change is recorded in `answers.rule`.
2. **Parser agreement after the split-cour shift.** "Slime 4th Season - 24(96)" is the same
   official episode (96) whether read as 24 or 96.
3. **Late-release batch rule** *(replaced in v4 by "late releases only matter for
   single-episode torrents", see above)*. A release more than 30 days after airing (never
   an early one) is applied anyway when all of these hold:
   - the torrent has ≥ 3 main files on the same work, each passing every other check;
   - their numbers, with the stated season, fit no other work the torrent names, among the
     candidates and the work's family (`meta.UniqueFit`);
   - any year written in the title or those paths lies in the work's airing years.

   The work's specials that were held only for being late follow.
4. **Name evidence** (`meta.NameEvidence` / `WorkNames`):
   - " / " in a torrent title no longer cuts it like a path;
   - a two-CJK-character name counts when a whole bracket equals it (`[遮天]`);
   - the names of direct prequels / sequels count;
   - an English name derived from related works "<title> <subtitle>" with aliases
     "<name>: <subtitle>" counts when ≥ 2 such works agree (名探偵コナン → "Detective Conan").
5. **Season continuation.** A stated season that doesn't fit is accepted when all of these
   hold:
   - the entry is the direct sequel of a prequel whose numbering it continues (first sort =
     the prequel's last + 1, the file numbered in that continuation);
   - the same torrent has files with the same stated season mapped to the prequel.

   Example: SAO "S04E13–E23" after S04E01–E12 → War of Underworld part 1.
6. **第弐幕 / Dai Ni Maku = part 2**, as a stated season and as an entry's season (old
   numerals 壱弐参 too).

Offline replays (recorded answers, no API calls; `clefcalib analyze` / `clefcalib replay`):

| set | before | after |
|---|---|---|
| calibration v2 (240 torrents, 458 files): applied / wrong | 244 / 0 (coverage 91.4 %) | **255 / 0 (coverage 95.5 %)** |
| the 152 unresolved production files | 0 applied | **149 applied**, 2 not mapped, 1 rule decision unchanged |

- **Calibration:**
  - The date gate's three wrong catches (single-episode torrents) are still caught.
  - The second-cour 桃源暗鬼 answer is now caught by the date gate (released before the
    entry airs) instead of the name gate.
  - 武神 for 獸王武神 is still caught by the name gate.
- **Production replay:**
  - Run on a copy of the dev meta DB with the 25 torrents and fresh episode lists from
    production.
  - Two answers change: 归还者 S2 `[14]` → episode 2 (correct; 14 hadn't aired).
  - Still not mapped: the Kamen Rider film (not in the catalogue) and Chitose S02E13
    (genuinely ambiguous).
  - Sintel stays a rule decision without a release time.

## LLM backend v2 (current, 2026-10-19)

Owner decisions after testing reasoning effort on two failures (Onegai AiPri with the true entry
missing; Tanuki and Kitsune 2026 remake vs. the 2018 web series):

- **Short prompt, more information.** The system prompt is ~6 lines (no rule catalogue). The
  model gets: the torrent's **release date**, and per candidate the Japanese title, Chinese
  title, up to 8 aliases (bangumi infobox 别名 + bangumi-data titleTranslate: English / romaji /
  zh), an offline **kana→romaji** transliteration of the title when no romanised alias exists
  (タヌキとキツネ → "tanuki to kitsune"), type, season, air start / end date, episode count, the
  official main-episode sort range and the specials.
- **`reasoning_effort: medium`** (setting `llm.reasoning_effort`, default medium; `default` =
  not sent). The gateway accepts `temperature: 0` together with it, so it stays.
- **Retrieval** (`internal/meta/retrieve.go`): an in-memory index over every work's names in
  one normalised form, rebuilt every 30 min and after catalogue changes. Names include the title,
  the Chinese title, all aliases and the romaji form. Normalisation: lower case, no
  punctuation / spaces, macrons and long vowels folded (ou/oo → o, uu → u, ei → e), wo → o,
  season / part markers removed. Scoring:
  - exact title-fragment match;
  - the name contained in the text;
  - a title fragment contained in the name;
  - latin-word / CJK-bigram coverage;
  - +10 for works airing around the release date.

  The top 12, plus relations of the top 3, become the candidates. The old FTS search is gone.
- **Date gate** (owner decision; replaces the entry-age rule): a decision-model mapping is only
  applied when the release time fits the chosen episode's official airdate: at most 30 days
  after and 7 days before it, after the split-cour shift. If the episode has no airdate, the
  work's airing window is used (start − 7 d … end + 30 d; open-ended without an end date).
  Otherwise it goes to review with reason **日期不符** (`date_mismatch: <detail>`). BD / batch
  releases landing in review is intended.
- **Release time**:
  - Crawled torrents: their crawl `published_at`.
  - User submissions: `torrents.submitted_at` (with `submitted_by` = the app user id), set
    once from the first allowed play when the qB metadata creates the row. The `published_at`
    of a user row is not a release date, and the .torrent's `creation date` is not used.
  - Neither available → **缺少发布时间** (`missing_release_time`) → review, for the decision
    model *and* the rules (see plan.md).
  - A user torrent crawled later takes the crawl `published_at`.

### Retrieval recall (634 labelled torrents, `clefcalib recall`)

Share of torrents whose true subject is among the candidates:

| candidates from | before (FTS) | after (normalised index) |
|---|---|---|
| production list (rules' anchor + hints + search + relations) | 100 % | 100 % |
| search only, torrent title + file names | 100 % | 100 % (top 5: 99.7 %) |
| search only, **file names only** (harder: fansub romaji, no title) | 91.5 % (top 5: 89.0 %) | **97.2 %** (top 5: 95.7 %) |

The target was ≥ 98 %. It is met with the torrent title. With file names only, 18 torrents are
still missed, mostly abbreviations:
- "Jukishi", "Yukionna", "Saikyosoubi" (an abbreviation of the romaji title);
- "Tenipuri";
- "CONAN";
- non-Japanese titles ("Turma de Elite").

Retrieval takes ~60 ms per torrent.

### Sample

`clefcalib llm` on the same copy of the dev DB, seeds 1 / 7 / 11 / 23 with 60 torrents each
(240 torrents, 458 scored files, 267 mappable). Torrents whose stored label is invalid are
skipped. **The candidates come from production retrieval without the rules' anchor / hints**,
which is what an uncertain file gets. The sets:

- **pos** (160): the true entry is whatever the label says. If retrieval misses it, null is
  the right answer.
- **neg** (60, realistic): retrieval run as if the true entry weren't in the catalogue yet (a
  new show or season). Only siblings, namesakes and remakes come back.
- **neg-removed** (20): the old method, with the true entry taken out of the rule-built list.

Each set includes synthetic non-episode siblings (NCOP / NCED / PV / CM / Menu / Preview).

### Results (medium effort)

Cost: **1.03 requests per torrent**, 1,207 prompt + 128 completion tokens, latency median
**4.4 s**, p90 8.3 s, max 28 s. No errors.

| set | mode | files | mappable | applied | precision | coverage |
|---|---|---|---|---|---|---|
| pos | model alone | 306 | 267 | 272 | 97.1 % | 98.9 % |
| pos | with gates | 306 | 267 | 244 | **100 %** | 91.4 % |
| neg (realistic) | model alone | 115 | 0 | 8 | 0 % (8 wrong) | – |
| neg (realistic) | with gates | 115 | 0 | 0 | – | – |
| neg-removed | model alone | 37 | 0 | 0 | – | – |
| neg-removed | with gates | 37 | 0 | 0 | – | – |
| **all** | **model alone** | 458 | 267 | 280 | **94.3 %** | **98.9 %** |
| **all** | **with gates** | 458 | 267 | 244 | **100 %** | **91.4 %** |

"Model alone" means every answer that passes the hard validation is applied (no confidence
threshold, no gates).

**The 16 wrong answers of the model alone:**
- **8** were synthetic "Preview" files answered as PV (the label says other). The parser-kind
  gate catches them, and in production the parser classifies them as extras anyway.
- **8** were real namesakes when the true entry was absent:
  - Collector (2001) ×2, for 恐怖コレクター 2026: caught by the date gate;
  - 桃源暗鬼 season 1, for 日光・華厳の滝編: caught by the date gate;
  - 桃源暗鬼 第2クール (2027): caught by the name gate;
  - 武神 动态漫画 ×2, for 獸王武神丹帝拜恩: caught by the name gate;
  - タヌキとキツネ (2018) ×2: confidence 0.82 / 0.75, so review band. The model noted itself
    that the release is much later than the entry; the date gate would catch it too.

With the true entry among the candidates (pos), the model alone picked a wrong *entry* 0
times.

What the gates held back (right answer to review / wrong answer caught):

| gate | right → review | wrong caught |
|---|---|---|
| date (日期不符) | 15 | 3 |
| missing release time (user rows of the sample) | 4 | 0 |
| name evidence | 0 | 3 |
| parser number / kind | 1 | 8 |
| below apply threshold (0.9) | 0 | 2 |

The 15 right answers sent to review by the date gate are late releases: BD / batch rips and
re-uploads of old episodes. That is the intended trade-off.

Self-reported confidence still says little: 272 of 280 applied answers report ≥ 0.98, and
95.6 % of those are right.

### Compared with v1 and Clef

| | Clef (seeds 7 + 11) | LLM v1 (300 torrents) | LLM v2 (240 torrents) |
|---|---|---|---|
| candidates | rule-built | rule-built | production retrieval, no rule help |
| negatives | removed from list | removed from list | mostly realistic (removed from the catalogue) |
| apply precision | 100 % (107) | 100 % (298) | 100 % (244) |
| coverage of mappable | 79.9 % | 93.4 % | 91.4 % (98.9 % before the date gate) |
| tokens / torrent | ~5.5k in | 2.3k + 0.14k | 1.2k + 0.13k |
| latency median | 2 × 1–2 s | 3.8 s | 4.4 s (medium) |

### Defaults (v2)

| setting | default |
|---|---|
| `clef.provider` | `openai` |
| `llm.model` | `gpt-6-luna` |
| `llm.reasoning_effort` | `medium` |
| `llm.apply_threshold` | 0.9 |
| `llm.review_threshold` | 0.5 |
| `llm.require_agreement` | true |
| `llm.concurrency` | 2 |
| `llm.rate_per_min` | 30 |
| `llm.daily_cap` | 5000 |
| `llm.timeout_sec` | 120 |

The gates are fixed in code and apply to both backends: name evidence, special next to a main
episode, and release time / date (`clef.GateRelease`).

## LLM backend, first version (v1, superseded)

Kept for comparison. The request, gates and defaults changed in v2 (above): short prompt,
medium reasoning, retrieval without the rules' help, per-episode date gate instead of the
entry-age rule.

#### Request

- **One chat completion per torrent**, temperature 0, `response_format: {type: "json_schema", strict: true}`.
  Torrents with more than 60 video files are split into chunks, each with the same candidates.
- **Input (JSON user message)**: torrent title, publish date, video files (index + path), and up
  to 20 candidates. Each candidate has: id `bgm_…`, Japanese title, Chinese title, ≤ 4 aliases,
  type, season, air / end date, episode count, the official main-episode sorts as runs
  (`"13–24"`), and specials (`"sp1–2, ncop1"`).
- **Output**: per file `{index, subject_id (enum of candidate ids | null), episode (number as
  written | null), kind (main/sp/op/ed/pv/cm/other), confidence, reason}`, plus a torrent-level
  `suggested_title`.
- **Second round**: if some file got no subject and the model suggested a title, the catalogue
  is searched for it (FTS + related works of the top hits, ≤ 10 new candidates). If that finds
  new candidates, those files are asked about once more with the extended list. This happened
  for 10 % of torrents.
- **System prompt**: only choose from the candidates, null when unsure; seasons, sequels,
  remakes, re-edits and movies are separate entries; a stated season must match; the episode
  number exactly as written (no season offsets, no absolute numbering; a special's own number:
  NCOP2 → 2).

#### Validation (hard, same as Clef)

- subject = candidate id;
- the number must be one of the numbers written in the file name;
- the key, after the split-cour shift, must be in the work's official list;
- the season stated in the name must fit the entry.

#### Gates for "apply" (calibrated below)

- confidence ≥ apply (0.9);
- **name evidence**: the torrent title / paths contain one of the work's titles or aliases, or
  a title fragment of the torrent occurs in one of its names (after normName, with season /
  part markers removed; `meta.NameEvidence`);
- **parser agreement**: the number the file-name parser found (if any), and its explicit
  special kind (NCOP/NCED/PV/CM keyword), aren't contradicted;
- an unlisted special (most works list no OP/ED/PV) only next to an applied main episode of the
  same work in the torrent;
- the entry is recent (not ended > 1 year before publication) and doesn't start more than a
  month *after* publication.

Anything that fails a gate goes to the review queue (reason `no_name_evidence`,
`parser_disagrees`, `unlisted_special`, `old_entry`, `future_entry`).

#### Sample

Same method and script as for Clef: `cmd/clefcalib`, new subcommand `llm` (concurrency 2; the
key comes from `LLM_API_KEY` and is never printed). It ran on the same copy of the dev DB, with
the same sampling:

- positives, plus negatives with the true subject removed. The catalogue search in the second
  round also skips the removed subject;
- synthetic non-episode siblings (NCOP / NCED / PV / CM / Menu / Preview);
- the remake / re-edit cases (タヌキとキツネ, ドラゴンボール超 ビルス, KERORO 軍曹☆).

Seeds 1 (120 torrents), 7 + 11 (the 120 torrents of Clef's final calibration) and a fresh
holdout seed 23 (60). 300 torrents in all, 551 scored files. 11 torrents whose stored label is
itself invalid are dropped: the stored episode isn't in the work's list, and the synthetic
specials derived from it are unreliable too.

```sh
LLM_API_KEY=… clefcalib llm -meta-db /tmp/calib/meta.db -out llm-s7.jsonl -pos 40 -neg 20 -seed 7
clefcalib analyze -meta-db /tmp/calib/meta.db [-llm-dump] llm-s7.jsonl
```

#### Results

Cost and speed: **1.1 requests per torrent**, 2,290 prompt + 136 completion tokens, latency
median **3.8 s**, p90 6.6 s, max 12.4 s. No errors in 331 requests.

**Self-reported confidence is useless for routing.** 92 % of answers report ≥ 0.98, and 43 of
the wrong subjects are among them:

| confidence | files | answer correct | wrong subject |
|---|---|---|---|
| < 0.9 | 11 | 9 | 3 |
| 0.90–0.98 | 34 | 26 (76 %) | 15 |
| ≥ 0.98 | 506 | 496 (98.0 %) | 43 |

Wrong answers are almost all adversarial negatives: with the true entry removed, the model picks
a namesake. Examples: とある暗部の少女共棲 for "Tempal: Item no Chikara", ひみつのアイプリ リング編
for "Onegai AiPri", the old ケロロ / タヌキとキツネ / ドラゴンボール超, and a not-yet-airing 第2クール.
On positives it is nearly always right, including the season (薬屋 S3, 夜桜 第2期 第2クール,
千歳くん 第2期) and the split-cour numbers.

Apply precision by gate (all 300 torrents, apply ≥ 0.9, old entries → review):

| gate | applied | precision | coverage |
|---|---|---|---|
| none | 311 | 95.8 % | 93.4 % |
| parser number | 311 | 95.8 % | 93.4 % |
| rule hint agrees | 306 | 97.4 % | 93.4 % |
| name evidence | 300 | 99.3 % | 93.4 % |
| **all gates (default)** | **298** | **100 %** | **93.4 %** |

The apply threshold barely matters: 0.5 to 0.95 give the same result, and 0.98 drops coverage
to 90 %. The default stays at 0.9. Name evidence does the work: every wrong namesake lacks it,
and positives practically always have it.

Bands at the defaults (apply ≥ 0.9 + gates, review ≥ 0.5):

| set | torrents | applied | precision | coverage of mappable positives | review (correct) |
|---|---|---|---|---|---|
| seeds 1 + 7 + 11 | 240 | 236 | 100 % | 93.7 % | 22 (8) |
| holdout seed 23 | 60 | 62 | 100 % | 92.5 % | 6 (3) |
| **total** | **300** | **298** | **100 %** | **93.4 %** | 28 (11) |

Caveat: the gates were chosen after looking at the seed 1 / 7 / 11 errors. On the untouched
holdout (seed 23) they gave 62/62. Its one earlier error was a synthetic `Preview` file answered
as PV; in production such files never reach the model, because the parser classifies them as
extra and the kind gate catches them.

The rest of the mappable positives (~6 %) are not mapped:
- multi-episode files (`045~046`) answered `other`;
- unnumbered OVA files named by date / title (`OVA 1989-12-27 ハートをつかめ`): no number in the
  name, and the rules no longer match official titles;
- a few numbers the parser would read differently (`invalid_number`).

#### Compared with Clef (same 120 torrents, seeds 7 + 11)

| | Clef (clef-flash, separate + yes/no, prob ≥ 0.8) | LLM (gpt-6-luna, gates) |
|---|---|---|
| applied / correct | 107 / 107 | 120 / 120 |
| coverage of mappable positives | 79.9 % | **95.2 %** |
| review items | 34 (24 correct) | 10 (3 correct) |
| requests / torrent | 2 | 1.1 |
| tokens / torrent | ~5.5k input | ~2.4k input + 0.14k output |
| latency / torrent | ~2 × 1–2 s | median 3.5 s |
| cost | free plan ≈ 400 torrents/day, then paid | subscription (≈ 0 marginal) |

#### Defaults

`clef.provider = openai`, `llm.model = gpt-6-luna`, `llm.apply_threshold = 0.9`,
`llm.review_threshold = 0.5`, `llm.require_agreement = true`, `llm.concurrency = 2`,
`llm.rate_per_min = 30`, `llm.daily_cap = 5000`, `llm.timeout_sec = 120`. The name-evidence,
special and date gates are fixed in code.

## Clef backend (Cloudflare Workers AI, 2026-10-19)

Torrent files the rules can't map with certainty go to Cloudflare's decision model **Clef** (Workers AI
`@cf/cloudflare/clef-flash` / `@cf/cloudflare/clef`). This page explains how the defaults for the
admin-editable thresholds (settings group 决策模型) were picked. Design and API:
plan.md / api.md.

### Facts about the API (measured)

- Answers carry `probabilities` (one per option, summing to 1) and a `confidence`. **`confidence` is
  not the top probability.** It sits far below it: subject 0.3–0.6 at top probability 0.6–0.9, and
  episode / kind about 0.7–0.9 at 0.85–0.97. So a fixed confidence threshold sends nearly
  everything to review (see the `conf` rows below: at `confidence ≥ 0.6` only 13 % of positives
  get applied).
- Questions in one request affect each other (owner's measurement, and here: same torrents, same
  options, but different numbers once the subject question sits in its own request).
- `noul` (yes/no) answers: `{"type":"noul","noul":p}`.
- Real usage: about 2.6–2.9k input tokens per request with ~12 candidates and up to 4 files.
  Our byte-based estimate (`EstimateTokens`, bytes × 0.4) matches it.
- Free plan: the daily allocation ("10,000 neurons") ran out after **≈ 2.5 M clef-flash + 0.1 M clef
  input tokens** (≈ 935 requests of this size). After that every request fails with HTTP 429, code
  4006 ("you have used up your daily free allocation"). The worker treats that like the daily
  cap: the job waits until the next UTC day and the failed run doesn't count as an attempt.
  At 2 requests (~5.5k tokens) per torrent, the free plan covers roughly **400 torrents/day**.

### Method

Script: `cmd/clefcalib` (in the repo). Run it on a **copy** of the mapping DB (here a copy of the
dev `meta.db`, 928 torrents):

```sh
source ~/.config/onlinetorrent/cloudflare.env; source ~/.config/onlinetorrent/clef.env
export CF_ACCOUNT_ID CF_AI_TOKEN          # the token is never printed
go build -o /tmp/calib/clefcalib ./cmd/clefcalib
clefcalib run -meta-db /tmp/calib/meta.db -out final.jsonl -mode separate -verify -pos 40 -neg 20 -seed 11
clefcalib analyze -meta-db /tmp/calib/meta.db [-apply 0.8 -margin 0.3 -review 0.5] [-dump 0.7] final.jsonl
```

- **Positives**: torrents whose stored mapping the *current (tightened)* rules reproduce with
  certainty (exact title match + valid episode; stratum `certain`), plus some that were mapped by
  the old heuristics (alias substring, season / folder / type routing; stratum `heuristic`,
  labelled with the stored mapping). Batches contribute up to 3 episode files.
- **Negatives**: every third sampled torrent with **every true subject removed from the
  candidates**, so the right answer is `none`. Its siblings (other seasons, the movie …) stay in
  the list, which makes these deliberately adversarial.
- **Non-episode files**: real NCOP/NCED/PV/CM/extra files are rare in the DB (5 torrents). Each
  torrent therefore also gets a synthetic sibling: the first file's name with its episode number
  replaced by `NCOP` / `NCED` / `PV` / `Menu` / `CM01` / `NCOP2` / `Preview` (truth: op1 / ed1 /
  pv1 / not mapped / cm1 / op2 / not mapped).
- Analysis reruns the production `clef.Decide` on the recorded answers (validation, conditioning,
  thresholds). It drops labels that are themselves invalid: 8 heuristic mappings point at an
  episode the work doesn't have (e.g. "HOTEL INHUMANS - 16" stored on season 1, which has 1–13;
  Clef answered season 2, which lists 14–26, and that answer is right).
- Precision = correct mappings among *applied* ones (wrong subject, kind or episode key, or any
  mapping of a negative, counts as wrong). Coverage = correct applied / mappable positive files.

Budget used: 895 clef-flash requests (2.48 M input tokens) and 40 clef requests (0.10 M), all on
2026-10-19. That exhausted the free allocation.

### What changed between runs

| version | change |
|---|---|
| v1 | one request per torrent: `subject` (choice) + per file `f{i}_ep` + `f{i}_kind`; candidates as JSON `{title, cn, aliases, air_date, eps, type}` |
| v1.1 | candidates also get `season` (stated by their titles, or 1 for an entry without a TV prequel); the state gets `c_published`; the kind options name *Preview/予告* as `other` (v1 answered `pv`) |
| decide | **conditioning**: subject probabilities of candidates that lack the chosen episode (official list) or that are another season than the one the file name / title states are set to 0, and the rest is renormalised (no API change, applies to every run below) |
| v2 | longer subject instructions (seasons, remakes vs the publish date); **one yes/no question per candidate** `v_{id}` ("is it exactly this entry?"); the subject questions in their own request (**separate**) |
| decide | with yes/no answers the subject is the fitting candidate with the highest *yes*. Its score is that probability, its margin the gap to the next fitting one, and *yes* < 0.5 means none. **Age rule**: an entry that ended over a year before the publish date (or began over two years before it, with no end date) goes to review, never applied |

### Results

#### Question accuracy (final configuration, 120 torrents, seeds 7 + 11)

| question | n | accuracy | top prob ≥ 0.8 |
|---|---|---|---|
| subject (choice) | 124 | 86.3 % (positives 97.6 %, negatives 62.5 %) | 22/23 |
| episode number | 204 | 99.5 % | 171/171 |
| kind | 234 | 98.3 % | 199/199 |

The choice question for the subject is the weak spot. With the true entry removed, the model
picks a sibling (the first season for an "S2" title, a re-edit's parent series …) in 38 % of
cases. The yes/no questions separate much better: the truth's *yes* is ≥ 0.90 for 90 % of
positives (median 0.95), while the best *yes* among negatives has a median of 0.21. Only 6 of 40
negatives reach 0.5, and 3 of those are remakes / re-edits whose old namesake is still in the
list (タヌキとキツネ 2018 vs 2026, ドラゴンボール超 vs ドラゴンボール超 ビルス, ケロロ軍曹 vs
KERORO 軍曹☆). The age rule catches exactly those: they become review items.

#### Configurations compared (clef-flash unless noted; bands at apply ≥ 0.8 ∧ margin ≥ 0.3, review ≥ 0.5)

| configuration | torrents | req / torrent | tokens / torrent | applied | precision | coverage |
|---|---|---|---|---|---|---|
| v1 joint, choice prob (seed 1) | 225 | 1 | 2.6k | 90 | 95.6 % | 35.1 % |
| v1 joint, choice prob (seed 7) | 120 | 1 | 2.9k | 50 | 96.0 % | 35.8 % |
| v1.1 joint, choice prob (seed 1) | 120 | 1 | 2.6k | 45 | 100 % | 34.6 % |
| v2 joint + yes/no (seed 7) | 60 | 1 | 5.5k | 48 | 100 % | 71.6 % |
| **v2 separate + yes/no (seeds 7 + 11) — default** | 120 | 2 | 5.5k | **107** | **100 %** | **79.9 %** |

Same 40 torrents (seed 1, v1.1, no yes/no questions), all questions in one request against
subject / episode in separate requests, and the full model:

| | subject pos / neg | episode | kind | precision / coverage at prob ≥ 0.7 |
|---|---|---|---|---|
| clef-flash, one request | 96.3 % / 53.8 % | 98.5 % | 97.3 % | 100 % / 47.6 % |
| clef-flash, separate requests | 100 % / 61.5 % | 98.5 % | 97.3 % | 100 % / 73.8 % |
| clef (27B), one request | 100 % / 46.2 % | 96.9 % | 97.3 % | 100 % / 57.1 % |

Separate requests help the subject question (fewer distractions) at about +8 % tokens (the state
is sent twice). The full model is no better at rejecting wrong entries, costs 6.3× per token, and
uses up the free allocation fast, so the default stays **clef-flash**. With yes/no questions both
layouts reach 100 % on the applied set. Separate requests cover more (79.9 % vs 71.6 %), so they
are the default (`clef.BuildOptions`).

#### Final configuration: outcome per band (120 torrents, 226 scored files, 134 mappable positives)

| band | files | correct | precision | share of mappable positives |
|---|---|---|---|---|
| apply (score ≥ 0.8 ∧ margin ≥ 0.3, entry recent) | 107 | 107 | **100 %** | **79.9 %** |
| review (score ≥ 0.5, or old entry) | 34 | 24 | 70.6 % | 17.9 % |
| discard (score < 0.5) | 1 | 0 | – | 0 % |
| not mapped (none / not an episode / validation) | 84 | 83 | 98.8 % | – |

Threshold sweep for the same data (metric = probability; `conf` = Clef's confidence):

| metric | apply | margin | applied | precision | coverage |
|---|---|---|---|---|---|
| prob | 0.50 | 0.0 | 130 | 97.7 % | 94.8 % |
| prob | 0.60 | 0.3 | 125 | 100 % | 93.3 % |
| prob | 0.70 | 0.3 | 122 | 100 % | 91.0 % |
| prob | **0.80** | **0.3** | **107** | **100 %** | **79.9 %** |
| prob | 0.85 | 0.3 | 85 | 100 % | 63.4 % |
| prob | 0.90 | 0.3 | 35 | 100 % | 26.1 % |
| conf | 0.50 | 0.0 | 57 | 100 % | 42.5 % |
| conf | 0.60 | 0.0 | 17 | 100 % | 12.7 % |

Without the age rule, the three remake / re-edit negatives would be applied at 0.84–0.92
(precision at 0.8 would drop to ~97 %). No threshold separates them; only the age rule does.

### Defaults

- Metric **`prob`**: the subject's *yes* probability (or the conditioned choice probability when no
  yes/no answers exist: per-file questions in multi-work batches), and the chosen option's
  probability for kind and episode; the minimum over the questions a file depends on.
- **Apply ≥ 0.8 and margin ≥ 0.3** (margin = min top1 − top2 over those questions), entry recent.
  0 wrong in 107 applied (one-sided 95 % bound on the error rate ≈ 2.8 % by the rule of three).
  0.6 also had no errors but leaves too little room. 0.8 keeps a margin below the band where the
  wrong remake answers sat.
- **Review ≥ 0.5**: 70 % of the review band is correct, which is worth an admin click. Below
  0.5 almost nothing is right.
- Model **clef-flash**, **separate** subject / episode requests with **yes/no** questions.
- Rate 60 / min, daily cap 2000 (setting). On the free plan the real limit is the neuron
  allocation, about 400 torrents/day at this request size. The worker waits for the next UTC day
  when it's used up.

### Limits / open points

- Sample size: 120 torrents / 107 applied files for the final configuration, from one dev
  catalogue (mostly current-season single episodes from bangumi.moe / ANi / Dynamis One / LoliHouse).
  Precision on 'apply' is 100 % here, but the sample can only show it is ≳ 97 %. Recheck after a
  few weeks with the review queue as ground truth (accept/reject rates per band).
- Multi-work batches use per-file subject questions without yes/no questions, which is the weaker
  setup (choice probabilities only). They weren't sampled separately. Expect more review items there.
- The calibration positives are easy cases (the rules can map them). Real uncertain files are
  harder, so expect more review/discard than measured, not lower precision on 'apply', because the
  yes/no score is what gates it.
- Labels for the `heuristic` stratum come from old rule mappings. 8 were invalid and dropped. A
  few wrong-but-valid ones may remain, so the true precision may be higher than measured.
