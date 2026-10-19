package meta

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
)

func TestClassifyFile(t *testing.T) {
	cases := []struct {
		path   string
		videos int
		kind   string
		ep     float64 // -1 none
	}{
		{"[Team] Show [01-12]/[Team] Show - 03 [1080p].mkv", 12, "episode", 3},
		{"[Team] Show [01-12]/[Team] Show - NCOP1 [1080p].mkv", 12, "op", 1},
		{"[Team] Show [01-12]/NCED/[Team] Show NCED [1080p].mkv", 12, "ed", -1}, // unnumbered (ClassifyFiles: 1 if alone)
		{"[Team] Show [01-12]/SPs/[Team] Show [PV01][1080p].mkv", 12, "pv", 1},
		{"[Team] Show [01-12]/SPs/[Team] Show [CM02][1080p].mkv", 12, "cm", 2},
		{"[Team] Show [01-12]/SPs/[Team] Show [Menu][1080p].mkv", 12, "extra", -1},
		{"[Team] Show [01-12]/SPs/[Team] Show - 01 [1080p].mkv", 12, "sp", 1},
		{"[Team] Show [01-12]/特典映像/Show 01.mkv", 12, "extra", -1},
		{"[Team] Show [01-12]/Extras/Show - 01 [1080p].mkv", 12, "extra", -1},
		{"Show - 05 [1080p] sample.mkv", 1, "extra", -1},
		{"[Team] Show [01-12]/Fonts/a.ttf", 12, "other", -1},
		{"[Team] Show - 04 [1080p].ass", 1, "other", -1},
		{"[Team] Show [1080p].mkv", 1, "episode", 7}, // single video without number: torrent title
		{"[Team] Show [BD]/SPs/Show - PV01.mkv", 12, "pv", 1},
		{"[Team] Show [BD]/SPs/Show - OVA [1080p].mkv", 12, "sp", -1},
		{"[Team] Show [BD]/SPs/Show [Preview01][1080p].mkv", 12, "extra", -1},
		{"[Team] Show [BD]/PVs/Show 02.mkv", 12, "pv", 2},
		{"[Team] Show [BD]/CM/CM02.mkv", 12, "cm", 2},
		{"[Team] Show [BD]/NCOP/Show.mkv", 12, "op", -1},
		{"[Team] Show [BD]/SPs/Show.mkv", 12, "sp", -1},
		{"[Team] Show [BD]/Show - 12.5 [1080p].mkv", 12, "episode", 12.5},
		{"[Team] Show [BD]/Show - 00.mkv", 12, "episode", 0},
	}
	for _, c := range cases {
		p := ClassifyFile(c.path, "[Team] Show - 07 [1080p]", c.videos)
		got := -1.0
		if p.Episode != nil {
			got = *p.Episode
		}
		if p.Kind != c.kind || got != c.ep {
			t.Errorf("%q: got %s/%v, want %s/%v", c.path, p.Kind, got, c.kind, c.ep)
		}
	}
}

func openTest(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "meta.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	return s
}

