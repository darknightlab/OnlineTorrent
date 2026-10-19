package swarm

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/net/websocket"

	"github.com/darknightlab/onlinetorrent/internal/meta"
	"github.com/darknightlab/onlinetorrent/internal/store"
)

func hashOf(n byte) [20]byte {
	var h [20]byte
	for i := range h {
		h[i] = n
	}
	h[0] = 0xff // non-ASCII byte: exercises latin1 / URL escaping
	return h
}

func ihOf(n byte) string { h := hashOf(n); return hex.EncodeToString(h[:]) }

// fakeUDP is a BEP 15 tracker: seeders = first byte after 0xff … (hash[1]), leechers = 2·that.
type fakeUDP struct {
	conn     *net.UDPConn
	connects atomic.Int32
	scrapes  atomic.Int32
	maxBatch atomic.Int32
	dropOnce atomic.Bool // drop the first scrape packet (exercise retries)
}

func newFakeUDP(t *testing.T) *fakeUDP {
	c, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeUDP{conn: c}
	t.Cleanup(func() { c.Close() })
	go f.serve()
	return f
}

func (f *fakeUDP) url() string { return "udp://" + f.conn.LocalAddr().String() + "/announce" }

const fakeConnID = 0x1122334455667788

func (f *fakeUDP) serve() {
	buf := make([]byte, 2048)
	for {
		n, addr, err := f.conn.ReadFromUDP(buf)
		if err != nil {
			return
		}
		p := buf[:n]
		if n < 16 {
			continue
		}
		action, tid := binary.BigEndian.Uint32(p[8:]), binary.BigEndian.Uint32(p[12:])
		switch action {
		case udpActConnect:
			if binary.BigEndian.Uint64(p) != udpProtocolID {
				continue
			}
			f.connects.Add(1)
			out := make([]byte, 16)
			binary.BigEndian.PutUint32(out[0:], udpActConnect)
			binary.BigEndian.PutUint32(out[4:], tid)
			binary.BigEndian.PutUint64(out[8:], fakeConnID)
			f.conn.WriteToUDP(out, addr)
		case udpActScrape:
			if binary.BigEndian.Uint64(p) != fakeConnID {
				out := make([]byte, 8, 32)
				binary.BigEndian.PutUint32(out[0:], udpActError)
				binary.BigEndian.PutUint32(out[4:], tid)
				f.conn.WriteToUDP(append(out, "bad connection id"...), addr)
				continue
			}
			if f.dropOnce.CompareAndSwap(true, false) {
				continue
			}
			f.scrapes.Add(1)
			k := (n - 16) / 20
			if int32(k) > f.maxBatch.Load() {
				f.maxBatch.Store(int32(k))
			}
			out := make([]byte, 8+12*k)
			binary.BigEndian.PutUint32(out[0:], udpActScrape)
			binary.BigEndian.PutUint32(out[4:], tid)
			for i := 0; i < k; i++ {
				v := uint32(p[16+20*i+1])
				binary.BigEndian.PutUint32(out[8+12*i:], v)     // seeders
				binary.BigEndian.PutUint32(out[8+12*i+4:], 7)   // completed
				binary.BigEndian.PutUint32(out[8+12*i+8:], 2*v) // leechers
			}
			f.conn.WriteToUDP(out, addr)
		}
	}
}

func fastScraper() *Scraper {
	sc := NewScraper()
	sc.AllowPrivate = true
	sc.Burst, sc.Interval = 1000, time.Millisecond
	return sc
}

