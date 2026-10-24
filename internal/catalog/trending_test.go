package catalog

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/darknightlab/onlinetorrent/internal/meta"
	"github.com/darknightlab/onlinetorrent/internal/store"
)

const sampleBgmAnimeHTML = `<!DOCTYPE html>
<html><body>
<div id="columnSubjectBrowserA" class="column">
  <div class="section">
    <h2 class="title">我的动画</h2>
    <a href="/subject/999999">Ignored Before</a>
  </div>
  <div class="section">
    <ul class="featuredItems">
      <li class="single clearit">
        <h2 class="title"><a href="/anime/browser/?sort=trends" class="l">注目动画</a> <small class="grey">根据最近 30 日标记</small></h2>
        <div class="grid">
          <div class="mainItem">
            <a href="/subject/554779" title="生徒会にも穴はある！"><div class="image"></div></a>
            <p class="info"><p class="title"><a href="/subject/554779" class="l">生徒会にも穴はある！</a></p></p>
          </div>
          <div class="mainItem">
            <a href="/subject/622288" title="FX戦士くるみちゃん"><div class="image"></div></a>
            <p class="info"><p class="title"><a href="/subject/622288" class="l">FX戦士くるみちゃん</a></p></p>
          </div>
          <div class="mainItem">
            <a href="/subject/513878" title="Cyberpunk: Edgerunners 2"><div class="image"></div></a>
            <p class="info"><p class="title"><a href="/subject/513878" class="l">Cyberpunk: Edgerunners 2</a></p></p>
          </div>
        </div>
      </li>
    </ul>
  </div>
  <div class="section">
    <h2 class="title"><a href="/anime/blog" class="l">动画日志</a></h2>
    <div id="entry_list">
      <a href="/subject/400602" class="l">葬送のフリーレン</a>
    </div>
  </div>
  <div class="section">
    <h2 class="title">所有动画的最新讨论</h2>
    <table><tr><td><a href="/subject/101266">進撃の巨人</a></td></tr></table>
  </div>
</div>
</body></html>`

func TestParseBgmTrendingIDs(t *testing.T) {
	ids, err := ParseBgmTrendingIDs(strings.NewReader(sampleBgmAnimeHTML))
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(ids, ","); got != "554779,622288,513878" {
		t.Fatalf("got %q, want 554779,622288,513878", got)
	}
	if _, err := ParseBgmTrendingIDs(strings.NewReader(`<html><body>no section</body></html>`)); err == nil {
		t.Fatal("expected error when trending section is missing")
	}
	if raw, err := os.ReadFile("/tmp/bgm-anime.html"); err == nil {
		realIDs, err := ParseBgmTrendingIDs(strings.NewReader(string(raw)))
		if err != nil {
			t.Fatal(err)
		}
		const wantReal = "554779,622288,513878,639938,568244,625799,537738,624085,630160,665390,622206,637802,646154,548156,624923"
		if got := strings.Join(realIDs, ","); got != wantReal {
			t.Fatalf("real fixture got %q, want %q", got, wantReal)
		}
	}
}

func TestTrendingCacheAndSingleflight(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "otd.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ms, err := meta.Open(filepath.Join(dir, "meta.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer ms.Close()
	ctx := context.Background()

	// Seed 2 of the 3 works in local DB; 513878 is missing and should be safely skipped.
	if _, err := ms.W.ExecContext(ctx, `INSERT INTO works(id,key,title,title_cn,type,begin_at,eps,updated_at) VALUES
		(1,'bgm:554779','生徒会にも穴はある！','学生会也有洞！','tv','2026-10-03T15:30:00Z',12,'x'),
		(2,'bgm:622288','FX戦士くるみちゃん','FX战士久留美','tv','2026-10-01T12:30:00Z',12,'x')`); err != nil {
		t.Fatal(err)
	}
	_, _ = ms.W.ExecContext(ctx, `INSERT INTO work_ids VALUES('bgm','554779','bgm:554779'),('bgm','622288','bgm:622288')`)

	var hits atomic.Int32
	var failUpstream atomic.Bool
	gate := make(chan struct{})
	var gateOnce sync.Once
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		<-gate
		if failUpstream.Load() {
			w.WriteHeader(502)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, sampleBgmAnimeHTML)
	}))
	defer srv.Close()

	cat := New(st, ms, slog.New(slog.NewTextHandler(io.Discard, nil)))
	cat.SetTrendingURL(srv.URL)

	// 1. Cold cache + upstream failure -> returns error and backs off.
	failUpstream.Store(true)
	gateOnce.Do(func() { close(gate) })
	if _, _, err := cat.TrendingWorks(ctx); err == nil {
		t.Fatal("expected error on cold cache when upstream fails")
	}
	if got := hits.Load(); got != 1 {
		t.Fatalf("hits = %d, want 1", got)
	}
	// Backoff prevents hammering upstream on immediate retry.
	if _, _, err := cat.TrendingWorks(ctx); err == nil {
		t.Fatal("expected backoff error")
	}
	if got := hits.Load(); got != 1 {
		t.Fatalf("hits during backoff = %d, want 1", got)
	}

	// Reset backoff and test concurrent singleflight fetch.
	failUpstream.Store(false)
	gate = make(chan struct{})
	cat.SetTrendingURL(srv.URL)

	const nConcurrent = 5
	var wg sync.WaitGroup
	results := make([][]*meta.Work, nConcurrent)
	errs := make([]error, nConcurrent)
	for i := 0; i < nConcurrent; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			results[idx], _, errs[idx] = cat.TrendingWorks(ctx)
		}(i)
	}
	time.Sleep(30 * time.Millisecond)
	close(gate)
	wg.Wait()

	if got := hits.Load(); got != 2 {
		t.Fatalf("hits after singleflight = %d, want 2", got)
	}
	for i := 0; i < nConcurrent; i++ {
		if errs[i] != nil || len(results[i]) != 2 || results[i][0].Key != "bgm:554779" || results[i][1].Key != "bgm:622288" {
			t.Fatalf("concurrent %d: err=%v works=%+v", i, errs[i], results[i])
		}
	}

	// 2. Within 24h TTL: serves from SQLite cache without hitting upstream.
	works, genAt, err := cat.TrendingWorks(ctx)
	if err != nil || genAt == "" || len(works) != 2 || hits.Load() != 2 {
		t.Fatalf("cached hit: err=%v genAt=%q len=%d hits=%d", err, genAt, len(works), hits.Load())
	}

	// 3. Expired cache (>24h) + upstream failure: retains stale cache and backs off.
	oldTime := time.Now().Add(-25 * time.Hour).UTC().Format(time.RFC3339)
	_ = ms.SetState(ctx, stateTrending, trendingCache{GeneratedAt: oldTime, IDs: []string{"622288", "554779"}})
	failUpstream.Store(true)
	cat.SetTrendingURL(srv.URL)

	works, genAt, err = cat.TrendingWorks(ctx)
	if err != nil || genAt != oldTime || len(works) != 2 || works[0].Key != "bgm:622288" || hits.Load() != 3 {
		t.Fatalf("stale fallback: err=%v genAt=%q works=%+v hits=%d", err, genAt, works, hits.Load())
	}
	// Immediate second call during backoff returns stale cache without hitting upstream.
	works2, _, err := cat.TrendingWorks(ctx)
	if err != nil || len(works2) != 2 || hits.Load() != 3 {
		t.Fatalf("stale during backoff: err=%v len=%d hits=%d", err, len(works2), hits.Load())
	}
}
