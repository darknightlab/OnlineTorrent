# Decision model (Cloudflare Clef): calibration

Torrent files the rules can't map with certainty go to Cloudflare's decision model **Clef** (Workers AI
`@cf/cloudflare/clef-flash` / `@cf/cloudflare/clef`). This page explains how the defaults for the
admin-editable thresholds (settings group 决策模型) were picked. Design and API:
[plan.md](../plan.md) 「映射关系」→「决策模型」 and [api.md](../api.md) 「后台」→「决策模型」.

## Facts about the API (measured)

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

## Method

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

## What changed between runs

| version | change |
|---|---|
| v1 | one request per torrent: `subject` (choice) + per file `f{i}_ep` + `f{i}_kind`; candidates as JSON `{title, cn, aliases, air_date, eps, type}` |
| v1.1 | candidates also get `season` (stated by their titles, or 1 for an entry without a TV prequel); the state gets `c_published`; the kind options name *Preview/予告* as `other` (v1 answered `pv`) |
| decide | **conditioning**: subject probabilities of candidates that lack the chosen episode (official list) or that are another season than the one the file name / title states are set to 0, and the rest is renormalised (no API change, applies to every run below) |
| v2 | longer subject instructions (seasons, remakes vs the publish date); **one yes/no question per candidate** `v_{id}` ("is it exactly this entry?"); the subject questions in their own request (**separate**) |
| decide | with yes/no answers the subject is the fitting candidate with the highest *yes*. Its score is that probability, its margin the gap to the next fitting one, and *yes* < 0.5 means none. **Age rule**: an entry that ended over a year before the publish date (or began over two years before it, with no end date) goes to review, never applied |

## Results

### Question accuracy (final configuration, 120 torrents, seeds 7 + 11)

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

### Configurations compared (clef-flash unless noted; bands at apply ≥ 0.8 ∧ margin ≥ 0.3, review ≥ 0.5)

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

### Final configuration: outcome per band (120 torrents, 226 scored files, 134 mappable positives)

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

## Defaults

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

## Limits / open points

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
