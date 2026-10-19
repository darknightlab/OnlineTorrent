package api

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"testing"

	"github.com/darknightlab/onlinetorrent/internal/catalog"
	"github.com/darknightlab/onlinetorrent/internal/crawl"
	"github.com/darknightlab/onlinetorrent/internal/meta"
	"github.com/darknightlab/onlinetorrent/internal/policy"
	"github.com/darknightlab/onlinetorrent/internal/qb"
	"github.com/darknightlab/onlinetorrent/internal/store"
)

func testServer(t *testing.T) (*Server, http.Handler) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "otd.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	ms, err := meta.Open(filepath.Join(dir, "meta.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(ms.Close)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	pool := qb.NewPool(st, log)
	cat := catalog.New(st, ms, log)
	s := &Server{St: st, Meta: ms, Cat: cat, Pool: pool, Policy: policy.New(st), Log: log, BaseCtx: context.Background(),
		Crawl: crawl.New(st, ms, cat, pool, log)}
	return s, s.Handler()
}

func getJSON(t *testing.T, h http.Handler, path string, out any) int {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
	if out != nil && rec.Code == 200 {
		if err := json.Unmarshal(rec.Body.Bytes(), out); err != nil {
			t.Fatalf("%s: %v %s", path, err, rec.Body.String())
		}
	}
	return rec.Code
}

func TestSearchAndTorrentStatus(t *testing.T) {
	s, h := testServer(t)
	ctx := context.Background()
	if _, err := s.Meta.W.ExecContext(ctx, `INSERT INTO works(id,key,title,title_cn,aliases,type,begin_at,eps,updated_at) VALUES
		(7,'bgm:400602','葬送のフリーレン','葬送的芙莉莲','["Sousou no Frieren"]','tv','2023-09-29T00:00:00Z',28,'x')`); err != nil {
		t.Fatal(err)
	}
	_, _ = s.Meta.W.ExecContext(ctx, `INSERT INTO work_ids VALUES('bgm','400602','bgm:400602'),('bangumi_moe','tag1','bgm:400602')`)
	ih := "ae5e267603990c2e6205f8e43df4c9eb368bbeba"
	if err := s.Meta.Match(ctx, meta.TorrentInput{Infohash: ih, Title: "[LoliHouse] Sousou no Frieren - 28 [1080p]", Source: "bangumi.moe", SourceKind: "bangumi_moe",
		SourceID: "moe1", Trusted: true, MoeTagIDs: []string{"tag1"}, Team: "LoliHouse", PublishedAt: "2026-10-18T10:00:00Z",
		Files: []meta.FileInput{{Index: 0, Path: "[LoliHouse] Sousou no Frieren - 28 [1080p].mkv", Size: 100}}}); err != nil {
		t.Fatal(err)
	}

	var res struct {
		Subjects []struct {
			SubjectID int64  `json:"subject_id"`
			TitleCN   string `json:"title_cn"`
		} `json:"subjects"`
		Torrents []struct {
			ID       string `json:"id"`
			Infohash string `json:"infohash"`
			Source   string `json:"source"`
			Team     *struct{ Name string }
			Access   *struct{ Action string }
		} `json:"torrents"`
		Page      int `json:"page"`
		PageCount int `json:"page_count"`
		Total     int `json:"total"`
	}
	if code := getJSON(t, h, "/api/v1/search/"+url.PathEscape("芙莉莲"), &res); code != 200 {
		t.Fatalf("search: %d", code)
	}
	if len(res.Subjects) != 1 || res.Subjects[0].SubjectID != 7 {
		t.Errorf("subjects: %+v", res.Subjects)
	}
	if code := getJSON(t, h, "/api/v1/search/frieren?page=1", &res); code != 200 || len(res.Torrents) != 1 {
		t.Fatalf("search torrents: %d %+v", code, res)
	}
	tr := res.Torrents[0]
	if tr.Infohash != ih || tr.ID != "moe1" || tr.Source != "bangumi.moe" || tr.Team == nil || tr.Access == nil || tr.Access.Action != "allow" || res.PageCount != 1 {
		t.Errorf("torrent: %+v", res)
	}
	if code := getJSON(t, h, "/api/v1/torrents/search?q=x", nil); code != 404 {
		t.Errorf("old search route still present: %d", code)
	}

	var st struct {
		Status string
		Title  string
		Files  []struct {
			Index   int
			Kind    string
			Episode *float64
			Subject *struct {
				SubjectID int64  `json:"subject_id"`
				BgmID     *int64 `json:"bgm_id"`
			}
		}
	}
	if code := getJSON(t, h, "/api/v1/torrents/hash/"+ih, &st); code != 200 {
		t.Fatalf("status: %d", code)
	}
	if st.Status != "ready" || len(st.Files) != 1 || st.Files[0].Subject == nil || st.Files[0].Subject.SubjectID != 7 ||
		*st.Files[0].Subject.BgmID != 400602 || *st.Files[0].Episode != 28 || st.Files[0].Kind != "episode" {
		t.Errorf("status: %+v", st)
	}
	st.Status = ""
	getJSON(t, h, "/api/v1/torrents/hash/0123456789abcdef0123456789abcdef01234567", &st)
	if st.Status != "unknown" {
		t.Errorf("unknown: %+v", st)
	}
	// queued for metadata → pending
	pih := "8d044029c825a9b881e3f6753f7093f8d256a85d"
	_ = s.Meta.UpsertPending(ctx, meta.TorrentInput{Infohash: pih, Title: "[Raw] X - 01", Source: "动漫花园", SourceKind: "rss"})
	_, _ = s.St.EnqueueMetadataJob(ctx, pih, nil, nil)
	getJSON(t, h, "/api/v1/torrents/hash/"+pih, &st)
	if st.Status != "pending" || st.Title != "[Raw] X - 01" || len(st.Files) != 0 {
		t.Errorf("pending: %+v", st)
	}

	// subject torrents come from the local catalogue
	var sub struct {
		Torrents []struct{ Infohash string } `json:"torrents"`
	}
	_, _ = s.Meta.W.ExecContext(ctx, `UPDATE works SET summary = 'x' WHERE id = 7`) // no bgm.tv fetch in tests
	if code := getJSON(t, h, "/api/v1/subjects/id/7", &sub); code != 200 || len(sub.Torrents) != 1 || sub.Torrents[0].Infohash != ih {
		t.Errorf("subject torrents: %d %+v", code, sub)
	}
	// admin routes are protected
	if code := getJSON(t, h, "/api/v1/admin/sources", nil); code != 401 {
		t.Errorf("admin sources as guest: %d", code)
	}
}
