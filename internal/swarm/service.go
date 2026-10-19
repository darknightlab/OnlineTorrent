package swarm

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"log/slog"
	"net"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/darknightlab/onlinetorrent/internal/meta"
	"github.com/darknightlab/onlinetorrent/internal/qb"
	"github.com/darknightlab/onlinetorrent/internal/store"
)

// On-demand scraping: there is no periodic scrape of the catalogue (it can hold tens of
// thousands of torrents). An API response that includes torrents whose stats are missing or
// older than StaleAfter scrapes exactly those right then (Lookup): batched per tracker, all
// trackers in parallel, the response waits at most its budget and the scrape finishes in the
// background. Tracker health is derived passively from these real scrapes: nothing is ever
// sent to a tracker except on behalf of an API response.
const (
	StaleAfter = 30 * time.Minute // cache lifetime of swarm stats

	ownPerTorrent    = 3   // trackers taken from a torrent's own announce list
	maxOwnTrackers   = 16  // distinct own (untrusted) trackers per scrape run
	scrapeWorkers    = 32  // trackers asked in parallel
	maxOnDemand      = 100 // stale torrents refreshed per request (subject lists ≤ 100)
	maxOnDemandRuns  = 8   // concurrent on-demand scrape runs (beyond: stored data only)
	onDemandTimeout  = 25 * time.Second
	partialFlush     = 1200 * time.Millisecond // < the API budget (1.5 s)
	healthHistoryCap = 200
)

// Health is the state of a configured tracker as of its last real use (an on-demand
// scrape). Status "unused" = not used since the process started.
type Health struct {
	URL         string  `json:"url"`
	Kind        string  `json:"kind"`   // udp|http|wss
	List        string  `json:"list"`   // bt|webtorrent
	Status      string  `json:"status"` // ok|error|unused
	Result      string  `json:"result"` // last result: ok|timeout|dns|network|protocol|error ("" if unused)
	LatencyMS   int64   `json:"latency_ms"`
	LastError   string  `json:"last_error"` // message of the last failure (kept after a later success)
	CheckedAt   *string `json:"checked_at"` // last use
	LastOKAt    *string `json:"last_ok_at"`
	LastErrorAt *string `json:"last_error_at"`
	OKCount     int     `json:"ok_count"`   // successful requests since start
	FailCount   int     `json:"fail_count"` // failed requests since start
}

// Health statuses / results.
const (
	StatusOK     = "ok"
	StatusError  = "error"
	StatusUnused = "unused"

	ResultOK       = "ok"
	ResultTimeout  = "timeout"
	ResultDNS      = "dns"
	ResultNetwork  = "network"
	ResultProtocol = "protocol"
	ResultError    = "error"
)

// classify maps a scrape error to a result kind.
func classify(err error) string {
	if err == nil {
		return ResultOK
	}
	var dnsErr *net.DNSError
	var ne net.Error
	var opErr *net.OpError
	switch {
	case errors.As(err, &dnsErr):
		return ResultDNS
	case errors.Is(err, context.DeadlineExceeded), errors.As(err, &ne) && ne.Timeout(), strings.Contains(err.Error(), "timeout"):
		return ResultTimeout
	case errors.Is(err, ErrPrivateTracker), errors.As(err, &opErr):
		return ResultNetwork
	case strings.Contains(err.Error(), "tracker error"), strings.Contains(err.Error(), "handshake failed"), strings.Contains(err.Error(), "closed the connection"), strings.Contains(err.Error(), "bad "), strings.Contains(err.Error(), "HTTP "),
		strings.Contains(err.Error(), "scrape"), strings.Contains(err.Error(), "invalid bencode"), strings.Contains(err.Error(), "bad status"):
		return ResultProtocol
	}
	return ResultError
}

// Info describes on-demand scraping (admin page).
type Info struct {
	Enabled   bool    `json:"enabled"`
	Running   int     `json:"running"`       // scrape runs in flight
	Runs      int     `json:"runs"`          // runs since start
	Torrents  int     `json:"torrents"`      // torrents scraped since start
	LastAt    *string `json:"last_at"`       // last run finished at
	LastMS    int64   `json:"last_ms"`       // … took
	LastCount int     `json:"last_torrents"` // … torrents
	LastKnown int     `json:"last_answered"` // … of which some tracker answered
	Skipped   int     `json:"skipped_busy"`  // requests served from stored data because too many runs were in flight
}

// Service runs on-demand scrapes and keeps the passive tracker health.
type Service struct {
	St      *store.Store
	Meta    *meta.Store
	Pool    *qb.Pool // may be nil
	Log     *slog.Logger
	Scraper *Scraper

	base context.Context
	wg   sync.WaitGroup

	healthMu sync.Mutex
	health   map[string]*Health

	mu       sync.Mutex
	inflight map[string]chan struct{}
	info     Info
}

