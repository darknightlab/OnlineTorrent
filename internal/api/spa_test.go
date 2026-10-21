package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/fstest"

	"github.com/darknightlab/onlinetorrent/internal/store"
)

// The PWA: the shell and its service worker are always revalidated (update detection), hashed
// assets are immutable, the manifest has its MIME type, the old /sw.min.js is a plain 404.
func TestSPACacheHeaders(t *testing.T) {
	s := &Server{WebFS: fstest.MapFS{
		"index.html":             {Data: []byte("<!doctype html>")},
		"sw.js":                  {Data: []byte("self")},
		"manifest.webmanifest":   {Data: []byte("{}")},
		"assets/index-AbC123.js": {Data: []byte("x")},
		"pwa-192.png":            {Data: []byte("png")},
	}}
	h := s.spa()
	for _, c := range []struct {
		path, cc, ct string
		code         int
	}{
		{"/", "no-cache", "text/html; charset=utf-8", 200},
		{"/sw.js", "no-cache", "", 200},
		{"/manifest.webmanifest", "no-cache", "application/manifest+json", 200},
		{"/assets/index-AbC123.js", "public, max-age=31536000, immutable", "", 200},
		{"/pwa-192.png", "public, max-age=3600", "image/png", 200},
		{"/subject/bgm/1", "no-cache", "text/html; charset=utf-8", 200}, // SPA route
		{"/sw.min.js", "", "", 404},
	} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", c.path, nil))
		if rec.Code != c.code || rec.Header().Get("Cache-Control") != c.cc || (c.ct != "" && rec.Header().Get("Content-Type") != c.ct) {
			t.Errorf("%s: %d cc=%q ct=%q", c.path, rec.Code, rec.Header().Get("Cache-Control"), rec.Header().Get("Content-Type"))
		}
	}
}

// The admin area's code (assets/admin/) is served only to admins — the same check as the admin
// API (role admin). Everyone else gets exactly the 404 of a missing asset; neither response may
// be stored by a shared cache (Cloudflare caches .js by extension).
func TestSPAAdminAssets(t *testing.T) {
	s, _ := testServer(t)
	s.WebFS = fstest.MapFS{
		"index.html":                      {Data: []byte("<!doctype html>")},
		"assets/index-AbC123.js":          {Data: []byte("main")},
		"assets/admin/AdminApp-XyZ789.js": {Data: []byte("admin code")},
	}
	h := s.withUser(s.spa())
	tokens := map[string]string{
		"guest":  "",
		"member": loginAs(t, s, "mem", store.RoleMember),
		"vip":    loginAs(t, s, "vip", store.RoleVIP),
		"admin":  loginAs(t, s, "boss", store.RoleAdmin),
		"bogus":  "not-a-session",
	}
	get := func(who, p string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", p, nil)
		if tok := tokens[who]; tok != "" {
			req.AddCookie(&http.Cookie{Name: sessionCookie, Value: tok})
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	missing := get("guest", "/assets/admin/Nope-000.js")
	if missing.Code != 404 || missing.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("missing asset: %d cc=%q", missing.Code, missing.Header().Get("Cache-Control"))
	}
	for who := range tokens {
		for _, p := range []string{"/assets/admin/AdminApp-XyZ789.js", "/assets/admin/../admin/AdminApp-XyZ789.js", "/assets//admin/AdminApp-XyZ789.js", "/assets/admin/", "/assets/admin"} {
			rec := get(who, p)
			if who == "admin" && p != "/assets/admin/" && p != "/assets/admin" {
				if rec.Code != 200 || rec.Body.String() != "admin code" || rec.Header().Get("Cache-Control") != "private, no-store" || rec.Header().Get("Vary") != "Cookie" {
					t.Errorf("admin %s: %d cc=%q vary=%q", p, rec.Code, rec.Header().Get("Cache-Control"), rec.Header().Get("Vary"))
				}
				continue
			}
			// indistinguishable from a missing file
			if rec.Code != missing.Code || rec.Body.String() != missing.Body.String() || rec.Header().Get("Cache-Control") != "no-store" ||
				rec.Header().Get("Content-Type") != missing.Header().Get("Content-Type") || rec.Header().Get("Vary") != "" {
				t.Errorf("%s %s: %d %q cc=%q", who, p, rec.Code, rec.Body.String(), rec.Header().Get("Cache-Control"))
			}
		}
	}
	// a missing ordinary asset is the same 404 (not the SPA shell); public assets stay public
	if rec := get("guest", "/assets/gone-111.js"); rec.Code != 404 || rec.Body.String() != missing.Body.String() {
		t.Errorf("missing public asset: %d %q", rec.Code, rec.Body.String())
	}
	if rec := get("guest", "/assets/index-AbC123.js"); rec.Code != 200 || rec.Header().Get("Cache-Control") != "public, max-age=31536000, immutable" {
		t.Errorf("public asset: %d cc=%q", rec.Code, rec.Header().Get("Cache-Control"))
	}
	if rec := get("admin", "/admin/users"); rec.Code != 200 || rec.Body.String() != "<!doctype html>" {
		t.Errorf("SPA route: %d", rec.Code)
	}
}
