// Package crawl polls the admin-configured torrent sources (RSS/Atom feeds and the
// bangumi.moe JSON latest page) and ingests every new torrent into the local catalogue
// (internal/meta): .torrent files are downloaded and parsed locally, magnet-only items
// go through a qBittorrent metadata queue. Each infohash is ingested once.
//
// Structure follows YotsubaNews' ingest/scheduler.go + sources/: a scheduler scans due
// sources on a tick and dispatches them to a bounded worker pool (inflight guard, the poll
// is stamped before fetching so failing sources also wait a full interval), plus
// exponential backoff on consecutive failures.
package crawl

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/darknightlab/onlinetorrent/internal/catalog"
	"github.com/darknightlab/onlinetorrent/internal/meta"
	"github.com/darknightlab/onlinetorrent/internal/qb"
	"github.com/darknightlab/onlinetorrent/internal/store"
)

const (
	schedulerTick = 30 * time.Second
	runTimeout    = 10 * time.Minute
	// .torrent downloads per run (the rest is picked up by the next run)
	maxDownloadsPerRun = 60
)

// KindSpec describes a source kind for the admin UI (GET /admin/sources/schema).
type KindSpec struct {
	Value       string `json:"value"`
	Label       string `json:"label"`
	Description string `json:"description"`
	ExampleURL  string `json:"example_url"`
}

// Kinds lists the supported source kinds.
func Kinds() []KindSpec {
	return []KindSpec{
		{Value: store.SourceKindRSS, Label: "RSS / Atom", Description: "通用 RSS/Atom：enclosure/link 中的 .torrent、磁力链接或 nyaa:infoHash", ExampleURL: "https://nyaa.si/?page=rss&c=1_3&f=0"},
		{Value: store.SourceKindBangumiMoe, Label: "bangumi.moe", Description: "bangumi.moe JSON 最新列表第 1 页（含标签与文件列表，作为强刮削线索）", ExampleURL: "https://bangumi.moe/api/torrent/page/1"},
	}
}

// Crawler runs sources and the metadata queue.
type Crawler struct {
	St    *store.Store
	Meta  *meta.Store
	Cat   *catalog.Catalog
	Pool  *qb.Pool
	Log   *slog.Logger
	Fetch *Fetcher

	mu       sync.Mutex
	inflight map[int64]bool
	wg       sync.WaitGroup

	queue metaQueue
}

// New creates a crawler.
func New(st *store.Store, ms *meta.Store, cat *catalog.Catalog, pool *qb.Pool, log *slog.Logger) *Crawler {
	c := &Crawler{St: st, Meta: ms, Cat: cat, Pool: pool, Log: log, inflight: map[int64]bool{}}
	c.Fetch = NewFetcher(func() string { return st.Str(store.KeyUpstreamProxy) }, func() string {
		base := strings.TrimRight(st.Str(store.KeySiteBaseURL), "/")
		if base == "" {
			return defaultUserAgent
		}
		return "onlinetorrent/2 (+" + base + ")"
	})
	c.queue.init()
	return c
}

// Run blocks until ctx is done; in-flight runs finish (bounded by their timeouts) first.
func (c *Crawler) Run(ctx context.Context) {
	c.wg.Go(func() { c.runQueue(ctx) })
	c.wg.Go(func() { c.maintenance(ctx) })
	t := time.NewTicker(schedulerTick)
	defer t.Stop()
	for {
		if c.St.Bool(store.KeyCrawlEnabled) {
			c.dispatchDue(ctx)
		}
		select {
		case <-ctx.Done():
			c.wg.Wait()
			return
		case <-t.C:
		}
	}
}

func (c *Crawler) workers() int {
	n := int(c.St.Num(store.KeyCrawlWorkers))
	return min(max(n, 1), 8)
}

