package meta

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"
)

// bangumi-data is imported without a year cut (old works get their aliases), while the
// airing timeline still only lists works by its own date window.
func TestImportBangumiDataAllYears(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	now := time.Now().UTC()
	begin := now.AddDate(0, 0, -20).Format(time.RFC3339)
	data := `{"items":[
		{"title":"とある魔術の禁書目録","type":"tv","begin":"2008-10-04T15:00:00.000Z","end":"2009-03-19T15:00:00.000Z",
		 "titleTranslate":{"zh-Hans":["魔法禁书目录"],"en":["A Certain Magical Index"]},
		 "sites":[{"site":"bangumi","id":"1014"},{"site":"gamer","id":"9999"}]},
		{"title":"今期の作品","type":"tv","begin":"` + begin + `","broadcast":"R/` + begin + `/P7D",
		 "titleTranslate":{"ja":["こんき"]},"sites":[{"site":"bangumi","id":"500001"}]}
	]}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(data)) }))
	defer srv.Close()
	n, err := s.ImportBangumiData(ctx, srv.Client(), srv.URL)
	if err != nil || n != 2 {
		t.Fatalf("import: %d %v", n, err)
	}
	old, err := s.GetWork(ctx, "bgm:1014")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(old.Aliases, "A Certain Magical Index") || old.External["gamer"] != "9999" {
		t.Errorf("old work: aliases %v external %v", old.Aliases, old.External)
	}
	cur, _ := s.GetWork(ctx, "bgm:500001")
	if cur == nil || !slices.Contains(cur.Aliases, "こんき") {
		t.Errorf("ja translations are aliases too: %+v", cur)
	}
	sched, err := s.WorksForSchedule(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(sched) != 1 || sched[0].Key != "bgm:500001" {
		t.Errorf("schedule candidates: %+v", sched)
	}
}

// bd_sites keeps the danmaku site ids of every subject, also shared ones (one Bahamut page
// for both cours), and is rebuilt on every import.
func TestImportDanmakuSites(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	data := `{"items":[
		{"title":"A","type":"tv","begin":"2022-04-09T15:00:00.000Z","sites":[{"site":"bangumi","id":"329906"},{"site":"gamer","id":"120396"},{"site":"bilibili","id":"28237119"},{"site":"mal","id":"1"}]},
		{"title":"A 2","type":"tv","begin":"2022-10-01T15:00:00.000Z","sites":[{"site":"bangumi","id":"371546"},{"site":"gamer","id":"120396"},{"site":"gamer_hk","id":"120396"}]},
		{"title":"no bgm id","type":"tv","begin":"2022-10-01T15:00:00.000Z","sites":[{"site":"gamer","id":"5"}]}
	]}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(data)) }))
	defer srv.Close()
	for range 2 { // re-import: same rows
		if _, err := s.ImportBangumiData(ctx, srv.Client(), srv.URL); err != nil {
			t.Fatal(err)
		}
	}
	sites, _ := s.SiteIDs(ctx, 329906)
	if len(sites) != 2 || sites[0] != (SiteID{"bilibili", "28237119"}) || sites[1] != (SiteID{"gamer", "120396"}) {
		t.Errorf("sites: %+v", sites)
	}
	ids, _ := s.SubjectsWithSite(ctx, []string{"gamer", "gamer_hk"}, "120396")
	if len(ids) != 2 || ids[0] != 329906 || ids[1] != 371546 {
		t.Errorf("sharing subjects: %v", ids)
	}
	var n int
	_ = s.R.QueryRow(`SELECT COUNT(*) FROM bd_sites`).Scan(&n)
	if n != 4 {
		t.Errorf("bd_sites rows: %d", n)
	}
}
