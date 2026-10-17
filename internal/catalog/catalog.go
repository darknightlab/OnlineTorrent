package catalog

import (
	"context"
	"log/slog"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"

	"github.com/darknightlab/onlinetorrent/internal/meta"
	"github.com/darknightlab/onlinetorrent/internal/policy"
	"github.com/darknightlab/onlinetorrent/internal/store"
)

// Ref is an id+name pair.
type Ref struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// Torrent is the normalized listing item (see docs/api.md).
type Torrent struct {
	ID          string         `json:"id"`
	Infohash    string         `json:"infohash"`
	Title       string         `json:"title"`
	SizeBytes   int64          `json:"size_bytes"`
	Files       int            `json:"files"`
	Team        *Ref           `json:"team"`
	Tags        []Ref          `json:"tags"`
	PublishedAt string         `json:"published_at"`
	Source      string         `json:"source"`
	Seeders     int            `json:"seeders"`
	Leechers    int            `json:"leechers"`
	Access      *policy.Access `json:"access,omitempty"`

	FileList  []string `json:"-"`
	FileSizes []int64  `json:"-"`
	tagIDs    []string
}

// Subject converts to a policy subject.
func (t *Torrent) Subject() policy.Subject {
	s := policy.Subject{Infohash: t.Infohash, Title: t.Title, Files: t.FileList, Trusted: t.Source == "bangumi.moe"}
	if t.Team != nil {
		s.Team = t.Team.Name
	}
	for _, g := range t.Tags {
		s.Tags = append(s.Tags, g.Name, g.ID)
	}
	return s
}

type cacheEntry[T any] struct {
	v   T
	exp time.Time
}

type ttlCache[T any] struct {
	mu sync.Mutex
	m  map[string]cacheEntry[T]
}

func (c *ttlCache[T]) get(k string) (T, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.m[k]
	if !ok || time.Now().After(e.exp) {
		var z T
		return z, false
	}
	return e.v, true
}

// ttlCacheMax bounds a ttlCache; beyond it expired entries are purged and, if still too
// big, an arbitrary half is dropped (map iteration order is random).
const ttlCacheMax = 2000

func (c *ttlCache[T]) set(k string, v T, ttl time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.m == nil {
		c.m = map[string]cacheEntry[T]{}
	}
	if len(c.m) >= ttlCacheMax {
		now := time.Now()
		for kk, e := range c.m {
			if now.After(e.exp) {
				delete(c.m, kk)
			}
		}
		if len(c.m) >= ttlCacheMax {
			drop := len(c.m) / 2
			for kk := range c.m {
				if drop == 0 {
					break
				}
				delete(c.m, kk)
				drop--
			}
		}
	}
	c.m[k] = cacheEntry[T]{v, time.Now().Add(ttl)}
}

func (c *ttlCache[T]) len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.m)
}

type pageResult struct {
	items     []*Torrent
	pageCount int
}

// Catalog is the aggregation service.
type Catalog struct {
	st   *store.Store
	meta *meta.Store
	log  *slog.Logger
	moe  *moeClient

	pages   ttlCache[pageResult]
	details ttlCache[*Torrent]
	sf      singleflight.Group // dedupes concurrent upstream fetches per cache key

	trMu    sync.Mutex
	trProxy string
	tr      *http.Transport

	namesMu sync.RWMutex
	teams   map[string]string
	tags    map[string]moeTag
	missMu  sync.Mutex
	missing map[string]time.Time // "team:id"/"tag:id" -> retry after (negative cache)

	schedMu   sync.RWMutex
	sched     *Schedule
	rebuildMu sync.Mutex

	coversMu sync.RWMutex
	covers   map[string]string // moe tag id -> cover URL

	matchCh chan meta.TorrentInput
	wg      sync.WaitGroup
}

// New creates the catalog.
func New(st *store.Store, ms *meta.Store, log *slog.Logger) *Catalog {
	c := &Catalog{st: st, meta: ms, log: log, teams: map[string]string{}, tags: map[string]moeTag{}, missing: map[string]time.Time{}, covers: map[string]string{}, matchCh: make(chan meta.TorrentInput, 512)}
	c.moe = &moeClient{base: func() string { return st.Str(store.KeyMoeBase) }, hc: c.HTTPClient, lim: newPacer(3, 2*time.Second)}
	return c
}

