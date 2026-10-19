package api

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/darknightlab/onlinetorrent/internal/meta"
	"github.com/darknightlab/onlinetorrent/internal/store"
)

func TestSwarmInListingsAndSiteRules(t *testing.T) {
	s, h := testServer(t)
	ctx := context.Background()
	if _, err := s.Meta.W.ExecContext(ctx, `INSERT INTO works(id,key,title,title_cn,aliases,type,begin_at,eps,updated_at) VALUES
		(7,'bgm:400602','葬送のフリーレン','葬送的芙莉莲','[]','tv','2023-09-29T00:00:00Z',28,'x')`); err != nil {
		t.Fatal(err)
	}
	_, _ = s.Meta.W.ExecContext(ctx, `INSERT INTO work_ids VALUES('bgm','400602','bgm:400602'),('bangumi_moe','tag1','bgm:400602')`)
	known, unknown := "ae5e267603990c2e6205f8e43df4c9eb368bbeba", "be5e267603990c2e6205f8e43df4c9eb368bbeba"
	for i, ih := range []string{known, unknown} {
		if err := s.Meta.Match(ctx, meta.TorrentInput{Infohash: ih, Title: "[LoliHouse] Sousou no Frieren - 28 [1080p]", Source: "bangumi.moe", SourceKind: "bangumi_moe",
			SourceID: "moe" + ih[:2], Trusted: true, MoeTagIDs: []string{"tag1"}, PublishedAt: "2026-10-18T10:00:0" + string(rune('0'+i)) + "Z",
			Files: []meta.FileInput{{Index: 0, Path: "[LoliHouse] Sousou no Frieren - 28 [1080p].mkv", Size: 100}}}); err != nil {
			t.Fatal(err)
		}
	}
	seven, two := 7, 2
	if err := s.Meta.PutSwarmInitial(ctx, known, &seven, &two, nil); err != nil {
		t.Fatal(err)
	}

	type sw struct {
		BTSeeders *int    `json:"bt_seeders"`
		WTSeeders *int    `json:"wt_seeders"`
		CheckedAt *string `json:"checked_at"`
	}
	var ep struct {
		Items []struct {
			Infohash string `json:"infohash"`
			Swarm    *sw    `json:"swarm"`
		} `json:"items"`
	}
	if code := getJSON(t, h, "/api/v1/subjects/bgm/400602/episodes/28", &ep); code != 200 || len(ep.Items) != 2 {
		t.Fatalf("episode %d %+v", code, ep)
	}
	// neutral order (newest first); stats attached, unknown = null
	if ep.Items[0].Infohash != unknown || ep.Items[0].Swarm != nil || ep.Items[1].Swarm == nil || *ep.Items[1].Swarm.BTSeeders != 7 || ep.Items[1].Swarm.WTSeeders != nil || ep.Items[1].Swarm.CheckedAt == nil {
		t.Fatalf("items %+v %+v", ep.Items[0], ep.Items[1].Swarm)
	}
	var sub struct {
		Torrents []struct {
			Infohash string `json:"infohash"`
			Seeders  int    `json:"seeders"`
			Swarm    *sw    `json:"swarm"`
		} `json:"torrents"`
	}
	getJSON(t, h, "/api/v1/subjects/bgm/400602", &sub)
	for _, tr := range sub.Torrents {
		if tr.Infohash == known && (tr.Swarm == nil || tr.Seeders != 7) {
			t.Fatalf("subject torrent %+v", tr)
		}
	}
	var st struct {
		Swarm *sw `json:"swarm"`
	}
	getJSON(t, h, "/api/v1/torrents/hash/"+known, &st)
	if st.Swarm == nil || *st.Swarm.BTSeeders != 7 {
		t.Fatalf("status %+v", st)
	}

	var site struct {
		RankRules struct {
			Availability struct {
				Dead int `json:"dead"`
			} `json:"availability"`
			Keywords []json.RawMessage `json:"keywords"`
		} `json:"rank_rules"`
	}
	getJSON(t, h, "/api/v1/site", &site)
	if site.RankRules.Availability.Dead != -100 || len(site.RankRules.Keywords) != 7 {
		t.Fatalf("site rank_rules %+v", site.RankRules)
	}
}

