package meta

import (
	"context"
	"testing"
)

func TestManamiDoesNotGrantFranchiseExemption(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	old := seedWork(t, s, 1, "古い作品", "", `["Old Series"]`, "tv", "2004-01-01", "2005-01-01", seq(1, 50), nil)
	newer := seedWork(t, s, 2, "新しい別作品", "", `["New Series Arc"]`, "tv", "2026-01-01", "", seq(1, 12), nil)
	for _, k := range []string{old, newer} {
		if _, err := s.W.Exec(`INSERT INTO manami_names VALUES(?, 'Generic Franchise', 'https://myanimelist.net/anime/1')`, k); err != nil {
			t.Fatal(err)
		}
	}
	w, _ := s.GetWork(ctx, old)
	res := s.mapFiles(ctx, []string{"[G] New Series Arc - 03.mkv"}, "[G] Old Series - 03", w, "title", 0.6, true)
	if !res[0].queue || res[0].work != nil {
		t.Fatalf("imported generic synonym bypassed contradiction: %+v", res)
	}
	res = s.mapFiles(ctx, []string{"[G] Generic Franchise - 03.mkv"}, "[G] Old Series - 03", w, "title", 0.6, true)
	if res[0].work != old {
		t.Fatalf("own imported name not recognised: %+v", res)
	}
}
