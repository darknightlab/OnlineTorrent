package api

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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

func TestParseRange(t *testing.T) {
	cases := []struct {
		h       string
		n       int64
		s, e    int64
		partial bool
		ok      bool
	}{
		{"", 100, 0, 99, false, true},
		{"bytes=0-9", 100, 0, 9, true, true},
		{"bytes=90-", 100, 90, 99, true, true},
		{"bytes=90-500", 100, 90, 99, true, true},
		{"bytes=-10", 100, 90, 99, true, true},
		{"bytes=-500", 100, 0, 99, true, true},
		{"bytes=100-", 100, 0, 0, false, false},
		{"bytes=5-2", 100, 0, 0, false, false},
		{"bytes=0-1,5-6", 100, 0, 0, false, false},
		{"items=0-1", 100, 0, 0, false, false},
	}
	for _, c := range cases {
		s, e, p, ok := seedsrc.ParseRange(c.h, c.n)
		if ok != c.ok || (ok && (s != c.s || e != c.e || p != c.partial)) {
			t.Errorf("parseRange(%q,%d) = %d,%d,%v,%v", c.h, c.n, s, e, p, ok)
		}
	}
}

// memSource serves torrent data from memory with a mutable set of verified pieces.
type memSource struct {
	id   string
	data []byte
	mu   sync.Mutex
	have []int // pieceStates-style 0/2
	read [][2]int64
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
func (m *memSource) ReadRange(_ context.Context, l *seedsrc.Layout, fi int, start, end int64, _ *seedsrc.Bits) (io.ReadCloser, error) {
	m.mu.Lock()
	m.read = append(m.read, [2]int64{start, end})
	m.mu.Unlock()
	off := l.Files[fi].Offset
	return io.NopCloser(bytes.NewReader(m.data[off+start : off+end+1])), nil
}

// progressiveServer: a server whose layout for ih is pre-loaded and whose sources are fakes.
func progressiveServer(t *testing.T, files []torrentfile.TorrentFile, pl int64, srcs ...seedsrc.Source) (*Server, *seedsrc.Layout, string) {
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

func webseedReq(s *Server, ih, p, rng string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("GET", "/webseed/hash/"+ih+"/"+p, nil)
	r.SetPathValue("path", p)
	if rng != "" {
		r.Header.Set("Range", rng)
	}
	w := httptest.NewRecorder()
	if !s.serveWebseedProgressive(w, r, ih) {
		w.Code = -1
	}
	return w
}

func TestProgressiveWebseedRanges(t *testing.T) {
	// 100-byte pieces: a.mkv(250) | pad(50) | b.srt(120)  → 420 B, 5 pieces
	files := []torrentfile.TorrentFile{{Path: "T/a.mkv", Size: 250}, {Path: "T/.pad/50", Size: 50, Pad: true}, {Path: "T/b.srt", Size: 120}}
	data := make([]byte, 420)
	for i := range data {
		data[i] = byte(i % 251)
	}
	for i := 250; i < 300; i++ {
		data[i] = 0
	}
	q1 := &memSource{id: "qb:1", data: data}
	q2 := &memSource{id: "qb:2", data: data}
	s, _, ih := progressiveServer(t, files, 100, q1, q2)
	q1.set(2, 0, 0, 0, 0) // piece 0 only
	q2.set(0, 2, 2, 0, 0) // pieces 1-2

	// a[0..99] = piece 0 → q1
	w := webseedReq(s, ih, "T/a.mkv", "bytes=0-99")
	if w.Code != 206 || !bytes.Equal(w.Body.Bytes(), data[0:100]) || w.Header().Get("Content-Range") != "bytes 0-99/250" || w.Header().Get("Content-Type") != "video/x-matroska" {
		t.Fatalf("a[0..99]: %d %q %v", w.Code, w.Header(), len(w.Body.Bytes()))
	}
	// a[100..249] = pieces 1-2 → only q2 has both
	w = webseedReq(s, ih, "T/a.mkv", "bytes=100-249")
	if w.Code != 206 || !bytes.Equal(w.Body.Bytes(), data[100:250]) || len(q2.read) != 1 {
		t.Fatalf("a[100..249]: %d (q2 reads %v)", w.Code, q2.read)
	}
	// a[50..149] spans pieces 0-1: on no single server → 503 + Retry-After
	w = webseedReq(s, ih, "T/a.mkv", "bytes=50-149")
	if w.Code != 503 || w.Header().Get("Retry-After") == "" {
		t.Fatalf("split range: %d %v", w.Code, w.Header())
	}
	// b.srt (pieces 3-4) missing everywhere → 503
	if w = webseedReq(s, ih, "T/b.srt", ""); w.Code != 503 {
		t.Fatalf("missing file: %d", w.Code)
	}
	// padding file: zeros, no source needed
	w = webseedReq(s, ih, "T/.pad/50", "bytes=10-19")
	if w.Code != 206 || !bytes.Equal(w.Body.Bytes(), make([]byte, 10)) {
		t.Fatalf("pad: %d %v", w.Code, w.Body.Bytes())
	}
	// unknown file → 404, bad range → 416
	if w = webseedReq(s, ih, "T/nope", ""); w.Code != 404 {
		t.Fatalf("unknown file: %d", w.Code)
	}
	if w = webseedReq(s, ih, "T/a.mkv", "bytes=900-"); w.Code != 416 || w.Header().Get("Content-Range") != "bytes */250" {
		t.Fatalf("416: %d %v", w.Code, w.Header())
	}
	// whole file without Range once it is complete → 200
	q2.set(2, 2, 2, 2, 2)
	time.Sleep(5 * time.Millisecond)
	w = webseedReq(s, ih, "T/b.srt", "")
	if w.Code != 200 || !bytes.Equal(w.Body.Bytes(), data[300:420]) || w.Header().Get("Content-Length") != "120" {
		t.Fatalf("b.srt full: %d", w.Code)
	}
	// progressive off: a range of an incomplete file is refused even if its pieces exist
	q2.set(0, 2, 2, 0, 0)
	time.Sleep(5 * time.Millisecond)
	setSetting(t, s, store.KeyWebseedProgressive, false)
	if w = webseedReq(s, ih, "T/a.mkv", "bytes=100-149"); w.Code != 503 {
		t.Fatalf("progressive off: %d", w.Code)
	}
	// no layout → not handled (caller falls back to whole-file lookup)
	if w = webseedReq(s, strings.Repeat("ef", 20), "x", ""); w.Code != -1 {
		t.Fatalf("no layout: handled with %d", w.Code)
	}
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
	mainSrc := &memSource{id: "qb:1"}
	edgeQB := &memSource{id: "qb:5"}
	edgeSrc := &seedsrc.Edge{ServerID: 5, ServerName: "ams", BaseURL: "https://edge.test", Client: fakeEdge{edgeQB}}
	s, _, ih := progressiveServer(t, files, 100, mainSrc, edgeSrc) // 7 pieces; a = 0..5, b = 6
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
	mainURL := "https://example.test/webseed/hash/" + ih + "/"
	edgeURL := "https://edge.test/webseed/hash/" + ih + "/"
	keys := s.edgePublicKeys()
	pub, _ := edgetoken.DecodePublic(keys[0])
	checkToken := func(tok any, exp any) {
		t.Helper()
		c, err := edgetoken.Verify(tok.(string), []ed25519.PublicKey{pub}, 5, ih, time.Now(), 0)
		if err != nil || c.Role != store.RoleAdmin || int64(exp.(float64)) != c.Exp.Unix() {
			t.Fatalf("token: %+v %v", c, err)
		}
		if _, err := edgetoken.Verify(tok.(string), []ed25519.PublicKey{pub}, 6, ih, time.Now(), 0); !errors.Is(err, edgetoken.ErrAudience) {
			t.Fatal("token accepted by another edge")
		}
	}

	waitFor("waiting") // nobody has the metadata yet
	mainSrc.set(2, 2, 0, 0, 0, 0, 0)
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
	if m := byURL[mainURL]; m == nil || !reflect.DeepEqual(m["have"], []any{0.0, 1.0}) || m["token"] != nil {
		t.Fatalf("main seed = %v", m)
	}
	em := byURL[edgeURL]
	if em == nil || !reflect.DeepEqual(em["have"], []any{0.0, 3.0}) {
		t.Fatalf("edge seed = %v", em)
	}
	checkToken(em["token"], em["exp"])

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
	checkToken(tk.data["token"], tk.data["exp"])

	edgeQB.set(2, 2, 2, 2, 2, 2, 2)
	if e := waitFor("complete"); e.data["url"] != edgeURL {
		t.Fatalf("complete = %v", e.data)
	}
	mainSrc.set(2, 2, 2, 2, 2, 2, 2)
	if e := waitFor("complete"); e.data["url"] != mainURL {
		t.Fatalf("complete = %v", e.data)
	}
	waitFor("done")
	<-done
}

func TestWebseedStreamForbidden(t *testing.T) {
	files := []torrentfile.TorrentFile{{Path: "T/a.mkv", Size: 100}}
	src := &memSource{id: "qb:1"}
	s, _, ih := progressiveServer(t, files, 100, src)
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
