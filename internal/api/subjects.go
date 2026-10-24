package api

import (
	"context"
	"errors"
	"io"
	"math"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/darknightlab/onlinetorrent/internal/catalog"
	"github.com/darknightlab/onlinetorrent/internal/httpx"
	"github.com/darknightlab/onlinetorrent/internal/meta"
	"github.com/darknightlab/onlinetorrent/internal/policy"
	"github.com/darknightlab/onlinetorrent/internal/store"
)

// Subjects are single rating units (usually one season), addressed as
// /subject/{provider}/{id}: provider "id" = our numeric id, or an external site
// (bgm, mal, anidb, anilist, tmdb, …). A "series" (whole franchise) groups subjects (TODO).

type subjectOut struct {
	*catalog.ScheduleItem
	Summary  string            `json:"summary"`
	Type     string            `json:"type"`
	Official string            `json:"official_site"`
	External map[string]string `json:"external"`
	Tags     []string          `json:"tags"`
	MetaTags []string          `json:"meta_tags"`
	NSFW     bool              `json:"nsfw"`
	Score    float64           `json:"score,omitempty"`
	Rank     int               `json:"rank,omitempty"`
	Eps      int               `json:"eps,omitempty"` // planned episode count (bgm 话数)
	// Finished: every official main episode has aired (or, without official episodes, the
	// broadcast estimate has ended). Subject pages then start from episode 1.
	Finished bool `json:"finished"`
}

func (s *Server) resolveSubject(r *http.Request) (*meta.Work, error) {
	work, err := s.Meta.ResolveSubject(r.Context(), r.PathValue("provider"), r.PathValue("id"))
	if err == nil && !s.scopeOf(r).Visible(work) {
		err = store.ErrNotFound // outside the user's content scope: same 404 as a missing subject
	}
	if errors.Is(err, store.ErrNotFound) {
		return nil, httpx.E(404, "not_found", "条目不存在")
	}
	return work, err
}

func (s *Server) subjectView(r *http.Request, work *meta.Work) subjectOut {
	return s.subjectCard(r, work, true)
}

// subjectCard builds the Subject JSON; detail fetches a missing summary from bgm.tv once
// (subject pages only, never for search result lists).
func (s *Server) subjectCard(r *http.Request, work *meta.Work, detail bool) subjectOut {
	if detail && work.Summary == "" && work.External["bgm"] != "" {
		s.fetchBgmDetail(r.Context(), work)
	}
	item := s.Cat.ScheduleItemByKey(work.Key) // includes latest torrent / episode
	official := false                         // finished per the official episode air dates
	known := false                            // official episodes exist
	if item == nil {
		item, _ = s.Cat.ItemFor(work, time.Now())
		// not on the current schedule: official air dates are better than the weekly estimate
		if item != nil && detail {
			if n, total, ok := s.Meta.OfficialProgress(r.Context(), work.Key, time.Now()); ok {
				item.EpisodesAired = n
				known, official = true, n >= total
				if official {
					item.NextAirAt = nil // all aired: no "next episode" from the weekly estimate
				}
			}
		}
	}
	finished := official || (!known && item != nil && item.NextAirAt == nil && item.LastAirAt != nil)
	if item == nil {
		item = &catalog.ScheduleItem{Key: work.Key, SubjectID: work.ID, Title: work.Title, TitleCN: work.TitleCN, Cover: s.Cat.CoverFor(work), Begin: work.Begin}
		if v, err := strconv.ParseInt(work.External["bgm"], 10, 64); err == nil {
			item.BgmID = &v
		}
		if v := work.External["bangumi_moe"]; v != "" {
			item.MoeTagID = &v
		}
	}
	out := subjectOut{ScheduleItem: item, Summary: work.Summary, Type: work.Type, Official: work.Official, External: work.External,
		Tags: work.Tags, MetaTags: work.MetaTags, NSFW: work.NSFW, Score: work.Score, Rank: work.Rank, Eps: work.Eps, Finished: finished}
	if out.Tags == nil {
		out.Tags = []string{}
	}
	if out.MetaTags == nil {
		out.MetaTags = []string{}
	}
	return out
}

const subjectTorrentLimit = 100

