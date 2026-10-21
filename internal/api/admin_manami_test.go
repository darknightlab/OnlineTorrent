package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/darknightlab/onlinetorrent/internal/meta"
	"github.com/darknightlab/onlinetorrent/internal/store"
)

func TestAdminManami(t *testing.T) {
	s, h := testServer(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"$schema":"s","lastUpdate":"2026-07-04"}` + "\n" + `{"title":"Sample Entry","sources":["https://myanimelist.net/anime/1"]}` + "\n"))
	}))
	defer srv.Close()
	b, _ := json.Marshal(srv.URL)
	if err := s.St.UpdateSettings(context.Background(), map[string]json.RawMessage{store.KeyManamiURL: b}); err != nil {
		t.Fatal(err)
	}
	if code := getJSON(t, h, "/api/v1/admin/manami", nil); code != 401 {
		t.Fatal(code)
	}
	cookie := loginAs(t, s, "manami-admin", store.RoleAdmin)
	call := func(method, path string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, nil)
		req.AddCookie(&http.Cookie{Name: sessionCookie, Value: cookie})
		req.Header.Set("X-Requested-With", "fetch")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	r := call("GET", "/api/v1/admin/manami")
	if r.Code != 200 {
		t.Fatal(r.Code, r.Body.String())
	}
	r = call("POST", "/api/v1/admin/manami/run")
	if r.Code != 202 {
		t.Fatal(r.Code, r.Body.String())
	}
	for deadline := time.Now().Add(5 * time.Second); ; {
		var st meta.ManamiStatus
		r = call("GET", "/api/v1/admin/manami")
		json.Unmarshal(r.Body.Bytes(), &st)
		if st.SyncedAt != "" {
			if st.Entries != 1 || st.Running {
				t.Fatal(st)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal(st)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
