package catalog

import (
	"context"
	"errors"
	"time"

	"github.com/darknightlab/onlinetorrent/internal/meta"
	"github.com/darknightlab/onlinetorrent/internal/store"
)

// Official releases are weekly. A failed import is retried at most every six hours.
const (
	manamiEvery = 7 * 24 * time.Hour
	manamiCheck = 6 * time.Hour
)

func manamiNext(st meta.ManamiStatus, url string) time.Time {
	last, _ := time.Parse(time.RFC3339, st.SyncedAt)
	next := last.Add(manamiEvery)
	if st.URL != url {
		next = time.Time{}
	}
	if failed, _ := time.Parse(time.RFC3339, st.ErrorAt); failed.After(last) && failed.Add(manamiCheck).After(next) {
		next = failed.Add(manamiCheck)
	}
	return next
}

func (c *Catalog) ManamiState(ctx context.Context) meta.ManamiStatus {
	st := c.meta.ManamiState(ctx)
	url := c.st.Str(store.KeyManamiURL)
	if url != "" {
		st.NextAt = manamiNext(st, url).UTC().Format(time.RFC3339)
	}
	st.URL = url
	return st
}

func (c *Catalog) SyncManami(ctx context.Context) (meta.ManamiStatus, error) {
	cctx, cancel := context.WithTimeout(ctx, 20*time.Minute)
	defer cancel()
	hc := c.HTTPClient()
	hc.Timeout = 0
	st, err := c.meta.ImportManami(cctx, hc, c.st.Str(store.KeyManamiURL))
	switch {
	case errors.Is(err, meta.ErrManamiRunning):
	case err != nil:
		c.log.Warn("anime-offline-database import failed", "err", err)
	default:
		c.log.Info("anime-offline-database imported", "release", st.Release, "entries", st.Entries, "works", st.Linked, "names", st.Names, "sec", st.DurationSec)
	}
	return st, err
}

func (c *Catalog) manamiLoop(ctx context.Context) {
	select {
	case <-ctx.Done():
		return
	case <-time.After(5 * time.Minute):
	}
	ticker := time.NewTicker(manamiCheck)
	defer ticker.Stop()
	for {
		url := c.st.Str(store.KeyManamiURL)
		if url != "" && !time.Now().Before(manamiNext(c.meta.ManamiState(ctx), url)) {
			_, _ = c.SyncManami(ctx)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
