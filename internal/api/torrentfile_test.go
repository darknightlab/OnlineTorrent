package api

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/darknightlab/onlinetorrent/internal/meta"
	"github.com/darknightlab/onlinetorrent/internal/policy"
	"github.com/darknightlab/onlinetorrent/internal/store"
)

// testTorrent builds a minimal single-file .torrent and returns it with its v1 infohash.
func testTorrent(name string) ([]byte, string) {
	info := fmt.Sprintf("d6:lengthi12345e4:name%d:%s12:piece lengthi16384e6:pieces20:%se", len(name), name, strings.Repeat("x", 20))
	sum := sha1.Sum([]byte(info))
	const tr = "wss://t.example/announce"
	return []byte(fmt.Sprintf("d8:announce%d:%s4:info%se", len(tr), tr, info)), hex.EncodeToString(sum[:])
}

func doReq(h http.Handler, method, path, cookie string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, nil)
	req.Header.Set("X-Requested-With", "fetch")
	if cookie != "" {
		req.AddCookie(&http.Cookie{Name: sessionCookie, Value: cookie})
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func loginAs(t *testing.T, s *Server, name, role string) string {
	t.Helper()
	ctx := context.Background()
	u, err := s.St.CreateUser(ctx, store.CreateUserParams{Username: name, Email: name + "@example.com", Password: "password-123", Role: role})
	if err != nil {
		t.Fatal(err)
	}
	tok, err := s.St.CreateSession(ctx, u.ID, time.Hour, "127.0.0.1", "test")
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

func TestMetainfoMigrationAndValidation(t *testing.T) {
	s, _ := testServer(t)
	ctx := context.Background()
	data, ih := testTorrent("Foo - 01.mkv")
	if s.Meta.HasMetainfo(ctx, ih) {
		t.Fatal("empty table has row")
	}
	// wrong infohash / garbage / too large are rejected
	if err := s.Meta.PutMetainfo(ctx, strings.Repeat("0", 40), data, meta.MetainfoQB); err == nil {
		t.Fatal("mismatching infohash accepted")
	}
	if err := s.Meta.PutMetainfo(ctx, ih, []byte("not bencode"), meta.MetainfoQB); err == nil {
		t.Fatal("garbage accepted")
	}
	if err := s.Meta.PutMetainfo(ctx, ih, make([]byte, 6<<20), meta.MetainfoQB); err == nil {
		t.Fatal("oversized accepted")
	}
	if err := s.Meta.PutMetainfo(ctx, ih, data, meta.MetainfoCrawl); err != nil {
		t.Fatal(err)
	}
	got, err := s.Meta.GetMetainfo(ctx, ih)
	if err != nil || string(got) != string(data) || !s.Meta.HasMetainfo(ctx, ih) {
		t.Fatalf("get: %v", err)
	}
	var src string
	_ = s.Meta.R.QueryRowContext(ctx, `SELECT source FROM torrent_metainfo WHERE infohash = ?`, ih).Scan(&src)
	if src != meta.MetainfoCrawl {
		t.Fatalf("source %q", src)
	}
}

func TestTorrentFileEndpoint(t *testing.T) {
	s, h := testServer(t)
	ctx := context.Background()
	data, ih := testTorrent("Foo - 01.mkv")
	if err := s.Meta.Match(ctx, meta.TorrentInput{Infohash: ih, Title: "Foo - 01", Source: "bangumi.moe", SourceKind: "bangumi_moe", Trusted: true,
		PublishedAt: "2026-01-10T00:00:00Z", Files: []meta.FileInput{{Index: 0, Path: "Foo - 01.mkv", Size: 1}}}); err != nil {
		t.Fatal(err)
	}
	if err := s.Meta.PutMetainfo(ctx, ih, data, meta.MetainfoCrawl); err != nil {
		t.Fatal(err)
	}
	path := "/api/v1/torrents/hash/" + ih + "/torrent"

	// guest (default grant): bytes + headers
	rec := doReq(h, "GET", path, "")
	if rec.Code != 200 || rec.Body.String() != string(data) {
		t.Fatalf("guest: %d %q", rec.Code, rec.Body.String())
	}
	if ct, cc := rec.Header().Get("Content-Type"), rec.Header().Get("Cache-Control"); ct != "application/x-bittorrent" || cc != "private, max-age=3600" {
		t.Fatalf("headers: %q %q", ct, cc)
	}
	// invalid / unknown infohash
	if rec := doReq(h, "GET", "/api/v1/torrents/hash/xyz/torrent", ""); rec.Code != 400 {
		t.Fatalf("invalid ih: %d", rec.Code)
	}
	member := loginAs(t, s, "alice", store.RoleMember)
	if rec := doReq(h, "GET", "/api/v1/torrents/hash/"+strings.Repeat("ab", 20)+"/torrent", member); rec.Code != 404 {
		t.Fatalf("unknown ih: %d %s", rec.Code, rec.Body.String())
	}
	// unknown torrent (no metadata) for a guest: nothing to serve → 404
	if rec := doReq(h, "GET", "/api/v1/torrents/hash/"+strings.Repeat("cd", 20)+"/torrent", ""); rec.Code != 404 {
		t.Fatalf("guest unknown ih: %d", rec.Code)
	}

	// policy block (admin verdict) → 403 for everyone but admins' route bypass doesn't skip policy
	data2, ih2 := testTorrent("Bar - 02.mkv")
	_ = s.Meta.Match(ctx, meta.TorrentInput{Infohash: ih2, Title: "Bar - 02", Source: "bangumi.moe", SourceKind: "bangumi_moe", Trusted: true,
		PublishedAt: "2026-01-10T00:00:00Z", Files: []meta.FileInput{{Index: 0, Path: "Bar - 02.mkv", Size: 1}}})
	_ = s.Meta.PutMetainfo(ctx, ih2, data2, meta.MetainfoCrawl)
	if err := s.St.SetVerdict(ctx, nil, store.Verdict{Infohash: ih2, Action: policy.Block, Label: "other"}); err != nil {
		t.Fatal(err)
	}
	if rec := doReq(h, "GET", "/api/v1/torrents/hash/"+ih2+"/torrent", member); rec.Code != 403 {
		t.Fatalf("blocked: %d", rec.Code)
	}

	// route permission revoked for guests → 401, members still allowed
	if err := s.St.SetRouteRoles(ctx, torrentFileKey, []string{store.RoleMember}); err != nil {
		t.Fatal(err)
	}
	if err := s.loadPerms(ctx); err != nil {
		t.Fatal(err)
	}
	if rec := doReq(h, "GET", path, ""); rec.Code != 401 {
		t.Fatalf("revoked guest: %d", rec.Code)
	}
	if rec := doReq(h, "GET", path, member); rec.Code != 200 {
		t.Fatalf("member: %d", rec.Code)
	}
}

func TestPlayMagnetXS(t *testing.T) {
	s, h := testServer(t)
	ctx := context.Background()
	if err := s.St.UpdateSettings(ctx, map[string]json.RawMessage{store.KeySiteBaseURL: json.RawMessage(`"https://www.example.org"`)}); err != nil {
		t.Fatal(err)
	}
	data, ih := testTorrent("Foo - 01.mkv")
	_, ih2 := testTorrent("Unknown - 01.mkv")
	if err := s.Meta.Match(ctx, meta.TorrentInput{Infohash: ih, PublishedAt: "2026-01-10T00:00:00Z", Title: "Foo - 01", Source: "Nyaa", SourceKind: "rss", Trusted: true,
		Files: []meta.FileInput{{Index: 0, Path: "Foo - 01.mkv", Size: 12345}}}); err != nil {
		t.Fatal(err)
	}
	if err := s.Meta.PutMetainfo(ctx, ih, data, meta.MetainfoCrawl); err != nil {
		t.Fatal(err)
	}
	play := func(ih, cookie string) string {
		t.Helper()
		rec := doReq(h, "POST", "/api/v1/torrents/hash/"+ih+"/play", cookie)
		if rec.Code != 200 {
			t.Fatalf("play %s: %d %s", ih, rec.Code, rec.Body.String())
		}
		var out struct{ Magnet string }
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return out.Magnet
	}
	wantXS := "&xs=" + url.QueryEscape("https://www.example.org/api/v1/torrents/hash/"+ih+"/torrent")
	if m := play(ih, ""); !strings.HasSuffix(m, wantXS) {
		t.Fatalf("stored torrent: no xs in %s", m)
	}
	// nothing stored, no qB server: no xs
	member := loginAs(t, s, "bob", store.RoleMember)
	if m := play(ih2, member); strings.Contains(m, "xs=") {
		t.Fatalf("unknown torrent got xs: %s", m)
	}
	// without permission for the .torrent route: no xs
	_ = s.St.SetRouteRoles(ctx, torrentFileKey, []string{store.RoleVIP})
	_ = s.loadPerms(ctx)
	if m := play(ih, ""); strings.Contains(m, "xs=") {
		t.Fatalf("guest without permission got xs: %s", m)
	}
}
