package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/darknightlab/onlinetorrent/internal/store"
)

// Routes of a bundle (WebSeed download + readiness streams) must keep identical grants: a
// save that changes only some of them is rejected; changing all of them together works.
func TestPermissionBundles(t *testing.T) {
	s, h := testServer(t)
	if err := s.loadPerms(context.Background()); err != nil {
		t.Fatal(err)
	}
	admin := loginAs(t, s, "boss", store.RoleAdmin)
	put := func(body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("PUT", "/api/v1/admin/permissions", strings.NewReader(body))
		req.Header.Set("X-Requested-With", "fetch")
		req.Header.Set("Content-Type", "application/json")
		req.AddCookie(&http.Cookie{Name: sessionCookie, Value: admin})
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	keys := routeBundles["webseed"].Keys
	for _, k := range keys {
		s.perms.mu.RLock()
		sp := s.perms.byKey[k]
		s.perms.mu.RUnlock()
		if sp == nil || sp.Bundle != "webseed" || sp.BundleLabel != "WebSeed" {
			t.Fatalf("route %s not registered in bundle: %+v", k, sp)
		}
	}

	// only the download route → rejected, nothing changed
	if rec := put(`{"grants":{"` + keys[0] + `":["guest"]}}`); rec.Code != 400 {
		t.Fatalf("partial bundle save: %d %s", rec.Code, rec.Body.String())
	}
	if s.allowed(store.RoleGuest, keys[0]) {
		t.Fatal("rejected save still applied")
	}

	// all three together → guests may use WebSeed
	body := `{"grants":{"` + keys[0] + `":["guest"],"` + keys[1] + `":["guest"],"` + keys[2] + `":["guest"]}}`
	if rec := put(body); rec.Code != 200 {
		t.Fatalf("full bundle save: %d %s", rec.Code, rec.Body.String())
	}
	for _, k := range keys {
		if !s.allowed(store.RoleGuest, k) {
			t.Fatalf("guest not granted %s", k)
		}
	}

	// unrelated routes are unaffected by the check
	if rec := put(`{"grants":{"GET /api/v1/schedule":["guest"]}}`); rec.Code != 200 {
		t.Fatalf("unrelated save: %d %s", rec.Code, rec.Body.String())
	}
}
