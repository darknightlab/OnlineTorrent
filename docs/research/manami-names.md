# anime-offline-database names (2026-10)

Source: [manami-project/anime-offline-database](https://github.com/manami-project/anime-offline-database).
The upstream database is licensed under [ODbL 1.0](https://opendatacommons.org/licenses/odbl/1-0/)
and [DbCL 1.0](https://opendatacommons.org/licenses/dbcl/1-0/). The site footer credits this
source and links its licence. Imported and ID-linked names remain attributed in
`manami_names(work_key, name, entry)`; they are not folded into `works.aliases` or
represented as Bangumi/bangumi-data data. Treat this linked-name database as ODbL when
redistributing it, preserve attribution and provide the linked database or the alterations
under ODbL as required by the licence. Reproduction: the public upstream release plus
`ImportManami`, our shared external-ID links, and the filtering rules below. Do not assume
that attribution alone replaces the licence's database redistribution obligations.

## Import

- Setting `upstream.manami_url`: official latest release JSONL asset, empty disables imports.
- Release inspected: **2026-27**, `lastUpdate=2026-07-04`:
  `anime-offline-database.jsonl`, 62,331,124 bytes, 41,537 entries / 243,194 synonyms.
  A ~6 MB zstd asset also exists; the plain JSONL avoids another compression dependency.
- First scheduled check five minutes after startup, then every six hours. A successful
  import is due weekly, based on persisted sync time; failure retries no sooner than six
  hours later. Manual sync is available on the admin overview.
- Streamed per line, not decoded into a whole database object: 2 MiB maximum line,
  512 MiB maximum response, 200,000 maximum entries, request context timeout 20 minutes.
  The importer retains linked entries' names plus capped per-name occurrence counts.
- Exact source hosts/path IDs: MAL, AniDB, AniList (the providers available in `work_ids`,
  populated by bangumi-data). No title-based joins, relatedAnime joins, or guessed IDs.
  Kitsu is **not** joined: we currently have no corresponding local ID source.
- All matching source IDs must resolve to one work. Contradicting IDs, and multiple entries
  resolving to the same work, are dropped conservatively. No new works or external IDs
  are created from this source.
- Names shorter than four Latin characters / three non-ASCII characters, purely numeric
  names, names over 200 bytes, and names shared by **three or more upstream entries** are
  dropped. Normalisation is used for occurrence counting, so case/punctuation variants
  in one entry count once. This conservatively removes shared names even when related.
- Replace the entire attributed table transactionally after a successful complete parse;
  failure retains previous names. This also removes names withdrawn by upstream.
  `DELETE FROM manami_names` removes the source (disable its URL first); invalidate the
  in-memory name index or restart afterwards.
- The rule index merges these names into raw/normalised/franchise-name maps; the file-name
  contradiction check also recognises them. `WorkNames` merges them for model evidence.
  They do not change canonical titles or get added to the LLM prompt's alias list.

## Name-evidence changes (not additional sources)

All are confined to the model evidence check, not global `normName` or rule certainty:

- Trailing sequel number stripped as in franchise matching: Edgerunners 2 ↔ Edgerunners S02.
  We do **not** broadly follow relation type 8.
- Prefix before `:` / ` - ` of the work's own alias, only if exactly that name is used by
  another work on its prequel/sequel chain: Tokyo Revengers.
- OVA/special uses parent names via outgoing 主线故事 (12) / incoming 番外篇 (6), not names
  of its children or sibling side stories: Nichijou OAD.
- Existing retrieval romanisation folding reused for evidence: macrons/circumflexes,
  ou/oo, uu, wo, ei (Genso/Gensou/Gensō).
- Name variant with bracketed content removed: Battle Spirits [Re] 絕界之空.

Each remains behind episode/season/parser/date checks. `CLEFCALIB_DISABLE` accepts
`manami,vowels,sequel,brackets,colon,parent` to reproduce calibration ablations without
changing production defaults. Every individual ablation and the combined changes kept
zero model auto-mapping errors in the recorded v2 set.

## Offline validation

No LLM calls; no production writes. Both baselines use the same calibration DB copy after
refreshing bangumi-data IDs from the downloaded full dataset (otherwise old IDs understate
link coverage).

| replay | baseline | new |
|---|---|---|
| `clefcalib analyze`, v2 240 torrents / 458 files | 255 applied / 0 wrong | **255 / 0** |
| `clefcalib rules`, labelled files present in DB: 205 torrents / 215 files | 197 certain / 7 raw wrong | **196 / 7**, no new wrong |

`rules` is a raw certainty check, before release-time gates and conclusion protection.
The seven baseline wrong labels are unchanged (Tougen Anki, Keroro, Aoashi, Grand Blue;
full bangumi-data refresh affects their existing external/tag links). They are not newly
introduced by these changes. The one correct rule mapping now deferred is Tenipuri U-17
Kesshou Member Ketteisen: “TeniPuri” now exactly names an older entry through manami,
so the conservative file-name contradiction check sends it to the model. No new heuristic
was added to bypass that check.

Production read-only pending export: 229 files, of which **91 no_name_evidence**. Replaying
only the name gate against the local metadata snapshot + shared-ID import:

| work | held files | now pass name gate |
|---|---:|---:|
| Index S1 | 31 | 31 |
| GochiUsa BLOOM | 14 | 14 |
| Marulk | 4 | 4 |
| Cyberpunk Edgerunners 2 | 10 | 10 |
| Tokyo Revengers S4 | 4 | 4 |
| Nichijou OAD | 1 | 1 |
| Genso Suikoden | 1 | 1 |
| Battle Spirits [Re] | 1 | 1 |
| Index movie / Index-tan | 7 + 6 | 0 |
| Ling Cage upper/lower | 6 + 6 | 0 |
| **total** | **91** | **66** |

Manami alone passes 54; evidence changes alone pass 17, with five overlapping.
A broader raw-rules check over 370 torrents / 395 labelled files went from 351 certain
(232 labelled right, 119 raw mismatches) to 347 (229 right, 118 raw mismatches): no new
mismatch and one older-format Tenipuri negative deferred. Older negative dumps often omit
the removed truth-subject IDs; these raw counts are not a reliable precision estimate.
A generic imported series synonym initially bypassed the file-name contradiction guard
for one BLEACH file. That was rejected: imported names now identify their own work but
**cannot grant a shared-franchise exemption to another work**; that exemption uses only
original-source names. The regression is unit-tested and the broader replay now introduces
no wrong mapping. The
remaining **25** lack usable local shared IDs; despite upstream titles being present,
we deliberately do not link by title. These are name-gate outcomes, **not** promises
of application after every other gate. No pending decision was accepted or requeued.

Local import after the full bangumi-data ID refresh: **8,228 linked works, 117,423 names**,
~3.1–3.5 seconds; process memory ~79 MiB system allocation in the import harness (streaming,
not a whole-file object tree). Name index with 31k works: ~0.9–1.7 s / 39.2 MiB retained
without manami, ~2.5–3.1 s / **69.6 MiB** with manami (~172k raw names / ~156k normalised
names, including the original-source-only franchise-exemption index). Built once and refreshed in the background on the existing ten-minute TTL; no
O(works) scan per torrent. These timings are local measurements, not production metrics.