func TestUDPScrapeBatchingAndConnReuse(t *testing.T) {
	f := newFakeUDP(t)
	sc := fastScraper()
	var hashes [][20]byte
	for i := 0; i < 100; i++ { // 2 packets: 74 + 26
		hashes = append(hashes, hashOf(byte(i)))
	}
	ctx := context.Background()
	res, err := sc.Scrape(ctx, f.url(), hashes, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 100 {
		t.Fatalf("got %d results", len(res))
	}
	if c := res[hashOf(9)]; c.Seeders != 9 || c.Leechers != 18 || c.Completed != 7 {
		t.Fatalf("counts %+v", c)
	}
	if f.maxBatch.Load() != udpMaxBatch || f.scrapes.Load() != 2 {
		t.Fatalf("batches: max %d, scrapes %d", f.maxBatch.Load(), f.scrapes.Load())
	}
	// the connection id is reused within its lifetime
	if _, err := sc.Scrape(ctx, f.url(), hashes[:1], true); err != nil {
		t.Fatal(err)
	}
	if f.connects.Load() != 1 {
		t.Fatalf("connects = %d, want 1 (reuse)", f.connects.Load())
	}
	// expired id: reconnect
	sc.udpFor(f.conn.LocalAddr().String()).connAt = time.Now().Add(-2 * time.Minute)
	if _, err := sc.Scrape(ctx, f.url(), hashes[:1], true); err != nil {
		t.Fatal(err)
	}
	if f.connects.Load() != 2 {
		t.Fatalf("connects = %d, want 2", f.connects.Load())
	}
}

func TestUDPRetryAndBadConnID(t *testing.T) {
	f := newFakeUDP(t)
	sc := fastScraper()
	old := udpTimeouts
	udpTimeouts = []time.Duration{150 * time.Millisecond, 300 * time.Millisecond}
	defer func() { udpTimeouts = old }()
	f.dropOnce.Store(true)
	res, err := sc.Scrape(context.Background(), f.url(), [][20]byte{hashOf(3)}, true)
	if err != nil || res[hashOf(3)].Seeders != 3 {
		t.Fatalf("retry: %v %+v", err, res)
	}
	// a stale connection id the tracker rejects: reconnect once and succeed
	tr := sc.udpFor(f.conn.LocalAddr().String())
	tr.connID = 42
	res, err = sc.Scrape(context.Background(), f.url(), [][20]byte{hashOf(4)}, true)
	if err != nil || res[hashOf(4)].Seeders != 4 {
		t.Fatalf("reconnect: %v %+v", err, res)
	}
}

func TestUDPTimeout(t *testing.T) {
	// a socket that never answers
	c, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	old := udpTimeouts
	udpTimeouts = []time.Duration{50 * time.Millisecond, 50 * time.Millisecond}
	defer func() { udpTimeouts = old }()
	_, err = fastScraper().Scrape(context.Background(), "udp://"+c.LocalAddr().String()+"/announce", [][20]byte{hashOf(1)}, true)
	if err == nil || !strings.Contains(err.Error(), "timeout") {
		t.Fatalf("err = %v", err)
	}
}

func bencodeScrape(files map[[20]byte]Counts) string {
	var b strings.Builder
	b.WriteString("d5:filesd")
	for h, c := range files {
		fmt.Fprintf(&b, "20:%sd8:completei%de10:downloadedi%de10:incompletei%dee", string(h[:]), c.Seeders, c.Completed, c.Leechers)
	}
	b.WriteString("ee")
	return b.String()
}

func TestHTTPScrape(t *testing.T) {
	var gotPath string
	var gotHashes int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		files := map[[20]byte]Counts{}
		for _, v := range r.URL.Query()["info_hash"] {
			gotHashes++
			var h [20]byte
			copy(h[:], v)
			if h[1] == 5 {
				continue // tracker doesn't know this one
			}
			files[h] = Counts{Seeders: int(h[1]), Leechers: 1, Completed: 9}
		}
		if r.URL.Query().Get("key") != "abc" {
			w.Write([]byte("d14:failure reason7:no passe"))
			return
		}
		w.Write([]byte(bencodeScrape(files)))
	}))
	defer srv.Close()
	sc := fastScraper()
	hs := [][20]byte{hashOf(1), hashOf(2), hashOf(5)}
	res, err := sc.Scrape(context.Background(), srv.URL+"/x/announce.php?key=abc", hs, true)
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/x/scrape.php" || gotHashes != 3 {
		t.Fatalf("path %q hashes %d", gotPath, gotHashes)
	}
	if res[hashOf(2)] != (Counts{Seeders: 2, Leechers: 1, Completed: 9}) {
		t.Fatalf("%+v", res[hashOf(2)])
	}
	if c, ok := res[hashOf(5)]; !ok || c != (Counts{}) {
		t.Fatalf("unlisted hash should be 0: %+v %v", c, ok)
	}
	if _, err := sc.Scrape(context.Background(), srv.URL+"/announce", hs, true); err == nil || !strings.Contains(err.Error(), "no pass") {
		t.Fatalf("failure reason: %v", err)
	}
	if _, err := sc.Scrape(context.Background(), srv.URL+"/tracker", hs, true); err == nil {
		t.Fatal("no announce segment: scrape unsupported")
	}
}

