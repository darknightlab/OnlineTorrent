package api

import (
	"context"
	"errors"
	"net/http"
	"sort"
	"time"

	"github.com/darknightlab/onlinetorrent/internal/crawl"
	"github.com/darknightlab/onlinetorrent/internal/seedsrc"
	"github.com/darknightlab/onlinetorrent/internal/store"
	"github.com/darknightlab/onlinetorrent/internal/torrentfile"
)

// WebSeed availability. Only qB servers with a connected edge node (internal/edge) offer a
// webseed: the edge serves browsers directly from qB's download directory — partial files
// included — at its public URL; the main server never relays data. The SSE stream tells the
// browser, per edge URL, which pieces are servable so webtorrent only requests those.

// seeds returns the seed-source set (created on first use from the qB pool: one source per
// connected edge node).
func (s *Server) seeds() *seedsrc.Set {
	s.seedsOnce.Do(func() {
		if s.SeedSources != nil {
			s.seedSet = &seedsrc.Set{Sources: s.SeedSources}
			return
		}
		s.seedSet = &seedsrc.Set{Sources: seedsrc.PoolSources(s.Pool, s.edgeClient)}
	})
	return s.seedSet
}

type layoutEntry struct {
	l   *seedsrc.Layout
	exp time.Time
}

// webseedLayout returns the piece layout of ih from its .torrent (stored copy, else a qB
// export). Cached: 30 min positive, 15 s negative. nil = metadata not available (yet).
func (s *Server) webseedLayout(ctx context.Context, ih string) *seedsrc.Layout {
	if v, ok := s.layouts.Load(ih); ok && time.Now().Before(v.(layoutEntry).exp) {
		return v.(layoutEntry).l
	}
	var l *seedsrc.Layout
	if s.Crawl != nil {
		cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		b, err := s.Crawl.Metainfo(cctx, ih)
		cancel()
		if err == nil {
			if mi, err := torrentfile.ParseTorrent(b); err == nil && mi.Infohash == ih {
				l, _ = seedsrc.NewLayout(mi)
			}
		} else if !errors.Is(err, crawl.ErrNoMetainfo) && ctx.Err() == nil {
			s.Log.Debug("webseed layout", "hash", ih, "err", err)
		}
	}
	ttl := 15 * time.Second
	if l != nil {
		ttl = 30 * time.Minute
	}
	s.layouts.Store(ih, layoutEntry{l, time.Now().Add(ttl)})
	return l
}

func (s *Server) poolFileProgress(ctx context.Context, ih string, idx int) (bool, float64, bool) {
	if s.Pool == nil {
		return false, 0, false
	}
	return s.Pool.FileProgress(ctx, ih, idx)
}

