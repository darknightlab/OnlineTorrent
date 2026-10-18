package api

import (
	"context"
	"strings"
	"testing"

	"github.com/darknightlab/onlinetorrent/internal/meta"
)

func TestEpisodeList(t *testing.T) {
	mapped := []meta.EpisodeCount{
		{Key: meta.EpisodeKey{Kind: meta.KeyMain, Number: 0}, Files: 1},
		{Key: meta.EpisodeKey{Kind: meta.KeyMain, Number: 2}, Files: 3},
		{Key: meta.EpisodeKey{Kind: meta.KeyMain, Number: 4}, Files: 1}, // beyond the aired estimate
		{Key: meta.EpisodeKey{Kind: meta.KeyMain, Number: 2.5}, Files: 1},
		{Key: meta.EpisodeKey{Kind: meta.KeyNCOP, Number: 1}, Files: 2},
		{Key: meta.EpisodeKey{Kind: meta.KeySP, Number: 1}, Files: 1},
	}
	items := episodeList(3, mapped)
	var keys []string
	for _, it := range items {
		keys = append(keys, it.Key)
	}
	if strings.Join(keys, ",") != "0,1,2,2.5,3,4,sp1,ncop1" {
		t.Fatalf("keys: %v", keys)
	}
	if items[0].Sources != 1 || items[1].Sources != 0 || items[2].Sources != 3 || items[7].Sources != 2 {
		t.Errorf("sources: %+v", items)
	}
	if !*items[1].Aired || *items[0].Aired || *items[5].Aired || items[6].Aired != nil {
		t.Errorf("aired: %+v %+v %+v", items[0], items[1], items[5])
	}
	if items[3].Label != "第 2.5 话" || items[6].Label != "SP1" || items[7].Kind != "ncop" {
		t.Errorf("labels: %+v", items)
	}
	if got := episodeList(0, nil); len(got) != 0 {
		t.Errorf("empty: %+v", got)
	}
}

func TestEpisodesEndpoints(t *testing.T) {
	s, h := testServer(t)
	ctx := context.Background()
	if _, err := s.Meta.W.ExecContext(ctx, `INSERT INTO works(id,key,title,title_cn,type,begin_at,summary,updated_at) VALUES
		(7,'bgm:400602','葬送のフリーレン','葬送的芙莉莲','tv','2099-09-29T00:00:00Z','x','x')`); err != nil {
		t.Fatal(err)
	}
	_, _ = s.Meta.W.ExecContext(ctx, `INSERT INTO work_ids VALUES('bgm','400602','bgm:400602'),('bangumi_moe','tag1','bgm:400602')`)
	ih := "ae5e267603990c2e6205f8e43df4c9eb368bbeba"
	if err := s.Meta.Match(ctx, meta.TorrentInput{Infohash: ih, Title: "[Team] Sousou no Frieren [BD]", Source: "bangumi.moe", SourceKind: "bangumi_moe",
		Trusted: true, MoeTagIDs: []string{"tag1"}, PublishedAt: "2026-10-18T10:00:00Z",
		Files: []meta.FileInput{
			{Index: 0, Path: "Frieren/[Team] Sousou no Frieren - 02 [1080p].mkv", Size: 100},
			{Index: 1, Path: "Frieren/[Team] Sousou no Frieren - 02.5 [1080p].mkv", Size: 100},
			{Index: 2, Path: "Frieren/SPs/[Team] Sousou no Frieren - NCOP1 [1080p].mkv", Size: 10},
			{Index: 3, Path: "Frieren/SPs/[Team] Sousou no Frieren - SP01 [1080p].mkv", Size: 10},
		}}); err != nil {
		t.Fatal(err)
	}

	var list struct {
		Items []episodeItem `json:"items"`
	}
	if code := getJSON(t, h, "/api/v1/subjects/id/7/episodes", &list); code != 200 {
		t.Fatalf("episodes: %d", code)
	}
	var keys []string
	for _, it := range list.Items {
		keys = append(keys, it.Key)
	}
	if strings.Join(keys, ",") != "1,2,2.5,sp1,ncop1" || list.Items[0].Sources != 0 || list.Items[1].Sources != 1 {
		t.Errorf("episodes: %+v", list.Items)
	}

	var ep struct {
		Key     string   `json:"key"`
		Kind    string   `json:"kind"`
		Label   string   `json:"label"`
		Number  float64  `json:"number"`
		Episode *float64 `json:"episode"`
		Items   []struct {
			FileIndex int `json:"file_index"`
		} `json:"items"`
	}
	if code := getJSON(t, h, "/api/v1/subjects/bgm/400602/episodes/NCOP", &ep); code != 200 || ep.Key != "ncop1" || ep.Kind != "ncop" || ep.Label != "NCOP1" ||
		ep.Episode != nil || len(ep.Items) != 1 || ep.Items[0].FileIndex != 2 {
		t.Errorf("ncop: %d %+v", code, ep)
	}
	ep.Episode = nil
	if code := getJSON(t, h, "/api/v1/subjects/id/7/episodes/2.5", &ep); code != 200 || ep.Key != "2.5" || ep.Label != "第 2.5 话" || ep.Episode == nil || *ep.Episode != 2.5 || len(ep.Items) != 1 {
		t.Errorf("2.5: %d %+v", code, ep)
	}
	for _, bad := range []string{"op1", "-1", "x", "sp1000"} {
		if code := getJSON(t, h, "/api/v1/subjects/id/7/episodes/"+bad, nil); code != 400 {
			t.Errorf("%s: %d", bad, code)
		}
	}

	var st struct {
		Files []struct {
			Index   int
			Kind    string
			Episode *float64
			Key     string `json:"episode_key"`
		}
	}
	if code := getJSON(t, h, "/api/v1/torrents/hash/"+ih, &st); code != 200 || len(st.Files) != 4 {
		t.Fatalf("status: %d %+v", code, st)
	}
	if st.Files[0].Key != "2" || *st.Files[0].Episode != 2 || st.Files[2].Key != "ncop1" || st.Files[2].Episode != nil || st.Files[3].Key != "sp1" {
		t.Errorf("status files: %+v", st.Files)
	}

	tm, _ := s.Meta.GetTorrentMap(ctx, ih)
	if m := playMetaOf(tm); m == nil || m.Key != "2" || m.Label != "第 2 话" || m.Episode == nil || m.FileIndex != 0 {
		t.Errorf("play meta: %+v", m)
	}
	tm.Files = tm.Files[2:] // only specials
	if m := playMetaOf(tm); m == nil || m.Key != "ncop1" || m.Label != "NCOP1" || m.Episode != nil || m.FileIndex != 2 {
		t.Errorf("play meta specials: %+v", m)
	}
}
