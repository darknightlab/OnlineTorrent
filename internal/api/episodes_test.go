package api

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/darknightlab/onlinetorrent/internal/catalog"
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
	items := episodeList(3, nil, mapped, "2026-10-18", nil)
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
	// episodes with resources count as aired even beyond the schedule estimate (0, 4)
	if !*items[1].Aired || !*items[0].Aired || !*items[5].Aired || items[6].Aired != nil {
		t.Errorf("aired: %+v %+v %+v", items[0], items[1], items[5])
	}
	if items[3].Label != "第 2.5 话" || items[6].Label != "SP1" || items[7].Kind != "ncop" {
		t.Errorf("labels: %+v", items)
	}
	if got := episodeList(0, nil, nil, "2026-10-18", nil); len(got) != 0 {
		t.Errorf("empty: %+v", got)
	}
	// official specials keep bgm_ep_id and bgm title (falling back to file title when empty);
	// unlisted specials use the file-derived title and keep bgm_ep_id = 0
	offSp := []meta.OfficialEpisode{
		{Key: meta.EpisodeKey{Kind: meta.KeySP, Number: 1}, BgmID: 901, NameCN: "官方SP1"},
		{Key: meta.EpisodeKey{Kind: meta.KeySP, Number: 2}, BgmID: 902},
	}
	mapSp := []meta.EpisodeCount{
		{Key: meta.EpisodeKey{Kind: meta.KeySP, Number: 1}, Files: 1, Title: "File SP1"},
		{Key: meta.EpisodeKey{Kind: meta.KeySP, Number: 2}, Files: 1, Title: "File SP2"},
		{Key: meta.EpisodeKey{Kind: meta.KeySP, Number: 5}, Files: 1, Title: "Machikado Mazoku Mini 05"},
	}
	spItems := episodeList(0, offSp, mapSp, "2026-10-18", nil)
	if len(spItems) != 3 || !spItems[0].Official || spItems[0].BgmEpID != 901 || spItems[0].Title != "官方SP1" ||
		!spItems[1].Official || spItems[1].BgmEpID != 902 || spItems[1].Title != "File SP2" ||
		spItems[2].Official || spItems[2].BgmEpID != 0 || spItems[2].Title != "Machikado Mazoku Mini 05" {
		t.Errorf("special titles/bgm_ep_id: %+v", spItems)
	}
}

