package swarm

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

// TestLiveTrackers scrapes real trackers; only with SWARM_LIVE=1 (unit tests stay offline).
//
//	SWARM_LIVE=1 SWARM_TRACKERS="udp://…,wss://…" SWARM_HASHES="40hex,…" go test -run TestLiveTrackers -v ./internal/swarm
func TestLiveTrackers(t *testing.T) {
	if os.Getenv("SWARM_LIVE") != "1" {
		t.Skip("set SWARM_LIVE=1")
	}
	sc := NewScraper()
	var hashes [][20]byte
	for _, ih := range strings.Split(os.Getenv("SWARM_HASHES"), ",") {
		if h, ok := parseHash(strings.TrimSpace(ih)); ok {
			hashes = append(hashes, h)
		}
	}
	if len(hashes) == 0 {
		h, _ := parseHash(RandomHash())
		hashes = append(hashes, h)
	}
	for _, tr := range strings.Split(os.Getenv("SWARM_TRACKERS"), ",") {
		tr = strings.TrimSpace(tr)
		if tr == "" {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		start := time.Now()
		res, err := sc.Scrape(ctx, tr, hashes, true)
		cancel()
		t.Logf("%-50s %6dms err=%v", tr, time.Since(start).Milliseconds(), err)
		for _, h := range hashes {
			if c, ok := res[h]; ok {
				t.Logf("    %x S=%d L=%d C=%d", h[:4], c.Seeders, c.Leechers, c.Completed)
			}
		}
	}
}