func (c *Crawler) dispatchDue(ctx context.Context) {
	due, err := c.St.ListDueSources(ctx, time.Now())
	if err != nil {
		if ctx.Err() == nil {
			c.Log.Error("crawl: list due sources", "err", err)
		}
		return
	}
	for _, src := range due {
		if !c.start(ctx, src, false) {
			break // worker pool full; the rest waits for the next tick
		}
	}
}

// Running reports whether a source is being crawled now.
func (c *Crawler) Running(id int64) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.inflight[id]
}

// ErrBusy is returned by RunNow when the source is already running.
var ErrBusy = errors.New("该抓取源正在抓取中")

// RunNow starts a run of one source in the background (admin "立即抓取"), regardless of
// its interval, enabled flag or the global switch.
func (c *Crawler) RunNow(ctx context.Context, id int64) error {
	src, err := c.St.GetSource(ctx, id)
	if err != nil {
		return err
	}
	if c.Running(id) {
		return ErrBusy
	}
	c.start(ctx, *src, true)
	return nil
}

// start dispatches src unless it is already in flight. forced runs ignore the worker cap.
func (c *Crawler) start(ctx context.Context, src store.Source, forced bool) bool {
	c.mu.Lock()
	if c.inflight[src.ID] {
		c.mu.Unlock()
		return true
	}
	if !forced && len(c.inflight) >= c.workers() {
		c.mu.Unlock()
		return false
	}
	c.inflight[src.ID] = true
	c.mu.Unlock()
	base := ctx
	c.wg.Go(func() {
		defer func() {
			c.mu.Lock()
			delete(c.inflight, src.ID)
			c.mu.Unlock()
		}()
		c.runSource(base, src)
	})
	return true
}

// Wait blocks until background runs have finished.
func (c *Crawler) Wait() { c.wg.Wait() }

func (c *Crawler) runSource(ctx context.Context, src store.Source) {
	// stamp first: a failing source also waits a full interval
	if err := c.St.TouchSourceRun(ctx, src.ID); err != nil {
		return
	}
	rctx, cancel := context.WithTimeout(ctx, runTimeout)
	defer cancel()
	res := c.crawl(rctx, src)
	if ctx.Err() != nil && res.Err == nil {
		res.Err = errors.New("服务关闭，抓取中断")
	}
	if err := c.St.FinishSourceRun(context.WithoutCancel(ctx), src.ID, res); err != nil {
		c.Log.Warn("crawl: save result", "source", src.Name, "err", err)
	}
	if res.Err != nil {
		c.Log.Warn("crawl failed", "source", src.Name, "err", res.Err)
		return
	}
	c.Log.Info("crawled", "source", src.Name, "items", res.Items, "new", res.New, "not_modified", res.NotModified)
	if res.New > 0 && c.Cat != nil {
		c.Cat.RefreshLatest(ctx)
	}
}

// crawl fetches and ingests one source.
func (c *Crawler) crawl(ctx context.Context, src store.Source) store.SourceResult {
	accept := "application/rss+xml, application/atom+xml, application/xml;q=0.9, text/xml;q=0.8, */*;q=0.5"
	if src.Kind == store.SourceKindBangumiMoe {
		accept = "application/json"
	}
	resp, err := c.Fetch.Get(ctx, src.URL, GetOptions{ETag: src.ETag, LastModified: src.LastModified, ProxyDisabled: src.ProxyDisabled, Accept: accept})
	if err != nil {
		return store.SourceResult{Err: feedError(src.URL, err)}
	}
	if resp.NotModified {
		return store.SourceResult{NotModified: true}
	}
	items, err := c.parse(ctx, src, resp.Body)
	if err != nil {
		return store.SourceResult{Err: feedError(src.URL, err)}
	}
	n, complete, err := c.ingestItems(ctx, src, items)
	res := store.SourceResult{Items: len(items), New: n}
	if complete {
		// only remember the validators when every item was handled; otherwise a 304 on
		// the next run would hide the remaining ones
		res.ETag, res.LastModified = resp.ETag, resp.LastModified
	}
	if err != nil {
		if ctx.Err() != nil || n == 0 && len(items) > 0 {
			res.Err = err
		}
		c.Log.Warn("crawl: some items failed", "source", src.Name, "err", err)
	}
	return res
}

