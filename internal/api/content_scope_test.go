package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/darknightlab/onlinetorrent/internal/catalog"
	"github.com/darknightlab/onlinetorrent/internal/meta"
	"github.com/darknightlab/onlinetorrent/internal/store"
)

func scopeReq(t *testing.T, h http.Handler, method, path, cookie, body string, out any) int {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("X-Requested-With", "fetch")
	if cookie != "" {
		req.AddCookie(&http.Cookie{Name: sessionCookie, Value: cookie})
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if out != nil && rec.Code == 200 {
		if err := json.Unmarshal(rec.Body.Bytes(), out); err != nil {
			t.Fatalf("%s: %v %s", path, err, rec.Body.String())
		}
	}
	return rec.Code
}

func TestContentScopeEndpoints(t *testing.T) {
	s, h := testServer(t)
	ctx := context.Background()
	// 7: hidden by the guest expression (no 原创); 8: visible; 9: 原创 but excluded (里番)
	if _, err := s.Meta.W.ExecContext(ctx, `INSERT INTO works(id,key,title,title_cn,type,begin_at,summary,tags,meta_tags,eps,updated_at) VALUES
		(7,'bgm:400602','共通作品 Frieren','葬送的芙莉莲','tv','2023-09-29T00:00:00Z','x','["奇幻","漫画改"]','["TV"]',28,'x'),
		(8,'bgm:800','共通作品 Original','原创番','tv','2023-10-01T00:00:00Z','x','["校园"]','["原创"]',12,'x'),
		(9,'bgm:900','共通作品 Adult','里番','ova','2023-10-01T00:00:00Z','x','["原创","里番"]','[]',2,'x')`); err != nil {
		t.Fatal(err)
	}
	_, _ = s.Meta.W.ExecContext(ctx, `INSERT INTO work_ids VALUES('bgm','400602','bgm:400602'),('bangumi_moe','tag7','bgm:400602'),
		('bgm','800','bgm:800'),('bangumi_moe','tag8','bgm:800')`)
	ih7 := "ae5e267603990c2e6205f8e43df4c9eb368bbeba"
	ih8 := "8d044029c825a9b881e3f6753f7093f8d256a85d"
	for _, tc := range []struct{ ih, tag, name string }{{ih7, "tag7", "Frieren"}, {ih8, "tag8", "Original"}} {
		if err := s.Meta.Match(ctx, meta.TorrentInput{Infohash: tc.ih, Title: "[TeamX] " + tc.name + " - 02 [1080p]", Source: "bangumi.moe", SourceKind: "bangumi_moe",
			SourceID: "moe-" + tc.tag, Trusted: true, MoeTagIDs: []string{tc.tag}, Team: "TeamX", PublishedAt: "2026-10-18T10:00:00Z",
			Files: []meta.FileInput{{Index: 0, Path: "[TeamX] " + tc.name + " - 02 [1080p].mkv", Size: 100}}}); err != nil {
			t.Fatal(err)
		}
	}
	w7, _ := s.Meta.GetWork(ctx, "bgm:400602")
	w8, _ := s.Meta.GetWork(ctx, "bgm:800")
	var items []*catalog.ScheduleItem
	for _, w := range []*meta.Work{w7, w8} {
		items = append(items, &catalog.ScheduleItem{Key: w.Key, SubjectID: w.ID, Title: w.Title, ScopeTags: w.Tags, ScopeMetaTags: w.MetaTags})
	}
	s.Cat.SetSchedule(items)

	admin, err := s.St.CreateUser(ctx, store.CreateUserParams{Username: "boss", Password: "devpass-123", Role: store.RoleAdmin})
	if err != nil {
		t.Fatal(err)
	}
	adminTok, err := s.St.CreateSession(ctx, admin.ID, time.Hour, "", "")
	if err != nil {
		t.Fatal(err)
	}

	// ---- admin API ----
	var list struct {
		Roles []string             `json:"roles"`
		Items []store.ContentScope `json:"items"`
	}
	if code := scopeReq(t, h, "GET", "/api/v1/admin/content-scopes", adminTok, "", &list); code != 200 || len(list.Items) != 3 || list.Items[0].Role != "guest" || list.Items[0].TagMode != "all" {
		t.Fatalf("list: %d %+v", code, list)
	}
	if code := scopeReq(t, h, "GET", "/api/v1/admin/content-scopes", "", "", nil); code != 401 {
		t.Errorf("list as guest: %d", code)
	}
	put := func(role, body string) int {
		return scopeReq(t, h, "PUT", "/api/v1/admin/content-scopes/role/"+role, adminTok, body, nil)
	}
	long := strings.Repeat("长", 61)
	many := `"` + strings.TrimSuffix(strings.Repeat(`t","`, 51), `","`) + `"`
	for _, c := range []struct {
		role, body string
		want       int
	}{
		{"guest", `{"tag_mode":"maybe"}`, 400},
		{"guest", `{"tag_mode":"selected","tags":[]}`, 400},
		{"guest", `{"tag_mode":"selected","tags":["a","b"],"tag_operators":["xor"]}`, 400},
		{"guest", `{"tag_mode":"selected","tags":["a","b"]}`, 400},
		{"guest", `{"tag_mode":"selected","tags":["` + long + `"]}`, 400},
		{"guest", `{"tag_mode":"all","exclude_tags":[` + many + `]}`, 400},
		{"guest", `{"tag_mode":"all","tag_match":"or"}`, 400}, // unknown field
		{"admin", `{"tag_mode":"all"}`, 400},
		{"owner", `{"tag_mode":"all"}`, 404},
	} {
		if got := put(c.role, c.body); got != c.want {
			t.Errorf("PUT %s %s: %d, want %d", c.role, c.body, got, c.want)
		}
	}
	var tags struct {
		Items []meta.SubjectTag `json:"items"`
	}
	if code := scopeReq(t, h, "GET", "/api/v1/admin/subject-tags?q="+url.QueryEscape("原"), adminTok, "", &tags); code != 200 || len(tags.Items) != 1 || tags.Items[0].Name != "原创" || tags.Items[0].Count != 2 {
		t.Errorf("subject tags: %+v", tags)
	}

	// guest: (原创 AND 校园) OR 原创, minus 里番 → only work 8
	if code := put("guest", `{"tag_mode":"selected","tags":["原创","校园","原创"],"tag_operators":["and","or"],"exclude_tags":["里番"]}`); code != 200 {
		t.Fatalf("put guest: %d", code)
	}
	if audit, _, _ := s.St.ListAudit(ctx, 1, 10); len(audit) == 0 || audit[0].Action != "content_scope.update" || !strings.Contains(audit[0].Detail, "(原创 AND 校园) OR 原创") {
		t.Errorf("audit: %+v", audit)
	}

	// ---- search ----
	type searchOut struct {
		Subjects []struct {
			SubjectID int64 `json:"subject_id"`
		} `json:"subjects"`
		Torrents []struct{ Infohash string } `json:"torrents"`
		Total    int                         `json:"total"`
	}
	var gs, as searchOut
	q := "/api/v1/search/" + url.PathEscape("共通作品")
	if code := scopeReq(t, h, "GET", q, "", "", &gs); code != 200 || len(gs.Subjects) != 1 || gs.Subjects[0].SubjectID != 8 {
		t.Errorf("guest subjects: %d %+v", code, gs)
	}
	if code := scopeReq(t, h, "GET", q, adminTok, "", &as); code != 200 || len(as.Subjects) != 3 {
		t.Errorf("admin subjects: %d %+v", code, as)
	}
	gs, as = searchOut{}, searchOut{}
	scopeReq(t, h, "GET", "/api/v1/search/TeamX", "", "", &gs)
	scopeReq(t, h, "GET", "/api/v1/search/TeamX", adminTok, "", &as)
	if len(gs.Torrents) != 1 || gs.Torrents[0].Infohash != ih8 || gs.Total != 1 || len(as.Torrents) != 2 || as.Total != 2 {
		t.Errorf("torrents: guest %+v admin %+v", gs, as)
	}

	// ---- schedule ----
	var sched struct {
		Items []map[string]any `json:"items"`
	}
	scopeReq(t, h, "GET", "/api/v1/schedule", "", "", &sched)
	if len(sched.Items) != 1 || sched.Items[0]["subject_id"] != float64(8) {
		t.Errorf("guest schedule: %+v", sched.Items)
	}
	if _, leaked := sched.Items[0]["ScopeTags"]; leaked {
		t.Error("scope fields serialized")
	}
	scopeReq(t, h, "GET", "/api/v1/schedule", adminTok, "", &sched)
	if len(sched.Items) != 2 {
		t.Errorf("admin schedule: %+v", sched.Items)
	}

	// ---- subject pages: 404 like a missing subject ----
	for _, p := range []string{"/api/v1/subjects/id/7", "/api/v1/subjects/bgm/400602", "/api/v1/subjects/id/7/episodes", "/api/v1/subjects/id/7/episodes/2",
		"/api/v1/subjects/id/9", "/meta/v1/subjects/id/7", "/meta/v1/subjects/id/7/episodes"} {
		if code := scopeReq(t, h, "GET", p, "", "", nil); code != 404 {
			t.Errorf("guest %s: %d", p, code)
		}
		if code := scopeReq(t, h, "GET", p, adminTok, "", nil); code != 200 {
			t.Errorf("admin %s: %d", p, code)
		}
	}
	if code := scopeReq(t, h, "GET", "/api/v1/subjects/id/8/episodes/2", "", "", nil); code != 200 {
		t.Errorf("visible episode: %d", code)
	}
	var ms struct {
		Items []struct{ ID int64 } `json:"items"`
	}
	scopeReq(t, h, "GET", "/meta/v1/subjects/search?q="+url.QueryEscape("共通作品"), "", "", &ms)
	if len(ms.Items) != 1 || ms.Items[0].ID != 8 {
		t.Errorf("meta search: %+v", ms)
	}

	// ---- torrent status / play meta / meta map: hidden subject shown as unmapped ----
	type statusOut struct {
		Status string
		Files  []struct {
			Subject *struct {
				SubjectID int64 `json:"subject_id"`
			}
			Episode *float64
			Key     string `json:"episode_key"`
		}
	}
	var st statusOut
	scopeReq(t, h, "GET", "/api/v1/torrents/hash/"+ih7, "", "", &st)
	if st.Status != "ready" || len(st.Files) != 1 || st.Files[0].Subject != nil || st.Files[0].Episode != nil || st.Files[0].Key != "" {
		t.Errorf("guest status: %+v", st)
	}
	st = statusOut{}
	scopeReq(t, h, "GET", "/api/v1/torrents/hash/"+ih7, adminTok, "", &st)
	if len(st.Files) != 1 || st.Files[0].Subject == nil || st.Files[0].Subject.SubjectID != 7 {
		t.Errorf("admin status: %+v", st)
	}
	var play struct {
		Meta *struct {
			SubjectID int64 `json:"subject_id"`
		} `json:"meta"`
	}
	if code := scopeReq(t, h, "POST", "/api/v1/torrents/hash/"+ih7+"/play", "", "", &play); code != 200 || play.Meta != nil {
		t.Errorf("guest play meta: %d %+v", code, play.Meta)
	}
	if code := scopeReq(t, h, "POST", "/api/v1/torrents/hash/"+ih8+"/play", "", "", &play); code != 200 || play.Meta == nil || play.Meta.SubjectID != 8 {
		t.Errorf("guest play visible: %d %+v", code, play.Meta)
	}
	var mm struct {
		Files []struct{ Work *struct{ ID int64 } } `json:"files"`
	}
	scopeReq(t, h, "GET", "/meta/v1/torrents/hash/"+ih7, "", "", &mm)
	if len(mm.Files) != 1 || mm.Files[0].Work != nil {
		t.Errorf("meta map: %+v", mm)
	}

	// back to the default: everything visible again (cache replaced on write)
	if code := put("guest", `{"tag_mode":"all","tags":[],"tag_operators":[],"exclude_tags":[]}`); code != 200 {
		t.Fatalf("reset: %d", code)
	}
	if code := scopeReq(t, h, "GET", "/api/v1/subjects/id/7", "", "", nil); code != 200 {
		t.Errorf("after reset: %d", code)
	}
}