// GET /api/v1/subjects/{provider}/{id}
func (s *Server) getSubject(w http.ResponseWriter, r *http.Request) error {
	work, err := s.resolveSubject(r)
	if err != nil {
		return err
	}
	// torrents with a file mapped to this work, from the local catalogue (newest first)
	rows, err := s.Meta.TorrentsByWork(r.Context(), work.Key, subjectTorrentLimit, true)
	if err != nil {
		return err
	}
	httpx.JSON(w, 200, map[string]any{"subject": s.subjectView(r, work), "torrents": s.listTorrents(r, catalog.FromCatalogList(rows))})
	return nil
}

// GET /api/v1/subjects/{provider}/{id}/cover
func (s *Server) getSubjectCover(w http.ResponseWriter, r *http.Request) error {
	work, err := s.resolveSubject(r)
	if err != nil {
		return err
	}
	coverPath := s.Meta.CoverPath(work.Key)
	if coverPath == "" {
		return httpx.E(404, "not_found", "封面不存在")
	}
	if served, err := serveCachedCover(w, r, coverPath); served || err != nil {
		return err
	}
	_, err, _ = s.coverSF.Do(work.Key, func() (any, error) {
		if info, statErr := os.Stat(coverPath); statErr == nil && info.Size() > 0 {
			return nil, nil
		}
		urls := s.Cat.UpstreamCoverURLs(work)
		if len(urls) == 0 {
			return nil, httpx.E(404, "not_found", "封面不存在")
		}
		baseCtx := s.BaseCtx
		if baseCtx == nil {
			baseCtx = context.Background()
		}
		ua := s.St.Str(store.KeyBgmUserAgent)
		hc := s.Cat.HTTPClient()
		coverDir := s.Meta.CoverDir()
		for _, u := range urls {
			fctx, cancel := context.WithTimeout(baseCtx, 20*time.Second)
			req, reqErr := http.NewRequestWithContext(fctx, http.MethodGet, u, nil)
			if reqErr != nil {
				cancel()
				continue
			}
			if (work.Cover == "" && work.External["bgm"] != "") || strings.Contains(u, "bgm.tv") {
				req.Header.Set("User-Agent", ua)
			}
			resp, doErr := hc.Do(req)
			if doErr != nil {
				cancel()
				continue
			}
			data, readErr := io.ReadAll(io.LimitReader(resp.Body, 10<<20))
			resp.Body.Close()
			cancel()
			if readErr != nil || resp.StatusCode != 200 || len(data) == 0 {
				continue
			}
			ct := http.DetectContentType(data)
			if !strings.HasPrefix(ct, "image/") {
				continue
			}
			if err := os.MkdirAll(coverDir, 0o755); err != nil {
				continue
			}
			tmp, err := os.CreateTemp(coverDir, ".cover-*")
			if err != nil {
				continue
			}
			tmpName := tmp.Name()
			_, werr := tmp.Write(data)
			cerr := tmp.Close()
			if werr != nil || cerr != nil {
				_ = os.Remove(tmpName)
				continue
			}
			if err := os.Rename(tmpName, coverPath); err != nil {
				_ = os.Remove(tmpName)
				continue
			}
			if work.Cover == "" && resp.Request != nil && resp.Request.URL != nil && resp.Request.URL.Scheme == "https" {
				_ = s.Meta.SetCover(baseCtx, work.Key, resp.Request.URL.String())
			}
			return nil, nil
		}
		return nil, httpx.E(404, "not_found", "封面不存在")
	})
	if err != nil {
		return err
	}
	if served, err := serveCachedCover(w, r, coverPath); served || err != nil {
		return err
	}
	return httpx.E(404, "not_found", "封面不存在")
}

func serveCachedCover(w http.ResponseWriter, r *http.Request, coverPath string) (bool, error) {
	f, err := os.Open(coverPath)
	if err != nil {
		return false, nil
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || info.Size() <= 0 {
		return false, nil
	}
	var head [512]byte
	n, _ := io.ReadFull(f, head[:])
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return false, err
	}
	w.Header().Set("Content-Type", http.DetectContentType(head[:n]))
	w.Header().Set("Cache-Control", "public, max-age=604800")
	http.ServeContent(w, r, "", info.ModTime(), f)
	return true, nil
}

