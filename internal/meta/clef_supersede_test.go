package meta

import (
	"context"
	"testing"
)

// A re-run adds new decisions; lists by status show only each file's latest decision
// (the old 未映射 row of a file mapped on the re-run disappears), a torrent's list shows
// the whole history, and the counts follow the latest decisions.
func TestClefDecisionsSuperseded(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	add := func(ih string, idx int, status string) {
		t.Helper()
		if err := s.AddClefDecision(ctx, &ClefDecision{Infohash: ih, FileIndex: idx, Path: "x.mkv", Status: status, CreatedAt: "2026-10-20T00:00:00Z"}); err != nil {
			t.Fatal(err)
		}
	}
	add("aa", 0, "discarded") // first run
	add("aa", 1, "discarded")
	add("aa", 0, "applied") // re-run: file 0 mapped, file 1 still discarded
	add("aa", 1, "discarded")
	add("bb", 0, "pending")

	if _, n, _ := s.ListClefDecisions(ctx, "discarded", "", 0, 10); n != 1 {
		t.Errorf("discarded (latest only) = %d, want 1", n)
	}
	if ds, n, _ := s.ListClefDecisions(ctx, "applied", "", 0, 10); n != 1 || ds[0].FileIndex != 0 {
		t.Errorf("applied = %d %+v", n, ds)
	}
	if _, n, _ := s.ListClefDecisions(ctx, "", "", 0, 10); n != 3 {
		t.Errorf("all (latest only) = %d, want 3", n)
	}
	if _, n, _ := s.ListClefDecisions(ctx, "", "aa", 0, 10); n != 4 {
		t.Errorf("torrent history = %d, want 4", n)
	}
	c, err := s.ClefDecisionCounts(ctx)
	if err != nil || c["discarded"] != 1 || c["applied"] != 1 || c["pending"] != 1 {
		t.Errorf("counts %v %v", c, err)
	}
}