func TestScrapeURLAndRequest(t *testing.T) {
	for in, want := range map[string]string{
		"http://t.example/announce":            "http://t.example/scrape",
		"https://t.example/a/announce.php?k=1": "https://t.example/a/scrape.php?k=1",
		"http://t.example:7777/announce":       "http://t.example:7777/scrape",
	} {
		if got, ok := scrapeURL(in); !ok || got != want {
			t.Errorf("%s → %s %v", in, got, ok)
		}
	}
	if _, ok := scrapeURL("http://t.example/x"); ok {
		t.Error("x")
	}
	h := hashOf(0x20)
	u := httpScrapeRequest("http://t/scrape", [][20]byte{h})
	if !strings.HasPrefix(u, "http://t/scrape?info_hash=%FF") {
		t.Errorf("request %s", u)
	}
}

func TestParseHTTPScrapeBad(t *testing.T) {
	for _, b := range []string{"", "i1e", "d5:filesi1ee", "d5:files"} {
		if _, err := parseHTTPScrape([]byte(b)); err == nil {
			t.Errorf("%q accepted", b)
		}
	}
}

func TestWSSScrape(t *testing.T) {
	var msgs atomic.Int32
	srv := httptest.NewServer(websocket.Server{Handler: func(ws *websocket.Conn) {
		for {
			var req wssReq
			if err := websocket.JSON.Receive(ws, &req); err != nil {
				return
			}
			msgs.Add(1)
			files := map[string]any{}
			for _, s := range req.InfoHash {
				b, ok := fromLatin1(s)
				if !ok || len(b) != 20 {
					continue
				}
				files[s] = map[string]int{"complete": int(b[1]), "incomplete": 1, "downloaded": 0}
			}
			// an unrelated message first (trackers may send other actions)
			websocket.JSON.Send(ws, map[string]any{"action": "announce", "interval": 120})
			websocket.JSON.Send(ws, map[string]any{"action": "scrape", "files": files})
		}
	}})
	defer srv.Close()
	var hs [][20]byte
	for i := 0; i < 60; i++ { // 2 messages (50 + 10)
		hs = append(hs, hashOf(byte(i)))
	}
	res, err := fastScraper().Scrape(context.Background(), "ws"+strings.TrimPrefix(srv.URL, "http"), hs, true)
	if err != nil {
		t.Fatal(err)
	}
	if msgs.Load() != 2 || len(res) != 60 || res[hashOf(42)].Seeders != 42 {
		t.Fatalf("msgs %d res %d %+v", msgs.Load(), len(res), res[hashOf(42)])
	}
}

func TestLatin1RoundTrip(t *testing.T) {
	h := hashOf(0x80)
	s := latin1(h[:])
	b, _ := json.Marshal(s)
	var back string
	_ = json.Unmarshal(b, &back)
	got, ok := fromLatin1(back)
	if !ok || string(got) != string(h[:]) {
		t.Fatalf("round trip %x", got)
	}
}

