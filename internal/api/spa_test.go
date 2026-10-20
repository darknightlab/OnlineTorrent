package api

import (
	"net/http/httptest"
	"testing"
	"testing/fstest"
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
