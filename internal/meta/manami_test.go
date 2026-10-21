package meta

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestManamiImport(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	for i := 1; i <= 5; i++ {
		seedWork(t, s, i, fmt.Sprintf("作品%d", i), "", `[]`, "tv", "2011-01-01", "2011-04-01", seq(1, 12), nil)
	}
	for _, q := range []string{
		`INSERT INTO work_ids VALUES('mal','10','bgm:1')`,
		`INSERT INTO work_ids VALUES('anilist','20','bgm:1')`,
		`INSERT INTO work_ids VALUES('mal','11','bgm:2')`,
		`INSERT INTO work_ids VALUES('anilist','21','bgm:3')`,
		`INSERT INTO work_ids VALUES('mal','12','bgm:4')`,
		`INSERT INTO work_ids VALUES('anilist','22','bgm:4')`,
		`INSERT INTO work_ids VALUES('mal','13','bgm:5')`,
	} {
		if _, err := s.W.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	var body strings.Builder
	body.WriteString(`{"$schema":"test","lastUpdate":"2026-07-04"}` + "\n")
	es := []manamiEntry{
		{Sources: []string{"https://myanimelist.net/anime/10/slug", "https://anilist.co/anime/20"}, Title: "Deterministic Name", Synonyms: []string{"Romaji Alternate", "123456", "ab", "中文", "Generic Name"}},
		{Sources: []string{"https://myanimelist.net/anime/11", "https://anilist.co/anime/21"}, Title: "Conflicting IDs"},
		{Sources: []string{"https://myanimelist.net/anime/12", "https://anilist.co/anime/999"}, Title: "Contradictory ID"},
		{Sources: []string{"https://myanimelist.net/anime/13"}, Title: "Duplicate Entry One"},
		{Sources: []string{"https://myanimelist.net/anime/13"}, Title: "Duplicate Entry Two"},
		{Sources: []string{"https://myanimelist.net/anime/9991"}, Title: "Generic Name"},
		{Sources: []string{"https://myanimelist.net/anime/9992"}, Title: "Generic Name"},
		{Sources: []string{"https://myanimelist.net.evil.test/anime/10"}, Title: "Forged Host"},
	}
	for _, e := range es {
		b, _ := json.Marshal(e)
		body.Write(b)
		body.WriteByte('\n')
	}
	data := body.String()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, data) }))
	defer srv.Close()
	st, err := s.ImportManami(ctx, srv.Client(), srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	if st.Running || st.Linked != 1 || st.Names != 2 || st.Entries != 8 {
		t.Fatalf("status %+v", st)
	}
	if got := strings.Join(s.ManamiNames(ctx, "bgm:1"), ","); !strings.Contains(got, "Romaji Alternate") || strings.Contains(got, "Generic") {
		t.Fatal(got)
	}
	for i := 2; i <= 5; i++ {
		if ns := s.ManamiNames(ctx, fmt.Sprintf("bgm:%d", i)); len(ns) != 0 {
			t.Fatal(ns)
		}
	}
	if !s.NameEvidence(ctx, "bgm:1", []string{"[G] Romaji Alternate - 03.mkv"}) {
		t.Fatal("not in evidence")
	}
	idx, _ := s.aliasIndex(ctx)
	if len(idx.norm[normName("Romaji Alternate")]) != 1 {
		t.Fatal("not in rule index")
	}
	if s.ManamiState(ctx).SyncedAt == "" {
		t.Fatal("state not persisted")
	}
	// Failed / truncated imports preserve the previous source; replacement removes old names.
	for _, bad := range []string{`{"title":"No metadata"}`, `{"$schema":"s","lastUpdate":"2026-07-04"}` + "\n" + `{"title":`, strings.Repeat("x", (2<<20)+1)} {
		data = bad
		if _, err := s.ImportManami(ctx, srv.Client(), srv.URL); err == nil {
			t.Fatal("accepted corrupt import")
		}
		if len(s.ManamiNames(ctx, "bgm:1")) != 2 {
			t.Fatal("previous names lost")
		}
	}
	data = `{"$schema":"s","lastUpdate":"2026-07-05"}` + "\n" + `{"sources":["https://myanimelist.net/anime/10"],"title":"Replacement Name"}` + "\n"
	if _, err := s.ImportManami(ctx, srv.Client(), srv.URL); err != nil {
		t.Fatal(err)
	}
	if ns := s.ManamiNames(ctx, "bgm:1"); len(ns) != 1 || ns[0] != "Replacement Name" {
		t.Fatal(ns)
	}
	idx, _ = s.aliasIndex(ctx)
	if len(idx.norm[normName("Romaji Alternate")]) != 0 {
		t.Fatal("old index remained")
	}
}

