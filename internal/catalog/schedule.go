package catalog

import (
	"context"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/darknightlab/onlinetorrent/internal/meta"
	"github.com/darknightlab/onlinetorrent/internal/store"
)

// ScheduleItem is one airing anime (see docs/api.md).
type ScheduleItem struct {
	Key           string            `json:"key"`
	Title         string            `json:"title"`
	TitleCN       string            `json:"title_cn"`
	Cover         string            `json:"cover"`
	Begin         string            `json:"begin"`
	Weekday       int               `json:"weekday"`
	AirTime       string            `json:"air_time"`
	LastAirAt     *string           `json:"last_air_at"`
	NextAirAt     *string           `json:"next_air_at"`
	EpisodesAired int               `json:"episodes_aired"`
	BgmID         *int64            `json:"bgm_id"`
	MoeTagID      *string           `json:"moe_tag_id"`
	External      map[string]string `json:"external,omitempty"`
	LatestTorrent *Torrent          `json:"latest_torrent"`
	LatestEpisode *float64          `json:"latest_episode"`
}

// Schedule is the timeline response.
type Schedule struct {
	GeneratedAt string          `json:"generated_at"`
	Season      string          `json:"season"`
	Items       []*ScheduleItem `json:"items"`
}

var cst = func() *time.Location {
	l, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		return time.FixedZone("CST", 8*3600)
	}
	return l
}()

func ts(t time.Time) *string {
	if t.IsZero() {
		return nil
	}
	s := t.UTC().Format(time.RFC3339)
	return &s
}

// Schedule returns the cached timeline (nil until first build).
func (c *Catalog) Schedule() *Schedule {
	c.schedMu.RLock()
	defer c.schedMu.RUnlock()
	return c.sched
}

// CoverFor returns the best cover for a work.
// bgm.tv's 400px "common" image is preferred (bangumi.moe covers can be 2000px+),
// bangumi.moe is the fallback.
func (c *Catalog) CoverFor(w *meta.Work) string {
	if w.Cover != "" {
		return w.Cover
	}
	if id := w.External["bgm"]; id != "" {
		return "https://api.bgm.tv/v0/subjects/" + id + "/image?type=common"
	}
	if id := w.External["bangumi_moe"]; id != "" {
		c.coversMu.RLock()
		cv := c.covers[id]
		c.coversMu.RUnlock()
		if cv != "" {
			return cv
		}
	}
	return ""
}

// ItemFor builds a schedule item for a work (without latest torrent).
func (c *Catalog) ItemFor(w *meta.Work, now time.Time) (*ScheduleItem, bool) {
	a, ok := meta.ComputeAiring(w, now)
	if !ok {
		return nil, false
	}
	ref := a.Last
	if ref.IsZero() {
		ref = a.Next
	}
	if ref.IsZero() {
		ref = a.Start
	}
	local := ref.In(cst)
	it := &ScheduleItem{
		Key: w.Key, Title: w.Title, TitleCN: w.TitleCN, Cover: c.CoverFor(w), Begin: w.Begin,
		Weekday: int(local.Weekday()), AirTime: local.Format("15:04"),
		LastAirAt: ts(a.Last), NextAirAt: ts(a.Next), EpisodesAired: a.Aired, External: w.External,
	}
	if v, err := strconv.ParseInt(w.External["bgm"], 10, 64); err == nil {
		it.BgmID = &v
	}
	if v := w.External["bangumi_moe"]; v != "" {
		it.MoeTagID = &v
	}
	return it, true
}

var reHiRes = regexp.MustCompile(`(?i)1080|1920`)
var reHEVC = regexp.MustCompile(`(?i)hevc|x\.?265|h\.?265|10-?bit|ma10p`)
var reAVC = regexp.MustCompile(`(?i)\bavc\b|x\.?264|h\.?264`)
var reCHS = regexp.MustCompile(`(?i)简|chs|\bgb\b|\bsc\b`)

// PickLatest chooses the torrent to auto-play for the newest episode.
func PickLatest(ts []*Torrent, maxBytes int64) (*Torrent, *float64) {
	type cand struct {
		t     *Torrent
		ep    float64
		score int
	}
	var cands []cand
	maxEp := -1.0
	for _, t := range ts {
		p := meta.ParseTitle(t.Title)
		if p.Episode == nil || p.Kind != "episode" {
			continue
		}
		sc := 0
		if maxBytes <= 0 || (t.SizeBytes > 0 && t.SizeBytes <= maxBytes) {
			sc += 8
		}
		videos := 0
		for _, f := range t.FileList {
			if meta.IsVideo(f) {
				videos++
			}
		}
		if videos == 1 {
			sc += 4
		}
		if reHiRes.MatchString(t.Title) {
			sc += 2
		}
		if reCHS.MatchString(t.Title) {
			sc++
		}
		// browsers decode H.264 everywhere; HEVC/10-bit only on some platforms
		if reHEVC.MatchString(t.Title) {
			sc -= 3
		} else if reAVC.MatchString(t.Title) {
			sc += 3
		}
		cands = append(cands, cand{t, *p.Episode, sc})
		if *p.Episode > maxEp {
			maxEp = *p.Episode
		}
	}
	var best *cand
	for i := range cands {
		c := &cands[i]
		if c.ep != maxEp {
			continue
		}
		if best == nil || c.score > best.score || (c.score == best.score && c.t.PublishedAt > best.t.PublishedAt) {
			best = c
		}
	}
	if best == nil {
		return nil, nil
	}
	ep := best.ep
	return best.t, &ep
}

