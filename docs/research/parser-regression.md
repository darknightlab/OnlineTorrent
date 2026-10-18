# File-name parser rewrite: regression report

Rewrite of `internal/meta/parse.go` (ParseEpisode / ParseTitle / ClassifyFile, new
ClassifyFiles) from a first-match regex cascade to token elimination modelled on
[Anitomy](https://github.com/erengy/anitomy) (MPL-2.0; Go port anitogo). Only the design and parts
of the keyword tables are borrowed; the code is our own and there's no dependency.

## Method

- Data: a copy of the dev mapping DB (`/tmp/otd-dev/meta.db` → `meta.db.bak-parser`, WAL
  checkpointed into the copy). It holds 753 torrents, 1160 files (1137 videos), all of them
  `status = 'auto'`. A re-export of the live DB later on (759 torrents, 1166 files) gave the same diff.
- Tool: a throwaway Go program (not committed) loaded the old `parse.go` as a separate package. For
  every torrent it ran `old.ClassifyFile` per file and the new `meta.ClassifyFiles` on the
  whole file list, and compared `kind/episode`. It also ran `old.ParseTitle` against `meta.ParseTitle`
  on each torrent title. I went through every diff and adjusted the parser until all
  remaining diffs were improvements, or changes made on purpose (listed below).

## Summary

| | count |
|---|---|
| files compared | 1160 (1137 videos) in 753 torrents |
| files whose kind/episode changed | **127** (1 torrent accounts for 119) |
| torrent titles whose ParseTitle result changed | 8 |
| unchanged | 1033 files, 745 titles |

Changed files by transition (old → new):

| transition | files | what |
|---|---|---|
| other → episode (number) | 5 | `Pocket Monsters (2023) 153`; Kusuriya `[03_51]` → 3 (×2); `ブラッククローバー 2nd Season ページ173` → 173 (×2) |
| other → episode (Episode=nil) | 97 | 逮捕しちゃうぞ TV S1/S2/S3: season-relative numbering in a multi-season batch, see below |
| sp 1 → sp 2…6 | 5 | 逮捕しちゃうぞ `Special 1999 (Special S1 0N)` |
| sp 1 → sp (no number) | 1 | `Special 2002-03-29 (Special S2) 逮捕しちゃうぞ` |
| sp 1 → op (no number) | 1 | `Special 1999 (Special S1). ノンテロップ・オープニング` |
| sp 1 → extra | 1 | `Extras (Special 1999)/… No Mercy !!`, which sits in an Extras folder |
| op 1 → op (no number) | 1 | `TV S3 NCOP`: the torrent has 3 unnumbered NCOPs |
| op 1 → extra | 1 | `ノンテロップ OP＆ED`: one video holding both, so it can't be a single key |
| other → op (no number) | 1 | `ノンテロップ·オープニング` |
| other → cm 1/2/3 | 3 | `リマスター版CM 01..03` |
| other → pv 1 | 1 | `特報` (teaser), the only PV in the torrent |
| other → extra | 3 | `01~14話ダイジェスト`, plus 2 bonus clips in `Extras (…)` folders |
| other → sp (no number) | 4 | `OVA (4S4E)/OVA 1994~1995/OVA 1994-09-24 …` |
| episode 1 → sp 1 | 1 | `Mahou no Shimai Lulutto Lilly [Special] - 01` |
| episode 21/8 → other | 2 | `Future.Boy.Conan.Disc4.EP21-EP26…`: one file holding a range of episodes (on purpose, see below) |

Changed titles (ParseTitle): Kusuriya `[03(51)]` → 3 (×2, was other); 雪飘工作室 `[29][简繁日外挂]`
→ 29 (×2, was other); Lulutto Lilly `[Special] - 01` → sp 1 (was episode 1); Future Boy Conan
`EP21-EP26` / `EP08-EP13` → other (was episode 21/8); the 逮捕令 batch title → sp without a
number (was other; not used, because that torrent has more than one video).

## The 逮捕しちゃうぞ batch (torrent #626, 123 videos in the DB)

Before: the cascade only accepted a number written directly after the keyword and fell back
to 1. All nine "Special" files were therefore **sp1**, both NCOPs were ncop1, and the whole TV
part was `other`.

| file (shortened) | old | new | why |
|---|---|---|---|
| `Special 1999 (Special S1 01); DVD_480P` | sp/1 | sp/1 | 1999=year, S1=season, DVD/480P=metadata, `01` follows the keyword |
| `Special 1999 (Special S1 02); DVD_480P` … `06` | sp/1 | **sp/2 … sp/6** | same |
| `Special 2002-03-29 (Special S2) 逮捕しちゃうぞ` | sp/1 | **sp/–** | date + season; no number left |
| `Extras (Special 1999)/Special 1999 (Special S1). No Mercy !!` | sp/1 | **extra** | lives in an Extras folder |
| `Extras (Special 1999)/… ノンテロップ・オープニング` | sp/1 | **op/–** | creditless opening; 3 unnumbered OPs in the torrent |
| `Extras (TV S1 1996)/… ノンテロップ·オープニング` | other | **op/–** | same |
| `Extras (TV S3 2007)/… NCOP` | op/1 | **op/–** | same |
| `Extras (TV S3 2007)/… NCED` | ed/1 | ed/1 | the torrent's only NCED → 1 (ClassifyFiles) |
| `Extras (TV S1 1996)/… ノンテロップ OP＆ED` | op/1 | **extra** | OP and ED in one video |
| `Extras (TV S2 2001)/… 01~14話ダイジェスト` | other | **extra** | digest; `01~14話` is a range |
| `Extras (Film 1994)/… リマスター版CM 01..03` | other | **cm/1..3** | `リマスター版` = edition, `CM 01` |
| `Extras (Film 1994)/… 予告` / `特報` | extra / other | extra / **pv/1** | 予告 = preview, 特報 = teaser (only PV) |
| `OVA (4S4E)/OVA 1994~1995/OVA 1994-09-24 そしてふたりは出会った` (×4) | other | **sp/–** | OVA folder; the date is no number |
| `TV (3S97E)/TV S1 1996/You're Under Arrest. TV S1 (1996) 05` (×97, S1 05–51, S2 01–26, S3 01–24) | other | **episode/–** | see below |

The TV files now resolve to episode numbers (`TV S1 (1996) 05` → 5). But S1, S2 and S3 number
from 01 independently, so `TV S1 05` and `TV S2 05` collide on the key episode 5 with different
content (different seasons). ClassifyFiles therefore marks every content group involved in such a
clash as ambiguous, including S1 27–51, which don't clash with anything themselves: in this
torrent the numbering is season-relative and the torrent is mapped to a single work. Episode=nil,
confidence 0.2, nothing gets mapped.

## Other representative fixes

| file | old | new |
|---|---|---|
| `[BeanSub][Kusuriya no Hitorigoto S3][03_51][CHT][1080P][x264_AAC].mp4` | other | episode/3 (03 = S3 episode, 51 = absolute) |
| `[NEO·QSW]ブラッククローバー 2nd Season ページ173 黒き守護者[WEBRIP HEVC 1080P].mp4` | other | episode/173 |
| `[Pokefans] Pocket Monsters (2023) 153 [RUS SUB, 1080p].mkv` | other | episode/153 (year eliminated, 153 is left) |
| `[Dynamis One] Mahou no Shimai Lulutto Lilly [Special] - 01 (Baha …).mp4` | episode/1 | sp/1 (collided with the real episode 1 before) |
| title `[雪飘工作室][…/Onegai_Aipri/…][29][简繁日外挂](检索:…)` | other | episode/29 |

## Changes made on purpose

- **Multi-episode files**: `Future.Boy.Conan.Disc4.EP21-EP26.Remux…mkv` used to be episode 21.
  `EP21-EP26` is now a range token, so the file is `other` (one file that holds episodes 21–26
  is not episode 21). If mapping such discs to their first episode is preferred, that would be a
  one-line rule in `parseName`.
- **Unnumbered specials**: ParseEpisode and ClassifyFile return `Episode=nil` for an
  unnumbered or ambiguous special (`Show - OVA`, `NCOP/Show.mkv`, `ncop.mkv`); they no longer
  default to 1. Only the batch pass (ClassifyFiles) numbers a special 1, and only when it is the
  **only** file of its kind in the whole torrent and carries no number candidate at all. A lone
  `Show - NCOP.mkv` next to the episodes is still ncop1, while several unnumbered NCOPs all stay
  without a number. Existing test expectations were updated to match (ParseEpisode
  `Show - OVA`, `Show - NCED`, `Show - Trailer`, `ncop.mkv`, `ノンテロップOP`; ClassifyFile
  `NCED/…`, `SPs/… OVA`, `NCOP/Show.mkv`, `SPs/Show.mkv`); the Reclassify test (which goes
  through the batch pass) is unchanged and still expects nced1/sp1.
- **Ambiguous episodes** (several different candidates of the best kind, or a same-key clash
  between different contents) are `kind=episode, Episode=nil, confidence 0.2` instead of
  picking the first match. The movie rule in match.go (a movie's only video is episode 1) also
  applies to these.

## Known limitations / remaining failures

- The dev DB is small (≈1.2k files) and has few specials. Most of the special-case coverage
  comes from table tests built from real naming conventions (`parse_real_test.go`, ~170 cases)
  rather than from stored rows.
- `第173页` ("page", Black Clover) in a torrent title is not an episode marker (only the
  Japanese file name `ページ173` is). The title without it, `… 2nd Season 173[WEBRIP…]`, stays
  `other` because titles don't accept bare numbers.
- Season-relative numbering in multi-season batches can't be mapped to a single work. Those files
  are left unmapped (nil), not mapped per season.
- Special number ranges in one file (`[SP01-SP03]`) give `sp` without a number.
- `TVSP` (TV special) is not a type keyword: a single-video TV-special torrent stays `other`
  (unchanged), since the work itself is usually the special.

## Re-classification of stored rows

`stateReclassify` is now `reclassify_parser_v2`. `ReclassifyOnce` inserts it as `pending`
(`INSERT OR IGNORE`, Go code, no migration) and runs `Reclassify` once, which now uses
ClassifyFiles per torrent and rewrites only rows with `status = 'auto'`.
