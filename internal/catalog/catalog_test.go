package catalog

import (
	"context"
	"fmt"
	"testing"
	"time"
)

func TestTTLCacheBounded(t *testing.T) {
	var c ttlCache[int]
	for i := range 5 * ttlCacheMax {
		c.set(fmt.Sprint(i), i, time.Hour)
	}
	if n := c.len(); n > ttlCacheMax {
		t.Errorf("cache size %d > %d", n, ttlCacheMax)
	}
	if v, ok := c.get(fmt.Sprint(5*ttlCacheMax - 1)); !ok || v != 5*ttlCacheMax-1 {
		t.Error("latest entry missing")
	}
}

func TestReCHS(t *testing.T) {
	for s, want := range map[string]bool{
		"[LoliHouse] Foo - 01 [简繁内封]":    true,
		"[Sub] Foo - 01 [CHS][1080p]":    true,
		"[Sub] Foo - 01 [GB][1080p]":     true,
		"[Sub] Foo - 01 [SC][1080p]":     true,
		"[Sub] Foo [BD Disc 1][1080p]":   false,
		"[Sub] Foo - 01 [BIG5][1080p]":   false,
		"[Sub] Foo - 01 [Sci-Fi][1080p]": false,
	} {
		if got := reCHS.MatchString(s); got != want {
			t.Errorf("%q: got %v want %v", s, got, want)
		}
	}
}

func TestPacer(t *testing.T) {
	p := newPacer(100, 20*time.Millisecond) // burst of 2, then 10ms apart
	start := time.Now()
	for range 6 {
		if err := p.wait(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if el := time.Since(start); el < 25*time.Millisecond {
		t.Errorf("not paced: %v", el)
	}
	ctx, cancel := context.WithCancel(context.Background())
	p2 := newPacer(0.01, 0) // one per 100s
	_ = p2.wait(ctx)
	cancel()
	if err := p2.wait(ctx); err == nil {
		t.Error("wait must honor ctx")
	}
}

func TestPickLatest(t *testing.T) {
	ts := []*Torrent{
		{Title: "[A] Show - 03 [1080p][CHS]", SizeBytes: 500 << 20, PublishedAt: "2026-01-01T00:00:00Z"},
		{Title: "[B] Show - 04 [720p]", SizeBytes: 300 << 20, PublishedAt: "2026-01-02T00:00:00Z"},
		{Title: "[C] Show - 04 [1080p][简体]", SizeBytes: 600 << 20, PublishedAt: "2026-01-02T00:00:00Z"},
		{Title: "[D] Show 05", SizeBytes: 600 << 20, PublishedAt: "2026-01-03T00:00:00Z"}, // bare trailing number in a title is ignored
	}
	got, ep := PickLatest(ts, 2<<30)
	if got == nil || *ep != 4 || got.Title != ts[2].Title {
		t.Errorf("got %+v ep=%v", got, ep)
	}
}

func TestPickLatestPrefersAVC(t *testing.T) {
	mk := func(title string) *Torrent {
		return &Torrent{Title: title, SizeBytes: 500 << 20, FileList: []string{title + ".mkv"}, PublishedAt: "2026-10-17T00:00:00Z"}
	}
	hevc := mk("[A] Show - 02 [1080p][HEVC-10bit]")
	avc := mk("[B] Show - 02 [1080p][AVC]")
	got, ep := PickLatest([]*Torrent{hevc, avc}, 2<<30)
	if got != avc || ep == nil || *ep != 2 {
		t.Fatalf("got %+v", got)
	}
}
