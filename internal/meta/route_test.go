package meta

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

// seedFamily inserts works (bgm:{id}), their bgm ids, official main episodes and relations.
func seedFamily(t *testing.T, s *Store, works []testWork, rels [][3]int) {
	t.Helper()
	ctx := context.Background()
	epID := 1000
	for _, w := range works {
		aliases := w.aliases
		if aliases == "" {
			aliases = "[]"
		}
		end := "x" // ended
		if w.running {
			end = ""
		}
		if _, err := s.W.ExecContext(ctx, `INSERT INTO works(id,key,title,title_cn,aliases,type,begin_at,end_at,eps,updated_at) VALUES(?,?,?,?,?,?,?,?,?,'x')`,
			w.id, fmt.Sprintf("bgm:%d", w.id), w.title, w.cn, aliases, w.typ, w.begin, end, w.eps); err != nil {
			t.Fatal(err)
		}
		if _, err := s.W.ExecContext(ctx, `INSERT INTO work_ids(provider,ext_id,work_key) VALUES('bgm',?,?)`, fmt.Sprint(w.id), fmt.Sprintf("bgm:%d", w.id)); err != nil {
			t.Fatal(err)
		}
		for i := 1; i <= w.eps; i++ {
			name := ""
			if i <= len(w.epNames) {
				name = w.epNames[i-1]
			}
			epID++
			if _, err := s.W.ExecContext(ctx, `INSERT INTO episodes(bgm_id,work_key,type,sort,name) VALUES(?,?,0,?,?)`, epID, fmt.Sprintf("bgm:%d", w.id), i, name); err != nil {
				t.Fatal(err)
			}
		}
	}
	for _, r := range rels {
		if _, err := s.W.ExecContext(ctx, `INSERT INTO subject_relations(subject_id,related_id,relation_type) VALUES(?,?,?)`, r[0], r[1], r[2]); err != nil {
			t.Fatal(err)
		}
	}
}

type testWork struct {
	id             int
	title, cn, typ string
	begin          string
	eps            int
	epNames        []string
	aliases        string // JSON
	running        bool   // no end date
}

// both directions of a relation, as in the bgm dump
func rel(a, b, ab, ba int) [][3]int { return [][3]int{{a, b, ab}, {b, a, ba}} }

func concat(xs ...[][3]int) [][3]int {
	var out [][3]int
	for _, x := range xs {
		out = append(out, x...)
	}
	return out
}

// fileWant: work id (0 = unmapped) and episode key ("" = none)
type fileWant struct {
	path string
	work int
	key  string
}

func checkMap(t *testing.T, s *Store, ih string, want []fileWant) {
	t.Helper()
	tm, err := s.GetTorrentMap(context.Background(), ih)
	if err != nil {
		t.Fatal(err)
	}
	if len(tm.Files) != len(want) {
		t.Fatalf("%s: %d files", ih, len(tm.Files))
	}
	for i, w := range want {
		f := tm.Files[i]
		gotWork := 0
		if f.Work != nil {
			gotWork = int(f.Work.ID)
		}
		if gotWork != w.work || f.EpisodeKey != w.key {
			t.Errorf("%s %q: got work %d key %q (kind %s, method %s), want %d %q", ih, w.path, gotWork, f.EpisodeKey, f.Kind, f.Method, w.work, w.key)
		}
	}
}

func matchFiles(t *testing.T, s *Store, ih, title string, tags []string, want []fileWant) {
	t.Helper()
	var files []FileInput
	for i, w := range want {
		files = append(files, FileInput{Index: i, Path: w.path, Size: 100})
	}
	if err := s.Match(context.Background(), TorrentInput{Infohash: ih, PublishedAt: "2026-12-20T00:00:00Z", Title: title, MoeTagIDs: tags, Files: files}); err != nil {
		t.Fatal(err)
	}
	checkMap(t, s, ih, want)
}

var showFamily = []testWork{
	{id: 10, title: "テストショー", cn: "测试秀", typ: "tv", begin: "2001-04-01T15:00:00Z", eps: 12},
	{id: 11, title: "テストショー 第二季", cn: "测试秀 第二季", typ: "tv", begin: "2003-04-01T15:00:00Z", eps: 12},
	{id: 12, title: "テストショー ファイナル", cn: "测试秀 最终季", typ: "tv", begin: "2005-04-01T15:00:00Z", eps: 10},
	{id: 13, title: "テストショー 劇場版", cn: "测试秀 剧场版", typ: "movie", begin: "2004-08-01T15:00:00Z", eps: 1},
	{id: 14, title: "テストショー", cn: "测试秀 OVA", typ: "ova", begin: "2000-01-01T15:00:00Z", eps: 2, epNames: []string{"はじまりの日", "さよならの日"}},
	{id: 15, title: "テストショー 特別編", cn: "测试秀 特别篇", typ: "tv", begin: "2004-01-01T15:00:00Z", eps: 1},
}

