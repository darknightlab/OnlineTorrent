package api

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/darknightlab/onlinetorrent/internal/edge"
	"github.com/darknightlab/onlinetorrent/internal/edgetoken"
	"github.com/darknightlab/onlinetorrent/internal/seedsrc"
	"github.com/darknightlab/onlinetorrent/internal/store"
	"github.com/darknightlab/onlinetorrent/internal/torrentfile"
)

func TestAdminEdgeTokenFlow(t *testing.T) {
	s, _ := testServer(t)
	ctx := context.Background()
	if _, err := s.NewEdgeHub(); err != nil {
		t.Fatal(err)
	}
	h := s.Handler()
	admin := loginAs(t, s, "boss", store.RoleAdmin)
	member := loginAs(t, s, "alice", store.RoleMember)
	id, err := s.St.SaveQBServer(ctx, store.QBServer{Name: "ams", URL: "http://127.0.0.1:1", Enabled: true, EdgeURL: "https://edge-ams.example"})
	if err != nil {
		t.Fatal(err)
	}
	path := "/api/v1/admin/qb-servers/id/" + itoa(id) + "/edge-token"
	if rec := doReq(h, "POST", path, member); rec.Code != 403 {
		t.Fatalf("member generated a token: %d", rec.Code)
	}
	rec := doReq(h, "POST", path, admin)
	var out struct {
		Token      string `json:"token"`
		ConnectURL string `json:"connect_url"`
	}
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &out) != nil || !strings.HasPrefix(out.Token, "otde_") || !strings.HasSuffix(out.ConnectURL, "/api/v1/edge/connect") {
		t.Fatalf("generate: %d %s", rec.Code, rec.Body)
	}
	// only the hash is stored
	ls, _ := s.St.ListQBServers(ctx)
	if ls[0].EdgeTokenHash != edge.HashToken(out.Token) || strings.Contains(ls[0].EdgeTokenHash, out.Token) {
		t.Fatal("token not stored as hash")
	}
	hello := func(tok string) int {
		req := httptest.NewRequest("GET", "/api/v1/edge/connect", nil)
		req.Header.Set("Authorization", "Bearer "+tok)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		return w.Code
	}
	if c := hello(out.Token); c != 204 {
		t.Fatalf("hello with token: %d", c)
	}
	if c := hello("otde_nope"); c != 401 {
		t.Fatalf("hello with a wrong token: %d", c)
	}
	// rotate: the old token stops working
	old := out.Token
	rec = doReq(h, "POST", path, admin)
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if hello(old) != 401 || hello(out.Token) != 204 {
		t.Fatal("rotation")
	}
	// list shows the binding (and no secrets)
	rec = doReq(h, "GET", "/api/v1/admin/qb-servers", admin)
	if !strings.Contains(rec.Body.String(), `"edge_bound":true`) || strings.Contains(rec.Body.String(), out.Token) || strings.Contains(rec.Body.String(), "edge_token_hash") ||
		!strings.Contains(rec.Body.String(), `"edge_url":"https://edge-ams.example"`) {
		t.Fatalf("list: %s", rec.Body)
	}
	// unbind
	if rec = doReq(h, "DELETE", path, admin); rec.Code != 200 || hello(out.Token) != 401 {
		t.Fatalf("unbind: %d", rec.Code)
	}

	// signing key rotation keeps the previous public key (grace for running tokens)
	before := s.edgePublicKeys()
	tok, _, err := s.issueEdgeToken(ctx, nil, strings.Repeat("ab", 20), id)
	if err != nil {
		t.Fatal(err)
	}
	if rec = doReq(h, "POST", "/api/v1/admin/edge/rotate-key", admin); rec.Code != 200 {
		t.Fatalf("rotate key: %d", rec.Code)
	}
	after := s.edgePublicKeys()
	if len(after) != 2 || after[1] != before[0] || after[0] == before[0] {
		t.Fatalf("keys %v → %v", before, after)
	}
	var pubs []edgetokenKey
	for _, k := range after {
		p, _ := edgetoken.DecodePublic(k)
		pubs = append(pubs, p)
	}
	if _, err := edgetoken.Verify(tok, pubs, id, strings.Repeat("ab", 20), time.Now(), 0); err != nil {
		t.Fatalf("token from the previous key: %v", err)
	}
	// the key survives a restart (stored in the settings)
	s.edgeKeys = nil
	if again := s.edgePublicKeys(); again[0] != after[0] || again[1] != after[1] {
		t.Fatal("keys not persisted")
	}
	// guests' tokens carry the guest role; no user id anywhere in the claims
	c, _ := edgetoken.Verify(tok, pubs, id, strings.Repeat("ab", 20), time.Now(), 0)
	if c.Role != store.RoleGuest {
		t.Fatalf("role %q", c.Role)
	}
}