type episodeFile struct {
	meta.EpisodeFile
	Access policy.Access   `json:"access"`
	Swarm  *meta.SwarmView `json:"swarm"`
}

// episodeItem is one entry of the episode list (选集).
type episodeItem struct {
	Key      string  `json:"key"`
	Kind     string  `json:"kind"` // main|sp|ncop|nced|pv|cm
	Number   float64 `json:"number"`
	Label    string  `json:"label"`
	Sources  int     `json:"sources"`         // mapped files
	Aired    *bool   `json:"aired,omitempty"` // main episodes only
	Title    string  `json:"title,omitempty"` // official episode title (Chinese if known)
	TitleJA  string  `json:"title_ja,omitempty"`
	Airdate  string  `json:"airdate,omitempty"`   // YYYY-MM-DD (official)
	Official bool    `json:"official"`            // listed by bangumi
	BgmEpID  int64   `json:"bgm_ep_id,omitempty"` // bangumi episode id (https://bgm.tv/ep/{id})
}

// maxListedEpisodes bounds the 1..n range of main episodes in the list.
const maxListedEpisodes = 2000

// episodeList merges official episodes (bangumi Archive; may be empty), the aired main
// episodes (1..max(episodes_aired, highest mapped integer episode) when there is no official
// main episode list) and every episode key that has mapped files.
//
// today is the current date in JST (bangumi air dates are Japanese dates). An official
// episode dated today has aired only once its broadcast time has passed: todayAired is that
// answer from the broadcast schedule (nil = unknown → the date alone decides).
func episodeList(aired int, official []meta.OfficialEpisode, mapped []meta.EpisodeCount, today string, todayAired *bool) []episodeItem {
	counts := map[meta.EpisodeKey]int{}
	titles := map[meta.EpisodeKey]string{}
	off := map[meta.EpisodeKey]meta.OfficialEpisode{}
	hasMain := false
	for _, o := range official {
		off[o.Key] = o
		hasMain = hasMain || o.Key.Kind == meta.KeyMain
	}
	top := aired
	for _, m := range mapped {
		counts[m.Key] = m.Files
		if m.Title != "" {
			titles[m.Key] = m.Title
		}
		if m.Key.Kind == meta.KeyMain && m.Key.Number == math.Trunc(m.Key.Number) && int(m.Key.Number) > top {
			top = int(m.Key.Number)
		}
	}
	top = min(top, maxListedEpisodes)
	keys := make([]meta.EpisodeKey, 0, top+len(mapped)+len(official))
	seen := map[meta.EpisodeKey]bool{}
	add := func(k meta.EpisodeKey) {
		if !seen[k] {
			keys, seen[k] = append(keys, k), true
		}
	}
	for _, o := range official {
		add(o.Key)
	}
	if !hasMain {
		for n := 1; n <= top; n++ {
			add(meta.EpisodeKey{Kind: meta.KeyMain, Number: float64(n)})
		}
	}
	for _, m := range mapped {
		add(m.Key)
	}
	sort.SliceStable(keys, func(i, j int) bool { return meta.LessKey(keys[i], keys[j]) })
	out := make([]episodeItem, len(keys))
	for i, k := range keys {
		it := episodeItem{Key: k.String(), Kind: k.Kind, Number: k.Number, Label: k.Label(), Sources: counts[k]}
		o, isOff := off[k]
		if isOff {
			it.Official, it.Airdate, it.BgmEpID = true, o.Airdate, o.BgmID
			it.Title, it.TitleJA = o.NameCN, o.Name
			if it.Title == "" {
				it.Title, it.TitleJA = o.Name, ""
			}
		}
		if it.Title == "" && k.Kind != meta.KeyMain {
			it.Title = titles[k]
		}
		if k.Kind == meta.KeyMain {
			var a bool
			switch {
			case isOff && o.Airdate != "" && o.Airdate == today && todayAired != nil:
				a = *todayAired
			case isOff && o.Airdate != "":
				a = o.Airdate <= today
			case hasMain && isOff:
				a = aired > 0 // undated official episode: aired if the work has started
			default:
				a = k.Number >= 1 && k.Number <= float64(aired)
			}
			if counts[k] > 0 {
				a = true // resources exist, so it has aired
			}
			it.Aired = &a
		}
		out[i] = it
	}
	return out
}