func TestNameEvidenceImprovements(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	seedWork(t, s, 1, "Cyberpunk: Edgerunners 2", "", `[]`, "tv", "2026-01-01", "", seq(1, 12), nil)
	seedWork(t, s, 2, "幻想水滸伝", "", `["Gensou Suikoden"]`, "tv", "2026-01-01", "", seq(1, 12), nil)
	seedWork(t, s, 3, "Battle Spirits [Re] 絕界之空", "", `[]`, "tv", "2026-01-01", "", seq(1, 12), nil)
	seedWork(t, s, 4, "日常", "", `["Nichijou"]`, "tv", "2011-01-01", "", seq(1, 26), nil)
	seedWork(t, s, 5, "日常の0話", "", `[]`, "ova", "2011-01-01", "", seq(1, 1), nil)
	seedWork(t, s, 6, "Unrelated OVA", "", `["Unrelated Side Story"]`, "ova", "2011-01-01", "", seq(1, 1), nil)
	relate(t, s, 5, 4, 12)
	relate(t, s, 5, 6, 6) // 6 is a child, NOT a name parent
	seedWork(t, s, 7, "東京リベンジャーズ", "", `["Tokyo Revengers"]`, "tv", "2021-01-01", "", seq(1, 24), nil)
	seedWork(t, s, 8, "東京リベンジャーズ 天竺編", "", `["Tokyo Revengers: Tenjiku-hen"]`, "tv", "2023-01-01", "", seq(1, 12), nil)
	seedWork(t, s, 9, "東京リベンジャーズ 三天戦争編", "", `["Tokyo Revengers: Santen Sensou-hen"]`, "tv", "2026-01-01", "", seq(1, 12), nil)
	relate(t, s, 9, 8, 2)
	relate(t, s, 8, 7, 2)
	for _, tc := range []struct {
		wk, text string
		want     bool
	}{
		{"bgm:1", "Cyberpunk.Edgerunners.S02E03.2160p.mkv", true},
		{"bgm:1", "Unrelated Show S02E03.mkv", false},
		{"bgm:2", "Genso.Suikoden.S01E03.mkv", true},
		{"bgm:2", "Gensō Suikoden - 03.mkv", true},
		{"bgm:2", "Gensou Otherthing - 03.mkv", false},
		{"bgm:3", "[ANi] Battle Spirits 絕界之空 [3] - Re [1080p].mp4", true},
		{"bgm:3", "Some Other Battle - 03.mkv", false},
		{"bgm:5", "[VCB] Nichijou [00].mkv", true},
		{"bgm:5", "[G] Unrelated Side Story - 01.mkv", false},
		{"bgm:9", "Tokyo.Revengers.S04E03.mkv", true},
	} {
		if got := s.NameEvidence(ctx, tc.wk, []string{tc.text}); got != tc.want {
			t.Errorf("%s %q: %v want %v", tc.wk, tc.text, got, tc.want)
		}
	}
	// A colon prefix outside the franchise must NOT count.
	seedWork(t, s, 10, "Another Story", "", `["Tokyo Revengers: Unrelated Story"]`, "tv", "2026-01-01", "", seq(1, 12), nil)
	if s.NameEvidence(ctx, "bgm:10", []string{"Tokyo.Revengers.S04E03.mkv"}) {
		t.Error("unrelated colon prefix used")
	}
	// A web mini-anime spin-off (番外篇 6 from parent) also borrows its parent's name.
	seedWork(t, s, 11, "無能なナナ", "无能的奈奈", `["Munou na Nana"]`, "tv", "2020-10-04", "", seq(1, 13), nil)
	seedWork(t, s, 12, "「無能なナナ」ミニアニメ", "「无能的奈奈」迷你动画", `[]`, "web", "2020-10-08", "", seq(1, 12), nil)
	relate(t, s, 11, 12, 6)
	if !s.NameEvidence(ctx, "bgm:12", []string{"[Nekomoe kissaten] Munou na Nana [Mini][10][BDRip 1080p HEVC-10bit FLAC].mkv"}) {
		t.Error("web mini-anime parent name")
	}
}
