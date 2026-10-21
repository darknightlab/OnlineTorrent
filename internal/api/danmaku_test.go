package api

import (
	"context"
	"net/http/httptest"
	"slices"
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