// New creates the service.
func New(st *store.Store, ms *meta.Store, pool *qb.Pool, log *slog.Logger) *Service {
	sc := NewScraper()
	sc.UserAgent = func() string {
		if b := strings.TrimRight(st.Str(store.KeySiteBaseURL), "/"); b != "" {
			return "onlinetorrent/2 (+" + b + ")"
		}
		return "onlinetorrent/2"
	}
	sc.Origin = func() string { return strings.TrimRight(st.Str(store.KeySiteBaseURL), "/") }
	return &Service{St: st, Meta: ms, Pool: pool, Log: log, Scraper: sc, base: context.Background(),
		health: map[string]*Health{}, inflight: map[string]chan struct{}{}}
}

func (s *Service) enabled() bool { return s.St.Bool(store.KeySwarmEnabled) }

// Run sets the parent context of on-demand scrapes (there are no periodic jobs); Wait
// blocks until running scrapes have finished after ctx is done.
func (s *Service) Run(ctx context.Context) { s.base = ctx }

// Wait waits for running scrapes.
func (s *Service) Wait() { s.wg.Wait() }

// Status returns the on-demand scraping counters.
func (s *Service) Status() Info {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := s.info
	c.Enabled = s.enabled()
	return c
}

// target is one tracker to ask for some hashes.
type target struct {
	url     string
	kind    string
	trusted bool
	list    string // bt|webtorrent|"" (torrent's own)
	hashes  [][20]byte
}

// plan groups the hashes by tracker: every configured tracker gets every hash, a
// torrent's own trackers (first ownPerTorrent udp/http ones not configured) get its hash.
func plan(ihs []string, bt, wss []string, own map[string][]string) []*target {
	var out []*target
	idx := map[string]*target{}
	add := func(u, list string, trusted bool, h [20]byte) {
		t := idx[u]
		if t == nil {
			t = &target{url: u, kind: KindOf(u), trusted: trusted, list: list}
			idx[u] = t
			out = append(out, t)
		}
		t.hashes = append(t.hashes, h)
	}
	configured := map[string]bool{}
	for _, u := range append(append([]string(nil), bt...), wss...) {
		configured[u] = true
	}
	var owned []*target
	for _, ih := range ihs {
		h, ok := parseHash(ih)
		if !ok {
			continue
		}
		for _, u := range Merge(0, bt) {
			if KindOf(u) != KindWSS {
				add(u, "bt", true, h)
			}
		}
		for _, u := range Merge(0, wss) {
			if KindOf(u) == KindWSS {
				add(u, "webtorrent", true, h)
			}
		}
		n := 0
		for _, u := range Merge(0, own[ih]) {
			if n >= ownPerTorrent {
				break
			}
			if k := KindOf(u); k == KindWSS || configured[u] {
				continue
			}
			n++
			before := idx[u]
			add(u, "", false, h)
			if before == nil {
				owned = append(owned, idx[u])
			}
		}
	}
	// keep the own trackers that cover the most hashes
	if len(owned) > maxOwnTrackers {
		sort.SliceStable(owned, func(i, j int) bool { return len(owned[i].hashes) > len(owned[j].hashes) })
		drop := map[*target]bool{}
		for _, t := range owned[maxOwnTrackers:] {
			drop[t] = true
		}
		kept := out[:0]
		for _, t := range out {
			if !drop[t] {
				kept = append(kept, t)
			}
		}
		out = kept
	}
	return out
}

// scrape asks every planned tracker and aggregates per torrent. progress supplies qB
// progress (may return nil).
func (s *Service) scrape(ctx context.Context, ihs []string, progress func(context.Context) map[string]float64) []meta.SwarmStats {
	return s.scrapeWithPartial(ctx, ihs, progress, 0, nil)
}

