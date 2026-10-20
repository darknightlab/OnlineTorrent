package qb

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/darknightlab/onlinetorrent/internal/store"
)

// fakeQB is a stateful qB WebUI with the semantics verified against qB 5.2.4 (see
// selection.go): .torrent adds have metadata at once, magnet adds only after metadataAt;
// hybrid torrents are known by their truncated v2 id after metadata.
type fakeQB struct {
	mu    sync.Mutex
	t     map[string]*fakeT // qB id → torrent
	calls []string
	srv   *httptest.Server
}

type fakeT struct {
	id, v1     string
	category   string
	tags       map[string]bool
	stopped    bool
	hasMeta    bool
	metaAt     time.Time // magnet: metadata arrives then
	stopOnMeta bool
	files      []string
	prio       []int
	added      int64
}

func newFake(t *testing.T) *fakeQB {
	f := &fakeQB{t: map[string]*fakeT{}}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

var fakeFiles = []string{"Pack/ep01.mkv", "Pack/ep02.mkv", "Pack/ep03.mkv"}

func (f *fakeQB) lookup(h string) *fakeT {
	if t := f.t[h]; t != nil {
		return t
	}
	return nil
}

func (f *fakeQB) tick() {
	for _, t := range f.t {
		if !t.hasMeta && !t.metaAt.IsZero() && time.Now().After(t.metaAt) {
			t.hasMeta = true
			t.files = slices.Clone(fakeFiles)
			t.prio = []int{1, 1, 1}
			if t.stopOnMeta {
				t.stopped = true
			}
			if strings.HasPrefix(t.v1, "e") && t.id == t.v1 { // "hybrid": id switches to the (truncated) v2 one
				delete(f.t, t.id)
				t.id = "f" + t.id[1:]
				f.t[t.id] = t
			}
		}
	}
}

func (f *fakeQB) serve(w http.ResponseWriter, r *http.Request) {
	ct := r.Header.Get("Content-Type")
	if strings.HasPrefix(ct, "multipart/") {
		_ = r.ParseMultipartForm(1 << 20)
	} else {
		_ = r.ParseForm()
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.tick()
	p := strings.TrimPrefix(r.URL.Path, "/api/v2/")
	q := r.FormValue
	f.calls = append(f.calls, p)
	js := func(v any) { _ = json.NewEncoder(w).Encode(v) }
	t := f.lookup(q("hash"))
	if t == nil {
		t = f.lookup(q("hashes"))
	}
	switch p {
	case "auth/login":
		w.WriteHeader(204)
	case "torrents/add":
		var tor *fakeT
		if r.MultipartForm != nil && len(r.MultipartForm.File["torrents"]) > 0 {
			fh, _ := r.MultipartForm.File["torrents"][0].Open()
			b, _ := io.ReadAll(fh)
			ih := string(b) // the fake's ".torrent" is the infohash
			tor = &fakeT{id: ih, v1: ih, hasMeta: true, files: slices.Clone(fakeFiles), prio: []int{1, 1, 1}, stopped: q("stopped") == "true"}
		} else {
			u := q("urls")
			ih := strings.ToLower(strings.SplitN(strings.SplitN(u, "btih:", 2)[1], "&", 2)[0])
			tor = &fakeT{id: ih, v1: ih, metaAt: time.Now().Add(300 * time.Millisecond), stopOnMeta: q("stopCondition") == "MetadataReceived"}
		}
		if f.t[tor.id] != nil {
			w.WriteHeader(409)
			return
		}
		tor.category, tor.tags, tor.added = q("category"), map[string]bool{}, time.Now().Unix()
		for _, x := range strings.Split(q("tags"), ",") {
			if x != "" {
				tor.tags[x] = true
			}
		}
		f.t[tor.id] = tor
		io.WriteString(w, "Ok.")
	case "torrents/info":
		var out []map[string]any
		for _, x := range f.t {
			if h := q("hashes"); h != "" && h != x.id {
				continue
			}
			if c := q("category"); c != "" && c != x.category {
				continue
			}
			state := "downloading"
			if x.stopped {
				state = "stoppedDL"
			}
			var tags []string
			for k := range x.tags {
				tags = append(tags, k)
			}
			m := map[string]any{"hash": x.id, "name": "Pack", "state": state, "has_metadata": x.hasMeta, "category": x.category, "tags": strings.Join(tags, ", "), "added_on": x.added}
			if x.v1 != x.id {
				m["infohash_v1"] = x.v1
			}
			out = append(out, m)
		}
		if out == nil {
			io.WriteString(w, "[]")
			return
		}
		js(out)
	case "torrents/files":
		if t == nil {
			w.WriteHeader(404)
			return
		}
		out := []map[string]any{}
		for i, n := range t.files {
			out = append(out, map[string]any{"index": i, "name": n, "size": 1000 * (i + 1), "priority": t.prio[i]})
		}
		js(out)
	case "torrents/filePrio":
		if t == nil {
			w.WriteHeader(404)
			return
		}
		if !t.hasMeta {
			w.WriteHeader(409)
			return
		}
		pr, _ := strconv.Atoi(q("priority"))
		for _, s := range strings.Split(q("id"), "|") {
			i, _ := strconv.Atoi(s)
			t.prio[i] = pr
		}
	case "torrents/start":
		if t != nil {
			t.stopped = false
		}
	case "torrents/setCategory":
		if t != nil {
			t.category = q("category")
		}
	case "torrents/addTags", "torrents/removeTags":
		if t != nil {
			for _, x := range strings.Split(q("tags"), ",") {
				if p == "torrents/addTags" {
					t.tags[x] = true
				} else {
					delete(t.tags, x)
				}
			}
		}
	case "torrents/delete":
		for _, h := range strings.Split(q("hashes"), "|") {
			delete(f.t, h)
		}
	}
}

func (f *fakeQB) get(v1 string) *fakeT {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.tick()
	for _, t := range f.t {
		if t.v1 == v1 {
			c := *t
			c.prio = slices.Clone(t.prio)
			return &c
		}
	}
	return nil
}

func (f *fakeQB) take() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	c := f.calls
	f.calls = nil
	return c
}

func testPool(t *testing.T, f *fakeQB) (*Pool, *Server) {
	t.Helper()
	p := &Pool{log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	s := &Server{Cfg: store.QBServer{ID: 1, Name: "fake", Category: "projectk", Enabled: true}, Client: NewClient(f.srv.URL, "u", "p", "")}
	s.Client.SetIDCategories("projectk", "projectk-meta")
	s.setStatus(Status{Online: true})
	p.servers = []*Server{s}
	return p, s
}

func idx(calls []string, name string) int { return slices.Index(calls, name) }

func TestSeedNewTorrentOnlyWantedFile(t *testing.T) {
	f := newFake(t)
	p, _ := testPool(t, f)
	ih := strings.Repeat("a1", 20)
	ctx := context.Background()
	res := p.Seed(ctx, SeedRequest{Infohash: ih, Name: "Pack", Torrent: []byte(ih), Want: []string{"Pack/ep02.mkv"}, Timeout: 5 * time.Second})
	if len(res.SeededBy) != 1 {
		t.Fatalf("seed: %+v", res)
	}
	tor := f.get(ih)
	if !slices.Equal(tor.prio, []int{0, 1, 0}) || tor.stopped || tor.tags[PendingTag] || tor.category != "projectk" {
		t.Fatalf("after first request: %+v", tor)
	}
	calls := f.take()
	// added stopped; priorities set before it is started
	if a, fp, st := idx(calls, "torrents/add"), idx(calls, "torrents/filePrio"), idx(calls, "torrents/start"); a < 0 || fp < a || st < fp {
		t.Fatalf("call order: %v", calls)
	}
	if got := p.Wanted(ctx, ih); !got["Pack/ep02.mkv"] || len(got) != 1 {
		t.Fatalf("Wanted = %v", got)
	}
	// another episode: raised, nothing lowered, started (to download it)
	p.Seed(ctx, SeedRequest{Infohash: ih, Torrent: []byte(ih), Want: []string{"Pack/ep03.mkv"}, Timeout: 5 * time.Second})
	if tor := f.get(ih); !slices.Equal(tor.prio, []int{0, 1, 1}) {
		t.Fatalf("after second request: %v", tor.prio)
	}
	if calls := f.take(); idx(calls, "torrents/add") >= 0 || idx(calls, "torrents/start") < 0 {
		t.Fatalf("second request calls: %v", calls)
	}
	// the same episode again: no priority change, no (re)start
	p.Seed(ctx, SeedRequest{Infohash: ih, Torrent: []byte(ih), Want: []string{"Pack/ep03.mkv"}, Timeout: 5 * time.Second})
	if calls := f.take(); idx(calls, "torrents/filePrio") >= 0 || idx(calls, "torrents/start") >= 0 {
		t.Fatalf("repeat request calls: %v", calls)
	}
	if got := p.Wanted(ctx, ih); len(got) != 2 {
		t.Fatalf("Wanted = %v", got)
	}
}

func TestSeedAdoptsMetadataTorrent(t *testing.T) {
	f := newFake(t)
	p, _ := testPool(t, f)
	ih := strings.Repeat("b2", 20)
	f.mu.Lock()
	f.t[ih] = &fakeT{id: ih, v1: ih, category: "projectk-meta", hasMeta: true, stopped: true, files: slices.Clone(fakeFiles), prio: []int{1, 1, 1}, tags: map[string]bool{}}
	f.mu.Unlock()
	p.Seed(context.Background(), SeedRequest{Infohash: ih, Torrent: []byte(ih), Want: []string{"Pack/ep01.mkv"}, Timeout: 5 * time.Second})
	tor := f.get(ih)
	if tor.category != "projectk" || !slices.Equal(tor.prio, []int{1, 0, 0}) || tor.stopped {
		t.Fatalf("adopted: %+v", tor)
	}
}

func TestSeedMagnetHybridAndMergedRequests(t *testing.T) {
	f := newFake(t)
	p, _ := testPool(t, f)
	v1 := "e" + strings.Repeat("c3", 19) + "c"
	ctx := context.Background()
	// no .torrent: magnet held at metadata; a second episode is requested while waiting
	p.Seed(ctx, SeedRequest{Infohash: v1, Name: "Pack", Want: []string{"Pack/ep01.mkv"}, Timeout: 5 * time.Second})
	p.Seed(ctx, SeedRequest{Infohash: v1, Name: "Pack", Want: []string{"Pack/ep03.mkv"}, Timeout: 5 * time.Second})
	if got := p.Wanted(ctx, v1); !got["Pack/ep01.mkv"] || !got["Pack/ep03.mkv"] {
		t.Fatalf("pending Wanted = %v", got)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		tor := f.get(v1)
		if tor != nil && tor.hasMeta && !tor.stopped && !tor.tags[PendingTag] {
			// hybrid: qB knows it by another id now; requests by v1 still worked
			if tor.id == v1 || !slices.Equal(tor.prio, []int{1, 0, 1}) {
				t.Fatalf("applied: %+v", tor)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("selection never applied: %+v", tor)
		}
		time.Sleep(50 * time.Millisecond)
	}
	ts, err := p.Servers()[0].Client.Info(ctx, "", v1)
	if err != nil || len(ts) != 1 || ts[0].Hash != v1 || ts[0].ID == v1 {
		t.Fatalf("Info by v1 = %+v %v", ts, err)
	}
	// raising works through the id translation as well
	p.Seed(ctx, SeedRequest{Infohash: v1, Want: []string{"Pack/ep02.mkv"}, Timeout: 5 * time.Second})
	if tor := f.get(v1); !slices.Equal(tor.prio, []int{1, 1, 1}) {
		t.Fatalf("raise via v1: %v", tor.prio)
	}
}

func TestCleanupStuckPending(t *testing.T) {
	f := newFake(t)
	p, s := testPool(t, f)
	old := time.Now().Add(-time.Hour).Unix()
	f.mu.Lock()
	for _, x := range []struct {
		ih      string
		pending bool
		stopped bool
	}{{"d1", true, true}, {"d2", true, true}, {"d3", false, true}, {"d4", true, false}} {
		ih := strings.Repeat(x.ih, 20)
		tags := map[string]bool{}
		if x.pending {
			tags[PendingTag] = true
		}
		f.t[ih] = &fakeT{id: ih, v1: ih, category: "projectk", hasMeta: true, stopped: x.stopped, files: slices.Clone(fakeFiles), prio: []int{1, 1, 1}, tags: tags, added: old}
	}
	f.mu.Unlock()
	// an in-process job protects its torrent
	j := p.job(s, strings.Repeat("d2", 20))
	_ = j
	p.Cleanup(context.Background())
	if f.get(strings.Repeat("d1", 20)) != nil {
		t.Error("stuck pending torrent kept")
	}
	if f.get(strings.Repeat("d2", 20)) == nil || f.get(strings.Repeat("d3", 20)) == nil || f.get(strings.Repeat("d4", 20)) == nil {
		t.Error("deleted a torrent that is not stuck")
	}
}

func TestMatchFile(t *testing.T) {
	files := []File{{Index: 0, Name: "Pack/a.mkv"}, {Index: 1, Name: "Pack/sub/b.ass"}}
	if f, ok := MatchFile(files, "Pack/sub/b.ass"); !ok || f.Index != 1 {
		t.Fatal("exact")
	}
	// qB without the root folder
	if f, ok := MatchFile([]File{{Index: 0, Name: "a.mkv"}, {Index: 1, Name: "sub/b.ass"}}, "Pack/sub/b.ass"); !ok || f.Index != 1 {
		t.Fatal("root stripped")
	}
	if _, ok := MatchFile(files, "Pack/c.mkv"); ok {
		t.Fatal("unknown file matched")
	}
}