// parse turns a response body into feed items.
func (c *Crawler) parse(ctx context.Context, src store.Source, body []byte) ([]FeedItem, error) {
	if src.Kind == store.SourceKindBangumiMoe {
		if c.Cat == nil {
			return nil, errors.New("bangumi.moe parser unavailable")
		}
		ts, err := c.Cat.ParseMoePage(ctx, body)
		if err != nil {
			return nil, err
		}
		items := make([]FeedItem, 0, len(ts))
		for _, t := range ts {
			items = append(items, FeedItem{GUID: t.ID, Title: t.Title, Infohash: t.Infohash, Size: t.SizeBytes, PublishedAt: t.PublishedAt,
				Magnet: "magnet:?xt=urn:btih:" + t.Infohash, moe: t})
		}
		return items, nil
	}
	_, items, err := ParseFeed(body)
	return items, err
}

// ProbeSample is one item of a probe.
type ProbeSample struct {
	Title       string `json:"title"`
	URL         string `json:"url,omitempty"`
	PublishedAt string `json:"published_at,omitempty"`
	Infohash    string `json:"infohash,omitempty"`
	TorrentURL  string `json:"torrent_url,omitempty"`
	Magnet      bool   `json:"magnet"`
}

// Probe is the result of testing a source URL without ingesting anything (same shape as
// YotsubaNews' RSSProbe, plus torrent fields).
type Probe struct {
	Title     string        `json:"title"`
	Link      string        `json:"link,omitempty"`
	Format    string        `json:"format"`
	ItemCount int           `json:"item_count"`
	Samples   []ProbeSample `json:"samples"`
}

// ProbeSource fetches and parses a source URL.
func (c *Crawler) ProbeSource(ctx context.Context, kind, rawURL string, proxyDisabled bool) (*Probe, error) {
	accept := "application/rss+xml, application/atom+xml, application/xml;q=0.9, */*;q=0.5"
	if kind == store.SourceKindBangumiMoe {
		accept = "application/json"
	}
	resp, err := c.Fetch.Get(ctx, rawURL, GetOptions{ProxyDisabled: proxyDisabled, Accept: accept})
	if err != nil {
		return nil, feedError(rawURL, err)
	}
	out := &Probe{Samples: []ProbeSample{}}
	var items []FeedItem
	if kind == store.SourceKindBangumiMoe {
		items, err = c.parse(ctx, store.Source{Kind: kind}, resp.Body)
		out.Title, out.Format = "bangumi.moe", "bangumi.moe JSON"
	} else {
		var info FeedInfo
		info, items, err = ParseFeed(resp.Body)
		out.Title, out.Link, out.Format = info.Title, info.Link, info.Format
	}
	if err != nil {
		return nil, feedError(rawURL, err)
	}
	out.ItemCount = len(items)
	for _, it := range items {
		if len(out.Samples) == 3 {
			break
		}
		out.Samples = append(out.Samples, ProbeSample{Title: it.Title, URL: it.PageURL, PublishedAt: it.PublishedAt, Infohash: it.Infohash, TorrentURL: it.TorrentURL, Magnet: it.Magnet != ""})
	}
	if out.ItemCount == 0 {
		return out, fmt.Errorf("%s: 没有可识别的种子条目（需要 .torrent 链接、磁力链接或 infohash）", endpointForError(rawURL))
	}
	return out, nil
}

func (c *Crawler) maintenance(ctx context.Context) {
	t := time.NewTicker(6 * time.Hour)
	defer t.Stop()
	for {
		if n, err := c.St.PruneSourceItems(ctx); err == nil && n > 0 {
			c.Log.Info("crawl: pruned item memory", "rows", n)
		}
		_, _ = c.St.PruneMetadataJobs(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
