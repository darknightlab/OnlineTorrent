package api

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/darknightlab/onlinetorrent/internal/edgetoken"
	"github.com/darknightlab/onlinetorrent/internal/seedsrc"
	"github.com/darknightlab/onlinetorrent/internal/store"
	"github.com/darknightlab/onlinetorrent/internal/torrentfile"
)

// memSource is a mutable set of verified pieces (what an edge reports).
type memSource struct {
	id   string
	mu   sync.Mutex
	have []int // pieceStates-style 0/2
}

func (m *memSource) ID() string   { return m.id }
func (m *memSource) Name() string { return m.id }
func (m *memSource) set(states ...int) {
	m.mu.Lock()
	m.have = states
	m.mu.Unlock()
}
func (m *memSource) Have(context.Context, *seedsrc.Layout) (seedsrc.Avail, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.have == nil {
		return seedsrc.Avail{}, nil
	}
	return seedsrc.Avail{Known: true, Pieces: seedsrc.FromStates(m.have)}, nil
}

// progressiveServer: a server whose layout for ih is pre-loaded and whose sources are fakes.
func webseedTestServer(t *testing.T, files []torrentfile.TorrentFile, pl int64, srcs ...seedsrc.Source) (*Server, *seedsrc.Layout, string) {
	t.Helper()
	s, _ := testServer(t)
	ih := strings.Repeat("cd", 20)
	var size int64
	for _, f := range files {
		size += f.Size
	}
	n := (size + pl - 1) / pl
	l, err := seedsrc.NewLayout(&torrentfile.Metainfo{Infohash: ih, Name: "T", Files: files, PieceLength: pl, Pieces: make([]byte, 20*n)})
	if err != nil {
		t.Fatal(err)
	}
	s.layouts.Store(ih, layoutEntry{l, time.Now().Add(time.Hour)})
	s.SeedSources = func() []seedsrc.Source { return srcs }
	s.seeds().TTL = time.Millisecond
	return s, l, ih
}

type wsEvent struct {
	name string
	data map[string]any
}

// fakeEdge is a connected edge node (availability only).
type fakeEdge struct{ src *memSource }

func (f fakeEdge) Have(ctx context.Context, ih string, n int) (*seedsrc.Bits, bool, error) {
	a, err := f.src.Have(ctx, nil)
	return a.Pieces, a.Known, err
}

