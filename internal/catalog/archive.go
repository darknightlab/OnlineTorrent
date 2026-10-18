package catalog

import (
	"context"
	"errors"
	"time"

	"github.com/darknightlab/onlinetorrent/internal/meta"
	"github.com/darknightlab/onlinetorrent/internal/store"
)

// bangumi Archive sync: check latest.json every 6 hours (the dump is published weekly) and
// import when its digest changed. The first check runs a couple of minutes after start.

const archiveCheckEvery = 6 * time.Hour

// SyncArchive imports the latest bangumi Archive dump (force: even if unchanged).
func (c *Catalog) SyncArchive(ctx context.Context, force bool) (meta.ArchiveStatus, bool, error) {
	cctx, cancel := context.WithTimeout(ctx, 90*time.Minute)
	defer cancel()
	hc := c.HTTPClient()
	hc.Timeout = 0 // entries are long streams; bounded by cctx
	ua := c.st.Str(store.KeyBgmUserAgent)
	st, skipped, err := c.meta.SyncArchive(cctx, hc, ua, force)
	switch {
	case errors.Is(err, meta.ErrArchiveRunning):
	case err != nil:
		c.log.Warn("bangumi archive sync failed", "err", err)
	case skipped:
		c.log.Debug("bangumi archive unchanged", "dump", st.Dump)
	default:
		c.log.Info("bangumi archive imported", "dump", st.Dump, "subjects", st.Subjects, "episodes", st.Episodes, "relations", st.Relations, "sec", st.DurationSec)
		c.rebuildSchedule(ctx)
	}
	return st, skipped, err
}

func (c *Catalog) archiveLoop(ctx context.Context) {
	select {
	case <-ctx.Done():
		return
	case <-time.After(2 * time.Minute):
	}
	t := time.NewTicker(archiveCheckEvery)
	defer t.Stop()
	for {
		if c.st.Bool(store.KeyBgmArchiveEnabled) {
			_, _, _ = c.SyncArchive(ctx, false)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
