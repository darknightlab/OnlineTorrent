package meta

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func buildDump(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, name := range []string{"subject.jsonlines", "person.jsonlines", "episode.jsonlines", "subject-relations.jsonlines"} {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write([]byte(files[name]))
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func jl(rows ...any) string {
	var b strings.Builder
	for _, r := range rows {
		j, _ := json.Marshal(r)
		b.Write(j)
		b.WriteByte('\n')
	}
	return b.String()
}

func TestSyncArchive(t *testing.T) {
	infobox := "{{Infobox animanga/TVAnime\r\n|中文名= 测试动画\r\n|别名={\r\n[Test Anime]\r\n[テスト]\r\n}\r\n|话数= 12\r\n|放送开始= 2026年10月4日\r\n|官方网站= https://example.jp/\r\n}}"
	dump := buildDump(t, map[string]string{
		"subject.jsonlines": jl(
			map[string]any{"id": 100, "type": 2, "name": "テストアニメ", "name_cn": "测试动画", "infobox": infobox, "platform": 1, "summary": "简介", "nsfw": false, "date": "2026-10-04", "score": 7.5, "rank": 321,
				"tags": []map[string]any{{"name": "原创", "count": 50}}, "meta_tags": []string{"TV", "原创"}},
			map[string]any{"id": 101, "type": 2, "name": "続編", "name_cn": "续篇", "infobox": "", "platform": 1, "nsfw": true, "date": "2027-01-10"},
			map[string]any{"id": 200, "type": 1, "name": "漫画", "name_cn": "漫画"}, // not anime
		),
		"episode.jsonlines": jl(
			map[string]any{"id": 1, "subject_id": 100, "type": 0, "sort": 1, "name": "始まり", "name_cn": "开始", "airdate": "2026-10-04"},
			map[string]any{"id": 2, "subject_id": 100, "type": 0, "sort": 2, "name": "", "airdate": "2099-01-01"},
			map[string]any{"id": 3, "subject_id": 100, "type": 1, "sort": 1, "name": "特別編"},
			map[string]any{"id": 4, "subject_id": 100, "type": 2, "sort": 1, "name": "OP"},
			map[string]any{"id": 5, "subject_id": 101, "type": 0, "sort": 13, "airdate": "2027-01-10"},
			map[string]any{"id": 6, "subject_id": 101, "type": 0, "sort": 14, "airdate": "2027-01-17"},
			map[string]any{"id": 9, "subject_id": 200, "type": 0, "sort": 1}, // not anime
		),
		"subject-relations.jsonlines": jl(
			map[string]any{"subject_id": 100, "relation_type": 3, "related_subject_id": 101, "order": 0},
			map[string]any{"subject_id": 100, "relation_type": 1, "related_subject_id": 200, "order": 0}, // manga: dropped
		),
	})
	mux := http.NewServeMux()
	var srvURL string
	mux.HandleFunc("/latest.json", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"browser_download_url": srvURL + "/dump.zip", "name": "dump-test.zip", "size": len(dump), "digest": "sha256:x", "created_at": "2026-10-13T21:03:54Z"})
	})
	mux.HandleFunc("/dump.zip", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Range") == "" || strings.Contains(r.Header.Get("Range"), "=-") {
			http.Error(w, "range required", 501) // like GitHub's CDN: no suffix ranges
			return
		}
		http.ServeContent(w, r, "dump.zip", time.Time{}, bytes.NewReader(dump))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	srvURL = srv.URL
	archiveLatestURL, archiveURLPrefix = srv.URL+"/latest.json", srv.URL+"/"

	s, err := Open(filepath.Join(t.TempDir(), "meta.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	// an existing bangumi-data row for the same subject keeps its title / schedule fields
	if _, err := s.W.Exec(`INSERT INTO works(id,key,title,title_cn,aliases,bd_aliases,type,begin_at,broadcast,source,updated_at) VALUES(7,'bgm:100','テストアニメ(bd)','','["bd alias"]','["bd alias"]','tv','2026-10-04T15:00:00Z','R/2026-10-04T15:00:00Z/P7D','bangumi-data','x')`); err != nil {
		t.Fatal(err)
	}

	st, skipped, err := s.SyncArchive(ctx, srv.Client(), "test", false)
	if err != nil || skipped {
		t.Fatalf("sync: %v skipped=%v", err, skipped)
	}
	if st.Subjects != 2 || st.Episodes != 6 || st.Relations != 1 || st.Dump != "dump-test.zip" {
		t.Fatalf("status %+v", st)
	}
	w, err := s.GetWork(ctx, "bgm:100")
	if err != nil {
		t.Fatal(err)
	}
	if w.ID != 7 || w.Title != "テストアニメ(bd)" || w.TitleCN != "测试动画" || w.Summary != "简介" || w.Eps != 12 || w.Rank != 321 || w.Score != 7.5 || w.Broadcast == "" {
		t.Errorf("merged work %+v", w)
	}
	al := strings.Join(w.Aliases, "|")
	if !strings.Contains(al, "bd alias") || !strings.Contains(al, "Test Anime") || !strings.Contains(al, "テスト") {
		t.Errorf("aliases %q", al)
	}
	if len(w.Tags) != 1 || len(w.MetaTags) != 2 || w.NSFW {
		t.Errorf("tags %v meta %v nsfw %v", w.Tags, w.MetaTags, w.NSFW)
	}
	w2, err := s.GetWork(ctx, "bgm:101")
	if err != nil || !w2.NSFW || w2.TitleCN != "续篇" || w2.ID <= 7 || w2.Type != "tv" {
		t.Fatalf("new work %+v %v", w2, err)
	}
	if ws, _ := s.SearchWorks(ctx, "Test Anime", 10); len(ws) != 1 {
		t.Errorf("alias search found %d", len(ws))
	}

	eps, err := s.OfficialEpisodes(ctx, "bgm:100")
	if err != nil || len(eps) != 4 {
		t.Fatalf("official %v %v", eps, err)
	}
	if eps[0].Key.String() != "1" || eps[0].NameCN != "开始" || eps[2].Key.String() != "sp1" || eps[3].Key.String() != "ncop1" {
		t.Errorf("keys %+v", eps)
	}
	now := time.Date(2026, 10, 18, 0, 0, 0, 0, time.UTC)
	if n, ok := s.AiredOfficial(ctx, "bgm:100", now); !ok || n != 1 {
		t.Errorf("aired %d %v", n, ok)
	}

	// split-cour numbering: files "01" map to official sort 13
	if k := s.CanonicalKey(ctx, "bgm:101", EpisodeKey{Kind: KeyMain, Number: 1}); k.Number != 13 {
		t.Errorf("canonical %v", k)
	}
	if k := s.CanonicalKey(ctx, "bgm:101", EpisodeKey{Kind: KeyMain, Number: 14}); k.Number != 14 {
		t.Errorf("canonical abs %v", k)
	}
	if nums := s.fileNumbers(ctx, "bgm:101", EpisodeKey{Kind: KeyMain, Number: 13}); len(nums) != 2 || nums[1] != 1 {
		t.Errorf("file numbers %v", nums)
	}

	// unchanged digest → skipped; force → re-import (idempotent, ids stable)
	if _, skipped, err := s.SyncArchive(ctx, srv.Client(), "test", false); err != nil || !skipped {
		t.Errorf("second sync skipped=%v err=%v", skipped, err)
	}
	if _, _, err := s.SyncArchive(ctx, srv.Client(), "test", true); err != nil {
		t.Fatal(err)
	}
	if w3, _ := s.GetWork(ctx, "bgm:101"); w3 == nil || w3.ID != w2.ID {
		t.Errorf("id changed on re-import")
	}
	if got := s.ArchiveState(ctx); got.Digest != "sha256:x" || got.Error != "" || got.Running {
		t.Errorf("state %+v", got)
	}
}

func TestArchiveDate(t *testing.T) {
	for in, want := range map[string]string{
		"2008-04-06": "2008-04-05T15:00:00Z",
		"2008年4月6日":  "2008-04-05T15:00:00Z",
		"2008年4月":    "2008-03-31T15:00:00Z",
		"":           "",
		"不明":         "",
		"1890-01-01": "",
	} {
		if got := archiveDate(in); got != want {
			t.Errorf("%q → %q want %q", in, got, want)
		}
	}
}
