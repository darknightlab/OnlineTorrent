package swarm

import (
	"context"
	"encoding/hex"
	"errors"
	"net"
	"net/http"
	"net/url"
	"sync"
	"time"
)

// Scraper scrapes trackers of any supported kind, batching hashes, reusing UDP connection
// ids and pacing requests per tracker.
type Scraper struct {
	hc *http.Client
	// AllowPrivate disables the public-address check for untrusted trackers (tests).
	AllowPrivate bool
	UserAgent    func() string
	Origin       func() string
	// Pace: per-tracker token bucket (burst requests, then one per interval).
	Burst    int
	Interval time.Duration

	mu   sync.Mutex
	udp  map[string]*udpTracker
	lims map[string]*pacer
}

// NewScraper returns a scraper with default pacing (burst 4, then 1 request/s per tracker).
func NewScraper() *Scraper {
	tr := &http.Transport{
		Proxy:                 nil, // trackers are contacted directly
		DialContext:           (&net.Dialer{Timeout: 5 * time.Second}).DialContext,
		TLSHandshakeTimeout:   5 * time.Second,
		ResponseHeaderTimeout: 8 * time.Second,
		MaxIdleConnsPerHost:   2,
		IdleConnTimeout:       90 * time.Second,
	}
	return &Scraper{
		hc:  &http.Client{Transport: tr, Timeout: 12 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
		udp: map[string]*udpTracker{}, lims: map[string]*pacer{}, Burst: 4, Interval: time.Second,
	}
}

func (sc *Scraper) userAgent() string {
	if sc.UserAgent != nil {
		return sc.UserAgent()
	}
	return "onlinetorrent/2"
}

func (sc *Scraper) origin() string {
	if sc.Origin != nil {
		if o := sc.Origin(); o != "" {
			return o
		}
	}
	return "https://www.projectk.org"
}

type pacer struct {
	mu     sync.Mutex
	tokens float64
	last   time.Time
}

func (sc *Scraper) wait(ctx context.Context, tracker string) error {
	sc.mu.Lock()
	p := sc.lims[tracker]
	if p == nil {
		if len(sc.lims) > 2000 {
			sc.lims = map[string]*pacer{}
		}
		p = &pacer{tokens: float64(sc.Burst), last: time.Now()}
		sc.lims[tracker] = p
	}
	sc.mu.Unlock()
	for {
		p.mu.Lock()
		now := time.Now()
		p.tokens = min(float64(sc.Burst), p.tokens+now.Sub(p.last).Seconds()/sc.Interval.Seconds())
		p.last = now
		if p.tokens >= 1 {
			p.tokens--
			p.mu.Unlock()
			return nil
		}
		need := time.Duration((1 - p.tokens) * float64(sc.Interval))
		p.mu.Unlock()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(need):
		}
	}
}

func (sc *Scraper) udpFor(hostPort string) *udpTracker {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	t := sc.udp[hostPort]
	if t == nil {
		if len(sc.udp) > 2000 {
			sc.udp = map[string]*udpTracker{}
		}
		t = &udpTracker{hostPort: hostPort}
		sc.udp[hostPort] = t
	}
	return t
}

// ErrPrivateTracker: an untrusted tracker resolves to a non-public address.
var ErrPrivateTracker = errors.New("tracker address is not public")

// Scrape asks one tracker for hashes. trusted = configured by the admin (no address
// check). The result maps every answered hash; on a partial failure (a later batch
// failed) the answered part is returned with the error.
func (sc *Scraper) Scrape(ctx context.Context, tracker string, hashes [][20]byte, trusted bool) (map[[20]byte]Counts, error) {
	kind := KindOf(tracker)
	if kind == "" {
		return nil, errors.New("unsupported tracker")
	}
	u, err := url.Parse(tracker)
	if err != nil || u.Hostname() == "" {
		return nil, errors.New("invalid tracker URL")
	}
	if !trusted && !sc.AllowPrivate && !publicHost(ctx, u.Hostname()) {
		return nil, ErrPrivateTracker
	}
	batch := batchSize(kind)
	if batch == 0 {
		batch = max(1, len(hashes)) // wss: one connection, batched messages
	}
	out := map[[20]byte]Counts{}
	for start := 0; start < len(hashes); start += batch {
		part := hashes[start:min(len(hashes), start+batch)]
		if err := sc.wait(ctx, tracker); err != nil {
			return out, err
		}
		var res map[[20]byte]Counts
		switch kind {
		case KindUDP:
			hp, err := udpHostPort(tracker)
			if err != nil {
				return out, err
			}
			cs, err := sc.udpFor(hp).scrape(ctx, part)
			if err != nil {
				return out, err
			}
			res = map[[20]byte]Counts{}
			for i, c := range cs {
				res[part[i]] = c
			}
		case KindHTTP:
			res, err = sc.httpScrape(ctx, tracker, part)
		case KindWSS:
			res, err = sc.wssScrape(ctx, tracker, part)
		}
		if err != nil {
			return out, err
		}
		// an answering tracker that doesn't list a hash has no peers for it
		for _, h := range part {
			out[h] = res[h]
		}
	}
	return out, nil
}

// parseHash converts a 40-hex infohash.
func parseHash(ih string) ([20]byte, bool) {
	var h [20]byte
	b, err := hex.DecodeString(ih)
	if err != nil || len(b) != 20 {
		return h, false
	}
	copy(h[:], b)
	return h, true
}