func TestAggregateMax(t *testing.T) {
	a, b := hashOf(1), hashOf(2)
	agg := Aggregate([]Answer{
		{Kind: KindUDP, Counts: map[[20]byte]Counts{a: {Seeders: 5, Leechers: 1, Completed: 10}}},
		{Kind: KindHTTP, Counts: map[[20]byte]Counts{a: {Seeders: 3, Leechers: 4, Completed: 2}}},
		{Kind: KindWSS, Counts: map[[20]byte]Counts{a: {Seeders: 1}, b: {Seeders: 0, Leechers: 2}}},
		{Kind: KindWSS, Counts: map[[20]byte]Counts{a: {Seeders: 2}}},
		{Kind: KindUDP, Err: fmt.Errorf("down")},
	})
	g := agg[a]
	if *g.BT != (Counts{Seeders: 5, Leechers: 4, Completed: 10}) || g.WT.Seeders != 2 || g.Trackers != 4 {
		t.Fatalf("%+v %+v", g.BT, g.WT)
	}
	if agg[b].BT != nil || agg[b].WT.Leechers != 2 {
		t.Fatalf("b: %+v", agg[b])
	}
}

func TestMergeAndPlan(t *testing.T) {
	got := Merge(3, []string{"wss://a", " udp://b:1/announce ", "magnet:x", ""}, []string{"udp://b:1/announce", "http://c/announce", "http://d/announce"})
	if strings.Join(got, ",") != "wss://a,udp://b:1/announce,http://c/announce" {
		t.Fatalf("merge %v", got)
	}
	bt := []string{"udp://bt:1/announce"}
	wss := []string{"wss://wt"}
	own := map[string][]string{
		ihOf(1): {"udp://bt:1/announce", "wss://x", "http://o1/announce", "http://o2/announce", "http://o3/announce", "http://o4/announce"},
	}
	ts := plan([]string{ihOf(1), ihOf(2), "zz"}, bt, wss, own)
	byURL := map[string]*target{}
	for _, t := range ts {
		byURL[t.url] = t
	}
	if len(byURL["udp://bt:1/announce"].hashes) != 2 || len(byURL["wss://wt"].hashes) != 2 || !byURL["wss://wt"].trusted {
		t.Fatalf("configured trackers get every hash: %+v", ts)
	}
	if byURL["http://o1/announce"] == nil || byURL["http://o3/announce"] == nil || byURL["http://o4/announce"] != nil || byURL["wss://x"] != nil {
		t.Fatalf("own trackers: first %d udp/http not configured: %v", ownPerTorrent, ts)
	}
	if byURL["http://o1/announce"].trusted {
		t.Fatal("own trackers are untrusted")
	}
}

func TestPrivateTrackerRejected(t *testing.T) {
	sc := NewScraper()
	_, err := sc.Scrape(context.Background(), "udp://127.0.0.1:6969/announce", [][20]byte{hashOf(1)}, false)
	if err != ErrPrivateTracker {
		t.Fatalf("err = %v", err)
	}
	for _, a := range []string{"10.0.0.1", "192.168.1.1", "100.64.0.1", "::1", "fd00::1", "169.254.1.1"} {
		if publicHost(context.Background(), a) {
			t.Errorf("%s public", a)
		}
	}
	if !publicHost(context.Background(), "8.8.8.8") {
		t.Error("8.8.8.8")
	}
}

