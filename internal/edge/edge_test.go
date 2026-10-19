package edge

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/darknightlab/onlinetorrent/internal/edgetoken"
	"github.com/darknightlab/onlinetorrent/internal/qb"
	"github.com/darknightlab/onlinetorrent/internal/tunnel"
)

// ---------------------------------------------------------------- test torrent

type tfile struct {
	path string // inside the torrent, without the root name
	size int64
	pad  bool
}

func bencode(v any) []byte {
	var b bytes.Buffer
	var enc func(any)
	enc = func(v any) {
		switch x := v.(type) {
		case int64:
			fmt.Fprintf(&b, "i%de", x)
		case int:
			fmt.Fprintf(&b, "i%de", x)
		case string:
			fmt.Fprintf(&b, "%d:%s", len(x), x)
		case []byte:
			fmt.Fprintf(&b, "%d:", len(x))
			b.Write(x)
		case []any:
			b.WriteByte('l')
			for _, e := range x {
				enc(e)
			}
			b.WriteByte('e')
		case map[string]any:
			keys := make([]string, 0, len(x))
			for k := range x {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			b.WriteByte('d')
			for _, k := range keys {
				enc(k)
				enc(x[k])
			}
			b.WriteByte('e')
		}
	}
	enc(v)
	return b.Bytes()
}

// makeTorrent returns the .torrent, its infohash and the concatenated data (pads = zeros).
func makeTorrent(name string, pl int64, files []tfile) ([]byte, string, []byte) {
	rng := rand.New(rand.NewSource(7))
	var data []byte
	var flist []any
	for _, f := range files {
		d := make([]byte, f.size)
		if !f.pad {
			rng.Read(d)
		}
		data = append(data, d...)
		e := map[string]any{"length": f.size, "path": []any{}}
		for _, seg := range strings.Split(f.path, "/") {
			e["path"] = append(e["path"].([]any), seg)
		}
		if f.pad {
			e["attr"] = "p"
		}
		flist = append(flist, e)
	}
	var pieces []byte
	for off := int64(0); off < int64(len(data)); off += pl {
		sum := sha1.Sum(data[off:min(off+pl, int64(len(data)))])
		pieces = append(pieces, sum[:]...)
	}
	info := map[string]any{"name": name, "piece length": pl, "pieces": pieces, "files": flist}
	ib := bencode(info)
	sum := sha1.Sum(ib)
	return bencode(map[string]any{"info": info}), hex.EncodeToString(sum[:]), data
}

// ---------------------------------------------------------------- fake qB WebUI

type fakeQB struct {
	srv       *httptest.Server
	mu        sync.Mutex
	torrent   []byte
	ih        string
	states    []int
	save      string
	names     []string // qB file names (padding files hidden)
	stateHits atomic.Int64
	refererOK atomic.Int64
	refererNo atomic.Int64
}

func newFakeQB(t *testing.T) *fakeQB {
	f := &fakeQB{}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// qB CSRF check: Referer must point at qB itself
		if ref := r.Header.Get("Referer"); ref != "" && !strings.HasPrefix(ref, f.srv.URL) {
			f.refererNo.Add(1)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		f.refererOK.Add(1)
		if r.URL.Path == "/api/v2/auth/login" {
			_ = r.ParseForm()
			if r.Form.Get("username") != "u" || r.Form.Get("password") != "p" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			http.SetCookie(w, &http.Cookie{Name: "SID", Value: "s1", Path: "/"})
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if c, err := r.Cookie("SID"); err != nil || c.Value != "s1" {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		hash := r.URL.Query().Get("hash")
		known := hash == f.ih
		switch r.URL.Path {
		case "/api/v2/app/version":
			io.WriteString(w, "v5.3.0beta1")
		case "/api/v2/torrents/pieceStates":
			f.stateHits.Add(1)
			if !known {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			_ = json.NewEncoder(w).Encode(f.states)
		case "/api/v2/torrents/export":
			if !known {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			w.Write(f.torrent)
		case "/api/v2/torrents/properties":
			if !known {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"save_path": f.save, "piece_size": 0})
		case "/api/v2/torrents/files":
			var out []map[string]any
			for i, n := range f.names {
				out = append(out, map[string]any{"index": i, "name": n, "size": 0, "progress": 0})
			}
			_ = json.NewEncoder(w).Encode(out)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeQB) setStates(s ...int) {
	f.mu.Lock()
	f.states = s
	f.mu.Unlock()
}

// fixture: torrent "T" with a.mkv (250) | pad (50) | sub/b.srt (120), 100-byte pieces (5 pieces).
// On disk (qB's save path = <root>/qb/files): a.mkv.!qB (unfinished, sparse), sub/b.srt (done).
type fixture struct {
	qb   *fakeQB
	root string
	ih   string
	data []byte
	disk *Disk
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	files := []tfile{{"a.mkv", 250, false}, {".pad/50", 50, true}, {"sub/b.srt", 120, false}}
	tor, ih, data := makeTorrent("T", 100, files)
	root := t.TempDir()
	save := filepath.Join(root, "qb", "files")
	if err := os.MkdirAll(filepath.Join(save, "T", "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	// a.mkv unfinished: sparse, only bytes 0..199 (pieces 0, 1) written
	fa, _ := os.Create(filepath.Join(save, "T", "a.mkv.!qB"))
	_ = fa.Truncate(250)
	_, _ = fa.WriteAt(data[0:200], 0)
	fa.Close()
	_ = os.WriteFile(filepath.Join(save, "T", "sub", "b.srt"), data[300:420], 0o644)
	q := newFakeQB(t)
	q.torrent, q.ih, q.save = tor, ih, filepath.ToSlash(save)
	q.names = []string{"T/a.mkv", "T/sub/b.srt"}
	q.states = []int{2, 2, 1, 2, 2} // piece 2 (a.mkv 200..249 + pad) still downloading
	d := &Disk{QB: qb.NewClient(q.srv.URL, "u", "p", ""), Root: root, Verify: true, HaveTTL: time.Hour}
	return &fixture{qb: q, root: root, ih: ih, data: data, disk: d}
}

func readAll(rc io.ReadCloser, err error) ([]byte, error) {
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	return io.ReadAll(rc)
}

func TestDiskReadRange(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	// a.mkv bytes 0..199 (pieces 0-1, whole pieces → SHA-1 checked) from "a.mkv.!qB"
	got, err := readAll(f.disk.ReadRange(ctx, f.ih, 0, 0, 199))
	if err != nil || !bytes.Equal(got, f.data[0:200]) {
		t.Fatalf("a[0..199]: %v", err)
	}
	// partial piece read (no hash check possible) still fine
	got, err = readAll(f.disk.ReadRange(ctx, f.ih, 0, 50, 120))
	if err != nil || !bytes.Equal(got, f.data[50:121]) {
		t.Fatalf("a[50..120]: %v", err)
	}
	// piece 2 not verified → refused before touching the (sparse) file
	if _, err := f.disk.ReadRange(ctx, f.ih, 0, 150, 249); !errors.Is(err, ErrNotAvailable) {
		t.Fatalf("unverified piece: %v", err)
	}
	// padding file: zeros, no disk access
	got, err = readAll(f.disk.ReadRange(ctx, f.ih, 1, 0, 49))
	if err != nil || !bytes.Equal(got, make([]byte, 50)) {
		t.Fatalf("pad: %v", err)
	}
	// complete file under its final name, spanning pieces 3-4 (piece 3 starts in the pad)
	got, err = readAll(f.disk.ReadRange(ctx, f.ih, 2, 0, 119))
	if err != nil || !bytes.Equal(got, f.data[300:420]) {
		t.Fatalf("b.srt: %v", err)
	}
	// out of range / unknown torrent
	if _, err := f.disk.ReadRange(ctx, f.ih, 2, 0, 120); err == nil {
		t.Fatal("range past EOF accepted")
	}
	if _, err := f.disk.ReadRange(ctx, strings.Repeat("0", 40), 0, 0, 1); !errors.Is(err, ErrNoTorrent) {
		t.Fatalf("unknown torrent: %v", err)
	}
}

func TestDiskVerifyDetectsCorruption(t *testing.T) {
	f := newFixture(t)
	p := filepath.Join(f.root, "qb", "files", "T", "a.mkv.!qB")
	fh, _ := os.OpenFile(p, os.O_WRONLY, 0)
	_, _ = fh.WriteAt([]byte{f.data[10] ^ 0xff}, 10)
	fh.Close()
	if _, err := readAll(f.disk.ReadRange(context.Background(), f.ih, 0, 0, 99)); err == nil || !strings.Contains(err.Error(), "hash") {
		t.Fatalf("corrupted piece served: %v", err)
	}
}

func TestDiskPathSafety(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	outside := t.TempDir()
	_ = os.WriteFile(filepath.Join(outside, "secret"), bytes.Repeat([]byte("x"), 250), 0o644)
	// 1. qB's save path (or a name with "..") resolves outside DATA_ROOT: refused even though
	// the file exists there
	_ = os.MkdirAll(filepath.Join(outside, "T"), 0o755)
	_ = os.WriteFile(filepath.Join(outside, "T", "a.mkv"), f.data[0:250], 0o644)
	f.qb.mu.Lock()
	f.qb.save = filepath.ToSlash(outside)
	f.qb.mu.Unlock()
	f.disk.tor = nil
	if _, err := readAll(f.disk.ReadRange(ctx, f.ih, 0, 0, 99)); err == nil || !strings.Contains(err.Error(), "outside") {
		t.Fatalf("save path outside the data root: %v", err)
	}
	f.qb.mu.Lock()
	f.qb.save = filepath.ToSlash(filepath.Join(f.root, "qb", "files")) + "/../../../../../../../.." + filepath.ToSlash(outside)
	f.qb.mu.Unlock()
	f.disk.tor = nil
	if _, err := readAll(f.disk.ReadRange(ctx, f.ih, 0, 0, 99)); err == nil || !strings.Contains(err.Error(), "outside") {
		t.Fatalf("save path with ..: %v", err)
	}
	f.qb.mu.Lock()
	f.qb.save = filepath.ToSlash(filepath.Join(f.root, "qb", "files"))
	f.qb.mu.Unlock()
	// 2. a symlink inside the data root pointing outside
	f.qb.mu.Lock()
	f.qb.names = []string{"T/a.mkv", "T/sub/b.srt"}
	f.qb.mu.Unlock()
	f.disk.tor = nil
	dir := filepath.Join(f.root, "qb", "files", "T")
	_ = os.Remove(filepath.Join(dir, "a.mkv.!qB"))
	if err := os.Symlink(filepath.Join(outside, "secret"), filepath.Join(dir, "a.mkv")); err != nil {
		t.Fatal(err)
	}
	if _, err := readAll(f.disk.ReadRange(ctx, f.ih, 0, 0, 99)); err == nil || !strings.Contains(err.Error(), "outside") {
		t.Fatalf("symlink escape: %v", err)
	}
	// 3. DATA_MAP: qB's paths differ from the edge's (e.g. /Data/Torrent inside qB)
	f.disk.tor = nil
	_ = os.Remove(filepath.Join(dir, "a.mkv"))
	_ = os.WriteFile(filepath.Join(dir, "a.mkv"), f.data[0:250], 0o644)
	f.qb.mu.Lock()
	f.qb.save = "/Data/Torrent/files"
	f.qb.mu.Unlock()
	f.disk.PathMap = [][2]string{{"/Data/Torrent", filepath.Join(f.root, "qb")}}
	got, err := readAll(f.disk.ReadRange(ctx, f.ih, 0, 0, 99))
	if err != nil || !bytes.Equal(got, f.data[0:100]) {
		t.Fatalf("mapped path: %v", err)
	}
}

func TestDiskHaveCache(t *testing.T) {
	f := newFixture(t)
	f.disk.HaveTTL = 200 * time.Millisecond
	ctx := context.Background()
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			b, known, err := f.disk.Have(ctx, f.ih)
			if err != nil || !known || b.Count() != 4 {
				t.Errorf("have: %v %v %v", b, known, err)
			}
		}()
	}
	wg.Wait()
	if n := f.qb.stateHits.Load(); n != 1 {
		t.Fatalf("pieceStates called %d times for 20 concurrent Have", n)
	}
	time.Sleep(250 * time.Millisecond)
	f.qb.setStates(2, 2, 2, 2, 2)
	b, _, _ := f.disk.Have(ctx, f.ih)
	if !b.Full() || f.qb.stateHits.Load() != 2 {
		t.Fatalf("after TTL: %v, %d calls", b.Ranges(), f.qb.stateHits.Load())
	}
	if _, known, err := f.disk.Have(ctx, strings.Repeat("1", 40)); known || err != nil {
		t.Fatalf("unknown torrent: %v %v", known, err)
	}
}

// ---------------------------------------------------------------- edge HTTP server

func newAgent(t *testing.T, f *fixture) *Agent {
	t.Helper()
	a, err := New(Config{Main: "ws://127.0.0.1:1/x", Token: "t", QBURL: f.qb.srv.URL, QBUser: "u", QBPass: "p", DataRoot: f.root, Verify: true,
		Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	a.disk = f.disk
	return a
}

func TestEdgeHTTP(t *testing.T) {
	f := newFixture(t)
	a := newAgent(t, f)
	const origin = "https://www.example.test"
	pub, priv, _ := ed25519.GenerateKey(nil)
	pubOld, privOld, _ := ed25519.GenerateKey(nil)
	_, privStale, _ := ed25519.GenerateKey(nil)
	a.SetIdentity(5, origin, []ed25519.PublicKey{pub, pubOld})
	srv := httptest.NewServer(a.Handler())
	defer srv.Close()
	tok := func(priv ed25519.PrivateKey, aud int64, ih string, exp time.Duration) string {
		s, err := edgetoken.Sign(priv, edgetoken.Claims{Role: "member", IH: ih, Aud: aud, Exp: time.Now().Add(exp)})
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	do := func(method, path, token, rng string, hdr map[string]string) *http.Response {
		req, _ := http.NewRequest(method, srv.URL+path, nil)
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		if rng != "" {
			req.Header.Set("Range", rng)
		}
		req.Header.Set("Origin", origin)
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}
	body := func(r *http.Response) []byte {
		defer r.Body.Close()
		b, _ := io.ReadAll(r.Body)
		return b
	}
	pathA := "/webseed/hash/" + f.ih + "/T/a.mkv"
	good := tok(priv, 5, f.ih, 10*time.Minute)

	// preflight: Authorization + Range allowed, no credentials, cached
	r := do("OPTIONS", pathA, "", "", map[string]string{"Access-Control-Request-Method": "GET", "Access-Control-Request-Headers": "authorization, range, cache-control, user-agent"})
	body(r)
	if r.StatusCode != 204 || r.Header.Get("Access-Control-Allow-Origin") != origin || r.Header.Get("Access-Control-Allow-Credentials") != "" ||
		!strings.Contains(r.Header.Get("Access-Control-Allow-Headers"), "authorization") || r.Header.Get("Access-Control-Max-Age") == "" {
		t.Fatalf("preflight: %d %v", r.StatusCode, r.Header)
	}
	// foreign origin / disallowed header / method
	r = do("OPTIONS", pathA, "", "", map[string]string{"Origin": "https://evil.test", "Access-Control-Request-Method": "GET"})
	if body(r); r.StatusCode != 403 || r.Header.Get("Access-Control-Allow-Origin") != "" {
		t.Fatalf("foreign preflight: %d", r.StatusCode)
	}
	r = do("OPTIONS", pathA, "", "", map[string]string{"Access-Control-Request-Method": "GET", "Access-Control-Request-Headers": "x-evil"})
	if body(r); r.StatusCode != 403 {
		t.Fatalf("x-evil preflight: %d", r.StatusCode)
	}

	// valid token, verified pieces → 206 with CORS (no credentials)
	r = do("GET", pathA, good, "bytes=0-199", nil)
	if b := body(r); r.StatusCode != 206 || !bytes.Equal(b, f.data[0:200]) || r.Header.Get("Content-Range") != "bytes 0-199/250" ||
		r.Header.Get("Access-Control-Allow-Origin") != origin || r.Header.Get("Access-Control-Allow-Credentials") != "" ||
		!strings.Contains(r.Header.Get("Access-Control-Expose-Headers"), "Content-Range") || r.Header.Get("Content-Type") != "video/x-matroska" {
		t.Fatalf("GET: %d %v", r.StatusCode, r.Header)
	}
	// token from the previous key (rotation grace) still works
	if r = do("GET", pathA, tok(privOld, 5, f.ih, time.Minute), "bytes=0-9", nil); body(r) != nil && r.StatusCode != 206 {
		t.Fatalf("previous key: %d", r.StatusCode)
	}
	// 401: missing / other edge / other torrent / expired / unknown key / garbage
	for name, tk := range map[string]string{
		"missing":   "",
		"aud":       tok(priv, 6, f.ih, time.Minute),
		"ih":        tok(priv, 5, strings.Repeat("ab", 20), time.Minute),
		"expired":   tok(priv, 5, f.ih, -5*time.Minute),
		"stale key": tok(privStale, 5, f.ih, time.Minute),
		"garbage":   "abc",
	} {
		r = do("GET", pathA, tk, "bytes=0-9", nil)
		if body(r); r.StatusCode != 401 || !strings.Contains(r.Header.Get("WWW-Authenticate"), "Bearer") {
			t.Errorf("%s: %d", name, r.StatusCode)
		}
	}
	// a cookie alone doesn't help
	r = do("GET", pathA, "", "bytes=0-9", map[string]string{"Cookie": "ot_session=whatever"})
	if body(r); r.StatusCode != 401 {
		t.Fatalf("cookie only: %d", r.StatusCode)
	}
	// unverified piece → 503 + Retry-After; padding file; whole file; 416; HEAD; unknown file
	r = do("GET", pathA, good, "bytes=150-249", nil)
	if body(r); r.StatusCode != 503 || r.Header.Get("Retry-After") == "" {
		t.Fatalf("unverified: %d", r.StatusCode)
	}
	r = do("GET", "/webseed/hash/"+f.ih+"/T/.pad/50", good, "", nil)
	if b := body(r); r.StatusCode != 200 || !bytes.Equal(b, make([]byte, 50)) {
		t.Fatalf("pad: %d", r.StatusCode)
	}
	r = do("GET", "/webseed/hash/"+f.ih+"/T/sub/b.srt", good, "", nil)
	if b := body(r); r.StatusCode != 200 || !bytes.Equal(b, f.data[300:420]) {
		t.Fatalf("b.srt: %d", r.StatusCode)
	}
	r = do("GET", pathA, good, "bytes=900-", nil)
	if body(r); r.StatusCode != 416 {
		t.Fatalf("416: %d", r.StatusCode)
	}
	r = do("HEAD", pathA, good, "bytes=0-9", nil)
	if b := body(r); r.StatusCode != 206 || len(b) != 0 || r.Header.Get("Content-Length") != "10" {
		t.Fatalf("HEAD: %d %d", r.StatusCode, len(b))
	}
	r = do("GET", "/webseed/hash/"+f.ih+"/T/nope", good, "", nil)
	if body(r); r.StatusCode != 404 {
		t.Fatalf("unknown file: %d", r.StatusCode)
	}
	// GET from a foreign origin is refused (CORS would hide it anyway)
	r = do("GET", pathA, good, "bytes=0-9", map[string]string{"Origin": "https://evil.test"})
	if body(r); r.StatusCode != 403 {
		t.Fatalf("foreign GET: %d", r.StatusCode)
	}
	if st := a.bytes.Load(); st < 200 {
		t.Fatalf("bytes served counter %d", st)
	}
	// an edge that never got keys can't verify anything
	b := newAgent(t, f)
	srv2 := httptest.NewServer(b.Handler())
	defer srv2.Close()
	req, _ := http.NewRequest("GET", srv2.URL+pathA, nil)
	req.Header.Set("Authorization", "Bearer "+good)
	resp, _ := http.DefaultClient.Do(req)
	body(resp)
	if resp.StatusCode != 503 {
		t.Fatalf("no keys: %d", resp.StatusCode)
	}
}

// ---------------------------------------------------------------- tunnel: hub ↔ agent

func TestHubAgentTunnel(t *testing.T) {
	f := newFixture(t)
	tokGood, hashGood := NewToken()
	var keys atomic.Value
	keys.Store([]string{"k-unused"})
	_, priv, _ := ed25519.GenerateKey(nil)
	keys.Store([]string{edgetoken.EncodePublic(priv.Public().(ed25519.PublicKey))})
	var changes sync.Map
	hub := &Hub{
		Lookup: func(_ context.Context, h string) (int64, bool, error) {
			return 9, h == hashGood, nil
		},
		Config:   func() string { return "https://www.example.test" },
		Keys:     func() []string { return keys.Load().([]string) },
		OnChange: func(id int64, c bool) { changes.Store(fmt.Sprint(id, c), true) },
		Log:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		Options:  tunnel.Options{PingEvery: 50 * time.Millisecond},
	}
	main := httptest.NewServer(hub)
	defer main.Close()
	wsURL := "ws://" + strings.TrimPrefix(main.URL, "http://")

	// auth failure: a clear ErrUnauthorized (the loop then waits minutes, not seconds)
	bad, _ := New(Config{Main: wsURL, Token: "otde_wrong", QBURL: f.qb.srv.URL, DataRoot: f.root, Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err := bad.connectOnce(context.Background()); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("bad token: %v", err)
	}

	a, _ := New(Config{Main: wsURL, Token: tokGood, QBURL: f.qb.srv.URL, QBUser: "u", QBPass: "p", DataRoot: f.root, Version: "test", Host: "nas",
		Tunnel: tunnel.Options{PingEvery: 50 * time.Millisecond}, Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
	a.disk = f.disk
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go a.TunnelLoop(ctx)
	waitFor := func(what string, cond func() bool) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for !cond() {
			if time.Now().After(deadline) {
				t.Fatalf("timeout: %s", what)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	waitFor("connected", func() bool { return hub.Get(9) != nil })
	waitFor("config", func() bool {
		a.mu.Lock()
		defer a.mu.Unlock()
		return a.edgeID == 9 && a.origin == "https://www.example.test" && len(a.keys) == 1
	})
	if _, ok := changes.Load("9 true"); !ok {
		t.Error("OnChange(connected) not called")
	}
	c := hub.Get(9)
	if c.Version != "test" || c.Host != "nas" {
		t.Errorf("conn = %+v", c)
	}

	// qB passthrough: the main server's qB client (cookie login, Referer) through the edge
	client := qb.NewClientTransport("http://qb.via-edge", "u", "p", "", hub.Transport(9))
	if v, err := client.Version(ctx); err != nil || v != "v5.3.0beta1" {
		t.Fatalf("version via edge: %q %v", v, err)
	}
	if f.qb.refererNo.Load() != 0 {
		t.Fatal("edge forwarded a foreign Referer to qB")
	}
	states, err := client.PieceStates(ctx, f.ih)
	if err != nil || len(states) != 5 {
		t.Fatalf("pieceStates via edge: %v %v", states, err)
	}
	// Have (availability for the SSE)
	bits, known, err := c.Have(ctx, f.ih, 5)
	if err != nil || !known || bits.Count() != 4 || bits.Has(2) {
		t.Fatalf("have: %v %v %v", bits, known, err)
	}
	if _, known, err := c.Have(ctx, strings.Repeat("3", 40), 1); known || err != nil {
		t.Fatalf("have unknown: %v %v", known, err)
	}
	// stats
	if st := hub.Status(ctx, 9); !st.Connected || st.StatsError != "" || st.Listen != ":8890" {
		t.Fatalf("status = %+v", st)
	}
	// key rotation pushed to the edge
	_, priv2, _ := ed25519.GenerateKey(nil)
	keys.Store([]string{edgetoken.EncodePublic(priv2.Public().(ed25519.PublicKey)), edgetoken.EncodePublic(priv.Public().(ed25519.PublicKey))})
	hub.PushKeys(ctx)
	waitFor("keys pushed", func() bool {
		a.mu.Lock()
		defer a.mu.Unlock()
		return len(a.keys) == 2
	})
	// passthrough is restricted to the qB API
	if _, _, err := c.call(ctx, Req{Op: OpHTTP, Method: "GET", Path: "/../etc/passwd"}, nil, false); err == nil {
		t.Fatal("non-API path forwarded")
	}

	// the main server drops the edge (e.g. restart): it reconnects by itself
	first := hub.Get(9)
	hub.Disconnect(9)
	waitFor("reconnected", func() bool { c := hub.Get(9); return c != nil && c != first })
	if _, ok := changes.Load("9 false"); !ok {
		t.Error("OnChange(disconnected) not called")
	}
	// without the edge the transport fails fast
	cancel()
	waitFor("gone", func() bool { return hub.Get(9) == nil })
	if _, err := client.Version(context.Background()); err == nil {
		t.Fatal("request succeeded without an edge")
	}
}