// webseedStream runs the availability stream once the layout is known. Availability is per
// webseed URL = the public URL of every connected edge node holding the torrent; each comes
// with a short-lived access token (Authorization: Bearer) refreshed before it expires.
// Servers without an edge offer nothing (the client keeps waiting / retrying).
//
//	ready    {pieces, seeds: [{url, have, complete, token, exp}]}   first availability
//	seed     {url, have, complete, token, exp}   another edge URL became available
//	have     {url, ranges}   newly servable pieces (≤ 1/s)
//	lost     {url, ranges}   pieces no longer servable (e.g. the edge node disconnected)
//	complete {url}           that URL can serve every piece
//	token    {url, token, exp}   refreshed edge token (exp: unix seconds)
//	done     {}              every URL can serve every piece; the stream ends
//
// ranges: inclusive [start, end] pairs flattened, e.g. [0, 41, 50, 50]. Nothing is offered
// unless the user may use the webseed for ih (route permission + policy + content scope).
func (s *Server) webseedStream(ctx context.Context, r *http.Request, send func(string, any) bool, ping func() bool, ih string, idx int) {
	tick := time.NewTicker(time.Second)
	beat := time.NewTicker(25 * time.Second)
	defer tick.Stop()
	defer beat.Stop()
	u := userOf(r)
	var l *seedsrc.Layout
	last := map[string]*seedsrc.Bits{}
	tokExp := map[string]time.Time{} // edge URL → expiry of the token last sent
	order := []string{}
	started := false
	lastProgress := -1.0
	notFound := 0
	nextLayout := time.Time{}
	// edge token for url (edge id) — "" when it can't be issued
	token := func(url string, edgeID int64) map[string]any {
		tok, exp, err := s.issueEdgeToken(ctx, u, ih, edgeID)
		if err != nil {
			s.Log.Warn("edge token", "err", err)
			return nil
		}
		tokExp[url] = exp
		return map[string]any{"token": tok, "exp": exp.Unix()}
	}
	for {
		if l == nil && time.Now().After(nextLayout) {
			if l = s.webseedLayout(ctx, ih); l == nil {
				nextLayout = time.Now().Add(5 * time.Second)
				// nobody holds the torrent (e.g. no seed request yet): say so after ~1 min
				_, progress, found := s.poolFileProgress(ctx, ih, idx)
				if !found {
					if notFound++; notFound >= 12 {
						send("unavailable", map[string]string{"reason": "not_seeded"})
						return
					}
				} else if notFound = 0; progress != lastProgress {
					lastProgress = progress
					if !send("waiting", map[string]float64{"progress": progress}) {
						return
					}
				}
			} else if idx >= len(l.Files) {
				send("unavailable", map[string]string{"reason": "no_such_file"})
				return
			}
		}
		if l != nil {
			if !s.St.Bool(store.KeyWebseedEnabled) {
				send("unavailable", map[string]string{"reason": "disabled"})
				return
			}
			allowed, err := s.webseedAllowed(r, ih)
			if err == nil && (!allowed || !s.allowed(roleOf(u), webseedRouteKey)) {
				send("unavailable", map[string]string{"reason": "forbidden"})
				return
			}
			cur := s.webseedAvailability(ctx, l)
			if len(cur) == 0 && !started {
				_, _, found := s.poolFileProgress(ctx, ih, idx)
				if !found {
					if notFound++; notFound >= 60 {
						send("unavailable", map[string]string{"reason": "not_seeded"})
						return
					}
				} else {
					notFound = 0
				}
				if lastProgress != 0 {
					lastProgress = 0
					if !send("waiting", map[string]float64{"progress": 0}) {
						return
					}
				}
			}
			var fresh []map[string]any
			for _, url := range sortedKeys(cur) {
				if _, known := last[url]; known {
					continue
				}
				a := cur[url]
				t := token(url, a.edge) // every webseed URL is an edge: never offered without a token
				if t == nil {
					continue
				}
				seed := map[string]any{"url": url, "have": a.bits.Ranges(), "complete": a.bits.Full(), "token": t["token"], "exp": t["exp"]}
				fresh = append(fresh, seed)
				order = append(order, url)
				last[url] = nil
			}
			switch {
			case !started && len(fresh) > 0:
				if !send("ready", map[string]any{"pieces": l.NumPieces, "seeds": fresh}) {
					return
				}
				started = true
			case len(fresh) > 0:
				for _, f := range fresh {
					if !send("seed", f) {
						return
					}
				}
			}
			// refresh edge tokens before they expire (at 4/5 of their lifetime)
			margin := s.edgeTokenTTL() / 5
			for _, url := range order {
				if exp, ok := tokExp[url]; ok && time.Until(exp) < margin {
					a := cur[url]
					if a == nil {
						continue // edge gone: refreshed when it comes back
					}
					if t := token(url, a.edge); t != nil && !send("token", map[string]any{"url": url, "token": t["token"], "exp": t["exp"]}) {
						return
					}
				}
			}
			allDone := started
			for _, url := range order {
				prev := last[url]
				var now *seedsrc.Bits
				if a := cur[url]; a != nil {
					now = a.bits
				} else {
					now = seedsrc.NewBits(l.NumPieces) // edge disconnected / server gone
				}
				if prev != nil {
					if add := now.RangesNotIn(prev); len(add) > 0 && !send("have", map[string]any{"url": url, "ranges": add}) {
						return
					}
					if lost := prev.RangesNotIn(now); len(lost) > 0 && !send("lost", map[string]any{"url": url, "ranges": lost}) {
						return
					}
					if now.Full() && !prev.Full() && !send("complete", map[string]any{"url": url}) {
						return
					}
				}
				last[url] = now
				allDone = allDone && now.Full()
			}
			if allDone {
				send("done", map[string]any{})
				return
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-beat.C:
			if !ping() {
				return
			}
		case <-tick.C:
		}
	}
}

type urlAvail struct {
	bits *seedsrc.Bits
	edge int64 // edge id (qB server id)
}

// webseedAvailability groups the edge sources by webseed URL (each edge's public URL).
func (s *Server) webseedAvailability(ctx context.Context, l *seedsrc.Layout) map[string]*urlAvail {
	out := map[string]*urlAvail{}
	for _, sa := range s.seeds().All(ctx, l) {
		e, ok := sa.Source.(*seedsrc.Edge)
		if !ok || !sa.Avail.Known {
			continue
		}
		u, edgeID := e.BaseURL+"/webseed/hash/"+l.Infohash+"/", e.ServerID
		if out[u] == nil {
			out[u] = &urlAvail{bits: seedsrc.NewBits(l.NumPieces), edge: edgeID}
		}
		out[u].bits.Or(sa.Avail.Pieces)
	}
	return out
}

func sortedKeys(m map[string]*urlAvail) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