// HTTPClient returns a client honoring the upstream proxy setting. The transport (and its
// connection pool) is shared and only rebuilt when the proxy setting changes.
func (c *Catalog) HTTPClient() *http.Client {
	p := c.st.Str(store.KeyUpstreamProxy)
	c.trMu.Lock()
	if c.tr == nil || c.trProxy != p {
		tr := http.DefaultTransport.(*http.Transport).Clone()
		tr.MaxIdleConnsPerHost = 4
		if p != "" {
			if u, err := url.Parse(p); err == nil {
				tr.Proxy = http.ProxyURL(u)
			}
		}
		if c.tr != nil {
			c.tr.CloseIdleConnections()
		}
		c.tr, c.trProxy = tr, p
	}
	tr := c.tr
	c.trMu.Unlock()
	return &http.Client{Transport: tr, Timeout: 30 * time.Second}
}

const (
	missTTL    = time.Hour        // id not returned by bangumi.moe
	missErrTTL = 10 * time.Minute // lookup request failed
)

func (c *Catalog) isMissing(k string, now time.Time) bool {
	c.missMu.Lock()
	defer c.missMu.Unlock()
	t, ok := c.missing[k]
	if ok && now.After(t) {
		delete(c.missing, k)
		return false
	}
	return ok
}

func (c *Catalog) markMissing(keys []string, ttl time.Duration) {
	c.missMu.Lock()
	defer c.missMu.Unlock()
	now := time.Now()
	if len(c.missing) > 20000 {
		for k, t := range c.missing {
			if now.After(t) {
				delete(c.missing, k)
			}
		}
	}
	for _, k := range keys {
		c.missing[k] = now.Add(ttl)
	}
}

func (c *Catalog) resolveNames(ctx context.Context, ts []moeTorrent) {
	var needTeams, needTags []string
	seenTeam, seenTag := map[string]bool{}, map[string]bool{}
	now := time.Now()
	c.namesMu.RLock()
	for _, t := range ts {
		if (t.Team == nil || t.Team.Name == "") && t.TeamID != "" && c.teams[t.TeamID] == "" && !seenTeam[t.TeamID] {
			seenTeam[t.TeamID] = true
			needTeams = append(needTeams, t.TeamID)
		}
		for _, id := range t.TagIDs {
			if _, ok := c.tags[id]; !ok && !seenTag[id] {
				seenTag[id] = true
				needTags = append(needTags, id)
			}
		}
	}
	c.namesMu.RUnlock()
	needTeams = slicesFilter(needTeams, func(id string) bool { return !c.isMissing("team:"+id, now) })
	needTags = slicesFilter(needTags, func(id string) bool { return !c.isMissing("tag:"+id, now) })
	if len(needTeams) > 0 {
		teams, err := c.moe.teams(ctx, needTeams)
		got := map[string]bool{}
		if err == nil {
			c.namesMu.Lock()
			for _, t := range teams {
				c.teams[t.ID] = t.Name
				got[t.ID] = true
			}
			c.namesMu.Unlock()
		}
		var miss []string
		for _, id := range needTeams {
			if !got[id] {
				miss = append(miss, "team:"+id)
			}
		}
		c.markMissing(miss, ttlFor(err))
	}
	if len(needTags) > 0 {
		tags, err := c.moe.tags(ctx, needTags)
		got := map[string]bool{}
		if err == nil {
			c.namesMu.Lock()
			for _, t := range tags {
				c.tags[t.ID] = t
				got[t.ID] = true
			}
			c.namesMu.Unlock()
		}
		var miss []string
		for _, id := range needTags {
			if !got[id] {
				miss = append(miss, "tag:"+id)
			}
		}
		c.markMissing(miss, ttlFor(err))
	}
}

func ttlFor(err error) time.Duration {
	if err != nil {
		return missErrTTL
	}
	return missTTL
}

func slicesFilter(in []string, keep func(string) bool) []string {
	out := in[:0]
	for _, v := range in {
		if keep(v) {
			out = append(out, v)
		}
	}
	return out
}