func TestCatalogAndSearch(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	if _, err := s.W.ExecContext(ctx, `INSERT INTO works(id,key,title,title_cn,aliases,type,begin_at,eps,updated_at) VALUES
		(1,'bgm:400602','葬送のフリーレン','葬送的芙莉莲','["Frieren: Beyond Journey''s End","Sousou no Frieren"]','tv',?,28, 'x'),
		(2,'bgm:1','ぼっち・ざ・ろっく！','孤独摇滚！','["Bocchi the Rock!"]','tv','2022-10-08T15:00:00Z',12,'x')`, "2026-01-01T00:00:00Z"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.W.ExecContext(ctx, `INSERT INTO work_ids(provider,ext_id,work_key) VALUES('bgm','400602','bgm:400602'),('bangumi_moe','tag1','bgm:400602')`); err != nil {
		t.Fatal(err)
	}
	// moe tag → strong match; files: an episode, an NCOP and a subtitle
	err := s.Match(ctx, TorrentInput{Infohash: "aa", Title: "[LoliHouse] Sousou no Frieren - 28 [WebRip 1080p]", Source: "bangumi.moe", SourceKind: "bangumi_moe", SourceID: "moe1",
		Trusted: true, Team: "LoliHouse", PublishedAt: "2026-10-18T10:00:00Z", MoeTagIDs: []string{"tag1"}, Tags: []TagRef{{ID: "tag1", Name: "葬送的芙莉莲"}},
		Files: []FileInput{{0, "Frieren/[LoliHouse] Sousou no Frieren - 28 [1080p].mkv", 100}, {1, "Frieren/NCOP.mkv", 10}, {2, "Frieren/[LoliHouse] Sousou no Frieren - 28.ass", 1}}})
	if err != nil {
		t.Fatal(err)
	}
	// pending (magnet only), untrusted
	if err := s.UpsertPending(ctx, TorrentInput{Infohash: "bb", Title: "[Sub] 孤独摇滚 - 03 [1080p]", Source: "Nyaa", SourceKind: "rss", PublishedAt: "2026-10-17T00:00:00Z"}); err != nil {
		t.Fatal(err)
	}

	ct, err := s.CatalogByHash(ctx, "aa")
	if err != nil {
		t.Fatal(err)
	}
	if !ct.Trusted || ct.Status != "ready" || ct.FileCount != 3 || ct.Size != 111 || len(ct.Files) != 3 || len(ct.Tags) != 1 {
		t.Errorf("catalog row: %+v", ct)
	}
	// episode 28 is certain (tag + " - 28" within the planned 28 episodes); the lone
	// unnumbered NCOP is only *inferred* to be ncop1, so since the decision model it stays
	// unmapped (key kept for display) and the torrent is queued for the model
	tm, _ := s.GetTorrentMap(ctx, "aa")
	if tm.Files[0].Kind != "episode" || tm.Files[0].Work == nil || *tm.Files[0].Episode != 28 || tm.Files[1].Kind != "op" || tm.Files[1].Work != nil || tm.Files[1].EpisodeKey != "ncop1" || tm.Files[0].EpisodeKey != "28" || tm.Files[2].Kind != "other" {
		t.Errorf("files: %+v", tm.Files)
	}
	if jobs, _ := s.ListClefJobs(ctx, 10); len(jobs) != 1 || jobs[0].Infohash != "aa" || jobs[0].Status != "pending" {
		t.Errorf("clef jobs: %+v", jobs)
	}
	if st, _ := s.KnownTorrent(ctx, "bb"); st != "pending" {
		t.Errorf("bb status %q", st)
	}
	// an untrusted user submission must not take over a crawled entry
	_ = s.UpsertPending(ctx, TorrentInput{Infohash: "aa", Title: "evil", Source: "user", SourceKind: "user"})
	if ct, _ := s.CatalogByHash(ctx, "aa"); ct.Source != "bangumi.moe" || !ct.Trusted || ct.Status != "ready" {
		t.Errorf("downgraded: %+v", ct)
	}

	for q, want := range map[string]string{
		"Frieren":     "aa", // title (case-insensitive trigram)
		"frieren 28":  "aa", // two terms AND; "28" (< 3 chars) → LIKE fallback on titles
		"NCOP":        "aa", // file name
		"孤独摇滚":        "bb", // CJK trigram
		"摇滚":          "bb", // 2 chars → LIKE
		"nothinghere": "",
	} {
		list, total, err := s.SearchTorrents(ctx, q, 0, 10, nil)
		if err != nil {
			t.Fatalf("%q: %v", q, err)
		}
		if want == "" {
			if total != 0 {
				t.Errorf("%q: want none, got %d", q, total)
			}
			continue
		}
		if total != 1 || len(list) != 1 || list[0].Infohash != want {
			t.Errorf("%q: got total=%d %+v", q, total, list)
		}
	}
	// FTS query syntax in user input is quoted, not interpreted
	if _, _, err := s.SearchTorrents(ctx, `"a OR b* NEAR(`, 0, 10, nil); err != nil {
		t.Errorf("quoted query: %v", err)
	}

	for q, want := range map[string]int64{"芙莉莲": 1, "sousou no": 1, "Bocchi": 2, "孤独": 2} {
		ws, err := s.SearchWorksFTS(ctx, q, 10, nil)
		if err != nil || len(ws) != 1 || ws[0].ID != want {
			t.Errorf("works %q: %v %+v", q, err, ws)
		}
	}
	// works FTS follows updates (trigger) and external upserts
	if _, err := s.W.ExecContext(ctx, `UPDATE works SET title_cn = '波奇酱' WHERE id = 2`); err != nil {
		t.Fatal(err)
	}
	if ws, _ := s.SearchWorksFTS(ctx, "波奇酱", 10, nil); len(ws) != 1 {
		t.Errorf("after update: %+v", ws)
	}
	w, err := s.UpsertExternalWork(ctx, ExternalWork{Provider: "bgm", ExtID: "999", Title: "Some Old Show", TitleCN: "某老番", Type: "tv"})
	if err != nil || w.ID != 3 || w.External["bgm"] != "999" {
		t.Fatalf("external: %v %+v", err, w)
	}
	if ws, _ := s.SearchWorksFTS(ctx, "某老番", 10, nil); len(ws) != 1 || ws[0].Key != "bgm:999" {
		t.Errorf("external work not searchable: %+v", ws)
	}
	// existing work (bangumi-data) is reused, not duplicated
	w2, err := s.UpsertExternalWork(ctx, ExternalWork{Provider: "bgm", ExtID: "400602", Title: "x", Cover: "https://c"})
	if err != nil || w2.Key != "bgm:400602" || w2.Cover != "https://c" {
		t.Errorf("reuse: %v %+v", err, w2)
	}

	byWork, err := s.TorrentsByWork(ctx, "bgm:400602", 10, true)
	if err != nil || len(byWork) != 1 || byWork[0].Infohash != "aa" || len(byWork[0].Files) != 3 {
		t.Errorf("by work: %v %+v", err, byWork)
	}
	if ct, err := s.CatalogBySourceID(ctx, "bangumi_moe", "moe1"); err != nil || ct.Infohash != "aa" {
		t.Errorf("by source id: %v", err)
	}
	if files, _ := s.EpisodeFiles(ctx, "bgm:400602", EpisodeKey{Kind: KeyMain, Number: 28}); len(files) != 1 || !files[0].Trusted || files[0].Source != "bangumi.moe" {
		t.Errorf("episode files: %+v", files)
	}
}

func TestReclassifyAndEpisodeKeys(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	if _, err := s.W.ExecContext(ctx, `INSERT INTO works(id,key,title,title_cn,type,begin_at,updated_at) VALUES (1,'bgm:1','Show','番','tv','2026-01-01T00:00:00Z','x')`); err != nil {
		t.Fatal(err)
	}
	// rows as written before specials kept their number: op/extra unmapped, sp without number
	if _, err := s.W.ExecContext(ctx, `INSERT INTO torrents(infohash,seq,title,status,published_at,updated_at) VALUES ('aa',1,'[Team] Show [BD]','ready','2026-02-01T00:00:00Z','x')`); err != nil {
		t.Fatal(err)
	}
	rows := []struct {
		idx        int
		path, kind string
		work       any
		ep         any
		status     string
	}{
		{0, "Show/[Team] Show - 01 [1080p].mkv", "episode", "bgm:1", 1.0, "auto"},
		{1, "Show/[Team] Show - 12.5 [1080p].mkv", "episode", "bgm:1", 12.5, "auto"},
		{2, "Show/SPs/[Team] Show - NCOP1 [1080p].mkv", "op", nil, nil, "auto"},
		{3, "Show/SPs/[Team] Show - NCED [1080p].mkv", "ed", nil, nil, "auto"},
		{4, "Show/SPs/[Team] Show [PV01][1080p].mkv", "extra", nil, nil, "auto"},
		{5, "Show/SPs/[Team] Show - SP [1080p].mkv", "sp", "bgm:1", nil, "auto"},
		{6, "Show/SPs/[Team] Show [CM02][1080p].mkv", "extra", nil, nil, "verified"}, // manual rows are kept
		{7, "Show/SPs/[Team] Show [Menu][1080p].mkv", "extra", nil, nil, "auto"},
	}
	for _, r := range rows {
		if _, err := s.W.ExecContext(ctx, `INSERT INTO torrent_files(infohash,file_index,path,kind,work_key,episode,confidence,status,method,updated_at) VALUES('aa',?,?,?,?,?,0.9,?,'moe_tag+name','x')`,
			r.idx, r.path, r.kind, r.work, r.ep, r.status); err != nil {
			t.Fatal(err)
		}
	}
	// Only certain results rewrite stored rows: NCOP1 and PV01 carry their own number (the
	// work lists no OP/PV episodes, nothing to check against). The lone unnumbered NCED / SP
	// used to become nced1 / sp1 — an inference, so since the decision model those rows are
	// left as they are (Reclassify never unmaps or re-decides existing data).
	n, err := s.Reclassify(ctx)
	if err != nil || n != 2 {
		t.Fatalf("reclassify: %d %v", n, err)
	}
	tm, _ := s.GetTorrentMap(ctx, "aa")
	want := []string{"1", "12.5", "ncop1", "", "pv1", "", "", ""}
	for i, f := range tm.Files {
		if f.EpisodeKey != want[i] || (want[i] != "" && (f.Work == nil || f.Work.Key != "bgm:1")) {
			t.Errorf("file %d: key %q work %+v kind %s", i, f.EpisodeKey, f.Work, f.Kind)
		}
	}
	if tm.Files[6].Kind != "extra" || tm.Files[6].Status != "verified" {
		t.Errorf("verified row changed: %+v", tm.Files[6])
	}
	// second run is a no-op
	if n, _ := s.Reclassify(ctx); n != 0 {
		t.Errorf("second run changed %d", n)
	}

	keys, err := s.EpisodeKeys(ctx, "bgm:1")
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, k := range keys {
		got = append(got, k.Key.String())
	}
	if strings.Join(got, ",") != "1,12.5,ncop1,pv1" {
		t.Errorf("keys: %v", got)
	}
	if files, _ := s.EpisodeFiles(ctx, "bgm:1", EpisodeKey{Kind: KeyNCOP, Number: 1}); len(files) != 1 || files[0].Index != 2 {
		t.Errorf("ncop1 files: %+v", files)
	}
	// manual correction by episode key
	if err := s.CorrectFile(ctx, "aa", 6, SetFile{WorkKey: strPtr("bgm:1"), Key: "cm2"}, "t"); err != nil {
		t.Fatal(err)
	}
	if files, _ := s.EpisodeFiles(ctx, "bgm:1", EpisodeKey{Kind: KeyCM, Number: 2}); len(files) != 1 {
		t.Errorf("cm2 files: %+v", files)
	}
	if err := s.CorrectFile(ctx, "aa", 6, SetFile{Kind: "bogus"}, "t"); err == nil {
		t.Error("invalid kind accepted")
	}
	// the one-off job runs once; its flag is inserted by ReclassifyOnce, not by a migration
	var v string
	_ = s.R.QueryRowContext(ctx, `SELECT value FROM meta_state WHERE key = ?`, stateReclassify).Scan(&v)
	if v != "" {
		t.Errorf("state %q", v)
	}
	s.ReclassifyOnce(ctx, slog.New(slog.NewTextHandler(io.Discard, nil)))
	_ = s.R.QueryRowContext(ctx, `SELECT value FROM meta_state WHERE key = ?`, stateReclassify).Scan(&v)
	if v != "done" {
		t.Errorf("state after %q", v)
	}
	// a later start does not re-arm it
	s.ReclassifyOnce(ctx, slog.New(slog.NewTextHandler(io.Discard, nil)))
	_ = s.R.QueryRowContext(ctx, `SELECT value FROM meta_state WHERE key = ?`, stateReclassify).Scan(&v)
	if v != "done" {
		t.Errorf("state after second run %q", v)
	}
}

func strPtr(s string) *string { return &s }