type edgetokenKey = ed25519.PublicKey

// The main server has no webseed data path: /webseed/… is 404 for everybody (not the SPA),
// while the permission still exists, sits in the webseed bundle and gates token issuance.
func TestMainServerHasNoWebseed(t *testing.T) {
	s, _ := testServer(t)
	s.WebFS = fstest.MapFS{"index.html": {Data: []byte("<!doctype html><html>spa</html>")}} // unknown paths fall to the SPA
	h := s.Handler()
	if rec := doReq(h, "GET", "/some/page", ""); rec.Code != 200 || !strings.Contains(rec.Body.String(), "spa") {
		t.Fatalf("SPA fallback not active: %d", rec.Code)
	}
	admin := loginAs(t, s, "boss", store.RoleAdmin)
	ih := strings.Repeat("ab", 20)
	for _, p := range []string{"/webseed/hash/" + ih + "/", "/webseed/hash/" + ih + "/T/a.mkv", "/webseed/x"} {
		for _, m := range []string{"GET", "HEAD"} {
			for _, c := range []string{"", admin} {
				rec := doReq(h, m, p, c)
				if rec.Code != 404 || strings.Contains(rec.Body.String(), "<html") || strings.Contains(rec.Body.String(), "<!doctype") {
					t.Fatalf("%s %s (cookie %v): %d %.80s", m, p, c != "", rec.Code, rec.Body.String())
				}
			}
		}
	}
	// the permission spec exists (handler-less), configurable, in the webseed bundle
	s.perms.mu.RLock()
	spec := s.perms.byKey[webseedRouteKey]
	s.perms.mu.RUnlock()
	if spec == nil || spec.Access != AccessConfigurable || spec.Bundle != "webseed" || !strings.Contains(spec.Label, "边缘节点") {
		t.Fatalf("spec = %+v", spec)
	}
	if !s.allowed(store.RoleMember, webseedRouteKey) || s.allowed(store.RoleGuest, webseedRouteKey) {
		t.Fatal("default grants changed")
	}
}

func TestWebseedPermissionGatesTokens(t *testing.T) {
	files := []torrentfile.TorrentFile{{Path: "T/a.mkv", Size: 100}}
	src := &memSource{id: "qb:5"}
	src.set(2)
	s, _, ih := webseedTestServer(t, files, 100, &seedsrc.Edge{ServerID: 5, BaseURL: "https://edge.test", Client: fakeEdge{src}})
	// guests: policy allows this torrent, but they lack the route permission → no seeds, no token
	s.wsAllow.Store(store.RoleGuest+"|"+ih+"|"+strconv.FormatUint(s.Policy.Generation(), 10), wsAllowEntry{true, time.Now().Add(time.Hour)})
	run := func() []string {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		var got []string
		s.webseedStream(ctx, httptest.NewRequest("GET", "http://x/", nil), func(name string, d any) bool {
			b, _ := json.Marshal(d)
			got = append(got, name+" "+string(b))
			return name != "ready"
		}, func() bool { return true }, ih, 0)
		return got
	}
	if got := run(); len(got) != 1 || !strings.Contains(got[0], "forbidden") {
		t.Fatalf("guest without permission: %v", got)
	}
	// grant the permission to guests → the edge URL comes with a token
	s.perms.mu.Lock()
	if s.perms.grants[store.RoleGuest] == nil {
		s.perms.grants[store.RoleGuest] = map[string]bool{}
	}
	s.perms.grants[store.RoleGuest][webseedRouteKey] = true
	s.perms.mu.Unlock()
	got := run()
	if len(got) == 0 || !strings.HasPrefix(got[len(got)-1], "ready ") || !strings.Contains(got[len(got)-1], `"token":"`) {
		t.Fatalf("guest with permission: %v", got)
	}
}
