package danmaku

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/darknightlab/onlinetorrent/internal/meta"
)

func seq(from, to float64) []float64 {
	var out []float64
	for n := from; n <= to; n++ {
		out = append(out, n)
	}
	return out
}

func eps(nums ...float64) []platEp {
	var out []platEp
	for _, n := range nums {
		out = append(out, platEp{Num: n, Ref: fmt.Sprintf("r%g", n)})
	}
	return out
}

func TestMapEpisode(t *testing.T) {
	cases := []struct {
		name   string
		ours   []float64
		shared [][]float64
		plat   []platEp
		want   float64
		ref    string
	}{
		{"same numbering", seq(1, 28), nil, eps(seq(1, 28)...), 1, "r1"},
		{"airing: platform lists fewer", seq(1, 12), nil, eps(1, 2, 3), 3, "r3"},
		{"airing: not uploaded yet", seq(1, 12), nil, eps(1, 2, 3), 4, ""},
		{"Bahamut numbers across seasons like bangumi (S2 = 29–38)", seq(29, 38), nil, eps(seq(29, 38)...), 31, "r31"},
		{"per-season numbering on the platform", seq(13, 24), nil, eps(seq(1, 12)...), 13, "r1"},
		{"per-season, airing", seq(13, 24), nil, eps(1, 2), 14, "r2"},
		{"per-season: platform has more episodes than we do", seq(13, 24), nil, eps(seq(1, 13)...), 13, ""},
		{"per-season needs integer, gapless numbers", []float64{13, 14, 15.5}, nil, eps(1, 2, 3), 13, ""},
		{"a recap the platform counts as an episode", seq(1, 12), nil, eps(seq(1, 13)...), 5, ""},
		{"duplicate numbers", seq(1, 12), nil, append(eps(1, 2), platEp{Num: 2, Ref: "x"}), 1, ""},
		{"not an official episode of ours", seq(1, 12), nil, eps(seq(1, 13)...), 13, ""},
		{"shared listing, disjoint cours (part 2)", seq(13, 25), [][]float64{seq(1, 12)}, eps(seq(1, 25)...), 13, "r13"},
		{"shared listing, disjoint cours (part 1)", seq(1, 12), [][]float64{seq(13, 25)}, eps(seq(1, 25)...), 12, "r12"},
		{"shared listing, both numbered from 1", seq(1, 13), [][]float64{seq(1, 12)}, eps(seq(1, 25)...), 1, ""},
		{"shared listing with numbers of nobody", seq(1, 12), [][]float64{seq(13, 24)}, eps(seq(1, 26)...), 1, ""},
		{"shared listing, per-season numbering (part 1)", seq(67, 77), [][]float64{seq(78, 85)}, eps(seq(1, 19)...), 67, "r1"},
		{"shared listing, per-season numbering (part 2)", seq(78, 85), [][]float64{seq(67, 77)}, eps(seq(1, 19)...), 80, "r14"},
		{"shared listing, per-season, gap between the parts", seq(78, 85), [][]float64{seq(60, 70)}, eps(seq(1, 10)...), 80, ""},
		{"empty listing", seq(1, 12), nil, nil, 1, ""},
		{"a platform-only recap 12.5 is ignored", seq(13, 24), nil, eps(12.5, 13, 14), 14, "r14"},
		{"a recap 12.5 we have too", append(seq(1, 12), 12.5), nil, eps(12, 12.5), 12.5, "r12.5"},
	}
	for _, c := range cases {
		ref, ok := mapEpisode(c.ours, c.shared, c.plat, c.want)
		if ref != c.ref || ok != (c.ref != "") {
			t.Errorf("%s: got %q %v, want %q", c.name, ref, ok, c.ref)
		}
	}
}

func TestConvert(t *testing.T) {
	in := []apiComment{
		{P: "12.345,1,16777215,[bilibili1]", M: " hello "},
		{P: "3,5,255,[bilibili1]", M: "top"},
		{P: "4,7,0,[bilibili1]", M: `[0,0,"1-1",4.5,"positioned"]`}, // advanced: dropped
		{P: "5,6,0,[bilibili1]", M: "reverse"},
		{P: "6,4,0,[bahamut]", M: ""},
		{P: "bad", M: "x"},
	}
	got := convert(in)
	want := []meta.Comment{{Time: 3, Mode: 5, Color: 255, Text: "top"}, {Time: 5, Mode: 1, Color: 0, Text: "reverse"}, {Time: 12.35, Mode: 1, Color: 0xFFFFFF, Text: "hello"}}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("convert: %+v", got)
	}
}