func (c *Catalog) normalize(t moeTorrent) *Torrent {
	out := &Torrent{
		ID: t.ID, Infohash: strings.ToLower(t.InfoHash), Title: t.Title, SizeBytes: parseSize(t.Size),
		PublishedAt: moeTime(t.PublishTime), Source: "bangumi.moe", Seeders: t.Seeders, Leechers: t.Leechers, tagIDs: t.TagIDs,
		Tags: []Ref{},
	}
	for _, f := range t.Content {
		if len(f) > 0 {
			out.FileList = append(out.FileList, f[0])
			var sz int64
			if len(f) > 1 {
				sz = parseSize(f[1])
			}
			out.FileSizes = append(out.FileSizes, sz)
		}
	}
	out.Files = len(out.FileList)
	c.namesMu.RLock()
	defer c.namesMu.RUnlock()
	if t.Team != nil && t.Team.Name != "" {
		out.Team = &Ref{ID: t.Team.ID, Name: t.Team.Name}
	} else if n := c.teams[t.TeamID]; n != "" {
		out.Team = &Ref{ID: t.TeamID, Name: n}
	}
	for _, id := range t.TagIDs {
		if tg, ok := c.tags[id]; ok && tg.Type != "misc" {
			out.Tags = append(out.Tags, Ref{ID: id, Name: tg.DisplayName()})
		}
	}
	return out
}

func (c *Catalog) normalizeAll(ctx context.Context, raw []moeTorrent) []*Torrent {
	c.resolveNames(ctx, raw)
	out := make([]*Torrent, 0, len(raw))
	for _, t := range raw {
		if t.InfoHash == "" {
			continue
		}
		n := c.normalize(t)
		out = append(out, n)
		if len(n.FileList) > 0 {
			c.enqueueMatch(n)
		}
	}
	return out
}

func (c *Catalog) enqueueMatch(t *Torrent) {
	in := meta.TorrentInput{Infohash: t.Infohash, Title: t.Title, Source: t.Source, SourceID: t.ID, PublishedAt: t.PublishedAt, MoeTagIDs: t.tagIDs}
	if t.Team != nil {
		in.Team = t.Team.Name
	}
	for i, f := range t.FileList {
		var sz int64
		if i < len(t.FileSizes) {
			sz = t.FileSizes[i]
		}
		in.Files = append(in.Files, meta.FileInput{Index: i, Path: f, Size: sz})
	}
	select {
	case c.matchCh <- in:
	default:
	}
}

// MatchTorrent records a user-played torrent's files once metadata is known.
func (c *Catalog) MatchTorrent(in meta.TorrentInput) {
	select {
	case c.matchCh <- in:
	default:
	}
}

// fetchPage serves key from cache or fetches it once (singleflight) for all concurrent callers.
// The upstream fetch runs detached from the caller's cancellation (bounded by 45s) so one
// impatient client doesn't fail everybody sharing the flight.
func (c *Catalog) fetchPage(ctx context.Context, key string, ttl time.Duration, fetch func(context.Context) (moePage, error)) ([]*Torrent, int, error) {
	if v, ok := c.pages.get(key); ok {
		return v.items, v.pageCount, nil
	}
	ch := c.sf.DoChan(key, func() (any, error) {
		if v, ok := c.pages.get(key); ok {
			return v, nil
		}
		fctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 45*time.Second)
		defer cancel()
		p, err := fetch(fctx)
		if err != nil {
			return nil, err
		}
		res := pageResult{c.normalizeAll(fctx, p.Torrents), p.PageCount}
		c.pages.set(key, res, ttl)
		return res, nil
	})
	select {
	case <-ctx.Done():
		return nil, 0, ctx.Err()
	case r := <-ch:
		if r.Err != nil {
			return nil, 0, r.Err
		}
		v := r.Val.(pageResult)
		return v.items, v.pageCount, nil
	}
}

// Latest returns a page of the newest bangumi.moe torrents.
func (c *Catalog) Latest(ctx context.Context, page int) ([]*Torrent, int, error) {
	return c.fetchPage(ctx, "latest:"+itoa(page), 3*time.Minute, func(ctx context.Context) (moePage, error) { return c.moe.latest(ctx, page) })
}

// Search queries bangumi.moe.
func (c *Catalog) Search(ctx context.Context, q string, page int) ([]*Torrent, int, error) {
	return c.fetchPage(ctx, "search:"+itoa(page)+":"+q, 5*time.Minute, func(ctx context.Context) (moePage, error) { return c.moe.search(ctx, q, page) })
}

// tagTTL is how long a tag listing is cached (schedule rebuilds only refetch recent airings).
const tagTTL = 60 * time.Minute

