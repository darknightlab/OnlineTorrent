package meta

import (
	"context"
	"path/filepath"
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
		{"[Team] Show [01-12]/[Team] Show - NCOP1 [1080p].mkv", 12, "op", -1},
		{"[Team] Show [01-12]/NCED/[Team] Show NCED [1080p].mkv", 12, "ed", -1},
		{"[Team] Show [01-12]/SPs/[Team] Show [PV01][1080p].mkv", 12, "extra", -1},
		{"[Team] Show [01-12]/SPs/[Team] Show [CM02][1080p].mkv", 12, "extra", -1},
		{"[Team] Show [01-12]/SPs/[Team] Show [Menu][1080p].mkv", 12, "extra", -1},
		{"[Team] Show [01-12]/SPs/[Team] Show - 01 [1080p].mkv", 12, "sp", 1},
		{"[Team] Show [01-12]/特典映像/Show 01.mkv", 12, "extra", -1},
		{"[Team] Show [01-12]/Extras/Show - 01 [1080p].mkv", 12, "extra", -1},
		{"Show - 05 [1080p] sample.mkv", 1, "extra", -1},
		{"[Team] Show [01-12]/Fonts/a.ttf", 12, "other", -1},
		{"[Team] Show - 04 [1080p].ass", 1, "other", -1},
		{"[Team] Show [1080p].mkv", 1, "episode", 7}, // single video without number: torrent title
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
	if _, err := s.W.ExecContext(ctx, `INSERT INTO works(id,key,title,title_cn,aliases,type,begin_at,updated_at) VALUES
		(1,'bgm:400602','葬送のフリーレン','葬送的芙莉莲','["Frieren: Beyond Journey''s End","Sousou no Frieren"]','tv',?, 'x'),
		(2,'bgm:1','ぼっち・ざ・ろっく！','孤独摇滚！','["Bocchi the Rock!"]','tv','2022-10-08T15:00:00Z','x')`, "2026-01-01T00:00:00Z"); err != nil {
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
	tm, _ := s.GetTorrentMap(ctx, "aa")
	if tm.Files[0].Kind != "episode" || tm.Files[0].Work == nil || *tm.Files[0].Episode != 28 || tm.Files[1].Kind != "op" || tm.Files[1].Work != nil || tm.Files[2].Kind != "other" {
		t.Errorf("files: %+v", tm.Files)
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
		list, total, err := s.SearchTorrents(ctx, q, 0, 10)
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
	if _, _, err := s.SearchTorrents(ctx, `"a OR b* NEAR(`, 0, 10); err != nil {
		t.Errorf("quoted query: %v", err)
	}

	for q, want := range map[string]int64{"芙莉莲": 1, "sousou no": 1, "Bocchi": 2, "孤独": 2} {
		ws, err := s.SearchWorksFTS(ctx, q, 10)
		if err != nil || len(ws) != 1 || ws[0].ID != want {
			t.Errorf("works %q: %v %+v", q, err, ws)
		}
	}
	// works FTS follows updates (trigger) and external upserts
	if _, err := s.W.ExecContext(ctx, `UPDATE works SET title_cn = '波奇酱' WHERE id = 2`); err != nil {
		t.Fatal(err)
	}
	if ws, _ := s.SearchWorksFTS(ctx, "波奇酱", 10); len(ws) != 1 {
		t.Errorf("after update: %+v", ws)
	}
	w, err := s.UpsertExternalWork(ctx, ExternalWork{Provider: "bgm", ExtID: "999", Title: "Some Old Show", TitleCN: "某老番", Type: "tv"})
	if err != nil || w.ID != 3 || w.External["bgm"] != "999" {
		t.Fatalf("external: %v %+v", err, w)
	}
	if ws, _ := s.SearchWorksFTS(ctx, "某老番", 10); len(ws) != 1 || ws[0].Key != "bgm:999" {
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