// GET /api/v1/subjects/{provider}/{id}/episodes — the episode list (选集): aired main
// episodes plus every episode key with mapped files, main episodes first, then specials.
func (s *Server) getEpisodes(w http.ResponseWriter, r *http.Request) error {
	work, err := s.resolveSubject(r)
	if err != nil {
		return err
	}
	// sources = files the user may play (others are not listed on the episode page either)
	mapped, err := s.Meta.EpisodeKeysWhere(r.Context(), work.Key, s.fileFilter(r))
	if err != nil {
		return err
	}
	official, err := s.Meta.OfficialEpisodes(r.Context(), work.Key)
	if err != nil {
		return err
	}
	now := time.Now()
	aired := 0
	sched := s.Cat.ScheduleItemByKey(work.Key)
	if sched != nil {
		aired = sched.EpisodesAired
	} else if n, ok := s.Meta.AiredOfficial(r.Context(), work.Key, now); ok {
		aired = n
	} else if item, _ := s.Cat.ItemFor(work, now); item != nil {
		aired = item.EpisodesAired
	}
	jst := time.FixedZone("JST", 9*3600)
	today := now.In(jst).Format("2006-01-02")
	httpx.JSON(w, 200, map[string]any{"items": episodeList(aired, official, mapped, today, airedToday(sched, today, jst))})
	return nil
}

// GET /api/v1/subjects/{provider}/{id}/episodes/{n}
// {n} is an episode key ("12", "12.5", "0", "sp1", "ncop1", …; see meta.ParseEpisodeKey).
// Lists every known file of that episode the user may play (admins: all), newest first,
// with per-user access and swarm
// stats. The browser ranks them (rank rules from GET /site, web/src/lib/rank.ts) and
// auto-plays the best one.
func (s *Server) getEpisode(w http.ResponseWriter, r *http.Request) error {
	work, err := s.resolveSubject(r)
	if err != nil {
		return err
	}
	key, ok := meta.ParseEpisodeKey(r.PathValue("n"))
	if !ok {
		return httpx.E(400, "bad_request", "无效集数")
	}
	files, err := s.Meta.EpisodeFiles(r.Context(), work.Key, key)
	if err != nil {
		return err
	}
	// files of torrents the user may not play are not listed (listing.go)
	dc := s.decider(r)
	items := make([]episodeFile, 0, len(files))
	ihs := make([]string, 0, len(files))
	for _, f := range files {
		d, ok := dc.decide(episodeSubject(f))
		if !ok {
			continue
		}
		items = append(items, episodeFile{EpisodeFile: f, Access: d.Access()})
		ihs = append(ihs, f.Infohash)
	}
	views := s.swarmFor(r.Context(), ihs)
	for i := range items {
		items[i].Swarm = views[items[i].Infohash]
	}
	out := map[string]any{"subject": s.subjectView(r, work), "key": key.String(), "kind": key.Kind, "number": key.Number, "label": key.Label(), "items": items}
	if key.Kind == meta.KeyMain {
		out["episode"] = key.Number
	}
	httpx.JSON(w, 200, out)
	return nil
}

// airedToday answers "has today's broadcast already happened?" from the schedule's exact
// broadcast times (bangumi-data): the last broadcast fell on today (JST) → yes; the next one
// is today → not yet; otherwise (or no schedule entry) unknown.
func airedToday(it *catalog.ScheduleItem, today string, jst *time.Location) *bool {
	if it == nil {
		return nil
	}
	day := func(ts *string) string {
		if ts == nil {
			return ""
		}
		t, err := time.Parse(time.RFC3339, *ts)
		if err != nil {
			return ""
		}
		return t.In(jst).Format("2006-01-02")
	}
	yes, no := true, false
	switch {
	case day(it.LastAirAt) == today:
		return &yes
	case day(it.NextAirAt) == today:
		return &no
	}
	return nil
}
