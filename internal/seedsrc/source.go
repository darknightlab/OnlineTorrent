package seedsrc

import (
	"context"
	"sync"
	"time"
)

// Avail is what a source has of a torrent.
type Avail struct {
	// Known: the source holds the torrent and its metadata.
	Known bool
	// Pieces: pieces the source can serve now (nil when !Known).
	Pieces *Bits
}

// Source is a server holding torrent data (today: a qB server's connected edge node; the
// browser fetches the data from the edge itself, the main server only reports availability). Implementations must be safe for concurrent use.
type Source interface {
	// ID is stable for the process lifetime (e.g. "qb:3"); Name is for logs / UI.
	ID() string
	Name() string
	// Have reports the verified pieces of torrent l (numPieces = l.NumPieces). It may be
	// called often; the Set caches it for a second.
	Have(ctx context.Context, l *Layout) (Avail, error)
}

// Set is the list of sources plus a short-lived availability cache shared by every
// request and SSE stream (so N viewers of a torrent cost one pieceStates call per source
// per second).
type Set struct {
	Sources func() []Source
	TTL     time.Duration // default 1 s

	mu    sync.Mutex
	cache map[string]*availEntry // sourceID|ih
}

type availEntry struct {
	at    time.Time
	done  chan struct{}
	avail Avail
	err   error
}

// Have returns the (cached) availability of torrent l on src.
func (s *Set) Have(ctx context.Context, src Source, l *Layout) (Avail, error) {
	ttl := s.TTL
	if ttl == 0 {
		ttl = time.Second
	}
	key := src.ID() + "|" + l.Infohash
	s.mu.Lock()
	if s.cache == nil {
		s.cache = map[string]*availEntry{}
	}
	e, ok := s.cache[key]
	if ok && (e.done != nil || time.Since(e.at) < ttl) {
		done := e.done
		s.mu.Unlock()
		if done != nil {
			select {
			case <-done:
			case <-ctx.Done():
				return Avail{}, ctx.Err()
			}
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		return e.avail, e.err
	}
	e = &availEntry{done: make(chan struct{})}
	s.cache[key] = e
	if len(s.cache) > 4096 {
		for k, v := range s.cache {
			if v.done == nil && time.Since(v.at) > time.Minute {
				delete(s.cache, k)
			}
		}
	}
	s.mu.Unlock()
	cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 8*time.Second)
	avail, err := src.Have(cctx, l)
	cancel()
	s.mu.Lock()
	e.avail, e.err, e.at = avail, err, time.Now()
	close(e.done)
	e.done = nil
	s.mu.Unlock()
	return avail, err
}

// SourceAvail is one source's availability.
type SourceAvail struct {
	Source Source
	Avail  Avail
}

// All queries every source concurrently (cached). Sources that fail are left out.
func (s *Set) All(ctx context.Context, l *Layout) []SourceAvail {
	srcs := s.Sources()
	out := make([]SourceAvail, len(srcs))
	ok := make([]bool, len(srcs))
	var wg sync.WaitGroup
	for i, src := range srcs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			a, err := s.Have(ctx, src, l)
			if err == nil {
				out[i], ok[i] = SourceAvail{src, a}, true
			}
		}()
	}
	wg.Wait()
	var res []SourceAvail
	for i := range out {
		if ok[i] {
			res = append(res, out[i])
		}
	}
	return res
}

// Union is the set of pieces available on at least one source; known = some source holds
// the torrent with metadata.
func (s *Set) Union(ctx context.Context, l *Layout) (union *Bits, known bool) {
	union = NewBits(l.NumPieces)
	for _, sa := range s.All(ctx, l) {
		if sa.Avail.Known {
			known = true
			union.Or(sa.Avail.Pieces)
		}
	}
	return union, known
}
