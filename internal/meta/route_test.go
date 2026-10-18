package meta

import (
	"context"
	"fmt"
	"testing"
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
	if err := s.Match(context.Background(), TorrentInput{Infohash: ih, Title: title, MoeTagIDs: tags, Files: files}); err != nil {
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
	// old works, no bangumi.moe tag: the anchor comes from the title fragments (测试秀 and
	// テストショー both name bgm:10)
	matchFiles(t, s, "batch", "[整理] 测试秀 (テストショー) TV+OVA+剧场版 合集 (整理时间：2026.07.15)", nil, []fileWant{
		{root + "TV/Season 1/[G] Test Show - 01 [1080p].mkv", 10, "1"},
		{root + "TV/Season 1/[G] Test Show - 12 [1080p].mkv", 10, "12"},
		{root + "TV/Season 2/[G] Test Show S2 - 01 [1080p].mkv", 11, "1"},
		{root + "TV/Season 2/[G] Test Show S2 - 12 [1080p].mkv", 11, "12"},
		{root + "TV/Season 2/[G] Test Show S2 - NCOP [1080p].mkv", 11, "ncop1"},
		{root + "TV/テストショー ファイナル/[G] Test Show - 01 [1080p].mkv", 12, "1"},
		{root + "TV/テストショー ファイナル/[G] Test Show - 10 [1080p].mkv", 12, "10"},
		{root + "Movie/[G] Test Show The Movie [1080p].mkv", 13, "1"},
		{root + "OVA/[G] Test Show OVA はじまりの日 [1080p].mkv", 14, "1"},
		{root + "OVA/[G] Test Show OVA さよならの日 [1080p].mkv", 14, "2"},
		{root + "SPs/[G] Test Show - NCOP [1080p].mkv", 10, "ncop1"},
		{root + "TV/Season 1/[G] Test Show - 01 [1080p].ass", 0, ""},
	})
	tm, _ := s.GetTorrentMap(context.Background(), "batch")
	if m := tm.Files[0].Method; m != "title+name" {
		t.Errorf("anchor method %q", m)
	}
	if m := tm.Files[2].Method; m != "title+season+name" {
		t.Errorf("routed method %q", m)
	}

	// the anchor is season 2 (tagged); its title says 第二季, so S2 / S3 land on 11 / 12
	matchFiles(t, s, "s2s3", "[G] Test Show S2+S3 [BD]", []string{"tag11"}, []fileWant{
		{"Show/S2/[G] Test Show S2 - 01.mkv", 11, "1"},
		{"Show/S3/[G] Test Show S3 - 01.mkv", 12, "1"},
		{"Show/[G] Test Show - NCOP.mkv", 11, "ncop1"},
	})
	// a single-season S2 torrent tagged with S2 stays on the anchor
	matchFiles(t, s, "s2only", "[G] Test Show S2 - 05 [1080p]", []string{"tag11"}, []fileWant{
		{"[G] Test Show S2 - 05 [1080p].mkv", 11, "5"},
	})
	// an implausible season group (14 episodes for a 10-episode work) is left unmapped;
	// the same numbers in another season do not clash
	matchFiles(t, s, "implausible", "[G] Test Show S1+S3", []string{"tag10"}, func() []fileWant {
		w := []fileWant{{"Show/Season 1/[G] Test Show - 01.mkv", 10, "1"}}
		for i := 1; i <= 14; i++ {
			w = append(w, fileWant{fmt.Sprintf("Show/Season 3/[G] Test Show S3 - %02d.mkv", i), 0, ""})
		}
		return w
	}())
	// season 4 is not in the chain: unmapped (not guessed onto the anchor)
	matchFiles(t, s, "s4", "[G] Test Show S1+S4", []string{"tag10"}, []fileWant{
		{"Show/Season 1/[G] Test Show - 01.mkv", 10, "1"},
		{"Show/Season 4/[G] Test Show S4 - 01.mkv", 0, ""},
	})
	// within one work the old clash rule stands: two contents with the same number
	matchFiles(t, s, "clash", "[G] Test Show", []string{"tag10"}, []fileWant{
		{"Show/[G] Test Show - 01.mkv", 10, ""},
		{"Show/[H] Other Cut - 01.mkv", 10, ""},
	})

	// stored rows are re-routed by Reclassify the same way (anchor = unrouted files' work)
	if _, err := s.W.Exec(`UPDATE torrent_files SET work_key = 'bgm:10', episode = NULL, method = 'title+name' WHERE infohash = 'batch' AND kind != 'other'`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Reclassify(context.Background()); err != nil {
		t.Fatal(err)
	}
	if tm, _ := s.GetTorrentMap(context.Background(), "batch"); tm.Files[3].Work == nil || tm.Files[3].Work.ID != 11 || tm.Files[3].EpisodeKey != "12" ||
		tm.Files[9].Work == nil || tm.Files[9].Work.ID != 14 || tm.Files[9].EpisodeKey != "2" {
		t.Errorf("reclassify: %+v / %+v", tm.Files[3], tm.Files[9])
	}
	if n, _ := s.Reclassify(context.Background()); n != 0 {
		t.Errorf("second reclassify changed %d", n)
	}
	// the subject lists see the torrent under every work it holds
	for _, k := range []string{"bgm:10", "bgm:11", "bgm:12", "bgm:13", "bgm:14"} {
		list, _ := s.TorrentsByWork(context.Background(), k, 10, false)
		found := false
		for _, x := range list {
			found = found || x.Infohash == "batch"
		}
		if !found {
			t.Errorf("%s: batch not listed", k)
		}
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
		{"Another/Season 3/[G] Another S3 - 03.mkv", 22, "3"},
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
	matchFiles(t, s, "cour2", "[G] Mahou no Lulu - 14", []string{"t31"}, []fileWant{
		{"[G] Mahou no Lulu - 14 [1080p].mkv", 31, "14"},
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
