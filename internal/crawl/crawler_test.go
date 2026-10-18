package crawl

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/darknightlab/onlinetorrent/internal/meta"
	"github.com/darknightlab/onlinetorrent/internal/store"
)

func testCrawler(t *testing.T) *Crawler {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "otd.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	ms, err := meta.Open(filepath.Join(dir, "meta.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(ms.Close)
	c := New(st, ms, nil, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	c.Fetch.allowPrivate = true
	c.Fetch.gap = 0
	return c
}

func TestDefaultSourcesSeeded(t *testing.T) {
	c := testCrawler(t)
	srcs, err := c.St.ListSources(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	enabled := map[string]bool{}
	for _, s := range srcs {
		enabled[s.URL] = s.Enabled
	}
	want := map[string]bool{
		"https://bangumi.moe/api/torrent/page/1":              true,
		"https://mikanani.me/RSS/Classic":                     true,
		"https://nyaa.si/?page=rss&c=1_3&f=0":                 true,
		"https://share.dmhy.org/topics/rss/sort_id/2/rss.xml": false,
		"https://acg.rip/.xml":                                false,
	}
	for u, en := range want {
		if got, ok := enabled[u]; !ok || got != en {
			t.Errorf("%s: present=%v enabled=%v", u, ok, got)
		}
	}
	due, _ := c.St.ListDueSources(context.Background(), time.Now())
	if len(due) != 3 {
		t.Errorf("due: %d", len(due))
	}
}

func TestCrawlRSS(t *testing.T) {
	ctx := context.Background()
	c := testCrawler(t)
	info := map[string]any{"name": "[Sub] Foo - 01 [1080p].mkv", "piece length": 16384, "pieces": strings.Repeat("y", 20), "length": 12345}
	torrent := benc(map[string]any{"info": info})
	mi, _ := ParseTorrent([]byte(torrent))

	var feedHits, torrentHits, notModified atomic.Int32
	var srvURL string
	mux := http.NewServeMux()
	mux.HandleFunc("/rss", func(w http.ResponseWriter, r *http.Request) {
		feedHits.Add(1)
		if r.Header.Get("If-None-Match") == `"v1"` {
			notModified.Add(1)
			w.WriteHeader(http.StatusNotModified)
			return
		}
		if !strings.HasPrefix(r.UserAgent(), "onlinetorrent/") {
			t.Errorf("UA %q", r.UserAgent())
		}
		w.Header().Set("ETag", `"v1"`)
		fmt.Fprintf(w, `<?xml version="1.0"?><rss version="2.0"><channel><title>Test</title>
<item><title>[Sub] Foo - 01 [1080p]</title><guid>g1</guid><link>%[1]s/view/1</link><enclosure url="%[1]s/1.torrent" type="application/x-bittorrent" length="12345"/><pubDate>Sun, 18 Oct 2026 10:00:00 +0000</pubDate></item>
<item><title>[Raw] Bar - 02</title><guid>g2</guid><enclosure url="magnet:?xt=urn:btih:RUCEAKOIEWU3RAPD6Z2T64ET7DJFNKC5&amp;tr=udp%%3A%%2F%%2Ft.example%%3A80"/></item>
<item><title>[Sub] Foo - 01 [1080p] (mirror)</title><guid>g3</guid><enclosure url="%[1]s/2.torrent" type="application/x-bittorrent"/></item>
</channel></rss>`, srvURL)
	})
	mux.HandleFunc("/1.torrent", func(w http.ResponseWriter, r *http.Request) { torrentHits.Add(1); io.WriteString(w, torrent) })
	mux.HandleFunc("/2.torrent", func(w http.ResponseWriter, r *http.Request) { torrentHits.Add(1); io.WriteString(w, torrent) })
	srv := httptest.NewServer(mux)
	defer srv.Close()
	srvURL = srv.URL

	id, err := c.St.AddSource(ctx, store.Source{Name: "test", Kind: "rss", URL: srv.URL + "/rss", IntervalMin: 5, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	src, _ := c.St.GetSource(ctx, id)
	res := c.crawl(ctx, *src)
	if res.Err != nil || res.Items != 3 || res.New != 2 || res.ETag != `"v1"` {
		t.Fatalf("first run: %+v", res)
	}
	_ = c.St.FinishSourceRun(ctx, id, res)

	// .torrent item: ready with files
	ct, err := c.Meta.CatalogByHash(ctx, mi.Infohash)
	if err != nil || ct.Status != "ready" || len(ct.Files) != 1 || ct.Source != "test" || !ct.Trusted || ct.PageURL != srv.URL+"/view/1" || ct.PublishedAt != "2026-10-18T10:00:00Z" {
		t.Fatalf("torrent item: %v %+v", err, ct)
	}
	// magnet item: pending + metadata job with the feed's tracker
	if st, ok := c.Meta.KnownTorrent(ctx, "8d044029c825a9b881e3f6753f7093f8d256a85d"); !ok || st != "pending" {
		t.Errorf("magnet item: %v %q", ok, st)
	}
	job, _ := c.St.GetMetadataJob(ctx, "8d044029c825a9b881e3f6753f7093f8d256a85d")
	if job == nil || job.Status != "pending" || len(job.Trackers) != 1 || !c.JobActive(ctx, job.Infohash) {
		t.Errorf("job: %+v", job)
	}
	// the mirror item had the same infohash: downloaded once, not duplicated
	if n := torrentHits.Load(); n != 2 {
		t.Errorf("torrent downloads: %d", n)
	}

	// second run: conditional GET → 304, nothing downloaded
	src, _ = c.St.GetSource(ctx, id)
	res = c.crawl(ctx, *src)
	if res.Err != nil || !res.NotModified || notModified.Load() != 1 {
		t.Fatalf("second run: %+v", res)
	}
	// even without validators the items are remembered (no re-download)
	_, _ = c.St.W.ExecContext(ctx, `UPDATE sources SET etag = '' WHERE id = ?`, id)
	src, _ = c.St.GetSource(ctx, id)
	res = c.crawl(ctx, *src)
	if res.Err != nil || res.New != 0 || torrentHits.Load() != 2 {
		t.Fatalf("third run: %+v hits=%d", res, torrentHits.Load())
	}
}

func TestCrawlFailureBackoff(t *testing.T) {
	ctx := context.Background()
	c := testCrawler(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "nope", 503) }))
	defer srv.Close()
	id, _ := c.St.AddSource(ctx, store.Source{Name: "bad", Kind: "rss", URL: srv.URL + "/rss?token=secret", IntervalMin: 10, Enabled: true})
	src, _ := c.St.GetSource(ctx, id)
	c.runSource(ctx, *src)
	src, _ = c.St.GetSource(ctx, id)
	if src.FailCount != 1 || src.LastError == "" || strings.Contains(src.LastError, "secret") || src.LastRunAt == nil {
		t.Fatalf("after failure: %+v", src)
	}
	if d := store.SourceBackoff(*src); d != 20*time.Minute {
		t.Errorf("backoff %v", d)
	}
	src.FailCount = 20
	if d := store.SourceBackoff(*src); d != store.MaxSourceBackoff {
		t.Errorf("capped backoff %v", d)
	}
}

func TestFetcherBlocksPrivateAddresses(t *testing.T) {
	f := NewFetcher(nil, nil)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()
	if _, err := f.Get(context.Background(), srv.URL, GetOptions{}); err == nil || !strings.Contains(err.Error(), "non-public") {
		t.Errorf("expected SSRF guard, got %v", err)
	}
}
