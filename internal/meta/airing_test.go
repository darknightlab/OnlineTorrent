package meta

import (
	"testing"
	"time"
)

func TestComputeAiring(t *testing.T) {
	start := time.Date(2025, 1, 5, 14, 0, 0, 0, time.UTC)
	now := start.AddDate(0, 0, 7*40) // 40 weeks later
	b := "R/" + start.Format(time.RFC3339) + "/P7D"

	// tv without end: capped at 26 episodes, no next
	a, ok := ComputeAiring(&Work{Type: "tv", Broadcast: b, Begin: start.Format(time.RFC3339)}, now)
	if !ok || a.Aired != maxEpisodesNoEnd || !a.Next.IsZero() {
		t.Errorf("no end cap: %+v", a)
	}
	// tv still airing within the cap
	a, _ = ComputeAiring(&Work{Type: "tv", Broadcast: b}, start.Add(2*7*24*time.Hour+time.Hour))
	if a.Aired != 3 || a.Next.IsZero() {
		t.Errorf("airing: %+v", a)
	}
	// unknown interval: single broadcast
	a, _ = ComputeAiring(&Work{Type: "tv", Broadcast: "R/" + start.Format(time.RFC3339) + "/P1M"}, now)
	if a.Aired != 1 || !a.Next.IsZero() || !a.Last.Equal(start) {
		t.Errorf("unknown interval: %+v", a)
	}
	// movie: single episode even with a weekly broadcast string
	a, _ = ComputeAiring(&Work{Type: "movie", Broadcast: b}, now)
	if a.Aired != 1 || !a.Next.IsZero() {
		t.Errorf("movie: %+v", a)
	}
	// with end: clamped
	a, _ = ComputeAiring(&Work{Type: "tv", Broadcast: b, End: start.AddDate(0, 0, 7*11).Format(time.RFC3339)}, now)
	if a.Aired != 12 || !a.Next.IsZero() {
		t.Errorf("ended: %+v", a)
	}
	// not started yet
	a, _ = ComputeAiring(&Work{Type: "tv", Broadcast: b}, start.Add(-time.Hour))
	if a.Aired != 0 || !a.Next.Equal(start) {
		t.Errorf("upcoming: %+v", a)
	}
}