var showRelations = concat(
	rel(10, 11, 3, 2), rel(11, 12, 3, 2), // TV S1 → S2 → S3
	rel(10, 13, 6, 12), // movie: 番外篇
	rel(14, 10, 3, 2),  // the OVA is a 前传 of S1
	rel(11, 15, 3, 2),  // a one-episode TV special inside the chain: no season
)

func TestRouteMultiSeasonBatch(t *testing.T) {
	s := openTest(t)
	seedFamily(t, s, showFamily, showRelations)
	if _, err := s.W.Exec(`INSERT INTO work_ids(provider,ext_id,work_key) VALUES('bangumi_moe','tag10','bgm:10'),('bangumi_moe','tag11','bgm:11')`); err != nil {
		t.Fatal(err)
	}
	const root = "Test Show TV+OVA+Movie/"
	// Old works, no bangumi.moe tag: the anchor comes from the title fragments (测试秀 names
	// only bgm:10, テストショー names bgm:10 and the OVA bgm:14; bgm:10 wins and is certain).
	// Since the decision model only files that stay on the certain anchor are mapped by rule.
	// Changed expectations (were mapped by inference before, now unmapped + queued; the
	// parsed key is still stored for display):
	//   - Season 2 / ファイナル folders: season-chain and related-title routing (were 11 / 12);
	//   - Movie / OVA folders: movie/OVA keyword routing (were 13 / 14; the OVA numbers came
	//     from matching official episode titles, which is gone);
	//   - unnumbered NCOPs: "only NCOP of its kind → ncop1" is an inference (were 11 / 10).
	matchFiles(t, s, "batch", "[整理] 测试秀 (テストショー) TV+OVA+剧场版 合集 (整理时间：2026.07.15)", nil, []fileWant{
		{root + "TV/Season 1/[G] Test Show - 01 [1080p].mkv", 10, "1"},
		{root + "TV/Season 1/[G] Test Show - 12 [1080p].mkv", 10, "12"},
		{root + "TV/Season 2/[G] Test Show S2 - 01 [1080p].mkv", 0, "1"},
		{root + "TV/Season 2/[G] Test Show S2 - 12 [1080p].mkv", 0, "12"},
		{root + "TV/Season 2/[G] Test Show S2 - NCOP [1080p].mkv", 0, "ncop1"},
		{root + "TV/テストショー ファイナル/[G] Test Show - 01 [1080p].mkv", 0, "1"},
		{root + "TV/テストショー ファイナル/[G] Test Show - 10 [1080p].mkv", 0, "10"},
		{root + "Movie/[G] Test Show The Movie [1080p].mkv", 0, "1"},
		{root + "OVA/[G] Test Show OVA はじまりの日 [1080p].mkv", 0, ""},
		{root + "OVA/[G] Test Show OVA さよならの日 [1080p].mkv", 0, ""},
		{root + "SPs/[G] Test Show - NCOP [1080p].mkv", 0, "ncop1"},
		{root + "TV/Season 1/[G] Test Show - 01 [1080p].ass", 0, ""},
	})
	tm, _ := s.GetTorrentMap(context.Background(), "batch")
	if m := tm.Files[0].Method; m != "title+name" {
		t.Errorf("anchor method %q", m)
	}
	if m := tm.Files[2].Method; m != "" {
		t.Errorf("unmapped method %q", m)
	}
	// the rules' guesses are kept as hints for the decision model
	task, err := s.BuildClefTask(context.Background(), "batch", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(task.Files) != 9 || !task.PerFile {
		t.Errorf("clef task: %d files, per-file %v", len(task.Files), task.PerFile)
	}
	hints := map[int]string{}
	for _, f := range task.Files {
		hints[f.Index] = f.Hint
	}
	if hints[2] != "bgm:11" || hints[5] != "bgm:12" || hints[7] != "bgm:13" || hints[8] != "bgm:14" || hints[10] != "bgm:10" {
		t.Errorf("hints %v", hints)
	}
	cands := map[string]bool{}
	for _, c := range task.Candidates {
		cands[c.WorkKey] = true
	}
	for _, k := range []string{"bgm:10", "bgm:11", "bgm:12", "bgm:13", "bgm:14"} {
		if !cands[k] {
			t.Errorf("candidate %s missing: %+v", k, task.Candidates)
		}
	}

	// the anchor is season 2 (tagged; its title says 第二季): S2 files are certain, S3 files
	// (season-chain routing, was 12) and the unnumbered NCOP (was ncop1 of 11) are queued
	matchFiles(t, s, "s2s3", "[G] Test Show S2+S3 [BD]", []string{"tag11"}, []fileWant{
		{"Show/S2/[G] Test Show S2 - 01.mkv", 11, "1"},
		{"Show/S3/[G] Test Show S3 - 01.mkv", 0, "1"},
		{"Show/[G] Test Show - NCOP.mkv", 0, "ncop1"},
	})
	// a single-season S2 torrent tagged with S2 stays on the anchor
	matchFiles(t, s, "s2only", "[G] Test Show S2 - 05 [1080p]", []string{"tag11"}, []fileWant{
		{"[G] Test Show S2 - 05 [1080p].mkv", 11, "5"},
	})
	// season 3 files are not mapped (routing is a guess); they keep their parsed numbers
	// (the old plausibility check that cleared them is gone)
	matchFiles(t, s, "implausible", "[G] Test Show S1+S3", []string{"tag10"}, func() []fileWant {
		w := []fileWant{{"Show/Season 1/[G] Test Show - 01.mkv", 10, "1"}}
		for i := 1; i <= 14; i++ {
			w = append(w, fileWant{fmt.Sprintf("Show/Season 3/[G] Test Show S3 - %02d.mkv", i), 0, fmt.Sprint(i)})
		}
		return w
	}())
	// season 4 is not in the chain: unmapped
	matchFiles(t, s, "s4", "[G] Test Show S1+S4", []string{"tag10"}, []fileWant{
		{"Show/Season 1/[G] Test Show - 01.mkv", 10, "1"},
		{"Show/Season 4/[G] Test Show S4 - 01.mkv", 0, "1"},
	})
	// two contents with the same number within one work: ambiguous, no number → no longer
	// mapped to the work either (was work 10 without episode)
	matchFiles(t, s, "clash", "[G] Test Show", []string{"tag10"}, []fileWant{
		{"Show/[G] Test Show - 01.mkv", 0, ""},
		{"Show/[H] Other Cut - 01.mkv", 0, ""},
	})

	// Reclassify only rewrites stored rows with certain results: Season 1 gets its numbers
	// back, the routed files keep their stored (old) mapping (was: re-routed to 11 / 14)
	if _, err := s.W.Exec(`UPDATE torrent_files SET work_key = 'bgm:10', episode = NULL, method = 'title+name' WHERE infohash = 'batch' AND kind != 'other'`); err != nil {
		t.Fatal(err)
	}
	if n, _ := s.Reclassify(context.Background()); n == 0 {
		t.Error("reclassify changed nothing")
	}
	if tm, _ := s.GetTorrentMap(context.Background(), "batch"); tm.Files[0].EpisodeKey != "1" || tm.Files[3].Work == nil || tm.Files[3].Work.ID != 10 || tm.Files[3].EpisodeKey != "" ||
		tm.Files[9].Work == nil || tm.Files[9].Work.ID != 10 || tm.Files[9].EpisodeKey != "" {
		t.Errorf("reclassify: %+v / %+v / %+v", tm.Files[0], tm.Files[3], tm.Files[9])
	}
	if n, _ := s.Reclassify(context.Background()); n != 0 {
		t.Errorf("second reclassify changed %d", n)
	}
}

// the chain lacks season 1, but the anchor's own title says 第二季: it is season 2
func TestRouteAnchorSeasonFromTitle(t *testing.T) {
	s := openTest(t)
	seedFamily(t, s, []testWork{
		{id: 21, title: "アザー 第二季", cn: "其他 第二季", typ: "tv", begin: "2010-01-01T15:00:00Z", eps: 12},
		{id: 22, title: "アザー 第三季", cn: "其他 第三季", typ: "tv", begin: "2012-01-01T15:00:00Z", eps: 12},
	}, rel(21, 22, 3, 2))
	if _, err := s.W.Exec(`INSERT INTO work_ids(provider,ext_id,work_key) VALUES('bangumi_moe','t21','bgm:21')`); err != nil {
		t.Fatal(err)
	}
	matchFiles(t, s, "o", "[G] Another S2-S3", []string{"t21"}, []fileWant{
		{"Another/Season 2/[G] Another S2 - 03.mkv", 21, "3"},
		{"Another/Season 3/[G] Another S3 - 03.mkv", 0, "3"}, // season-chain routing: queued (was 22)
	})
}

// a tagged second cour whose files carry only the base title stays on the anchor; a
// title year that does not fit an old work prevents the title-fragment match
func TestRouteKeepsAnchor(t *testing.T) {
	s := openTest(t)
	seedFamily(t, s, []testWork{
		{id: 30, title: "ルルの姉妹", cn: "露露姐妹", typ: "tv", begin: "2026-04-05T13:30:00Z", eps: 12, aliases: `["Mahou no Lulu"]`},
		{id: 31, title: "ルルの姉妹(第2クール)", cn: "露露姐妹 第2部分", typ: "tv", begin: "2026-10-04T13:30:00Z", eps: 12},
		{id: 40, title: "リメイク", cn: "重制", typ: "tv", begin: "1989-04-14T15:00:00Z", eps: 18, aliases: `["Remake Show"]`},
		{id: 41, title: "ロングラン", cn: "长寿番", typ: "tv", begin: "1996-01-07T15:00:00Z", eps: 1200, running: true},
	}, rel(30, 31, 3, 2))
	if _, err := s.W.Exec(`INSERT INTO work_ids(provider,ext_id,work_key) VALUES('bangumi_moe','t31','bgm:31')`); err != nil {
		t.Fatal(err)
	}
	// the tagged second cour: "14" is not in its official list (1–12) as written, and the
	// file names the first cour's distinctive title — both inferences now, so it is queued
	// (was 31 / 14 via the "all files moved onto one other work" fallback)
	matchFiles(t, s, "cour2", "[G] Mahou no Lulu - 14", []string{"t31"}, []fileWant{
		{"[G] Mahou no Lulu - 14 [1080p].mkv", 0, "14"},
	})
	matchFiles(t, s, "remake", "[G] Remake Show (2024) - 01", nil, []fileWant{
		{"[G] Remake Show (2024) - 01.mkv", 0, "1"},
	})
	matchFiles(t, s, "old", "[G] Remake Show - 01", nil, []fileWant{
		{"[G] Remake Show - 01.mkv", 40, "1"},
	})
	matchFiles(t, s, "long", "[G][长寿番][2026][1150]", nil, []fileWant{
		{"[G][长寿番][2026][1150].mkv", 41, "1150"},
	})
}

func TestRouteSeasonHints(t *testing.T) {
	cases := map[string]float64{
		"[G] Show S2 - 01":             2,
		"Show Season 3":                3,
		"Show 2nd Season":              2,
		"测试秀 第二季":                      2,
		"Show 第2期":                     2,
		"Show S02E05":                  2,
		"とある魔術の禁書目録Ⅱ":                  2,
		"Show II":                      2,
		"Special 1999 (Special S1 01)": 0,
		"Show S1+S2":                   0,
		"TV S3 2007":                   3,
		"Extras (5S14E)":               0,
		"Show":                         0,
	}
	for in, want := range cases {
		if got := nameHint(in).Season; got != want {
			t.Errorf("%q: season %v, want %v", in, got, want)
		}
	}
	types := map[string]string{
		"OVA 1994~1995":                   "ova",
		"Film (1S1E)_BDrip_1080P":         "movie",
		"劇場版 Show":                        "movie",
		"Show_TV+OVA+Film+Manga":          "",
		"TV (3S97E)_DVDrip_480P_h264_aac": "tv",
	}
	for in, want := range types {
		if got := nameHint(in).Type; got != want {
			t.Errorf("%q: type %q, want %q", in, got, want)
		}
	}
}

func TestNumberOptions(t *testing.T) {
	ids := func(p, title string, single bool) string {
		var out []string
		for _, n := range NumberOptions(p, title, single) {
			out = append(out, n.ID)
		}
		return strings.Join(out, ",")
	}
	for in, want := range map[string]string{
		"[G] Show - 03 [1080p][ABCD1234].mkv":          "n_03,n_1080,n_1234",
		"Show/Season 2/Show.S02E05.1080p.x264.mkv":     "n_02,n_05,n_1080,n_264",
		"赤影疾风.Akai.Hayate.1991.OVA.E01.480p.LDRip.mkv": "n_1991,n_01,n_480",
		"[G] Show - 12.5 [AAC 5.1ch].mkv":              "n_12.5,n_5,n_1",
	} {
		if got := ids(in, "", false); got != want {
			t.Errorf("%q: %s, want %s", in, got, want)
		}
	}
	// a single-video torrent: the title's numbers too; duplicates by value once
	if got := ids("abc.mkv", "[G] Show - 07 [1080p]", true); got != "n_07,n_1080" {
		t.Errorf("single: %s", got)
	}
	if n := NumberOptions("[G] Show - 03 [1080p].mkv", "", false); !strings.Contains(n[0].Context, "⟦03⟧") || !strings.HasPrefix(n[0].Context, "file name: ") {
		t.Errorf("context %q", n[0].Context)
	}
}

// rule tightening: the same work found by an exact title fragment maps by rule; found only
// by an alias substring (or with a season the work doesn't state) it is queued with a hint
func TestUncertainAnchorsQueue(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	begin := time.Now().AddDate(0, -2, 0).UTC().Format(time.RFC3339) // recent: in the alias index
	seedFamily(t, s, []testWork{{id: 50, title: "ルルの姉妹", cn: "露露姐妹", typ: "tv", begin: begin, eps: 12, aliases: `["Mahou no Lulu"]`, running: true}}, nil)
	matchFiles(t, s, "exact", "[G] Mahou no Lulu - 03 [1080p]", nil, []fileWant{{"[G] Mahou no Lulu - 03 [1080p].mkv", 50, "3"}})
	matchFiles(t, s, "alias", "[G] Mahou no Lulu Director's Cut - 03 [1080p]", nil, []fileWant{{"[G] Mahou no Lulu Director's Cut - 03 [1080p].mkv", 0, "3"}})
	matchFiles(t, s, "season", "[G] Mahou no Lulu S2 - 03 [1080p]", nil, []fileWant{{"[G] Mahou no Lulu S2 - 03 [1080p].mkv", 0, "3"}})
	// an episode outside the official list (12 episodes) is not certain either
	matchFiles(t, s, "range", "[G] Mahou no Lulu - 13 [1080p]", nil, []fileWant{{"[G] Mahou no Lulu - 13 [1080p].mkv", 0, "13"}})
	jobs, _ := s.ListClefJobs(ctx, 10)
	queued := map[string]bool{}
	for _, j := range jobs {
		queued[j.Infohash] = true
	}
	if queued["exact"] || !queued["alias"] || !queued["season"] || !queued["range"] {
		t.Errorf("queued %v", queued)
	}
	task, _ := s.BuildClefTask(ctx, "alias", false)
	if len(task.Files) != 1 || task.Candidates[0].WorkKey != "bgm:50" || task.Candidates[0].Why != "anchor" {
		t.Errorf("alias task %+v", task)
	}
	if task, _ := s.BuildClefTask(ctx, "season", false); task.Files[0].Season != 2 || task.Candidates[0].Season != 1 || task.Candidates[0].SeasonFits(2) {
		t.Errorf("season task %+v", task)
	}
}

func TestNameEvidence(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	seedFamily(t, s, []testWork{
		{id: 60, title: "ホテル・インヒューマンズ(第2期)", cn: "杀手旅店 第二季", typ: "tv", begin: "2026-10-04T15:00:00Z", eps: 13, aliases: `["HOTEL INHUMANS Season 2"]`},
		{id: 61, title: "とある暗部の少女共棲", cn: "某暗部的少女共生", typ: "tv", begin: "2026-10-09T15:00:00Z", eps: 12, aliases: `["Toaru Anbu no Item"]`},
	}, nil)
	for _, c := range []struct {
		work int
		text string
		want bool
	}{
		{60, "[LoliHouse] 杀手旅店 / HOTEL INHUMANS - 16 [WebRip 1080p]", true}, // season words stripped
		{60, "[Dynamis One] Hotel Inhumans 2nd Season - 15 (CR 1920x1080)", true},
		{61, "[Dynamis One] Tempal: Item no Chikara - 03 (ABEMA 1920x1080 AVC AAC MKV)", false},
		{61, "[G] Toaru Anbu no Item - 03", true},
	} {
		if got := s.NameEvidence(ctx, fmt.Sprintf("bgm:%d", c.work), []string{c.text}); got != c.want {
			t.Errorf("%d %q: %v", c.work, c.text, got)
		}
	}
}
