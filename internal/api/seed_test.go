package api

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/darknightlab/onlinetorrent/internal/meta"
	"github.com/darknightlab/onlinetorrent/internal/store"
)

// selQB is a fake qB that keeps file priorities (see internal/qb/seed_test.go for the
// semantics, verified against qB 5.2.4).
type selQB struct {
	mu      sync.Mutex
	files   []string
	sizes   []int64
	prio    []int
	present bool
	stopped bool
	adds    int
	deleted bool
}

func (f *selQB) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/") {
			_ = r.ParseMultipartForm(1 << 20)
		} else {
			_ = r.ParseForm()
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		q := r.FormValue
		switch strings.TrimPrefix(r.URL.Path, "/api/v2/") {
		case "auth/login":
			w.WriteHeader(204)
		case "app/version":
			io.WriteString(w, "v5.3.0")
		case "app/buildInfo":
			io.WriteString(w, `{"libtorrent":"2.1.1.0"}`)
		case "torrents/add":
			f.adds++
			if !f.present {
				f.present, f.stopped = true, q("stopped") == "true"
				f.prio = make([]int, len(f.files))
				for i := range f.prio {
					f.prio[i] = 1
				}
			}
			io.WriteString(w, "Ok.")
		case "torrents/info":
			if !f.present || (q("category") != "" && q("category") != "projectk") {
				io.WriteString(w, "[]")
				return
			}
			_ = json.NewEncoder(w).Encode([]map[string]any{{"hash": q("hashes"), "name": "Pack", "state": "downloading", "has_metadata": true, "category": "projectk"}})
		case "torrents/files":
			if !f.present {
				w.WriteHeader(404)
				return
			}
			var out []map[string]any
			for i, n := range f.files {
				out = append(out, map[string]any{"index": i, "name": n, "size": f.sizes[i], "priority": f.prio[i]})
			}
			_ = json.NewEncoder(w).Encode(out)
		case "torrents/filePrio":
			p, _ := strconv.Atoi(q("priority"))
			for _, s := range strings.Split(q("id"), "|") {
				i, _ := strconv.Atoi(s)
				f.prio[i] = p
			}
		case "torrents/start":
			f.stopped = false
		case "torrents/delete":
			f.deleted = true
		}
	})
}

// packTorrent builds a 3-episode .torrent with a BEP 47 padding file between episodes 1 and 2
// (player / .torrent index 2 = ep02; qB hides the pad: its index 1).
func packTorrent() ([]byte, string, []string, []int64) {
	tor, ih := buildPack([]string{"ep01.mkv", ".pad/1", "ep02.mkv", "ep03.mkv"}, []int64{700_000, 1, 700_000, 700_000})
	return tor, ih, []string{"Pack/ep01.mkv", "Pack/ep02.mkv", "Pack/ep03.mkv"}, []int64{700_000, 700_000, 700_000}
}

// buildPack builds a multi-file .torrent named Pack (".pad/…" entries are BEP 47 padding files).
func buildPack(names []string, sizes []int64) ([]byte, string) {
	var fl strings.Builder
	var total int64
	for i, n := range names {
		var path strings.Builder
		for _, seg := range strings.Split(n, "/") {
			fmt.Fprintf(&path, "%d:%s", len(seg), seg)
		}
		attr := ""
		if strings.HasPrefix(n, ".pad") {
			attr = "4:attr1:p"
		}
		fmt.Fprintf(&fl, "d%s6:lengthi%de4:pathl%see", attr, sizes[i], path.String())
		total += sizes[i]
	}
	const pl = 1 << 18
	n := (total + pl - 1) / pl
	info := fmt.Sprintf("d5:filesl%se4:name4:Pack12:piece lengthi%de6:pieces%d:%se", fl.String(), pl, 20*n, strings.Repeat("x", int(20*n)))
	sum := sha1.Sum([]byte(info))
	return []byte("d4:info" + info + "e"), hex.EncodeToString(sum[:])
}