// ByTag lists torrents of a bangumi.moe tag (e.g. an anime).
func (c *Catalog) ByTag(ctx context.Context, tagID string, page int) ([]*Torrent, int, error) {
	return c.fetchPage(ctx, "tag:"+itoa(page)+":"+tagID, tagTTL, func(ctx context.Context) (moePage, error) { return c.moe.byTag(ctx, []string{tagID}, page) })
}

// Detail fetches one bangumi.moe torrent (with files).
func (c *Catalog) Detail(ctx context.Context, id string) (*Torrent, error) {
	if v, ok := c.details.get(id); ok {
		return v, nil
	}
	ch := c.sf.DoChan("detail:"+id, func() (any, error) {
		if v, ok := c.details.get(id); ok {
			return v, nil
		}
		fctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 45*time.Second)
		defer cancel()
		raw, err := c.moe.detail(fctx, id)
		if err != nil {
			return nil, err
		}
		c.namesMu.Lock()
		for _, tg := range raw.Tags {
			c.tags[tg.ID] = tg
		}
		c.namesMu.Unlock()
		raw.Tags = nil
		items := c.normalizeAll(fctx, []moeTorrent{raw})
		if len(items) == 0 {
			return nil, store.ErrNotFound
		}
		c.details.set(id, items[0], 30*time.Minute)
		return items[0], nil
	})
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case r := <-ch:
		if r.Err != nil {
			return nil, r.Err
		}
		return r.Val.(*Torrent), nil
	}
}

func itoa(i int) string { return strconv.Itoa(i) }

// Run starts background jobs; Wait blocks until they exit after ctx is done.
func (c *Catalog) Run(ctx context.Context) {
	c.wg.Go(func() { c.matchLoop(ctx) })
	c.wg.Go(func() {
		c.refreshBangumiData(ctx)
		t := time.NewTicker(12 * time.Hour)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				c.refreshBangumiData(ctx)
			}
		}
	})
	c.wg.Go(func() {
		// schedule (and moe covers) every 15 min; ingest latest pages every 5 min
		c.refreshCovers(ctx)
		c.ingestLatest(ctx, 3)
		c.rebuildSchedule(ctx)
		sched := time.NewTicker(15 * time.Minute)
		ingest := time.NewTicker(5 * time.Minute)
		covers := time.NewTicker(6 * time.Hour)
		defer sched.Stop()
		defer ingest.Stop()
		defer covers.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ingest.C:
				c.ingestLatest(ctx, 1)
			case <-covers.C:
				c.refreshCovers(ctx)
			case <-sched.C:
				c.rebuildSchedule(ctx)
			}
		}
	})
}

// Wait blocks until the background jobs started by Run have exited.
func (c *Catalog) Wait() { c.wg.Wait() }

func (c *Catalog) matchLoop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case in := <-c.matchCh:
			if err := c.meta.Match(ctx, in); err != nil {
				c.log.Warn("meta match", "ih", in.Infohash, "err", err)
			}
		}
	}
}

func (c *Catalog) refreshBangumiData(ctx context.Context) {
	cctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	hc := c.HTTPClient()
	hc.Timeout = 3 * time.Minute
	n, err := c.meta.ImportBangumiData(cctx, hc, c.st.Str(store.KeyBangumiDataURL), 3)
	if err != nil {
		c.log.Warn("bangumi-data import failed", "err", err)
		return
	}
	c.log.Info("bangumi-data imported", "works", n)
	c.rebuildSchedule(ctx)
}

func (c *Catalog) refreshCovers(ctx context.Context) {
	list, err := c.moe.current(ctx)
	if err != nil {
		c.log.Warn("moe current failed", "err", err)
		return
	}
	base := strings.TrimRight(c.st.Str(store.KeyMoeBase), "/")
	c.coversMu.Lock()
	for _, b := range list {
		if b.Cover != "" {
			c.covers[b.TagID] = base + "/" + strings.TrimLeft(b.Cover, "/")
		}
	}
	c.coversMu.Unlock()
}

func (c *Catalog) ingestLatest(ctx context.Context, pages int) {
	for p := 1; p <= pages; p++ {
		if _, _, err := c.Latest(ctx, p); err != nil {
			c.log.Warn("ingest latest", "page", p, "err", err)
			return
		}
	}
}

func sortTorrentsNewest(ts []*Torrent) {
	sort.SliceStable(ts, func(i, j int) bool { return ts[i].PublishedAt > ts[j].PublishedAt })
}