// Whether our qB has a torrent is never exposed to users: no server_complete /
// server_progress in any response, and a torrent known only from our server (never
// scraped) has swarm null.
func TestSwarmNeverRevealsServerCache(t *testing.T) {
	s, h := testServer(t)
	ctx := context.Background()
	if _, err := s.Meta.W.ExecContext(ctx, `INSERT INTO works(id,key,title,title_cn,aliases,type,begin_at,eps,updated_at) VALUES
		(7,'bgm:400602','葬送のフリーレン','葬送的芙莉莲','[]','tv','2023-09-29T00:00:00Z',28,'x')`); err != nil {
		t.Fatal(err)
	}
	_, _ = s.Meta.W.ExecContext(ctx, `INSERT INTO work_ids VALUES('bgm','400602','bgm:400602'),('bangumi_moe','tag1','bgm:400602')`)
	scraped, cachedOnly := "ae5e267603990c2e6205f8e43df4c9eb368bbeba", "be5e267603990c2e6205f8e43df4c9eb368bbeba"
	for _, ih := range []string{scraped, cachedOnly} {
		if err := s.Meta.Match(ctx, meta.TorrentInput{Infohash: ih, Title: "[LoliHouse] Sousou no Frieren - 28 [1080p]", Source: "bangumi.moe", SourceKind: "bangumi_moe",
			SourceID: "moe" + ih[:2], Trusted: true, MoeTagIDs: []string{"tag1"}, PublishedAt: "2026-10-18T10:00:00Z",
			Files: []meta.FileInput{{Index: 0, Path: "[LoliHouse] Sousou no Frieren - 28 [1080p].mkv", Size: 100}}}); err != nil {
			t.Fatal(err)
		}
	}
	one := 1
	if err := s.Meta.PutSwarmScrape(ctx, []meta.SwarmStats{
		{Infohash: scraped, BTSeeders: &one, BTLeechers: &one, WTSeeders: &one, WTLeechers: &one, ServerComplete: true, ServerProgress: 1, ScrapedAt: store.Now()},
		{Infohash: cachedOnly, ServerComplete: true, ServerProgress: 1, ScrapedAt: store.Now()},
	}); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{
		"/api/v1/subjects/bgm/400602/episodes/28",
		"/api/v1/subjects/bgm/400602",
		"/api/v1/search/Frieren",
		"/api/v1/torrents/hash/" + scraped,
		"/api/v1/torrents/hash/" + cachedOnly,
		"/meta/v1/subjects/bgm/400602/episodes/28/files",
		"/meta/v1/torrents/hash/" + scraped,
	} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", p, nil))
		body := rec.Body.String()
		if rec.Code != 200 || strings.Contains(body, "server_complete") || strings.Contains(body, "server_progress") || strings.Contains(body, "服务器") {
			t.Errorf("%s: %d %s", p, rec.Code, body)
		}
	}
	var st struct {
		Swarm *struct{} `json:"swarm"`
	}
	getJSON(t, h, "/api/v1/torrents/hash/"+cachedOnly, &st)
	if st.Swarm != nil {
		t.Fatal("cached-only torrent must have swarm null")
	}
	var play map[string]any
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/v1/torrents/hash/"+scraped+"/play", nil)
	req.Header.Set("X-Requested-With", "fetch")
	h.ServeHTTP(rec, req)
	_ = json.Unmarshal(rec.Body.Bytes(), &play)
	if rec.Code != 200 || strings.Contains(rec.Body.String(), "server_complete") {
		t.Fatalf("play %d %s", rec.Code, rec.Body.String())
	}
}