func TestPacer(t *testing.T) {
	sc := NewScraper()
	sc.Burst, sc.Interval = 2, 100*time.Millisecond
	start := time.Now()
	for i := 0; i < 4; i++ {
		if err := sc.wait(context.Background(), "x"); err != nil {
			t.Fatal(err)
		}
	}
	if d := time.Since(start); d < 150*time.Millisecond {
		t.Fatalf("4 requests with burst 2 @100ms took %s", d)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if err := sc.wait(ctx, "x"); err == nil {
		t.Fatal("expected ctx error")
	}
}

func openStores(t *testing.T) (*store.Store, *meta.Store) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "otd.db"))
	if err != nil {
		t.Fatal(err)
	}
	ms, err := meta.Open(filepath.Join(dir, "meta.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ms.Close(); st.Close() })
	return st, ms
}

func TestOnDemandLookup(t *testing.T) {
	st, ms := openStores(t)
	ctx := context.Background()
	now := time.Now().UTC()
	ts := func(d time.Duration) string { return now.Add(-d).Format(time.RFC3339) }
	for n := byte(1); n <= 6; n++ {
		if err := ms.UpsertPending(ctx, meta.TorrentInput{Infohash: ihOf(n), Title: fmt.Sprint("t", n), Source: "Nyaa", SourceKind: "rss", PublishedAt: ts(time.Duration(n) * time.Hour)}); err != nil {
			t.Fatal(err)
		}
	}
	// 2: scraped 40 min ago (stale), 3: 10 min ago (fresh), 4: rss counts only (never scraped)
	one := 1
	if err := ms.PutSwarmScrape(ctx, []meta.SwarmStats{
		{Infohash: ihOf(2), BTSeeders: &one, BTLeechers: &one, ScrapedAt: ts(40 * time.Minute)},
		{Infohash: ihOf(3), BTSeeders: &one, BTLeechers: &one, ScrapedAt: ts(10 * time.Minute)},
	}); err != nil {
		t.Fatal(err)
	}
	s3, s0 := 3, 0
	if err := ms.PutSwarmInitial(ctx, ihOf(4), &s3, &s0, nil); err != nil {
		t.Fatal(err)
	}

	// one fake UDP tracker (configured) and a WebTorrent tracker that is down
	f := newFakeUDP(t)
	if err := st.UpdateSettings(ctx, map[string]json.RawMessage{
		store.KeyBTTrackers: mustJSON([]string{f.url()}),
		store.KeyTrackers:   mustJSON([]string{"ws://127.0.0.1:1/"}),
	}); err != nil {
		t.Fatal(err)
	}
	sw := New(st, ms, nil, discardLog())
	sw.Scraper = fastScraper()

	// only the requested stale / missing torrents are scraped, in one packet
	res := sw.Lookup(ctx, []string{ihOf(1), ihOf(2), ihOf(3), ihOf(4), ihOf(1)}, 3*time.Second)
	if g := res[ihOf(1)]; g == nil || g.BTSeeders == nil || *g.BTSeeders != 1 || *g.BTLeechers != 2 || g.WTSeeders != nil || g.ScrapedAt == "" || g.Source != meta.SwarmScrape {
		t.Fatalf("ih1 %+v", g)
	}
	if g := res[ihOf(2)]; g.ScrapedAt == ts(40*time.Minute) || *g.BTSeeders != 2 {
		t.Fatalf("stale ih2 not refreshed: %+v", g)
	}
	if g := res[ihOf(3)]; g.ScrapedAt != ts(10*time.Minute) {
		t.Fatalf("fresh ih3 rescraped: %+v", g)
	}
	if g := res[ihOf(4)]; *g.BTSeeders != 4 || g.Source != meta.SwarmScrape {
		t.Fatalf("rss row replaced by scrape: %+v", g)
	}
	if f.scrapes.Load() != 1 || f.maxBatch.Load() != 3 {
		t.Fatalf("scrapes %d batch %d (want 1 packet of 3)", f.scrapes.Load(), f.maxBatch.Load())
	}
	// torrents not in the response are never touched (no catalogue-wide scraping)
	if g, _ := ms.SwarmStatsMany(ctx, []string{ihOf(5), ihOf(6)}); len(g) != 0 {
		t.Fatalf("unrequested torrents scraped: %v", g)
	}
	// within 30 minutes: cached, no new request
	sw.Lookup(ctx, []string{ihOf(1), ihOf(2)}, time.Second)
	if f.scrapes.Load() != 1 {
		t.Fatal("cache not used")
	}
	// rss counts never overwrite scraped data
	_ = ms.PutSwarmInitial(ctx, ihOf(1), &s3, &s0, nil)
	if g, _ := ms.SwarmStatsMany(ctx, []string{ihOf(1)}); *g[ihOf(1)].BTSeeders != 1 {
		t.Fatal("rss overwrote scrape")
	}
	// health recorded from the scrape: udp ok, wss error
	hs := sw.Health()
	if len(hs) != 2 || hs[0].List != "webtorrent" || hs[0].Status != StatusError || hs[1].Status != StatusOK {
		t.Fatalf("health %+v", hs)
	}
	if info := sw.Status(); info.Runs != 1 || info.Torrents != 3 || info.LastKnown != 3 || !info.Enabled {
		t.Fatalf("info %+v", info)
	}

	// disabled: stored data only
	stale := ts(time.Hour)
	_ = st.UpdateSettings(ctx, map[string]json.RawMessage{store.KeySwarmEnabled: json.RawMessage("false")})
	_ = ms.PutSwarmScrape(ctx, []meta.SwarmStats{{Infohash: ihOf(3), ScrapedAt: stale}})
	if g := sw.Lookup(ctx, []string{ihOf(3)}, time.Second)[ihOf(3)]; g.ScrapedAt != stale {
		t.Fatal("disabled: no scrape")
	}
	if v := res[ihOf(1)].View(); v.CheckedAt == nil || *v.BTSeeders != 1 {
		t.Fatalf("view %+v", v)
	}
	sum, err := ms.SwarmSummary(ctx)
	if err != nil || sum.Torrents != 6 || sum.Scraped != 4 {
		t.Fatalf("summary %+v %v", sum, err)
	}
}

// A response never waits longer than its budget; the scrape finishes in the background and
// concurrent lookups of the same torrent share one scrape.
func TestOnDemandBudgetAndSingleflight(t *testing.T) {
	st, ms := openStores(t)
	ctx := context.Background()
	// HTTP tracker that answers after 600 ms
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		time.Sleep(600 * time.Millisecond)
		files := map[[20]byte]Counts{}
		for _, v := range r.URL.Query()["info_hash"] {
			var h [20]byte
			copy(h[:], v)
			files[h] = Counts{Seeders: 9}
		}
		w.Write([]byte(bencodeScrape(files)))
	}))
	defer srv.Close()
	_ = st.UpdateSettings(ctx, map[string]json.RawMessage{store.KeyBTTrackers: mustJSON([]string{srv.URL + "/announce"}), store.KeyTrackers: mustJSON([]string{})})
	sw := New(st, ms, nil, discardLog())
	sw.Scraper = fastScraper()
	ih := ihOf(7)

	start := time.Now()
	var wg sync.WaitGroup
	results := make([]map[string]*meta.SwarmStats, 3)
	for i := range results {
		wg.Add(1)
		go func() { defer wg.Done(); results[i] = sw.Lookup(ctx, []string{ih}, 100*time.Millisecond) }()
	}
	wg.Wait()
	if d := time.Since(start); d > 450*time.Millisecond {
		t.Fatalf("lookups waited %s (budget 100ms)", d)
	}
	for _, r := range results {
		if r[ih] != nil {
			t.Fatalf("never scraped yet: want null, got %+v", r[ih])
		}
	}
	sw.Wait() // background scrape finishes and is stored
	if hits.Load() != 1 {
		t.Fatalf("tracker hit %d times (singleflight)", hits.Load())
	}
	if g := sw.Lookup(ctx, []string{ih}, 0)[ih]; g == nil || *g.BTSeeders != 9 {
		t.Fatalf("after background scrape %+v", g)
	}
}

