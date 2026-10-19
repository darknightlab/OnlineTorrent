package catalog

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
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
	ID          string          `json:"id"`
	Infohash    string          `json:"infohash"`
	Title       string          `json:"title"`
	SizeBytes   int64           `json:"size_bytes"`
	Files       int             `json:"files"`
	Team        *Ref            `json:"team"`
	Tags        []Ref           `json:"tags"`
	PublishedAt string          `json:"published_at"`
	Source      string          `json:"source"`
	Seeders     int             `json:"seeders"`  // = swarm.bt_seeders (0 if unknown)
	Leechers    int             `json:"leechers"` // = swarm.bt_leechers (0 if unknown)
	Swarm       *meta.SwarmView `json:"swarm"`
	Access      *policy.Access  `json:"access,omitempty"`

	FileList  []string `json:"-"`
	FileSizes []int64  `json:"-"`
	Trusted   bool     `json:"-"` // from a trusted crawl source (policy: no user-magnet floor)
	TagIDs    []string `json:"-"` // every bangumi.moe tag id (incl. misc ones hidden from Tags)
	PageURL   string   `json:"-"`
}

// Subject converts to a policy subject.
func (t *Torrent) Subject() policy.Subject {
	s := policy.Subject{Infohash: t.Infohash, Title: t.Title, Files: t.FileList, Trusted: t.Trusted}
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

// Catalog is the aggregation service.
type Catalog struct {
	st   *store.Store
	meta *meta.Store
	log  *slog.Logger
	moe  *moeClient

	extNeg ttlCache[bool]     // external subject searches with no result (1h)
	extLim *pacer             // outbound bgm.tv / TMDB search requests
	sf     singleflight.Group // dedupes concurrent external searches per keyword

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

	wg sync.WaitGroup
}

// New creates the catalog.
func New(st *store.Store, ms *meta.Store, log *slog.Logger) *Catalog {
	c := &Catalog{st: st, meta: ms, log: log, teams: map[string]string{}, tags: map[string]moeTag{}, missing: map[string]time.Time{}, covers: map[string]string{}, extLim: newPacer(1, 10*time.Second)}
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
		PublishedAt: moeTime(t.PublishTime), Source: "bangumi.moe", Seeders: t.Seeders, Leechers: t.Leechers, TagIDs: t.TagIDs,
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
		out = append(out, c.normalize(t))
	}
	return out
}

// ParseMoePage decodes a bangumi.moe torrent page (GET /api/torrent/page/N) fetched by the
// crawler, resolving team and tag names (cached; looked up on bangumi.moe when missing).
func (c *Catalog) ParseMoePage(ctx context.Context, body []byte) ([]*Torrent, error) {
	var p moePage
	if err := json.Unmarshal(body, &p); err != nil {
		return nil, fmt.Errorf("bangumi.moe page: %w", err)
	}
	return c.normalizeAll(ctx, p.Torrents), nil
}

// MatchInput converts a listing torrent to a mapping-DB input.
func (t *Torrent) MatchInput() meta.TorrentInput {
	in := meta.TorrentInput{Infohash: t.Infohash, Title: t.Title, Source: t.Source, SourceID: t.ID, Trusted: t.Trusted,
		PublishedAt: t.PublishedAt, MoeTagIDs: t.TagIDs, Size: t.SizeBytes, PageURL: t.PageURL}
	if t.Team != nil {
		in.Team, in.TeamID = t.Team.Name, t.Team.ID
	}
	for _, g := range t.Tags {
		in.Tags = append(in.Tags, meta.TagRef{ID: g.ID, Name: g.Name})
	}
	for i, f := range t.FileList {
		var sz int64
		if i < len(t.FileSizes) {
			sz = t.FileSizes[i]
		}
		in.Files = append(in.Files, meta.FileInput{Index: i, Path: f, Size: sz})
	}
	return in
}

// FromCatalog converts a local catalogue row to the listing shape.
func FromCatalog(r *meta.CatalogTorrent) *Torrent {
	t := &Torrent{ID: "ih:" + r.Infohash, Infohash: r.Infohash, Title: r.Title, SizeBytes: r.Size, Files: r.FileCount,
		PublishedAt: r.PublishedAt, Source: r.Source, Trusted: r.Trusted, Tags: []Ref{}, PageURL: r.PageURL}
	if r.SourceKind == store.SourceKindBangumiMoe && r.SourceID != "" {
		t.ID = r.SourceID
	}
	if r.Team != "" {
		t.Team = &Ref{ID: r.TeamID, Name: r.Team}
	}
	for _, g := range r.Tags {
		t.Tags = append(t.Tags, Ref{ID: g.ID, Name: g.Name})
		t.TagIDs = append(t.TagIDs, g.ID)
	}
	for _, f := range r.Files {
		t.FileList = append(t.FileList, f.Path)
		t.FileSizes = append(t.FileSizes, f.Size)
	}
	if t.Files == 0 {
		t.Files = len(t.FileList)
	}
	return t
}

// FromCatalogList converts many rows.
func FromCatalogList(rs []*meta.CatalogTorrent) []*Torrent {
	out := make([]*Torrent, len(rs))
	for i, r := range rs {
		out[i] = FromCatalog(r)
	}
	return out
}

// Run starts background jobs; Wait blocks until they exit after ctx is done.
func (c *Catalog) Run(ctx context.Context) {
	c.wg.Go(func() { c.archiveLoop(ctx) })
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
		// schedule every 15 min (latest torrents come from the local catalogue, which the
		// crawler fills); bangumi.moe covers every 6h
		c.refreshCovers(ctx)
		c.rebuildSchedule(ctx)
		sched := time.NewTicker(15 * time.Minute)
		covers := time.NewTicker(6 * time.Hour)
		defer sched.Stop()
		defer covers.Stop()
		for {
			select {
			case <-ctx.Done():
				return
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