func TestWebseedStreamEvents(t *testing.T) {
	files := []torrentfile.TorrentFile{{Path: "T/a.mkv", Size: 600}, {Path: "T/b.srt", Size: 100}}
	plain := &memSource{id: "qb:1"} // not an edge: never offered (no main-server webseed)
	plain.set(2, 2, 2, 2, 2, 2, 2)
	edge2QB := &memSource{id: "qb:6"}
	edgeQB := &memSource{id: "qb:5"}
	edgeSrc := &seedsrc.Edge{ServerID: 5, ServerName: "ams", BaseURL: "https://edge.test", Client: fakeEdge{edgeQB}}
	edge2Src := &seedsrc.Edge{ServerID: 6, ServerName: "pku", BaseURL: "https://edge2.test", Client: fakeEdge{edge2QB}}
	s, _, ih := webseedTestServer(t, files, 100, plain, edge2Src, edgeSrc) // 7 pieces; a = 0..5, b = 6
	s.EdgeTokenTTL = 2 * time.Second
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	setSetting(t, s, store.KeySiteBaseURL, "https://example.test")
	admin, err := s.St.CreateUser(ctx, store.CreateUserParams{Username: "boss", Password: "password-123", Role: store.RoleAdmin})
	if err != nil {
		t.Fatal(err)
	}
	// the permission decision (policy + content scope) is cached per role and torrent
	s.wsAllow.Store(store.RoleAdmin+"|"+ih+"|"+strconv.FormatUint(s.Policy.Generation(), 10), wsAllowEntry{true, time.Now().Add(time.Hour)})
	var mu sync.Mutex
	var evs []wsEvent
	send := func(name string, data any) bool {
		b, _ := json.Marshal(data)
		var m map[string]any
		_ = json.Unmarshal(b, &m)
		mu.Lock()
		evs = append(evs, wsEvent{name, m})
		mu.Unlock()
		return true
	}
	r := httptest.NewRequest("GET", "http://x/", nil)
	r = r.WithContext(context.WithValue(r.Context(), userKey, admin))
	done := make(chan struct{})
	go func() { s.webseedStream(ctx, r, send, func() bool { return true }, ih, 0); close(done) }()
	waitFor := func(name string) wsEvent {
		t.Helper()
		deadline := time.Now().Add(8 * time.Second)
		for seen := 0; ; {
			mu.Lock()
			for ; seen < len(evs); seen++ {
				if evs[seen].name == name {
					e := evs[seen]
					evs = evs[seen+1:]
					mu.Unlock()
					return e
				}
			}
			mu.Unlock()
			if time.Now().After(deadline) {
				t.Fatalf("no %q event", name)
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	edge2URL := "https://edge2.test/webseed/hash/" + ih + "/"
	edgeURL := "https://edge.test/webseed/hash/" + ih + "/"
	keys := s.edgePublicKeys()
	pub, _ := edgetoken.DecodePublic(keys[0])
	checkToken := func(tok any, exp any, aud int64) {
		t.Helper()
		c, err := edgetoken.Verify(tok.(string), []ed25519.PublicKey{pub}, aud, ih, time.Now(), 0)
		if err != nil || c.Role != store.RoleAdmin || int64(exp.(float64)) != c.Exp.Unix() {
			t.Fatalf("token: %+v %v", c, err)
		}
		if _, err := edgetoken.Verify(tok.(string), []ed25519.PublicKey{pub}, aud+100, ih, time.Now(), 0); !errors.Is(err, edgetoken.ErrAudience) {
			t.Fatal("token accepted by another edge")
		}
	}

	waitFor("waiting") // nobody has the metadata yet
	edge2QB.set(2, 2, 0, 0, 0, 0, 0)
	edgeQB.set(2, 2, 2, 2, 0, 0, 0)
	ready := waitFor("ready")
	seeds := ready.data["seeds"].([]any)
	if ready.data["pieces"] != 7.0 || len(seeds) != 2 {
		t.Fatalf("ready = %v", ready.data)
	}
	byURL := map[string]map[string]any{}
	for _, x := range seeds {
		m := x.(map[string]any)
		byURL[m["url"].(string)] = m
	}
	m2 := byURL[edge2URL]
	if m2 == nil || !reflect.DeepEqual(m2["have"], []any{0.0, 1.0}) {
		t.Fatalf("edge2 seed = %v", m2)
	}
	checkToken(m2["token"], m2["exp"], 6)
	em := byURL[edgeURL]
	if em == nil || !reflect.DeepEqual(em["have"], []any{0.0, 3.0}) {
		t.Fatalf("edge seed = %v", em)
	}
	checkToken(em["token"], em["exp"], 5)

	edgeQB.set(2, 2, 2, 2, 2, 0, 0)
	if e := waitFor("have"); e.data["url"] != edgeURL || !reflect.DeepEqual(e.data["ranges"], []any{4.0, 4.0}) {
		t.Fatalf("have = %v", e.data)
	}
	edgeQB.set(2, 2, 2, 2, 0, 0, 0) // edge lost piece 4 (or disconnected)
	if e := waitFor("lost"); e.data["url"] != edgeURL || !reflect.DeepEqual(e.data["ranges"], []any{4.0, 4.0}) {
		t.Fatalf("lost = %v", e.data)
	}
	// the edge token is refreshed before it expires (TTL 2 s → after ~1.6 s)
	tk := waitFor("token")
	if tk.data["url"] != edgeURL || tk.data["token"] == em["token"] {
		t.Fatalf("token = %v", tk.data)
	}
	checkToken(tk.data["token"], tk.data["exp"], 5)

	edgeQB.set(2, 2, 2, 2, 2, 2, 2)
	if e := waitFor("complete"); e.data["url"] != edgeURL {
		t.Fatalf("complete = %v", e.data)
	}
	edge2QB.set(2, 2, 2, 2, 2, 2, 2)
	if e := waitFor("complete"); e.data["url"] != edge2URL {
		t.Fatalf("complete = %v", e.data)
	}
	waitFor("done")
	<-done
	mu.Lock()
	defer mu.Unlock()
	for _, e := range evs {
		if u, _ := e.data["url"].(string); u != "" && u != edgeURL && u != edge2URL {
			t.Fatalf("non-edge webseed URL offered: %v", e)
		}
	}
}

// Servers without an edge node offer no webseed: the stream keeps waiting (no ready).
func TestWebseedStreamNoEdge(t *testing.T) {
	files := []torrentfile.TorrentFile{{Path: "T/a.mkv", Size: 100}}
	plain := &memSource{id: "qb:1"}
	plain.set(2)
	s, _, ih := webseedTestServer(t, files, 100, plain)
	admin, _ := s.St.CreateUser(context.Background(), store.CreateUserParams{Username: "boss", Password: "password-123", Role: store.RoleAdmin})
	s.wsAllow.Store(store.RoleAdmin+"|"+ih+"|"+strconv.FormatUint(s.Policy.Generation(), 10), wsAllowEntry{true, time.Now().Add(time.Hour)})
	ctx, cancel := context.WithTimeout(context.Background(), 2500*time.Millisecond)
	defer cancel()
	var got []string
	r := httptest.NewRequest("GET", "http://x/", nil)
	r = r.WithContext(context.WithValue(r.Context(), userKey, admin))
	s.webseedStream(ctx, r, func(name string, _ any) bool { got = append(got, name); return true }, func() bool { return true }, ih, 0)
	for _, e := range got {
		if e != "waiting" {
			t.Fatalf("events = %v", got)
		}
	}
}

func TestWebseedStreamForbidden(t *testing.T) {
	files := []torrentfile.TorrentFile{{Path: "T/a.mkv", Size: 100}}
	src := &memSource{id: "qb:5"}
	s, _, ih := webseedTestServer(t, files, 100, &seedsrc.Edge{ServerID: 5, BaseURL: "https://edge.test", Client: fakeEdge{src}})
	src.set(2)
	var got []string
	send := func(name string, data any) bool { got = append(got, name+":"+fmt.Sprint(data)); return true }
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// guest, torrent unknown to the catalog → no URLs, no tokens
	s.webseedStream(ctx, httptest.NewRequest("GET", "http://x/", nil), send, func() bool { return true }, ih, 0)
	if len(got) != 1 || !strings.Contains(got[0], "forbidden") {
		t.Fatalf("events = %v", got)
	}
}

func setSetting(t *testing.T, s *Server, key string, v any) {
	t.Helper()
	b, _ := json.Marshal(v)
	if err := s.St.UpdateSettings(context.Background(), map[string]json.RawMessage{key: b}); err != nil {
		t.Fatal(err)
	}
}