// fixture: a work (bgm 400602, 28 official episodes) on Bilibili (media 21087073 → season
// 46089, previews titled 预告 are skipped) and Bahamut (acg 127137, videos 35241…), with fake
// platform APIs and a fake danmu_api.
type fixture struct {
	svc        *Service
	ms         *meta.Store
	work       *meta.Work
	calls      map[string]*atomic.Int32 // danmu_api calls per platform
	biliEmpty  atomic.Bool              // danmu_api returns no Bilibili comments
	bahaFail   atomic.Bool              // danmu_api answers 500 for Bahamut
	biliCutoff atomic.Bool              // Bilibili comments end early (one missing segment)
	gate       chan struct{}            // when set, danmu_api waits for it
	now        time.Time
	mu         sync.Mutex
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	ms, err := meta.Open(filepath.Join(t.TempDir(), "meta.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(ms.Close)
	ctx := context.Background()
	exec := func(q string, args ...any) {
		if _, err := ms.W.ExecContext(ctx, q, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO works(id,key,title,type,updated_at) VALUES(1,'bgm:400602','葬送のフリーレン','tv','x')`)
	exec(`INSERT INTO work_ids VALUES('bgm','400602','bgm:400602')`)
	for n := 1; n <= 28; n++ {
		exec(`INSERT INTO episodes(bgm_id,work_key,type,sort) VALUES(?,?,0,?)`, 1000+n, "bgm:400602", n)
	}
	exec(`INSERT INTO episodes(bgm_id,work_key,type,sort) VALUES(2001,'bgm:400602',1,1)`) // SP1
	exec(`INSERT INTO bd_sites VALUES(400602,'bilibili','21087073'),(400602,'gamer','127137'),(400602,'gamer_hk','127137')`)
	w, err := ms.GetWork(ctx, "bgm:400602")
	if err != nil {
		t.Fatal(err)
	}
	f := &fixture{ms: ms, work: w, calls: map[string]*atomic.Int32{Bilibili: {}, Bahamut: {}}, now: time.Date(2026, 10, 21, 0, 0, 0, 0, time.UTC)}

	plat := http.NewServeMux()
	plat.HandleFunc("/pgc/review/user", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("media_id") != "21087073" {
			_, _ = io.WriteString(w, `{"code":-404,"message":"not found"}`)
			return
		}
		_, _ = io.WriteString(w, `{"code":0,"result":{"media":{"season_id":46089}}}`)
	})
	plat.HandleFunc("/pgc/web/season/section", func(w http.ResponseWriter, r *http.Request) {
		var list []string
		for n := 1; n <= 28; n++ {
			list = append(list, fmt.Sprintf(`{"id":%d,"title":"%d"}`, 779774+n, n))
		}
		list = append(list, `{"id":1,"title":"第8话预告"}`)
		_, _ = io.WriteString(w, `{"code":0,"result":{"main_section":{"episodes":[`+strings.Join(list, ",")+`]}}}`)
	})
	plat.HandleFunc("/acgDetail.php", func(w http.ResponseWriter, r *http.Request) {
		li := func(sn, label string) string {
			return `<li><a href="//ani.gamer.com.tw/animeVideo.php?sn=` + sn + `" target="_blank">` + label + `</a></li>`
		}
		var b strings.Builder
		b.WriteString(`<a href="//ani.gamer.com.tw/animeVideo.php?sn=1">related</a><h4>動畫瘋線上看</h4><div class="seasonACG"><ul>`)
		switch r.URL.Query().Get("s") {
		case "127137":
			b.WriteString(li("35241", "1") + li("35242", "2"))
		case "555": // Taiwan only, complete list on the page
			b.WriteString(li("901", "1") + li("902", "2") + li("903", "2.5") + li("904", "3"))
		case "556": // Taiwan only, truncated list
			b.WriteString(li("911", "1") + li("912", "2") + li("913", "更多"))
		}
		b.WriteString(`</ul></div>`)
		_, _ = io.WriteString(w, b.String())
	})
	plat.HandleFunc("/anime/v1/video.php", func(w http.ResponseWriter, r *http.Request) {
		if sn := r.URL.Query().Get("videoSn"); strings.HasPrefix(sn, "9") {
			_, _ = io.WriteString(w, `{"error":{"code":1011,"message":"not available in your region"}}`)
			return
		}
		var list []string
		for n := 1; n <= 28; n++ {
			list = append(list, fmt.Sprintf(`{"episode":%d,"videoSn":%d}`, n, 35240+n))
		}
		_, _ = io.WriteString(w, `{"data":{"anime":{"acgSn":127137,"episodes":{"0":[`+strings.Join(list, ",")+`],"1":[{"episode":1,"videoSn":9}]}}}}`)
	})
	ps := httptest.NewServer(plat)
	t.Cleanup(ps.Close)

	api := http.NewServeMux()
	api.HandleFunc("GET /TOKEN/api/v2/comment", func(w http.ResponseWriter, r *http.Request) {
		f.calls[Bilibili].Add(1)
		if f.gate != nil {
			<-f.gate
		}
		u := r.URL.Query().Get("url")
		if !strings.HasPrefix(u, "https://www.bilibili.com/bangumi/play/ep7797") || !strings.HasSuffix(u, "?season_id=46089") || r.URL.Query().Get("format") != "json" {
			http.Error(w, "unexpected url "+u, 400)
			return
		}
		if f.biliEmpty.Load() {
			_, _ = io.WriteString(w, `{"errorCode":0,"success":true,"count":0,"comments":[]}`)
			return
		}
		last := 1550.0
		if f.biliCutoff.Load() {
			last = 700
		}
		_, _ = fmt.Fprintf(w, `{"errorCode":0,"success":true,"count":2,"comments":[{"cid":1,"p":"%g,1,16777215,[bilibili1]","m":"end"},{"cid":2,"p":"1.5,5,255,[bilibili1]","m":"开头"}],"videoDuration":1559.9}`, last)
	})
	api.HandleFunc("POST /TOKEN/api/v2/segmentcomment", func(w http.ResponseWriter, r *http.Request) {
		f.calls[Bahamut].Add(1)
		if f.gate != nil {
			<-f.gate
		}
		var seg map[string]any
		_ = json.NewDecoder(r.Body).Decode(&seg)
		if sn, _ := seg["url"].(string); seg["type"] != "bahamut" || !strings.HasPrefix(sn, "352") {
			http.Error(w, "unexpected segment", 400)
			return
		}
		if f.bahaFail.Load() {
			http.Error(w, `{"errorCode":500}`, 500)
			return
		}
		_, _ = io.WriteString(w, `{"errorCode":0,"success":true,"count":1,"comments":[{"cid":1,"p":"2.00,4,16711680,[bahamut]","m":"巴哈"}]}`)
	})
	as := httptest.NewServer(api)
	t.Cleanup(as.Close)

	f.svc = &Service{Meta: ms, BiliAPI: ps.URL, BahaAPI: ps.URL, BahaACG: ps.URL, APIURL: as.URL + "/TOKEN",
		Now: func() time.Time { f.mu.Lock(); defer f.mu.Unlock(); return f.now }}
	return f
}

func (f *fixture) advance(d time.Duration) {
	f.mu.Lock()
	f.now = f.now.Add(d)
	f.mu.Unlock()
}

func (f *fixture) get(t *testing.T, ep float64) []meta.Comment {
	t.Helper()
	c, err := f.svc.Get(context.Background(), context.Background(), f.work, meta.EpisodeKey{Kind: meta.KeyMain, Number: ep}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestCollectOnceAndStore(t *testing.T) {
	f := newFixture(t)
	got := f.get(t, 1)
	if fmt.Sprint(got) != fmt.Sprint([]meta.Comment{{Time: 1.5, Mode: 5, Color: 255, Text: "开头"}, {Time: 2, Mode: 4, Color: 0xFF0000, Text: "巴哈"}, {Time: 1550, Mode: 1, Color: 0xFFFFFF, Text: "end"}}) {
		t.Fatalf("merged: %+v", got)
	}
	sets, _ := f.ms.Danmaku(context.Background(), "bgm:400602", meta.EpisodeKey{Kind: meta.KeyMain, Number: 1})
	if len(sets) != 2 || sets[0].Platform != Bahamut || sets[0].Ref != "35241" || sets[1].Platform != Bilibili || sets[1].Ref != "779775" || sets[1].FetchedAt == "" {
		t.Fatalf("stored: %+v", sets)
	}
	// stored: never fetched again, even much later
	f.advance(30 * 24 * time.Hour)
	if got := f.get(t, 1); len(got) != 3 {
		t.Errorf("second read: %+v", got)
	}
	if f.calls[Bilibili].Load() != 1 || f.calls[Bahamut].Load() != 1 {
		t.Errorf("danmu_api calls: bilibili %d bahamut %d", f.calls[Bilibili].Load(), f.calls[Bahamut].Load())
	}
	// specials are not mapped
	if got, _ := f.svc.Get(context.Background(), context.Background(), f.work, meta.EpisodeKey{Kind: meta.KeySP, Number: 1}, nil); len(got) != 0 {
		t.Errorf("sp: %+v", got)
	}
	// not an official episode: nothing to map, no call
	if got := f.get(t, 29); len(got) != 0 || f.calls[Bilibili].Load() != 1 {
		t.Errorf("ep 29: %+v", got)
	}
}

func TestFailuresAreNotStored(t *testing.T) {
	f := newFixture(t)
	f.biliEmpty.Store(true)
	f.bahaFail.Store(true)
	if got := f.get(t, 2); len(got) != 0 {
		t.Fatalf("got %+v", got)
	}
	if has, _ := f.ms.HasDanmaku(context.Background(), "bgm:400602", meta.EpisodeKey{Kind: meta.KeyMain, Number: 2}); has {
		t.Fatal("an empty / failed fetch was stored")
	}
	// backoff: no new attempt within 10 minutes
	f.get(t, 2)
	f.advance(5 * time.Minute)
	f.get(t, 2)
	if n := f.calls[Bilibili].Load(); n != 1 {
		t.Errorf("retried during backoff: %d calls", n)
	}
	// after the backoff: retried; one platform succeeding stores it
	f.advance(6 * time.Minute)
	f.biliEmpty.Store(false)
	if got := f.get(t, 2); len(got) != 2 {
		t.Fatalf("after backoff: %+v", got)
	}
	// the episode has danmaku now: Bahamut (still failing) is not collected any more
	f.advance(24 * time.Hour)
	f.bahaFail.Store(false)
	if got := f.get(t, 2); len(got) != 2 || f.calls[Bahamut].Load() != 2 {
		t.Errorf("episode with danmaku collected again: %d comments, bahamut calls %d", len(got), f.calls[Bahamut].Load())
	}
	// the backoff doubles: second failure → 20 minutes
	f.biliEmpty.Store(true)
	f.bahaFail.Store(true)
	f.get(t, 3)
	f.advance(11 * time.Minute)
	f.get(t, 3) // second failure
	f.advance(11 * time.Minute)
	f.get(t, 3) // still in the 20-minute backoff
	if n := f.calls[Bilibili].Load(); n != 4 {
		t.Errorf("bilibili calls: %d, want 4", n)
	}
}

func TestCutOffBilibiliRetried(t *testing.T) {
	f := newFixture(t)
	f.bahaFail.Store(true)
	f.biliCutoff.Store(true)
	for i := 0; i < suspectRetries; i++ {
		if got := f.get(t, 4); len(got) != 0 {
			t.Fatalf("attempt %d: a cut-off result was stored: %+v", i, got)
		}
		f.advance(backoffMax)
	}
	// still cut off after the retries: accepted as it is
	if got := f.get(t, 4); len(got) != 2 {
		t.Fatalf("accepted after retries: %+v", got)
	}
}

func TestConcurrentRequestsShareOneFetch(t *testing.T) {
	f := newFixture(t)
	f.gate = make(chan struct{})
	var wg sync.WaitGroup
	results := make([]int, 8)
	for i := range results {
		wg.Go(func() { results[i] = len(f.get(t, 5)) })
	}
	time.Sleep(200 * time.Millisecond)
	close(f.gate)
	wg.Wait()
	for i, n := range results {
		if n != 3 {
			t.Errorf("request %d: %d comments", i, n)
		}
	}
	if f.calls[Bilibili].Load() != 1 || f.calls[Bahamut].Load() != 1 {
		t.Errorf("danmu_api calls: bilibili %d bahamut %d", f.calls[Bilibili].Load(), f.calls[Bahamut].Load())
	}
}

func TestRateLimitOnlyForNewFetches(t *testing.T) {
	f := newFixture(t)
	deny := func() bool { return false }
	if got, _ := f.svc.Get(context.Background(), context.Background(), f.work, meta.EpisodeKey{Kind: meta.KeyMain, Number: 6}, deny); len(got) != 0 || f.calls[Bilibili].Load() != 0 {
		t.Fatalf("rate-limited request fetched: %+v", got)
	}
	f.get(t, 6) // allowed: fetched and stored
	if got, _ := f.svc.Get(context.Background(), context.Background(), f.work, meta.EpisodeKey{Kind: meta.KeyMain, Number: 6}, deny); len(got) != 3 {
		t.Errorf("stored danmaku is served regardless of the limit: %+v", got)
	}
}

func TestUncertainMappingNoFetch(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	// a second Bilibili listing for the same subject (e.g. one per cour): only a listing with
	// all our episodes counts. Media 2 is unknown to the fake (lookup fails) and media
	// 21087073 is complete, so Bilibili still maps.
	if _, err := f.ms.W.ExecContext(ctx, `INSERT INTO bd_sites VALUES(400602,'bilibili_hk_mo_tw','2')`); err != nil {
		t.Fatal(err)
	}
	ts, err := f.svc.Targets(ctx, f.work, meta.EpisodeKey{Kind: meta.KeyMain, Number: 1})
	if err != nil || len(ts) != 2 {
		t.Fatalf("targets: %+v %v", ts, err)
	}
	// the Bahamut listing also belongs to another subject numbered 1–12: overlapping → not certain
	if _, err := f.ms.W.ExecContext(ctx, `INSERT INTO works(id,key,title,type,updated_at) VALUES(2,'bgm:9','x','tv','x')`); err != nil {
		t.Fatal(err)
	}
	for n := 1; n <= 12; n++ {
		_, _ = f.ms.W.ExecContext(ctx, `INSERT INTO episodes(bgm_id,work_key,type,sort) VALUES(?,?,0,?)`, 5000+n, "bgm:9", n)
	}
	_, _ = f.ms.W.ExecContext(ctx, `INSERT INTO bd_sites VALUES(9,'gamer','127137')`)
	ts, _ = f.svc.Targets(ctx, f.work, meta.EpisodeKey{Kind: meta.KeyMain, Number: 1})
	if len(ts) != 1 || ts[0].Platform != Bilibili {
		t.Errorf("targets with an overlapping shared Bahamut listing: %+v", ts)
	}
	// no site ids at all: nothing, no network
	if _, err := f.ms.W.ExecContext(ctx, `DELETE FROM bd_sites`); err != nil {
		t.Fatal(err)
	}
	if got := f.get(t, 7); len(got) != 0 || f.calls[Bilibili].Load()+f.calls[Bahamut].Load() != 0 {
		t.Errorf("fetched without a mapping: %+v", got)
	}
}

func TestBahamutRegionRestrictedList(t *testing.T) {
	f := newFixture(t)
	f.svc.init()
	ctx := context.Background()
	l, err := f.svc.bahamutList(ctx, "555")
	if err != nil || fmt.Sprint(l.Eps) != fmt.Sprint([]platEp{{1, "901"}, {2, "902"}, {2.5, "903"}, {3, "904"}}) {
		t.Errorf("complete page list: %+v %v", l, err)
	}
	if l, err := f.svc.bahamutList(ctx, "556"); err != nil || len(l.Eps) != 0 {
		t.Errorf("truncated page list must not be used: %+v %v", l, err)
	}
	// the full list from video.php wins over the page (and the "related" link outside the
	// 動畫瘋線上看 block is not used)
	if l, err := f.svc.bahamutList(ctx, "127137"); err != nil || len(l.Eps) != 28 {
		t.Errorf("api list: %+v %v", l, err)
	}
}