func (c *Catalog) rebuildSchedule(ctx context.Context) {
	c.rebuildMu.Lock()
	defer c.rebuildMu.Unlock()
	now := time.Now()
	works, err := c.meta.WorksForSchedule(ctx, now)
	if err != nil {
		c.log.Warn("schedule: works", "err", err)
		return
	}
	maxBytes := int64(c.st.Num(store.KeyMaxAutoSize)) << 20
	var items []*ScheduleItem
	for _, w := range works {
		if w.Type != "tv" && w.Type != "web" {
			continue
		}
		it, ok := c.ItemFor(w, now)
		if !ok {
			continue
		}
		// keep currently airing, or finished within the last 10 days
		if it.NextAirAt == nil && (it.LastAirAt == nil || *it.LastAirAt < now.AddDate(0, 0, -10).UTC().Format(time.RFC3339)) {
			continue
		}
		items = append(items, it)
	}
	// phase 1: publish immediately, carrying over latest torrents from the previous build
	if prev := c.Schedule(); prev != nil {
		old := map[string]*ScheduleItem{}
		for _, it := range prev.Items {
			old[it.Key] = it
		}
		for _, it := range items {
			if o := old[it.Key]; o != nil {
				it.LatestTorrent, it.LatestEpisode = o.LatestTorrent, o.LatestEpisode
			}
		}
	}
	c.publishSchedule(now, cloneItems(items))

	// covers: resolve api.bgm.tv image redirects once (they can take ~20s) and store the final URL
	c.resolveCovers(ctx, works, items)

	// phase 2: latest torrent per anime (bounded concurrency; ByTag is cached for an hour).
	// Only refetch shows that aired within the last 3 days or have no torrent yet.
	recent := now.AddDate(0, 0, -3).UTC().Format(time.RFC3339)
	sem := make(chan struct{}, 3)
	var wg sync.WaitGroup
	for _, it := range items {
		if it.MoeTagID == nil {
			continue
		}
		if it.LatestTorrent != nil && (it.LastAirAt == nil || *it.LastAirAt < recent) {
			continue
		}
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
			defer cancel()
			list, _, err := c.ByTag(cctx, *it.MoeTagID, 1)
			if err != nil {
				return
			}
			it.LatestTorrent, it.LatestEpisode = PickLatest(list, maxBytes)
		}()
	}
	wg.Wait()
	c.publishSchedule(now, items)
	c.log.Info("schedule rebuilt", "items", len(items))
}

func cloneItems(items []*ScheduleItem) []*ScheduleItem {
	out := make([]*ScheduleItem, len(items))
	for i, it := range items {
		c := *it
		out[i] = &c
	}
	return out
}

func (c *Catalog) publishSchedule(now time.Time, items []*ScheduleItem) {
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].Weekday != items[j].Weekday {
			return items[i].Weekday < items[j].Weekday
		}
		return items[i].AirTime < items[j].AirTime
	})
	m := now.In(cst)
	season := m.Format("2006") + "-" + []string{"01", "01", "01", "04", "04", "04", "07", "07", "07", "10", "10", "10"}[m.Month()-1]
	if items == nil {
		items = []*ScheduleItem{}
	}
	c.schedMu.Lock()
	c.sched = &Schedule{GeneratedAt: store.Now(), Season: season, Items: items}
	c.schedMu.Unlock()
}

// resolveCovers stores the final lain.bgm.tv URL for works that only have a bgm id,
// so browsers never hit the slow api.bgm.tv redirect.
func (c *Catalog) resolveCovers(ctx context.Context, works []*meta.Work, items []*ScheduleItem) {
	byKey := map[string]*ScheduleItem{}
	for _, it := range items {
		byKey[it.Key] = it
	}
	hc := c.HTTPClient()
	nc := &http.Client{Transport: hc.Transport, Timeout: 40 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	sem := make(chan struct{}, 2)
	var wg sync.WaitGroup
	for _, w := range works {
		it := byKey[w.Key]
		id := w.External["bgm"]
		if it == nil || w.Cover != "" || id == "" {
			continue
		}
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.bgm.tv/v0/subjects/"+id+"/image?type=common", nil)
			if err != nil {
				return
			}
			req.Header.Set("User-Agent", c.st.Str(store.KeyBgmUserAgent))
			resp, err := nc.Do(req)
			if err != nil {
				return
			}
			resp.Body.Close()
			loc := resp.Header.Get("Location")
			u, err := url.Parse(loc)
			if resp.StatusCode/100 != 3 || err != nil || u.Scheme != "https" || !strings.HasSuffix(u.Host, "bgm.tv") {
				return
			}
			if c.meta.SetCover(ctx, w.Key, loc) == nil {
				it.Cover = loc
			}
		}()
	}
	wg.Wait()
}
