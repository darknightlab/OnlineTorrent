package api

import (
	"errors"
	"math"
	"net/http"
	"regexp"
	"sort"
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
}

func (s *Server) resolveSubject(r *http.Request) (*meta.Work, error) {
	work, err := s.Meta.ResolveSubject(r.Context(), r.PathValue("provider"), r.PathValue("id"))
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
	if item == nil {
		item, _ = s.Cat.ItemFor(work, time.Now())
	}
	if item == nil {
		item = &catalog.ScheduleItem{Key: work.Key, SubjectID: work.ID, Title: work.Title, TitleCN: work.TitleCN, Cover: s.Cat.CoverFor(work)}
	}
	if t := item.LatestTorrent; t != nil {
		lt := *t
		if d, err := s.Policy.Decide(r.Context(), userOf(r), lt.Subject()); err == nil {
			a := d.Access()
			lt.Access = &a
		}
		item.LatestTorrent = &lt
	}
	return subjectOut{ScheduleItem: item, Summary: work.Summary, Type: work.Type, Official: work.Official, External: work.External, Tags: []string{}}
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
	httpx.JSON(w, 200, map[string]any{"subject": s.subjectView(r, work), "torrents": s.withAccess(r, catalog.FromCatalogList(rows))})
	return nil
}

type episodeFile struct {
	meta.EpisodeFile
	Access policy.Access `json:"access"`
}

var (
	reEpHEVC = regexp.MustCompile(`(?i)hevc|x\.?265|h\.?265|10-?bit|ma10p`)
	reEpAVC  = regexp.MustCompile(`(?i)\bavc\b|x\.?264|h\.?264`)
	reEpHi   = regexp.MustCompile(`(?i)1080|1920`)
	reEpCHS  = regexp.MustCompile(`(?i)简|chs|\bgb\b|\bsc\b`)
)

// episodeItem is one entry of the episode list (选集).
type episodeItem struct {
	Key     string  `json:"key"`
	Kind    string  `json:"kind"` // main|sp|ncop|nced|pv|cm
	Number  float64 `json:"number"`
	Label   string  `json:"label"`
	Sources int     `json:"sources"`         // mapped files
	Aired   *bool   `json:"aired,omitempty"` // main episodes only: within the aired count of the schedule
}

// maxListedEpisodes bounds the 1..n range of main episodes in the list.
const maxListedEpisodes = 2000

// episodeList merges the aired main episodes (1..max(episodes_aired, highest mapped integer
// episode)) with every episode key that has mapped files (decimals, 0, specials).
func episodeList(aired int, mapped []meta.EpisodeCount) []episodeItem {
	counts := map[meta.EpisodeKey]int{}
	top := aired
	for _, m := range mapped {
		counts[m.Key] = m.Files
		if m.Key.Kind == meta.KeyMain && m.Key.Number == math.Trunc(m.Key.Number) && int(m.Key.Number) > top {
			top = int(m.Key.Number)
		}
	}
	top = min(top, maxListedEpisodes)
	keys := make([]meta.EpisodeKey, 0, top+len(mapped))
	seen := map[meta.EpisodeKey]bool{}
	for n := 1; n <= top; n++ {
		k := meta.EpisodeKey{Kind: meta.KeyMain, Number: float64(n)}
		keys, seen[k] = append(keys, k), true
	}
	for _, m := range mapped {
		if !seen[m.Key] {
			keys, seen[m.Key] = append(keys, m.Key), true
		}
	}
	sort.SliceStable(keys, func(i, j int) bool { return meta.LessKey(keys[i], keys[j]) })
	out := make([]episodeItem, len(keys))
	for i, k := range keys {
		it := episodeItem{Key: k.String(), Kind: k.Kind, Number: k.Number, Label: k.Label(), Sources: counts[k]}
		if k.Kind == meta.KeyMain {
			a := k.Number >= 1 && k.Number <= float64(aired)
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
	mapped, err := s.Meta.EpisodeKeys(r.Context(), work.Key)
	if err != nil {
		return err
	}
	aired := 0
	if item := s.Cat.ScheduleItemByKey(work.Key); item != nil {
		aired = item.EpisodesAired
	} else if item, _ := s.Cat.ItemFor(work, time.Now()); item != nil {
		aired = item.EpisodesAired
	}
	httpx.JSON(w, 200, map[string]any{"items": episodeList(aired, mapped)})
	return nil
}

// GET /api/v1/subjects/{provider}/{id}/episodes/{n}
// {n} is an episode key ("12", "12.5", "0", "sp1", "ncop1", …; see meta.ParseEpisodeKey).
// Lists every known file of that episode with per-user access; items[0] is the
// automatic pick (playable for this user, browser friendly codec, sensible size).
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
	u := userOf(r)
	maxBytes := int64(s.St.Num(store.KeyMaxAutoSize)) << 20
	type scored struct {
		episodeFile
		score int
	}
	list := make([]scored, 0, len(files))
	for _, f := range files {
		sub := policy.Subject{Infohash: f.Infohash, Title: f.Title, Files: []string{f.Path}, Team: f.Team, Trusted: f.Trusted}
		d, err := s.Policy.Decide(r.Context(), u, sub)
		if err != nil {
			return err
		}
		sc := 0
		if d.Allowed {
			sc += 100
		}
		if maxBytes <= 0 || (f.Size > 0 && f.Size <= maxBytes) {
			sc += 8
		}
		name := f.Title + " " + f.Path
		if reEpHEVC.MatchString(name) {
			sc -= 3
		} else if reEpAVC.MatchString(name) {
			sc += 3
		}
		if reEpHi.MatchString(name) {
			sc += 2
		}
		if reEpCHS.MatchString(name) {
			sc++
		}
		list = append(list, scored{episodeFile{f, d.Access()}, sc})
	}
	sort.SliceStable(list, func(i, j int) bool {
		if list[i].score != list[j].score {
			return list[i].score > list[j].score
		}
		return list[i].PublishedAt > list[j].PublishedAt
	})
	items := make([]episodeFile, len(list))
	for i := range list {
		items[i] = list[i].episodeFile
	}
	out := map[string]any{"subject": s.subjectView(r, work), "key": key.String(), "kind": key.Kind, "number": key.Number, "label": key.Label(), "items": items}
	if key.Kind == meta.KeyMain {
		out["episode"] = key.Number
	}
	httpx.JSON(w, 200, out)
	return nil
}