func TestPassiveHealth(t *testing.T) {
	st, ms := openStores(t)
	ctx := context.Background()
	f := newFakeUDP(t)
	dead, _ := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	deadURL := "udp://" + dead.LocalAddr().String() + "/announce" // never answers
	defer dead.Close()
	old := udpTimeouts
	udpTimeouts = []time.Duration{50 * time.Millisecond}
	defer func() { udpTimeouts = old }()
	_ = st.UpdateSettings(ctx, map[string]json.RawMessage{
		store.KeyBTTrackers: mustJSON([]string{f.url(), deadURL, "udp://unused.invalid:1/announce"}),
		store.KeyTrackers:   mustJSON([]string{}),
	})
	sw := New(st, ms, nil, discardLog())
	sw.Scraper = fastScraper()
	// nothing used yet: no requests, all "unused"
	for _, h := range sw.Health() {
		if h.Status != StatusUnused || h.CheckedAt != nil {
			t.Fatalf("before use %+v", h)
		}
	}
	if f.connects.Load() != 0 {
		t.Fatal("health must not send requests by itself")
	}
	// a real scrape updates the trackers it used
	sw.Lookup(ctx, []string{ihOf(1)}, 2*time.Second)
	sw.Wait()
	hs := sw.Health()
	if hs[0].Status != StatusOK || hs[0].Result != ResultOK || hs[0].OKCount != 1 || hs[0].LastOKAt == nil || hs[0].CheckedAt == nil {
		t.Fatalf("ok tracker %+v", hs[0])
	}
	if hs[1].Status != StatusError || hs[1].Result != ResultTimeout || hs[1].FailCount != 1 || hs[1].LastErrorAt == nil || hs[1].LastError == "" {
		t.Fatalf("dead tracker %+v", hs[1])
	}
}

