package api

import (
	"context"
	"net/url"
	"testing"
	"time"

	"github.com/darknightlab/onlinetorrent/internal/meta"
	"github.com/darknightlab/onlinetorrent/internal/store"
)

// Torrents the policy doesn't let the user play are not listed anywhere (admins see all).
func TestListingsHideUnplayableTorrents(t *testing.T) {
	s, h := testServer(t)
	ctx := context.Background()
	if _, err := s.Meta.W.ExecContext(ctx, `INSERT INTO works(id,key,title,title_cn,aliases,type,begin_at,eps,updated_at) VALUES
		(7,'bgm:400602','葬送のフリーレン','葬送的芙莉莲','[]','tv','2023-09-29T00:00:00Z',28,'x')`); err != nil {
		t.Fatal(err)
	}
	_, _ = s.Meta.W.ExecContext(ctx, `INSERT INTO work_ids VALUES('bgm','400602','bgm:400602'),('bangumi_moe','tag1','bgm:400602')`)
	ihs := map[string]string{
		"open":   "ae5e267603990c2e6205f8e43df4c9eb368bbeba",
		"member": "be5e267603990c2e6205f8e43df4c9eb368bbeba",
		"vip":    "ce5e267603990c2e6205f8e43df4c9eb368bbeba",
		"block":  "de5e267603990c2e6205f8e43df4c9eb368bbeba",
	}
	tags := map[string]string{"open": "", "member": " MEMBERONLY", "vip": " VIPONLY", "block": " BLOCKED"}
	for name, ih := range ihs {
		title := "[LoliHouse] Sousou no Frieren - 28 [1080p]" + tags[name]
		if err := s.Meta.Match(ctx, meta.TorrentInput{Infohash: ih, Title: title, Source: "bangumi.moe", SourceKind: "bangumi_moe",
			SourceID: "moe-" + name, Trusted: true, MoeTagIDs: []string{"tag1"}, PublishedAt: "2026-10-18T10:00:00Z",
			Files: []meta.FileInput{{Index: 0, Path: "[LoliHouse] Sousou no Frieren - 28 [1080p].mkv", Size: 100}}}); err != nil {
			t.Fatal(err)
		}
	}
	for _, r := range []store.Rule{
		{Name: "m", Enabled: true, Priority: 1, Field: "title", Op: "contains", Value: "MEMBERONLY", Action: "login"},
		{Name: "v", Enabled: true, Priority: 1, Field: "title", Op: "contains", Value: "VIPONLY", Action: "vip"},
		{Name: "b", Enabled: true, Priority: 1, Field: "title", Op: "contains", Value: "BLOCKED", Action: "block", Label: "copyright"},
	} {
		if _, err := s.St.SaveRule(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	s.Policy.Invalidate()
	session := func(role string) string {
		u, err := s.St.CreateUser(ctx, store.CreateUserParams{Username: "u" + role, Password: "devpass-123", Role: role})
		if err != nil {
			t.Fatal(err)
		}
		tok, err := s.St.CreateSession(ctx, u.ID, time.Hour, "", "")
		if err != nil {
			t.Fatal(err)
		}
		return tok
	}
	cookies := map[string]string{"guest": "", "member": session(store.RoleMember), "admin": session(store.RoleAdmin)}
	name := map[string]string{}
	for n, ih := range ihs {
		name[ih] = n
	}
	type item struct {
		Infohash string `json:"infohash"`
		Access   struct {
			Action string `json:"action"`
		} `json:"access"`
	}
	set := func(items []item) map[string]string {
		m := map[string]string{}
		for _, it := range items {
			m[name[it.Infohash]] = it.Access.Action
		}
		return m
	}
	want := map[string][]string{
		"guest":  {"open"},
		"member": {"open", "member"},
		"admin":  {"open", "member", "vip", "block"},
	}
	for role, cookie := range cookies {
		check := func(where string, got map[string]string) {
			t.Helper()
			if len(got) != len(want[role]) {
				t.Errorf("%s %s: got %v, want %v", role, where, got, want[role])
			}
			for _, n := range want[role] {
				if _, ok := got[n]; !ok {
					t.Errorf("%s %s: missing %s (got %v)", role, where, n, got)
				}
			}
		}
		var ep struct{ Items []item }
		scopeReq(t, h, "GET", "/api/v1/subjects/bgm/400602/episodes/28", cookie, "", &ep)
		check("episode", set(ep.Items))
		var sub struct{ Torrents []item }
		scopeReq(t, h, "GET", "/api/v1/subjects/bgm/400602", cookie, "", &sub)
		check("subject", set(sub.Torrents))
		var sr struct {
			Torrents  []item
			Total     int `json:"total"`
			PageCount int `json:"page_count"`
		}
		scopeReq(t, h, "GET", "/api/v1/search/"+url.PathEscape("Frieren"), cookie, "", &sr)
		check("search", set(sr.Torrents))
		if sr.Total != len(want[role]) || sr.PageCount != 1 {
			t.Errorf("%s search total %d page_count %d", role, sr.Total, sr.PageCount)
		}
		var eps struct {
			Items []struct {
				Key     string `json:"key"`
				Sources int    `json:"sources"`
			}
		}
		scopeReq(t, h, "GET", "/api/v1/subjects/bgm/400602/episodes", cookie, "", &eps)
		for _, e := range eps.Items {
			if e.Key == "28" && e.Sources != len(want[role]) {
				t.Errorf("%s episode list sources %d", role, e.Sources)
			}
		}
		var mf struct{ Items []item }
		scopeReq(t, h, "GET", "/meta/v1/subjects/bgm/400602/episodes/28/files", cookie, "", &mf)
		check("meta files", set(mf.Items))
		var me struct {
			Items []struct {
				Key   string `json:"key"`
				Files int    `json:"files"`
			}
		}
		scopeReq(t, h, "GET", "/meta/v1/subjects/bgm/400602/episodes", cookie, "", &me)
		if len(me.Items) != 1 || me.Items[0].Files != len(want[role]) {
			t.Errorf("%s meta episodes %+v", role, me.Items)
		}
	}
	// admins see the blocked torrent with its access; a direct play is still refused
	var ep struct{ Items []item }
	scopeReq(t, h, "GET", "/api/v1/subjects/bgm/400602/episodes/28", cookies["admin"], "", &ep)
	if set(ep.Items)["block"] != "block" {
		t.Fatalf("admin access %v", set(ep.Items))
	}
	if code := scopeReq(t, h, "POST", "/api/v1/torrents/hash/"+ihs["block"]+"/play", cookies["admin"], "", nil); code != 451 {
		t.Fatalf("admin play of blocked torrent: %d", code)
	}
	// direct play protection unchanged for a guest
	if code := scopeReq(t, h, "POST", "/api/v1/torrents/hash/"+ihs["member"]+"/play", "", "", nil); code != 401 {
		t.Fatalf("guest play of member torrent: %d", code)
	}
}
