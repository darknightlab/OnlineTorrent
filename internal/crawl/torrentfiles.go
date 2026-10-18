package crawl

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/darknightlab/onlinetorrent/internal/meta"
	"github.com/darknightlab/onlinetorrent/internal/qb"
	"github.com/darknightlab/onlinetorrent/internal/store"
)

// Raw .torrent files for HTTP metadata delivery (magnet xs=, GET /api/v1/torrents/hash/{ih}/torrent).
// Sources, in the order they are tried:
//  1. the meta store (feed .torrent downloads, earlier qB exports / bangumi.moe downloads);
//  2. qBittorrent torrents/export (any online server that has the metadata) — stored;
//  3. bangumi.moe items: their .torrent (/download/torrent/{_id}/x.torrent), fetched lazily
//     on demand (the crawler reads bangumi.moe's JSON and never downloads .torrent files),
//     serialised + paced like every bangumi.moe request (Fetcher per-host gap), negative
//     results cached for 10 minutes — stored.

// ErrNoMetainfo: no .torrent is available for the infohash (yet).
var ErrNoMetainfo = errors.New("no torrent file available")

const moeNegativeTTL = 10 * time.Minute

type metainfoCache struct {
	mu       sync.Mutex
	moeFail  map[string]time.Time // infohash -> retry after
	inflight map[string]chan struct{}
}

// saveExport stores qB's export of ih (best effort, logged).
func (c *Crawler) saveExport(ctx context.Context, cl *qb.Client, ih string) {
	if c.Meta.HasMetainfo(ctx, ih) {
		return
	}
	b, err := cl.Export(ctx, ih)
	if err == nil {
		err = c.Meta.PutMetainfo(ctx, ih, b, meta.MetainfoQB)
	}
	if err != nil {
		c.Log.Info("store qB export", "ih", ih, "err", err)
	}
}

// SaveExport stores the .torrent of ih from any online qB server (e.g. after a member's
// seed request obtained the metadata). Best effort.
func (c *Crawler) SaveExport(ctx context.Context, ih string) {
	if c.Pool == nil || c.Meta.HasMetainfo(ctx, ih) {
		return
	}
	b, err := c.Pool.Export(ctx, ih)
	if err == nil {
		err = c.Meta.PutMetainfo(ctx, ih, b, meta.MetainfoQB)
	}
	if err != nil && !errors.Is(err, qb.ErrNoTorrent) {
		c.Log.Info("store qB export", "ih", ih, "err", err)
	}
}

// MetainfoAvailable is a cheap check (local only) whether a .torrent of ih is stored or can
// probably be fetched from bangumi.moe. qB is not contacted.
func (c *Crawler) MetainfoAvailable(ctx context.Context, ih string) bool {
	if c.Meta.HasMetainfo(ctx, ih) {
		return true
	}
	_, ok := c.moeSource(ctx, ih)
	return ok
}

// moeSource returns the bangumi.moe id of a catalogue torrent crawled from bangumi.moe
// (unless a recent download failed).
func (c *Crawler) moeSource(ctx context.Context, ih string) (string, bool) {
	in, ok := c.Meta.GetTorrentInput(ctx, ih)
	if !ok || in.SourceKind != store.SourceKindBangumiMoe || !reMoeID(in.SourceID) {
		return "", false
	}
	c.mi.mu.Lock()
	defer c.mi.mu.Unlock()
	if t, bad := c.mi.moeFail[ih]; bad && time.Now().Before(t) {
		return "", false
	}
	return in.SourceID, true
}

func reMoeID(s string) bool {
	if s == "" || len(s) > 64 {
		return false
	}
	for _, r := range s {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z') {
			return false
		}
	}
	return true
}

// Metainfo returns the .torrent of ih: stored, else exported from qB, else downloaded from
// bangumi.moe (both stored on success). ErrNoMetainfo if none is available.
func (c *Crawler) Metainfo(ctx context.Context, ih string) ([]byte, error) {
	if b, err := c.Meta.GetMetainfo(ctx, ih); err == nil {
		return b, nil
	} else if !errors.Is(err, store.ErrNotFound) {
		return nil, err
	}
	// one live lookup per infohash at a time; concurrent callers wait and re-read the store
	c.mi.mu.Lock()
	if c.mi.inflight == nil {
		c.mi.inflight = map[string]chan struct{}{}
	}
	if ch, busy := c.mi.inflight[ih]; busy {
		c.mi.mu.Unlock()
		select {
		case <-ch:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		if b, err := c.Meta.GetMetainfo(ctx, ih); err == nil {
			return b, nil
		}
		return nil, ErrNoMetainfo
	}
	ch := make(chan struct{})
	c.mi.inflight[ih] = ch
	c.mi.mu.Unlock()
	defer func() {
		c.mi.mu.Lock()
		delete(c.mi.inflight, ih)
		c.mi.mu.Unlock()
		close(ch)
	}()

	if c.Pool != nil {
		qctx, cancel := context.WithTimeout(ctx, 8*time.Second)
		b, err := c.Pool.Export(qctx, ih)
		cancel()
		if err == nil {
			if err := c.Meta.PutMetainfo(ctx, ih, b, meta.MetainfoQB); err != nil {
				c.Log.Warn("qB export rejected", "ih", ih, "err", err)
			} else {
				return b, nil
			}
		}
	}
	if id, ok := c.moeSource(ctx, ih); ok {
		b, err := c.fetchMoeTorrent(ctx, ih, id)
		if err == nil {
			return b, nil
		}
		c.Log.Info("bangumi.moe .torrent", "ih", ih, "id", id, "err", err)
		if ctx.Err() == nil {
			c.mi.mu.Lock()
			if c.mi.moeFail == nil {
				c.mi.moeFail = map[string]time.Time{}
			}
			now := time.Now()
			for k, t := range c.mi.moeFail {
				if now.After(t) {
					delete(c.mi.moeFail, k)
				}
			}
			c.mi.moeFail[ih] = now.Add(moeNegativeTTL)
			c.mi.mu.Unlock()
		}
	}
	return nil, ErrNoMetainfo
}

// fetchMoeTorrent downloads and stores a bangumi.moe item's .torrent.
func (c *Crawler) fetchMoeTorrent(ctx context.Context, ih, id string) ([]byte, error) {
	base := strings.TrimRight(c.St.Str(store.KeyMoeBase), "/")
	if base == "" {
		base = "https://bangumi.moe"
	}
	proxyDisabled := false
	if srcs, err := c.St.ListSources(ctx); err == nil {
		for _, s := range srcs {
			if s.Kind == store.SourceKindBangumiMoe {
				proxyDisabled = s.ProxyDisabled
				break
			}
		}
	}
	fctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	resp, err := c.Fetch.Get(fctx, base+"/download/torrent/"+url.PathEscape(id)+"/"+ih+".torrent",
		GetOptions{MaxBytes: MaxTorrentBytes, ProxyDisabled: proxyDisabled, Accept: "application/x-bittorrent, */*;q=0.5"})
	if err != nil {
		return nil, err
	}
	if err := c.Meta.PutMetainfo(ctx, ih, resp.Body, meta.MetainfoMoe); err != nil {
		return nil, fmt.Errorf("bangumi.moe .torrent: %w", err)
	}
	return resp.Body, nil
}
