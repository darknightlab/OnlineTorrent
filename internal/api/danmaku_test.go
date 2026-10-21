package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/darknightlab/onlinetorrent/internal/danmaku"
	"github.com/darknightlab/onlinetorrent/internal/meta"
	"github.com/darknightlab/onlinetorrent/internal/store"
)

const danmakuRoute = "GET /api/v1/subjects/{provider}/{id}/episodes/{n}/danmaku"

func TestDanmakuEndpoint(t *testing.T) {
	s, h := testServer(t)
	ctx := context.Background()
	s.Danmaku = &danmaku.Service{Meta: s.Meta} // no danmu_api: stored danmaku only
	if _, err := s.Meta.W.ExecContext(ctx, `INSERT INTO works(id,key,title,type,updated_at) VALUES(7,'bgm:400602','葬送のフリーレン','tv','x')`); err != nil {
		t.Fatal(err)
	}
	_, _ = s.Meta.W.ExecContext(ctx, `INSERT INTO work_ids VALUES('bgm','400602','bgm:400602')`)
	ep1 := meta.EpisodeKey{Kind: meta.KeyMain, Number: 1}
	if _, err := s.Meta.SaveDanmaku(ctx, "bgm:400602", ep1, "bilibili", "779775", []meta.Comment{{Time: 3, Mode: 1, Color: 0xFFFFFF, Text: "b"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Meta.SaveDanmaku(ctx, "bgm:400602", ep1, "bahamut", "35241", []meta.Comment{{Time: 1, Mode: 5, Color: 255, Text: "a"}}); err != nil {
		t.Fatal(err)
	}
	// a stored set is never replaced; empty sets are refused
	if ok, _ := s.Meta.SaveDanmaku(ctx, "bgm:400602", ep1, "bilibili", "1", []meta.Comment{{Text: "other"}}); ok {
		t.Error("stored set replaced")
	}
	if _, err := s.Meta.SaveDanmaku(ctx, "bgm:400602", meta.EpisodeKey{Kind: meta.KeyMain, Number: 2}, "bilibili", "1", nil); err == nil {
		t.Error("empty set stored")
	}

	var out struct {
		Items []meta.Comment `json:"items"`
	}
	if code := getJSON(t, h, "/api/v1/subjects/bgm/400602/episodes/1/danmaku", &out); code != 200 {
		t.Fatalf("danmaku: %d", code)
	}
	if len(out.Items) != 2 || out.Items[0].Text != "a" || out.Items[1].Text != "b" || out.Items[0].Mode != 5 || out.Items[0].Color != 255 {
		t.Errorf("items: %+v", out.Items)
	}
	// the episode key is canonical ("01" = "1"); other episodes / specials: empty
	if getJSON(t, h, "/api/v1/subjects/id/7/episodes/01/danmaku", &out); len(out.Items) != 2 {
		t.Errorf("01: %+v", out.Items)
	}
	for _, p := range []string{"/api/v1/subjects/id/7/episodes/2/danmaku", "/api/v1/subjects/id/7/episodes/sp1/danmaku"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", p, nil))
		if rec.Code != 200 || rec.Body.String() != `{"items":[]}` || rec.Header().Get("Cache-Control") != "no-store" {
			t.Errorf("%s: %d %q %q", p, rec.Code, rec.Body.String(), rec.Header().Get("Cache-Control"))
		}
	}
	// revalidation
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/api/v1/subjects/id/7/episodes/1/danmaku", nil))
	etag := rec.Header().Get("ETag")
	req := httptest.NewRequest("GET", "/api/v1/subjects/id/7/episodes/1/danmaku", nil)
	req.Header.Set("If-None-Match", etag)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if etag == "" || rec.Code != 304 {
		t.Errorf("etag %q → %d", etag, rec.Code)
	}
	if code := getJSON(t, h, "/api/v1/subjects/id/99/episodes/1/danmaku", nil); code != 404 {
		t.Errorf("missing subject: %d", code)
	}
}

func TestDanmakuPermission(t *testing.T) {
	s, h := testServer(t)
	ctx := context.Background()
	// listed in the admin permission list, configurable, guests allowed by default
	s.perms.mu.RLock()
	spec := s.perms.byKey[danmakuRoute]
	s.perms.mu.RUnlock()
	if spec == nil || spec.Access != AccessConfigurable || !slices.Equal(spec.DefaultRoles, []string{store.RoleGuest}) {
		t.Fatalf("spec: %+v", spec)
	}
	if !slices.Contains(s.permissionsFor(store.RoleGuest), danmakuRoute) {
		t.Error("guests may not read danmaku by default")
	}
	if _, err := s.Meta.W.ExecContext(ctx, `INSERT INTO works(id,key,title,type,updated_at) VALUES(7,'bgm:1','x','tv','x')`); err != nil {
		t.Fatal(err)
	}
	if code := getJSON(t, h, "/api/v1/subjects/id/7/episodes/1/danmaku", nil); code != 200 {
		t.Errorf("guest: %d", code)
	}
	if err := s.St.SetRouteRoles(ctx, danmakuRoute, []string{store.RoleMember}); err != nil {
		t.Fatal(err)
	}
	if err := s.loadPerms(ctx); err != nil {
		t.Fatal(err)
	}
	if code := getJSON(t, h, "/api/v1/subjects/id/7/episodes/1/danmaku", nil); code != 401 {
		t.Errorf("guest after revoking: %d", code)
	}
}

func postDanmakuReq(h http.Handler, path, cookie, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("POST", path, strings.NewReader(body))
	req.Header.Set("X-Requested-With", "fetch")
	req.Header.Set("Content-Type", "application/json")
	if cookie != "" {
		req.AddCookie(&http.Cookie{Name: sessionCookie, Value: cookie})
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func getDanmakuItems(t *testing.T, h http.Handler, path, cookie string) ([][]any, string) {
	t.Helper()
	rec := doReq(h, "GET", path, cookie)
	if rec.Code != 200 {
		t.Fatalf("GET %s: %d %s", path, rec.Code, rec.Body.String())
	}
	var out struct {
		Items [][]any `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out.Items, rec.Header().Get("ETag")
}

const sendRoute = "POST /api/v1/subjects/{provider}/{id}/episodes/{n}/danmaku"

func TestSendDanmaku(t *testing.T) {
	s, h := testServer(t)
	ctx := context.Background()
	s.Danmaku = &danmaku.Service{Meta: s.Meta} // no danmu_api: stored danmaku only
	if _, err := s.Meta.W.ExecContext(ctx, `INSERT INTO works(id,key,title,type,updated_at) VALUES(7,'bgm:400602','葬送のフリーレン','tv','x')`); err != nil {
		t.Fatal(err)
	}
	for n := 1; n <= 3; n++ {
		_, _ = s.Meta.W.ExecContext(ctx, `INSERT INTO episodes(bgm_id,work_key,type,sort) VALUES(?,?,0,?)`, 100+n, "bgm:400602", n)
	}
	// episode 1 has collected danmaku, episode 2 none
	if _, err := s.Meta.SaveDanmaku(ctx, "bgm:400602", meta.EpisodeKey{Kind: meta.KeyMain, Number: 1}, "bilibili", "1", []meta.Comment{{Time: 5, Mode: 1, Color: 0xFFFFFF, Text: "collected"}}); err != nil {
		t.Fatal(err)
	}
	ep1 := "/api/v1/subjects/id/7/episodes/1/danmaku"
	ep2 := "/api/v1/subjects/id/7/episodes/2/danmaku"

	// permission: login required (AccessUser), members by default, listed for admins
	s.perms.mu.RLock()
	spec := s.perms.byKey[sendRoute]
	s.perms.mu.RUnlock()
	if spec == nil || spec.Access != AccessUser || !slices.Equal(spec.DefaultRoles, []string{store.RoleMember}) {
		t.Fatalf("spec: %+v", spec)
	}
	if rec := postDanmakuReq(h, ep1, "", `{"time":1,"duration":100,"text":"hi"}`); rec.Code != 401 {
		t.Errorf("guest: %d", rec.Code)
	}
	alice := loginAs(t, s, "alice", store.RoleMember)
	bob := loginAs(t, s, "bob", store.RoleMember)

	// validation (nothing is charged to the rate limit)
	for _, body := range []string{
		`{"time":1,"duration":100,"text":"   "}`,
		`{"time":1,"duration":100,"text":"` + strings.Repeat("字", 101) + `"}`,
		`{"time":1,"duration":100,"text":"a\nb"}`,
		`{"time":1,"duration":100,"text":"a\u0007b"}`,
		`{"duration":100,"text":"no time"}`,
		`{"time":-1,"duration":100,"text":"x"}`,
		`{"time":200,"duration":100,"text":"after the end"}`,
		`{"time":1,"duration":0,"text":"no duration"}`,
		`{"time":1,"duration":99999,"text":"absurd duration"}`,
		`{"time":1,"duration":100,"mode":2,"text":"x"}`,
		`{"time":1,"duration":100,"color":16777216,"text":"x"}`,
		`not json`,
	} {
		if rec := postDanmakuReq(h, ep1, alice, body); rec.Code != 400 {
			t.Errorf("%s: %d %s", body, rec.Code, rec.Body.String())
		}
	}
	if rec := postDanmakuReq(h, "/api/v1/subjects/id/7/episodes/9/danmaku", alice, `{"time":1,"duration":100,"text":"x"}`); rec.Code != 404 {
		t.Errorf("unknown episode: %d", rec.Code)
	}

	_, etag0 := getDanmakuItems(t, h, ep1, "")
	rec := postDanmakuReq(h, ep1, alice, `{"time":2.345,"duration":1440.5,"text":"  你好  "}`)
	if rec.Code != 201 || strings.TrimSpace(rec.Body.String()) != `{"item":[2.35,1,16777215,"你好",1]}` {
		t.Fatalf("send: %d %s", rec.Code, rec.Body.String())
	}
	// rate limit: one per 3 s per user (another user isn't affected)
	if rec := postDanmakuReq(h, ep1, alice, `{"time":3,"duration":1440,"text":"again"}`); rec.Code != 429 {
		t.Errorf("second within 3 s: %d", rec.Code)
	}
	if rec := postDanmakuReq(h, ep2, bob, `{"time":3,"duration":1440,"mode":5,"color":255,"text":"顶部"}`); rec.Code != 201 {
		t.Errorf("bob: %d %s", rec.Code, rec.Body.String())
	}

	// merged into GET, sorted by time; the caller's own are marked; fresh (ETag changes)
	items, etag1 := getDanmakuItems(t, h, ep1, alice)
	if len(items) != 2 || items[0][3] != "你好" || len(items[0]) != 5 || items[1][3] != "collected" || len(items[1]) != 4 {
		t.Errorf("alice ep1: %v", items)
	}
	if items, _ := getDanmakuItems(t, h, ep1, bob); len(items) != 2 || len(items[0]) != 4 {
		t.Errorf("bob sees alice's without the own mark: %v", items)
	}
	if guest, etag := getDanmakuItems(t, h, ep1, ""); len(guest) != 2 || etag == etag0 || etag == etag1 {
		t.Errorf("guest: %v etag %q (before %q)", guest, etag, etag0)
	}
	// an episode without collected danmaku still has the sent ones
	if items, _ := getDanmakuItems(t, h, ep2, ""); len(items) != 1 || items[0][1] != float64(5) || items[0][2] != float64(255) {
		t.Errorf("ep2: %v", items)
	}

	// 30 per 10 minutes: drain the window bucket directly, then the per-3 s bucket has room
	carol := loginAs(t, s, "carol", store.RoleMember)
	u, _ := s.St.SessionUser(ctx, carol)
	for i := 0; i < danmakuSendWindowMax; i++ {
		s.limiter.take("danmaku-send-window:"+strconv.FormatInt(u.ID, 10), danmakuSendWindowMax, danmakuSendWindow/danmakuSendWindowMax)
	}
	if rec := postDanmakuReq(h, ep2, carol, `{"time":1,"duration":100,"text":"x"}`); rec.Code != 429 {
		t.Errorf("window exhausted: %d", rec.Code)
	}

	// the permission is editable for member / vip; revoking it → 403
	if err := s.St.SetRouteRoles(ctx, sendRoute, []string{store.RoleVIP}); err != nil {
		t.Fatal(err)
	}
	if err := s.loadPerms(ctx); err != nil {
		t.Fatal(err)
	}
	if rec := postDanmakuReq(h, ep2, bob, `{"time":9,"duration":100,"text":"x"}`); rec.Code != 403 {
		t.Errorf("member after revoking: %d", rec.Code)
	}
}
