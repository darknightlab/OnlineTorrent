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