// scrapeWithPartial is scrape; if partialAfter > 0 and some (not all) trackers have answered
// by then, onPartial gets the rows of the torrents that already have an answer (so a
// response waiting on its budget can show them while slow / dead trackers time out).
func (s *Service) scrapeWithPartial(ctx context.Context, ihs []string, progress func(context.Context) map[string]float64,
	partialAfter time.Duration, onPartial func([]meta.SwarmStats)) []meta.SwarmStats {
	targets := plan(ihs, s.St.List(store.KeyBTTrackers), s.St.List(store.KeyTrackers), s.Meta.AnnounceMany(ctx, ihs))
	type res struct {
		i int
		a Answer
	}
	ch := make(chan res, len(targets))
	sem := make(chan struct{}, scrapeWorkers)
	for i, t := range targets {
		go func() {
			sem <- struct{}{}
			defer func() { <-sem }()
			start := time.Now()
			cs, err := s.Scraper.Scrape(ctx, t.url, t.hashes, t.trusted)
			if t.trusted {
				batches := 1
				if n := batchSize(t.kind); n > 0 {
					batches = max(1, (len(t.hashes)+n-1)/n)
				}
				s.record(t.url, t.kind, t.list, time.Since(start)/time.Duration(batches), err)
			}
			ch <- res{i, Answer{Tracker: t.url, Kind: t.kind, Counts: cs, Err: err}}
		}()
	}
	answers := make([]Answer, 0, len(targets))
	var partial <-chan time.Time
	if partialAfter > 0 && onPartial != nil {
		t := time.NewTimer(partialAfter)
		defer t.Stop()
		partial = t.C
	}
	for len(answers) < len(targets) {
		select {
		case r := <-ch:
			answers = append(answers, r.a)
		case <-partial:
			partial = nil
			if rows := s.rows(ctx, ihs, answers, progress, true); len(rows) > 0 {
				onPartial(rows)
			}
		}
	}
	return s.rows(ctx, ihs, answers, progress, false)
}

// rows aggregates answers per torrent; answeredOnly skips torrents without any answer.
func (s *Service) rows(ctx context.Context, ihs []string, answers []Answer, progress func(context.Context) map[string]float64, answeredOnly bool) []meta.SwarmStats {
	agg := Aggregate(answers)
	var prog map[string]float64
	if progress != nil && s.Pool != nil {
		prog = progress(ctx)
	}
	now := store.Now()
	rows := make([]meta.SwarmStats, 0, len(ihs))
	for _, ih := range ihs {
		h, ok := parseHash(ih)
		if !ok {
			continue
		}
		r := meta.SwarmStats{Infohash: ih, ScrapedAt: now}
		if g := agg[h]; g != nil {
			if g.BT != nil {
				r.BTSeeders, r.BTLeechers, r.BTCompleted = &g.BT.Seeders, &g.BT.Leechers, &g.BT.Completed
			}
			if g.WT != nil {
				r.WTSeeders, r.WTLeechers = &g.WT.Seeders, &g.WT.Leechers
			}
		} else if answeredOnly {
			continue
		} else {
			r.Error = firstError(answers)
		}
		if p, ok := prog[ih]; ok {
			r.ServerProgress, r.ServerComplete = p, p >= 1
		}
		rows = append(rows, r)
	}
	return rows
}

// batchSize is the number of hashes per request of a tracker kind (0 = one request).
func batchSize(kind string) int {
	switch kind {
	case KindUDP:
		return udpMaxBatch
	case KindHTTP:
		return httpMaxBatch
	}
	return 0
}

func firstError(answers []Answer) string {
	for _, a := range answers {
		if a.Err != nil {
			return "没有 tracker 响应：" + a.Err.Error()
		}
	}
	return "没有 tracker 响应"
}

func (s *Service) record(u, kind, list string, latency time.Duration, err error) {
	s.healthMu.Lock()
	defer s.healthMu.Unlock()
	h := s.health[u]
	if h == nil {
		if len(s.health) >= healthHistoryCap {
			s.health = map[string]*Health{}
		}
		h = &Health{URL: u, Kind: kind, List: list}
		s.health[u] = h
	}
	now := store.Now()
	h.CheckedAt, h.Result = &now, classify(err)
	if err != nil {
		h.Status, h.LastError, h.LastErrorAt = StatusError, sanitize(err.Error()), &now
		h.FailCount++
		return
	}
	h.Status, h.LatencyMS, h.LastOKAt = StatusOK, latency.Milliseconds(), &now
	h.OKCount++
}

// Health lists the configured trackers (WebTorrent first, then BT) with their status.
func (s *Service) Health() []Health {
	s.healthMu.Lock()
	defer s.healthMu.Unlock()
	var out []Health
	add := func(list string, urls []string) {
		for _, u := range urls {
			if h := s.health[u]; h != nil {
				c := *h
				c.List = list
				out = append(out, c)
				continue
			}
			out = append(out, Health{URL: u, Kind: KindOf(u), List: list, Status: StatusUnused})
		}
	}
	add("webtorrent", s.St.List(store.KeyTrackers))
	add("bt", s.St.List(store.KeyBTTrackers))
	if out == nil {
		out = []Health{}
	}
	return out
}

