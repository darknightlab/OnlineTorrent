package catalog

import (
	"github.com/darknightlab/onlinetorrent/internal/meta"
	"testing"
	"time"
)

func TestManamiSchedule(t *testing.T) {
	last := "2026-10-01T00:00:00Z"
	u := "https://example.test/data.jsonl"
	st := meta.ManamiStatus{URL: u, SyncedAt: last}
	if got := manamiNext(st, u).Format(time.RFC3339); got != "2026-10-08T00:00:00Z" {
		t.Fatal(got)
	}
	if !manamiNext(st, "https://other.test").IsZero() {
		t.Fatal("URL change must import")
	}
	st.ErrorAt = "2026-10-09T00:00:00Z"
	if got := manamiNext(st, u).Format(time.RFC3339); got != "2026-10-09T06:00:00Z" {
		t.Fatal(got)
	}
}
