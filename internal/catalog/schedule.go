package catalog

import (
	"context"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/darknightlab/onlinetorrent/internal/meta"
	"github.com/darknightlab/onlinetorrent/internal/store"
)

// ScheduleItem is one airing anime (see docs/api.md).
type ScheduleItem struct {
	Key           string            `json:"key"`
	SubjectID     int64             `json:"subject_id"` // /subject/id/{subject_id}
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
	LatestEpisode *float64          `json:"latest_episode"`

	// content scope (not serialized): the work's tags / meta_tags, so the shared cached
	// schedule can be filtered per request without loading works again
	ScopeTags     []string `json:"-"`
	ScopeMetaTags []string `json:"-"`
}

// ScopeFacts implements store.ScopeTarget.
func (it *ScheduleItem) ScopeFacts() (tags, metaTags []string) { return it.ScopeTags, it.ScopeMetaTags }

// Schedule is the timeline response.
type Schedule struct {
	GeneratedAt string          `json:"generated_at"`
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

// MoeCover returns the bangumi.moe cover URL for a tag id, if known.
func (c *Catalog) MoeCover(id string) string {
	c.coversMu.RLock()
	cv := c.covers[id]
	c.coversMu.RUnlock()
	return cv
}

// UpstreamCoverURLs returns the ordered candidate upstream image URLs for a work.
// bgm.tv's 400px "common" image is preferred (bangumi.moe covers can be 2000px+),
// bangumi.moe is the fallback.
func (c *Catalog) UpstreamCoverURLs(w *meta.Work) []string {
	if w == nil {
		return nil
	}
	var out []string
	add := func(u string) {
		if u == "" {
			return
		}
		for _, ex := range out {
			if ex == u {
				return
			}
		}
		out = append(out, u)
	}
	if strings.HasPrefix(w.Cover, "https://") || strings.HasPrefix(w.Cover, "http://") {
		add(w.Cover)
	}
	if id := w.External["bgm"]; id != "" {
		add("https://api.bgm.tv/v0/subjects/" + url.PathEscape(id) + "/image?type=common")
	}
	if id := w.External["bangumi_moe"]; id != "" {
		if cv := c.MoeCover(id); cv != "" {
			add(cv)
		}
	}
	return out
}

// CoverFor returns the local cover endpoint URL for a work, or "" if no upstream cover
// source exists (so the frontend shows a placeholder without making a dead request).
func (c *Catalog) CoverFor(w *meta.Work) string {
	if len(c.UpstreamCoverURLs(w)) == 0 {
		return ""
	}
	if p, id, ok := strings.Cut(w.Key, ":"); ok && p != "" && id != "" {
		return "/api/v1/subjects/" + url.PathEscape(p) + "/" + url.PathEscape(id) + "/cover"
	}
	if w.ID > 0 {
		return fmt.Sprintf("/api/v1/subjects/id/%d/cover", w.ID)
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
		Key: w.Key, SubjectID: w.ID, Title: w.Title, TitleCN: w.TitleCN, Cover: c.CoverFor(w), Begin: w.Begin,
		Weekday: int(local.Weekday()), AirTime: local.Format("15:04"),
		LastAirAt: ts(a.Last), NextAirAt: ts(a.Next), EpisodesAired: a.Aired, External: w.External,
		ScopeTags: w.Tags, ScopeMetaTags: w.MetaTags,
	}
	if v, err := strconv.ParseInt(w.External["bgm"], 10, 64); err == nil {
		it.BgmID = &v
	}
	if v := w.External["bangumi_moe"]; v != "" {
		it.MoeTagID = &v
	}
	return it, true
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
	// phase 1: publish immediately, carrying over latest episodes from the previous build
	if prev := c.Schedule(); prev != nil {
		old := map[string]*ScheduleItem{}
		for _, it := range prev.Items {
			old[it.Key] = it
		}
		for _, it := range items {
			if o := old[it.Key]; o != nil {
				it.LatestEpisode = o.LatestEpisode
			}
		}
	}
	c.publishSchedule(now, cloneItems(items))

	// phase 2: latest episode per anime, from the file mappings
	c.fillLatest(ctx, items)
	c.publishSchedule(now, items)
	c.log.Info("schedule rebuilt", "items", len(items))
}

// fillLatest sets each item's latest episode: the highest main episode with mapped files (the
// same data as the episode list). Cards link to that episode page; which file plays is decided
// there (internal/rank), not here.
func (c *Catalog) fillLatest(ctx context.Context, items []*ScheduleItem) {
	for _, it := range items {
		keys, err := c.meta.EpisodeKeys(ctx, it.Key)
		if err != nil {
			c.log.Warn("schedule: latest", "work", it.Key, "err", err)
			continue
		}
		var best *float64
		for _, k := range keys {
			if k.Key.Kind == meta.KeyMain && k.Key.Number > 0 && k.Files > 0 && (best == nil || k.Key.Number > *best) {
				n := k.Key.Number
				best = &n
			}
		}
		it.LatestEpisode = best
	}
}

// RefreshLatest recomputes the latest episodes of the current schedule (called by the
// crawler after it ingested new torrents).
func (c *Catalog) RefreshLatest(ctx context.Context) {
	c.rebuildMu.Lock()
	defer c.rebuildMu.Unlock()
	prev := c.Schedule()
	if prev == nil {
		return
	}
	items := cloneItems(prev.Items)
	c.fillLatest(ctx, items)
	c.publishSchedule(time.Now(), items)
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
	if items == nil {
		items = []*ScheduleItem{}
	}
	c.schedMu.Lock()
	c.sched = &Schedule{GeneratedAt: store.Now(), Items: items}
	c.schedMu.Unlock()
}

// ScheduleItemByKey returns the cached schedule entry for a work (with latest torrent),
// or nil if the work is not on the current schedule.
func (c *Catalog) ScheduleItemByKey(key string) *ScheduleItem {
	sc := c.Schedule()
	if sc == nil {
		return nil
	}
	for _, it := range sc.Items {
		if it.Key == key {
			cp := *it
			return &cp
		}
	}
	return nil
}

// SetSchedule publishes items as the current schedule (tests).
func (c *Catalog) SetSchedule(items []*ScheduleItem) { c.publishSchedule(time.Now(), items) }
