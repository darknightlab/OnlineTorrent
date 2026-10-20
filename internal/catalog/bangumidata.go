package catalog

import (
	"context"
	"sync"
	"time"

	"github.com/darknightlab/onlinetorrent/internal/store"
)

// bangumiDataEvery is the fixed bangumi-data sync interval.
const bangumiDataEvery = 12 * time.Hour

// BangumiDataStatus is the admin view of the bangumi-data sync (kept in memory: a sync
// runs at every start anyway).
type BangumiDataStatus struct {
	URL         string  `json:"url"`
	Running     bool    `json:"running"`
	SyncedAt    *string `json:"synced_at"`
	Works       int     `json:"works"` // works imported by the last successful sync
	DurationSec float64 `json:"duration_sec"`
	Error       string  `json:"error,omitempty"`
	ErrorAt     *string `json:"error_at,omitempty"`
	NextAt      *string `json:"next_at"` // next automatic sync
}

type bdState struct {
	mu sync.Mutex
	st BangumiDataStatus
}

// BangumiDataState returns the current bangumi-data sync status.
func (c *Catalog) BangumiDataState() BangumiDataStatus {
	c.bd.mu.Lock()
	defer c.bd.mu.Unlock()
	st := c.bd.st
	st.URL = c.st.Str(store.KeyBangumiDataURL)
	return st
}

// SyncBangumiData imports bangumi-data now; false if a sync is already running.
func (c *Catalog) SyncBangumiData(ctx context.Context) bool {
	c.bd.mu.Lock()
	if c.bd.st.Running {
		c.bd.mu.Unlock()
		return false
	}
	c.bd.st.Running = true
	c.bd.mu.Unlock()
	c.refreshBangumiData(ctx)
	return true
}

func (c *Catalog) setBangumiDataNext(t time.Time) {
	c.bd.mu.Lock()
	c.bd.st.NextAt = ts(t)
	c.bd.mu.Unlock()
}
