package api

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
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

func TestMainWebseedSkipsEdgeSources(t *testing.T) {
	files := []torrentfile.TorrentFile{{Path: "T/a.mkv", Size: 300}}
	data := make([]byte, 300)
	main := &memSource{id: "qb:1", data: data}
	edgeQB := &memSource{id: "qb:5", data: data}
	edgeSrc := &seedsrc.Edge{ServerID: 5, BaseURL: "https://edge.test", Client: fakeEdge{edgeQB}}
	s, _, ih := progressiveServer(t, files, 100, main, edgeSrc)
	main.set(2, 0, 0)
	edgeQB.set(2, 2, 2)
	// piece 0: the main server's own source has it
	if w := webseedReq(s, ih, "T/a.mkv", "bytes=0-99"); w.Code != 206 {
		t.Fatalf("main piece: %d", w.Code)
	}
	// pieces 1-2 only on the edge: the main server never relays edge data
	if w := webseedReq(s, ih, "T/a.mkv", "bytes=100-199"); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("edge-only piece served by the main server: %d", w.Code)
	}
}