// Lookup returns the stats of ihs for API responses. Stale ones (never scraped or older
// than StaleAfter) are refreshed when swarm statistics are enabled: the call waits at most
// budget for the refresh and returns what is known; the refresh finishes in the background.
// Server progress comes from the qB pool's cached snapshot (never blocks).
func (s *Service) Lookup(ctx context.Context, ihs []string, budget time.Duration) map[string]*meta.SwarmStats {
	ihs = dedupe(ihs)
	stored, err := s.Meta.SwarmStatsMany(ctx, ihs)
	if err != nil {
		stored = map[string]*meta.SwarmStats{}
	}
	if budget > 0 && s.enabled() {
		cut := time.Now().Add(-StaleAfter).UTC().Format(time.RFC3339)
		var stale []string
		for _, ih := range ihs {
			if st := stored[ih]; st == nil || st.ScrapedAt == "" || st.ScrapedAt < cut {
				stale = append(stale, ih)
			}
		}
		if len(stale) > maxOnDemand {
			stale = stale[:maxOnDemand]
		}
		if len(stale) > 0 {
			done := s.refresh(stale)
			t := time.NewTimer(budget)
			select {
			case <-done:
			case <-t.C:
			case <-ctx.Done():
			}
			t.Stop()
			if fresh, err := s.Meta.SwarmStatsMany(ctx, stale); err == nil {
				for k, v := range fresh {
					stored[k] = v
				}
			}
		}
	}
	if s.Pool != nil {
		if prog, ok := s.Pool.ProgressCached(); ok {
			for _, ih := range ihs {
				p, has := prog[ih]
				st := stored[ih]
				if st == nil {
					if !has {
						continue
					}
					st = &meta.SwarmStats{Infohash: ih}
					stored[ih] = st
				}
				st.ServerProgress, st.ServerComplete = p, has && p >= 1
			}
		}
	}
	return stored
}

// refresh scrapes ihs in the background (singleflight per infohash: hashes already being
// scraped are waited for, not scraped again); the returned channel closes when all of them
// are done.
func (s *Service) refresh(ihs []string) <-chan struct{} {
	s.mu.Lock()
	var waits []chan struct{}
	var mine []string
	for _, ih := range ihs {
		if ch, ok := s.inflight[ih]; ok {
			waits = append(waits, ch)
		} else {
			mine = append(mine, ih)
		}
	}
	if len(mine) > 0 && s.info.Running >= maxOnDemandRuns {
		mine = nil // busy: serve stored data
		s.info.Skipped++
	}
	var ch chan struct{}
	if len(mine) > 0 {
		ch = make(chan struct{})
		for _, ih := range mine {
			s.inflight[ih] = ch
		}
		s.info.Running++
		s.info.Runs++
		s.info.Torrents += len(mine)
		waits = append(waits, ch)
	}
	s.mu.Unlock()
	if ch != nil {
		s.wg.Go(func() {
			start := time.Now()
			known := 0
			defer func() {
				at := store.Now()
				s.mu.Lock()
				for _, ih := range mine {
					delete(s.inflight, ih)
				}
				s.info.Running--
				s.info.LastAt, s.info.LastMS, s.info.LastCount, s.info.LastKnown = &at, time.Since(start).Milliseconds(), len(mine), known
				s.mu.Unlock()
				close(ch)
			}()
			ctx, cancel := context.WithTimeout(s.base, onDemandTimeout)
			defer cancel()
			// partial results are stored shortly before the waiting responses' budget ends
			rows := s.scrapeWithPartial(ctx, mine, s.cachedProgress, partialFlush, func(rows []meta.SwarmStats) {
				if err := s.Meta.PutSwarmScrape(context.WithoutCancel(ctx), rows); err != nil {
					s.Log.Warn("swarm: store partial", "err", err)
				}
			})
			for _, r := range rows {
				if r.Error == "" {
					known++
				}
			}
			if err := s.Meta.PutSwarmScrape(context.WithoutCancel(ctx), rows); err != nil {
				s.Log.Warn("swarm: store", "err", err)
			}
		})
	}
	done := make(chan struct{})
	go func() {
		for _, w := range waits {
			<-w
		}
		close(done)
	}()
	return done
}

func (s *Service) cachedProgress(context.Context) map[string]float64 {
	if s.Pool == nil {
		return nil
	}
	m, _ := s.Pool.ProgressCached()
	return m
}

func dedupe(in []string) []string {
	seen := make(map[string]bool, len(in))
	out := in[:0:0]
	for _, x := range in {
		if x != "" && !seen[x] {
			seen[x] = true
			out = append(out, x)
		}
	}
	return out
}

// RandomHash is a helper for tests and probes.
func RandomHash() string {
	var h [20]byte
	_, _ = rand.Read(h[:])
	return hex.EncodeToString(h[:])
}