func TestSeedOnlyRequestedFiles(t *testing.T) {
	s, h := testServer(t)
	ctx := context.Background()
	tor, ih, qbNames, qbSizes := packTorrent()
	fq := &selQB{files: qbNames, sizes: qbSizes}
	qsrv := httptest.NewServer(fq.handler())
	defer qsrv.Close()
	if _, err := s.St.SaveQBServer(ctx, store.QBServer{Name: "fake", URL: qsrv.URL, Username: "u", Password: "p", Enabled: true, Category: "projectk"}); err != nil {
		t.Fatal(err)
	}
	_ = s.Pool.Reload(ctx)
	s.Pool.CheckAll(ctx)
	// known torrent: .torrent stored, catalogue ready, opened via play
	if err := s.Meta.PutMetainfo(ctx, ih, tor, meta.MetainfoQB); err != nil {
		t.Fatal(err)
	}
	var files []meta.FileInput
	for i, n := range qbNames {
		files = append(files, meta.FileInput{Index: i, Path: n, Size: qbSizes[i]})
	}
	if err := s.Meta.Match(ctx, meta.TorrentInput{Infohash: ih, Title: "Pack", Source: "user", SourceKind: "user", Files: files}); err != nil {
		t.Fatal(err)
	}
	_ = s.St.UpsertTorrent(ctx, store.TorrentRecord{Infohash: ih, Title: "Pack", Size: 2_100_001, Source: "user"})
	// one episode (0.7 MB) fits 1 MiB; the whole pack (2.1 MB) exceeds even 2 MiB
	setSetting(t, s, store.KeyMaxPushSize, 1)
	member := loginAs(t, s, "alice", store.RoleMember)
	seed := func(body, cookie string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", "/api/v1/torrents/hash/"+ih+"/seed", strings.NewReader(body))
		req.Header.Set("X-Requested-With", "fetch")
		req.Header.Set("Content-Type", "application/json")
		if cookie != "" {
			req.AddCookie(&http.Cookie{Name: sessionCookie, Value: cookie})
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	prio := func() []int {
		fq.mu.Lock()
		defer fq.mu.Unlock()
		return slices.Clone(fq.prio)
	}
	// guests can't trigger downloads (default permission: members)
	if rec := seed(`{"file":2}`, ""); rec.Code != 401 || fq.adds != 0 {
		t.Fatalf("guest: %d", rec.Code)
	}
	// multi-file torrent without a file / a padding file / out of range → 400
	for _, body := range []string{``, `{}`, `{"file":1}`, `{"file":9}`, `{"file":-1}`} {
		if rec := seed(body, member); rec.Code != 400 {
			t.Fatalf("body %q: %d %s", body, rec.Code, rec.Body)
		}
	}
	// episode 2 (player index 2, after the padding file): only it is downloaded
	rec := seed(`{"file":2}`, member)
	if rec.Code != 200 || strings.TrimSpace(rec.Body.String()) != `{"seeded":true}` {
		t.Fatalf("seed ep02: %d %s", rec.Code, rec.Body)
	}
	if got := prio(); !slices.Equal(got, []int{0, 1, 0}) || fq.stopped {
		t.Fatalf("priorities after ep02: %v (stopped %v)", got, fq.stopped)
	}
	// episode 1 too would make 1.4 MB wanted > 1 MiB → 413, nothing changes
	rec = seed(`{"file":0}`, member)
	if rec.Code != 413 || !strings.Contains(rec.Body.String(), "too_large") {
		t.Fatalf("over the limit: %d %s", rec.Code, rec.Body)
	}
	if got := prio(); !slices.Equal(got, []int{0, 1, 0}) {
		t.Fatalf("priorities changed by a refused request: %v", got)
	}
	// the same episode again is fine (sum unchanged)
	if rec := seed(`{"file":2}`, member); rec.Code != 200 {
		t.Fatalf("repeat: %d", rec.Code)
	}
	// 2 MiB: episode 1 is added (1.4 MB wanted although the pack is 2.1 MB), episode 2 stays
	setSetting(t, s, store.KeyMaxPushSize, 2)
	if rec := seed(`{"file":0}`, member); rec.Code != 200 {
		t.Fatalf("ep01: %d %s", rec.Code, rec.Body)
	}
	if got := prio(); !slices.Equal(got, []int{1, 1, 0}) {
		t.Fatalf("priorities after ep01: %v", got)
	}
	// recheck judges the wanted files (1.4 MB), not the pack (2.1 MB): kept under 2 MiB…
	qfiles, err := s.Pool.Servers()[0].Client.Files(ctx, ih)
	if err != nil {
		t.Fatal(err)
	}
	s.checkSeeded(ctx, s.Policy.Generation(), ih, "Pack", qfiles, nil, false)
	if fq.deleted {
		t.Fatal("recheck removed a torrent whose wanted files fit the limit")
	}
	// …removed under 1 MiB (untrusted user torrent)
	setSetting(t, s, store.KeyMaxPushSize, 1)
	s.checkSeeded(ctx, s.Policy.Generation(), ih, "Pack", qfiles, nil, false)
	if !fq.deleted {
		t.Fatal("recheck kept a torrent whose wanted files exceed the limit")
	}
}

func TestCompanions(t *testing.T) {
	files := []seedFile{
		{Path: "S/Ep 01.mkv", Size: 1 << 30},
		{Path: "S/Ep 01.sc.ass", Size: 40 << 10},
		{Path: "S/Ep 01.tc.ASS", Size: 40 << 10},
		{Path: "S/Ep 02.mkv", Size: 1 << 30}, // a video: never a companion
		{Path: "S/notes.txt", Size: 1 << 10}, // not a subtitle / font
		{Path: "S/.pad/1", Size: 100, Pad: true},
		{Path: "S/Fonts/big.ttc", Size: 40 << 20}, // over the per-file limit
		{Path: "S/Fonts/a.otf", Size: 32 << 20},
		{Path: "S/Fonts/b.ttf", Size: 32 << 20}, // a + b + subs > 64 MB: b dropped
	}
	got := companions(files, 0, []int{1, 2, 2, 3, 4, 5, 6, 7, 8, 0, -1, 99})
	want := []string{"S/Ep 01.sc.ass", "S/Ep 01.tc.ASS", "S/Fonts/a.otf"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("companions = %q, want %q", got, want)
	}
	if got := companions(files, 0, nil); len(got) != 0 {
		t.Fatalf("no extra: %q", got)
	}
}

// The player adds the video's external subtitles (and the release's fonts) as "extra": qB
// downloads them too; anything that isn't a subtitle / font file is ignored.
func TestSeedWithCompanions(t *testing.T) {
	s, h := testServer(t)
	ctx := context.Background()
	names := []string{"ep01.mkv", "ep01.sc.ass", "ep02.mkv", "notes.txt"}
	sizes := []int64{700_000, 40_000, 700_000, 1_000}
	tor, ih := buildPack(names, sizes)
	var qbNames []string
	for _, n := range names {
		qbNames = append(qbNames, "Pack/"+n)
	}
	fq := &selQB{files: qbNames, sizes: sizes}
	qsrv := httptest.NewServer(fq.handler())
	defer qsrv.Close()
	if _, err := s.St.SaveQBServer(ctx, store.QBServer{Name: "fake", URL: qsrv.URL, Username: "u", Password: "p", Enabled: true, Category: "projectk"}); err != nil {
		t.Fatal(err)
	}
	_ = s.Pool.Reload(ctx)
	s.Pool.CheckAll(ctx)
	if err := s.Meta.PutMetainfo(ctx, ih, tor, meta.MetainfoQB); err != nil {
		t.Fatal(err)
	}
	var files []meta.FileInput
	for i, n := range qbNames {
		files = append(files, meta.FileInput{Index: i, Path: n, Size: sizes[i]})
	}
	if err := s.Meta.Match(ctx, meta.TorrentInput{Infohash: ih, Title: "Pack", Source: "user", SourceKind: "user", Files: files}); err != nil {
		t.Fatal(err)
	}
	_ = s.St.UpsertTorrent(ctx, store.TorrentRecord{Infohash: ih, Title: "Pack", Size: 1_441_000, Source: "user"})
	member := loginAs(t, s, "alice", store.RoleMember)
	req := httptest.NewRequest("POST", "/api/v1/torrents/hash/"+ih+"/seed", strings.NewReader(`{"file":0,"extra":[1,2,3,1,7]}`))
	req.Header.Set("X-Requested-With", "fetch")
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: member})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("seed: %d %s", rec.Code, rec.Body)
	}
	fq.mu.Lock()
	got := slices.Clone(fq.prio)
	fq.mu.Unlock()
	if !slices.Equal(got, []int{1, 1, 0, 0}) {
		t.Fatalf("priorities: %v, want video + its subtitles only", got)
	}
}