func TestEpisodesEndpoints(t *testing.T) {
	s, h := testServer(t)
	ctx := context.Background()
	if _, err := s.Meta.W.ExecContext(ctx, `INSERT INTO works(id,key,title,title_cn,type,begin_at,summary,eps,updated_at) VALUES
		(7,'bgm:400602','葬送のフリーレン','葬送的芙莉莲','tv','2099-09-29T00:00:00Z','x',28,'x')`); err != nil {
		t.Fatal(err)
	}
	_, _ = s.Meta.W.ExecContext(ctx, `INSERT INTO work_ids VALUES('bgm','400602','bgm:400602'),('bangumi_moe','tag1','bgm:400602')`)
	ih := "ae5e267603990c2e6205f8e43df4c9eb368bbeba"
	if err := s.Meta.Match(ctx, meta.TorrentInput{Infohash: ih, Title: "[Team] Sousou no Frieren [BD]", Source: "bangumi.moe", SourceKind: "bangumi_moe",
		Trusted: true, MoeTagIDs: []string{"tag1"}, PublishedAt: "2099-10-18T10:00:00Z",
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
	if m := playMetaOf(tm, -1); m == nil || m.Key != "2" || m.Label != "第 2 话" || m.Episode == nil || m.FileIndex != 0 {
		t.Errorf("play meta: %+v", m)
	}
	// the file being played (its own subject and episode)
	if m := playMetaOf(tm, 3); m == nil || m.Key != "sp1" || m.FileIndex != 3 {
		t.Errorf("play meta file 3: %+v", m)
	}
	// a per-file subject: another work on one file of the torrent
	tm2 := *tm
	tm2.Files = append([]meta.FileMap(nil), tm.Files...)
	other := &meta.WorkRef{ID: 99, Key: "bgm:99", Title: "S2"}
	tm2.Files[3].Work = other
	if m := playMetaOf(&tm2, 3); m == nil || m.SubjectID != 99 || m.SubjectKey != "bgm:99" {
		t.Errorf("play meta per-file subject: %+v", m)
	}
	tm.Files = tm.Files[2:] // only specials
	if m := playMetaOf(tm, -1); m == nil || m.Key != "ncop1" || m.Label != "NCOP1" || m.Episode != nil || m.FileIndex != 2 {
		t.Errorf("play meta specials: %+v", m)
	}
}

func TestEpisodeListOfficial(t *testing.T) {
	official := []meta.OfficialEpisode{
		{Key: meta.EpisodeKey{Kind: meta.KeyMain, Number: 13}, NameCN: "第十三集", Name: "十三", Airdate: "2026-10-04"},
		{Key: meta.EpisodeKey{Kind: meta.KeyMain, Number: 14}, Name: "十四", Airdate: "2026-10-11"},
		{Key: meta.EpisodeKey{Kind: meta.KeyMain, Number: 15}, Airdate: "2026-10-25"},
		{Key: meta.EpisodeKey{Kind: meta.KeySP, Number: 1}, Name: "特別編"},
	}
	mapped := []meta.EpisodeCount{{Key: meta.EpisodeKey{Kind: meta.KeyMain, Number: 13}, Files: 2}}
	items := episodeList(2, official, mapped, "2026-10-18", nil)
	var keys []string
	for _, it := range items {
		keys = append(keys, it.Key)
	}
	// official list replaces the 1..aired range
	if strings.Join(keys, ",") != "13,14,15,sp1" {
		t.Fatalf("keys %v", keys)
	}
	if items[0].Title != "第十三集" || items[0].TitleJA != "十三" || items[1].Title != "十四" || items[1].TitleJA != "" || !items[0].Official || items[0].Sources != 2 {
		t.Errorf("titles %+v %+v", items[0], items[1])
	}
	if !*items[1].Aired || *items[2].Aired || items[3].Aired != nil {
		t.Errorf("aired %+v", items)
	}
}

// An official episode dated today is listed as aired only after its broadcast time: the
// schedule's last/next broadcast decides; without a schedule entry the date alone does.
func TestEpisodeAiredToday(t *testing.T) {
	jst := time.FixedZone("JST", 9*3600)
	official := []meta.OfficialEpisode{
		{Key: meta.EpisodeKey{Kind: meta.KeyMain, Number: 2}, Airdate: "2026-10-13"},
		{Key: meta.EpisodeKey{Kind: meta.KeyMain, Number: 3}, Airdate: "2026-10-20"},
	}
	aired := func(items []episodeItem, n float64) bool {
		for _, it := range items {
			if it.Kind == meta.KeyMain && it.Number == n {
				return it.Aired != nil && *it.Aired
			}
		}
		t.Fatalf("episode %v missing", n)
		return false
	}
	str := func(s string) *string { return &s }
	// 02:00 JST on air day, broadcast at 21:25 JST → next broadcast is today → not aired yet
	before := &catalog.ScheduleItem{LastAirAt: str("2026-10-13T12:25:00Z"), NextAirAt: str("2026-10-20T12:25:00Z")}
	items := episodeList(2, official, nil, "2026-10-20", airedToday(before, "2026-10-20", jst))
	if !aired(items, 2) || aired(items, 3) {
		t.Fatalf("before broadcast: ep2 aired=%v ep3 aired=%v", aired(items, 2), aired(items, 3))
	}
	// 22:00 JST: the last broadcast was today → aired
	after := &catalog.ScheduleItem{LastAirAt: str("2026-10-20T12:25:00Z"), NextAirAt: str("2026-10-27T12:25:00Z")}
	if items = episodeList(3, official, nil, "2026-10-20", airedToday(after, "2026-10-20", jst)); !aired(items, 3) {
		t.Fatal("after broadcast: ep3 should be aired")
	}
	// no schedule entry: date decides
	if items = episodeList(2, official, nil, "2026-10-20", airedToday(nil, "2026-10-20", jst)); !aired(items, 3) {
		t.Fatal("unknown broadcast time: dated today counts as aired")
	}
}