func TestClassify(t *testing.T) {
	cases := map[string]error{
		ResultOK:       nil,
		ResultTimeout:  context.DeadlineExceeded,
		ResultDNS:      &net.DNSError{Err: "no such host", Name: "x"},
		ResultNetwork:  &net.OpError{Op: "dial", Err: fmt.Errorf("connection refused")},
		ResultProtocol: fmt.Errorf("tracker error: banned"),
		ResultError:    fmt.Errorf("weird"),
	}
	for want, err := range cases {
		if got := classify(err); got != want {
			t.Errorf("%v → %s want %s", err, got, want)
		}
	}
	if classify(ErrPrivateTracker) != ResultNetwork {
		t.Error("private")
	}
}

func mustJSON(v any) json.RawMessage { b, _ := json.Marshal(v); return b }

func discardLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// A slow / dead tracker must not hide the answers of fast ones: they are stored before the
// response budget ends.
func TestOnDemandPartialFlush(t *testing.T) {
	st, ms := openStores(t)
	ctx := context.Background()
	f := newFakeUDP(t)
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(2500 * time.Millisecond)
		w.Write([]byte("d5:filesdee"))
	}))
	defer slow.Close()
	_ = st.UpdateSettings(ctx, map[string]json.RawMessage{
		store.KeyBTTrackers: mustJSON([]string{f.url(), slow.URL + "/announce"}),
		store.KeyTrackers:   mustJSON([]string{}),
	})
	sw := New(st, ms, nil, discardLog())
	sw.Scraper = fastScraper()
	ih := ihOf(5)
	start := time.Now()
	g := sw.Lookup(ctx, []string{ih}, 1500*time.Millisecond)[ih]
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("waited %s", d)
	}
	if g == nil || g.BTSeeders == nil || *g.BTSeeders != 5 {
		t.Fatalf("fast tracker's answer missing: %+v", g)
	}
	sw.Wait()
	if h := sw.Health(); h[1].Status != StatusOK {
		t.Fatalf("slow tracker recorded after finishing: %+v", h[1])
	}
}

func TestHTTPRetryOn5xx(t *testing.T) {
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if n.Add(1) == 1 {
			w.WriteHeader(505)
			return
		}
		w.Write([]byte(bencodeScrape(map[[20]byte]Counts{hashOf(3): {Seeders: 3}})))
	}))
	defer srv.Close()
	res, err := fastScraper().Scrape(context.Background(), srv.URL+"/announce", [][20]byte{hashOf(3)}, true)
	if err != nil || res[hashOf(3)].Seeders != 3 || n.Load() != 2 {
		t.Fatalf("%v %+v %d", err, res, n.Load())
	}
}
