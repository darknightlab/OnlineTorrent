package clef

import (
	"context"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/darknightlab/onlinetorrent/internal/meta"
)

func refineStore(t *testing.T) *meta.Store {
	t.Helper()
	ms, err := meta.Open(filepath.Join(t.TempDir(), "meta.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(ms.Close)
	return ms
}

func addWork(t *testing.T, ms *meta.Store, bgm int, title, aliases, begin, end string, from, to int, firstAir string) string {
	t.Helper()
	key := "bgm:" + strconv.Itoa(bgm)
	if _, err := ms.W.Exec(`INSERT INTO works(id,key,title,title_cn,aliases,type,begin_at,end_at,eps,updated_at) VALUES(?,?,?,'',?,'tv',?,?,?, 'x')`,
		bgm, key, title, aliases, begin, end, to-from+1); err != nil {
		t.Fatal(err)
	}
	_, _ = ms.W.Exec(`INSERT INTO work_ids VALUES('bgm', ?, ?)`, strconv.Itoa(bgm), key)
	d, _ := time.Parse("2006-01-02", firstAir)
	for i := from; i <= to; i++ {
		air := ""
		if firstAir != "" {
			air = d.AddDate(0, 0, 7*(i-from)).Format("2006-01-02")
		}
		if _, err := ms.W.Exec(`INSERT INTO episodes(bgm_id,work_key,type,sort,airdate) VALUES(?,?,0,?,?)`, bgm*1000+i, key, i, air); err != nil {
			t.Fatal(err)
		}
	}
	return key
}

func prequelOf(t *testing.T, ms *meta.Store, subj, pre int) {
	t.Helper()
	if _, err := ms.W.Exec(`INSERT INTO subject_relations(subject_id,related_id,relation_type) VALUES(?,?,2)`, subj, pre); err != nil {
		t.Fatal(err)
	}
}

// refineTask builds a task with files for paths (parser kind / number / season like
// BuildClefTask) and the given works as candidates.
func refineTask(t *testing.T, ms *meta.Store, title, released string, paths []string, works ...string) *meta.ClefTask {
	t.Helper()
	ctx := context.Background()
	tk := &meta.ClefTask{Infohash: strings.Repeat("ab", 20), Title: title, Released: released, ReleasedBy: "published"}
	if released != "" {
		tk.Published = released[:10]
	}
	ts := meta.TitleSeason(title)
	for i, p := range paths {
		pn := meta.ParseEpisode(p)
		tk.Videos = append(tk.Videos, p)
		tk.Files = append(tk.Files, meta.ClefFile{Index: i, Path: p, Kind: pn.Kind, Episode: pn.Episode, Numbers: meta.NumberOptions(p, title, len(paths) == 1), Season: meta.StatedSeason(p, ts)})
	}
	for _, w := range works {
		c, err := ms.CandidateFor(ctx, w)
		if err != nil {
			t.Fatal(err)
		}
		tk.Candidates = append(tk.Candidates, c)
	}
	return tk
}

// answerAll: every file → work id, episode = the parser's number (or eps[i])
func answerAll(tk *meta.ClefTask, id string, eps ...float64) map[int]LLMFileOut {
	out := map[int]LLMFileOut{}
	for i, f := range tk.Files {
		e := f.Episode
		if i < len(eps) {
			v := eps[i]
			e = &v
		}
		out[f.Index] = LLMFileOut{Index: f.Index, SubjectID: &id, Episode: e, Kind: "main", Confidence: 0.99}
	}
	return out
}

func decideRefine(ms *meta.Store, tk *meta.ClefTask, a map[int]LLMFileOut) []FileDecision {
	ctx := context.Background()
	ds := DecideLLM(ctx, tk, a, DefaultLLMThresholds, ms.ValidEpisode, TaskEvidence(ms, tk))
	Refine(ctx, ms, tk, ds, DefaultLLMThresholds)
	return ds
}

func statuses(ds []FileDecision) string {
	var b []string
	for _, d := range ds {
		b = append(b, fmt.Sprintf("%s:%s:%s", d.Status, d.Key, strings.SplitN(d.Reason, ":", 2)[0]))
	}
	return strings.Join(b, " ")
}

// Rule (prequel numbering + not aired yet): 归还者 S2 "[14]" released after S2's 2nd broadcast.
func TestRefinePrequelNumbering(t *testing.T) {
	ms := refineStore(t)
	addWork(t, ms, 425990, "帰還者の魔法は特別です", `["Kikansha no Mahou wa Tokubetsu Desu"]`, "2023-10-06", "2023-12-22", 1, 12, "2023-10-06")
	w := addWork(t, ms, 471844, "帰還者の魔法は特別です(第二期)", `["Kikansha no Mahou wa Tokubetsu Desu Season 2"]`, "2026-10-07", "", 1, 12, "2026-10-07")
	for i := 13; i <= 14; i++ { // listed without a date
		_, _ = ms.W.Exec(`INSERT INTO episodes(bgm_id,work_key,type,sort,airdate) VALUES(?,?,0,?, '')`, 471844000+i, w, i)
	}
	prequelOf(t, ms, 471844, 425990)
	path := "[orion origin] Kikansha no Mahou wa Tokubetsu desu S2 [14] [1080p] [H265 AAC] [CHS_JPN].mp4"
	title := "[猎户压制部] 归还者的魔法要特别 第二季 / Kikansha no Mahou wa Tokubetsu desu S2 [14] [1080p]"

	tk := refineTask(t, ms, title, "2026-10-19T12:00:00Z", []string{path}, w)
	ds := decideRefine(ms, tk, answerAll(tk, "bgm_471844"))
	if d := ds[0]; d.Status != StatusApplied || d.Key != "2" || *d.Episode != 2 || d.Answers["rule"].Note == "" {
		t.Fatalf("14 − 12 → 2: %s %+v", statuses(ds), d.Answers)
	}
	// late enough for 14 itself to have aired: kept as 14
	tk = refineTask(t, ms, title, "2027-01-10T00:00:00Z", []string{path}, w)
	if ds := decideRefine(ms, tk, answerAll(tk, "bgm_471844")); ds[0].Status != StatusApplied || ds[0].Key != "14" {
		t.Errorf("after ep 12: %s", statuses(ds))
	}
	// no season stated: no shift, 14 hasn't aired → review
	tk = refineTask(t, ms, "[G] Kikansha no Mahou wa Tokubetsu desu [14]", "2026-10-19T12:00:00Z", []string{"[G] Kikansha no Mahou wa Tokubetsu desu [14].mp4"}, w)
	if ds := decideRefine(ms, tk, answerAll(tk, "bgm_471844")); ds[0].Status != StatusPending || ds[0].Key != "14" || !strings.HasPrefix(ds[0].Reason, meta.ReasonDateMismatch) {
		t.Errorf("no season: %s", statuses(ds))
	}
	// the shifted episode fails the date gate too (released before ep 2 aired): review
	tk = refineTask(t, ms, title, "2026-10-01T00:00:00Z", []string{path}, w)
	if ds := decideRefine(ms, tk, answerAll(tk, "bgm_471844")); ds[0].Status != StatusPending || ds[0].Key != "14" {
		t.Errorf("shift fails the date: %s", statuses(ds))
	}
}

// Rule (parser agreement after the split-cour shift).
func TestRefineParserCanonical(t *testing.T) {
	ms := refineStore(t)
	w := addWork(t, ms, 515594, "転生したらスライムだった件 第4期", `["Tensei shitara Slime Datta Ken 4th Season"]`, "2026-04-03", "2026-09-25", 73, 96, "2026-04-03")
	path := "Tensei Shitara Slime Datta Ken 4th Season - 03(75) [WebRip 1080p HEVC-10bit AAC ASSx2].mkv"
	tk := refineTask(t, ms, "[LoliHouse] Tensei Shitara Slime Datta Ken 4th Season - 03(75)", "2026-04-18T00:00:00Z", []string{path}, w)
	if ds := decideRefine(ms, tk, answerAll(tk, "bgm_515594", 75)); ds[0].Status != StatusApplied || ds[0].Key != "75" {
		t.Errorf("24(96)-style: %s", statuses(ds))
	}
	// the parser read another episode (03 → 75, model 76): still disagrees
	path2 := "Tensei Shitara Slime Datta Ken 4th Season - 03(76) [WebRip].mkv"
	tk = refineTask(t, ms, "[LoliHouse] Tensei Shitara Slime Datta Ken 4th Season - 03(76)", "2026-04-18T00:00:00Z", []string{path2}, w)
	if ds := decideRefine(ms, tk, answerAll(tk, "bgm_515594", 76)); ds[0].Status != StatusPending || ds[0].Reason != ReasonDisagrees {
		t.Errorf("different episode: %s", statuses(ds))
	}
}

func spyBatch(t *testing.T) (*meta.Store, string, string) {
	ms := refineStore(t)
	s1 := addWork(t, ms, 100, "スパイ", `["Spy x Family"]`, "2022-04-09", "2022-09-24", 1, 25, "2022-04-09")
	s3 := addWork(t, ms, 300, "スパイ Season 3", `["Spy x Family Season 3"]`, "2025-10-04", "2025-12-27", 38, 50, "2025-10-04")
	prequelOf(t, ms, 300, 100)
	return ms, s1, s3
}

func spyFiles(n int, year string) []string {
	var p []string
	for i := 1; i <= n; i++ {
		p = append(p, fmt.Sprintf("[BDrip] Spy x Family S03 [7³ACG]/Spy x Family %s S03E%02d-[1080p][BDRIP][x265.OPUS].mkv", year, i))
	}
	return p
}

// Rule (late-release batch).
func TestRefineBatchRule(t *testing.T) {
	const title = "[7³ACG] SPY×FAMILY 间谍家家酒 第3季/Spy x Family S03 | 01-13 [简繁字幕] BDrip 1080p x265 OPUS 2.0"
	ms, s1, s3 := spyBatch(t)
	tk := refineTask(t, ms, title, "2026-10-20T00:00:00Z", spyFiles(13, "2025"), s3, s1)
	ds := decideRefine(ms, tk, answerAll(tk, "bgm_300"))
	for i, d := range ds {
		if d.Status != StatusApplied || d.Key != strconv.Itoa(38+i) {
			t.Fatalf("batch: %s", statuses(ds))
		}
	}
	// fewer than 3 main files: the date gate stands
	tk = refineTask(t, ms, title, "2026-10-20T00:00:00Z", spyFiles(2, "2025"), s3, s1)
	if ds := decideRefine(ms, tk, answerAll(tk, "bgm_300")); ds[0].Status != StatusPending {
		t.Errorf("2 files: %s", statuses(ds))
	}
	// a year that isn't the work's
	tk = refineTask(t, ms, title, "2026-10-20T00:00:00Z", spyFiles(13, "2023"), s3, s1)
	if ds := decideRefine(ms, tk, answerAll(tk, "bgm_300")); ds[0].Status != StatusPending {
		t.Errorf("year 2023: %s", statuses(ds))
	}
	// no season stated: season 1 (1–25) fits the numbers too → not unique
	var plain []string
	for i := 1; i <= 13; i++ {
		plain = append(plain, fmt.Sprintf("Spy x Family/Spy x Family - %02d [BDRIP].mkv", i))
	}
	tk = refineTask(t, ms, "[G] Spy x Family 01-13 BDrip", "2026-10-20T00:00:00Z", plain, s3, s1)
	ds = decideRefine(ms, tk, answerAll(tk, "bgm_300"))
	for _, d := range ds {
		if d.Status == StatusApplied {
			t.Fatalf("ambiguous batch applied: %s", statuses(ds))
		}
	}
	// early releases stay strict (before the episodes aired)
	tk = refineTask(t, ms, title, "2025-09-01T00:00:00Z", spyFiles(13, "2025"), s3, s1)
	for _, d := range decideRefine(ms, tk, answerAll(tk, "bgm_300")) {
		if d.Status == StatusApplied {
			t.Fatalf("early batch applied")
		}
	}
}

// Rule (season continuation): SAO "S04E13–E23" after S04E01–E12 mapped to the prequel.
func TestRefineSeasonContinuation(t *testing.T) {
	ms := refineStore(t)
	p := addWork(t, ms, 279457, "ソードアート・オンライン アリシゼーション War of Underworld", `["Sword Art Online -Alicization- War of Underworld","刀剑神域 第三季 第2部分"]`, "2019-10-12", "2019-12-28", 1, 12, "2019-10-12")
	w := addWork(t, ms, 292238, "ソードアート・オンライン アリシゼーション War of Underworld -THE LAST SEASON-", `["Sword Art Online: Alicization - War of Underworld - The Last Season","刀剑神域 第三季 第3部分"]`, "2020-07-11", "2020-09-19", 13, 23, "2020-07-11")
	prequelOf(t, ms, 292238, 279457)
	const title = "Sword Art Online : Alicization - War of Underworld - VOSTFR + VF [BD 1080p h.264 DTS] - T3KASHi"
	ih := strings.Repeat("ab", 20)
	file := func(e int) string {
		return fmt.Sprintf("Sword.Art.Online.Alicization.War.of.Underworld.MULTi.1080p.BluRay.x264-T3KASHi/Sword.Art.Online.Alicization.War.of.Underworld.S04E%02d.MULTi.1080p.BluRay.x264-T3KASHi.mkv", e)
	}
	var later []string
	for e := 13; e <= 15; e++ {
		later = append(later, file(e))
	}
	if c, _ := ms.CandidateFor(context.Background(), w); c.SeasonFits(4) {
		t.Fatalf("test needs a season mismatch: season %d", c.Season)
	}
	// without the prequel's files in the torrent: still a season mismatch
	tk := refineTask(t, ms, title, "2026-10-20T00:00:00Z", later, w, p)
	if ds := decideRefine(ms, tk, answerAll(tk, "bgm_292238")); ds[0].Status != StatusDiscarded || ds[0].Reason != ReasonSeason {
		t.Fatalf("no same-torrent evidence: %s", statuses(ds))
	}
	// S04E01–E12 of the same torrent mapped to the prequel (by the rules)
	if _, err := ms.W.Exec(`INSERT INTO torrents(infohash,seq,title,status,published_at,source,updated_at) VALUES(?,1,?, 'ready', '2026-10-20T00:00:00Z', 'Nyaa', 'x')`, ih, title); err != nil {
		t.Fatal(err)
	}
	for e := 1; e <= 12; e++ {
		if _, err := ms.W.Exec(`INSERT INTO torrent_files(infohash,file_index,path,kind,work_key,episode,confidence,status,method,updated_at) VALUES(?,?,?, 'episode', ?, ?, 0.9, 'auto', 'title+name', 'x')`, ih, e-1, file(e), p, e); err != nil {
			t.Fatal(err)
		}
	}
	tk = refineTask(t, ms, title, "2026-10-20T00:00:00Z", later, w, p)
	ds := decideRefine(ms, tk, answerAll(tk, "bgm_292238"))
	for i, d := range ds {
		if d.Status != StatusApplied || d.Key != strconv.Itoa(13+i) {
			t.Fatalf("continuation (+ late batch): %s", statuses(ds))
		}
	}
	// numbering that restarts (E01–E03 of the last season) is not a continuation
	tk = refineTask(t, ms, title, "2026-10-20T00:00:00Z", []string{file(1), file(2), file(3)}, w, p)
	for _, d := range decideRefine(ms, tk, answerAll(tk, "bgm_292238")) {
		if d.Status == StatusApplied {
			t.Fatalf("restarted numbering applied")
		}
	}
}
