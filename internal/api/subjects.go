package api

import (
	"errors"
	"github.com/darknightlab/onlinetorrent/internal/rank"
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
	MetaTags []string          `json:"meta_tags"`
	NSFW     bool              `json:"nsfw"`
	Score    float64           `json:"score,omitempty"`
	Rank     int               `json:"rank,omitempty"`
	Eps      int               `json:"eps,omitempty"` // planned episode count (bgm 话数)
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
	if item == nil {
		item, _ = s.Cat.ItemFor(work, time.Now())
		// not on the current schedule: official air dates are better than the weekly estimate
		if item != nil && detail {
			if n, ok := s.Meta.AiredOfficial(r.Context(), work.Key, time.Now()); ok {
				item.EpisodesAired = n
			}
		}
	}
	if item == nil {
		item = &catalog.ScheduleItem{Key: work.Key, SubjectID: work.ID, Title: work.Title, TitleCN: work.TitleCN, Cover: s.Cat.CoverFor(work)}
	}
	out := subjectOut{ScheduleItem: item, Summary: work.Summary, Type: work.Type, Official: work.Official, External: work.External,
		Tags: work.Tags, MetaTags: work.MetaTags, NSFW: work.NSFW, Score: work.Score, Rank: work.Rank, Eps: work.Eps}
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
	Key      string  `json:"key"`
	Kind     string  `json:"kind"` // main|sp|ncop|nced|pv|cm
	Number   float64 `json:"number"`
	Label    string  `json:"label"`
	Sources  int     `json:"sources"`         // mapped files
	Aired    *bool   `json:"aired,omitempty"` // main episodes only
	Title    string  `json:"title,omitempty"` // official episode title (Chinese if known)
	TitleJA  string  `json:"title_ja,omitempty"`
	Airdate  string  `json:"airdate,omitempty"` // YYYY-MM-DD (official)
	Official bool    `json:"official"`          // listed by bangumi
}

// maxListedEpisodes bounds the 1..n range of main episodes in the list.
const maxListedEpisodes = 2000

// episodeList merges official episodes (bangumi Archive; may be empty), the aired main
// episodes (1..max(episodes_aired, highest mapped integer episode) when there is no official
// main episode list) and every episode key that has mapped files.
func episodeList(aired int, official []meta.OfficialEpisode, mapped []meta.EpisodeCount, today string) []episodeItem {
	counts := map[meta.EpisodeKey]int{}
	off := map[meta.EpisodeKey]meta.OfficialEpisode{}
	hasMain := false
	for _, o := range official {
		off[o.Key] = o
		hasMain = hasMain || o.Key.Kind == meta.KeyMain
	}
	top := aired
	for _, m := range mapped {
		counts[m.Key] = m.Files
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
			it.Official, it.Airdate = true, o.Airdate
			it.Title, it.TitleJA = o.NameCN, o.Name
			if it.Title == "" {
				it.Title, it.TitleJA = o.Name, ""
			}
		}
		if k.Kind == meta.KeyMain {
			var a bool
			switch {
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
	mapped, err := s.Meta.EpisodeKeys(r.Context(), work.Key)
	if err != nil {
		return err
	}
	official, err := s.Meta.OfficialEpisodes(r.Context(), work.Key)
	if err != nil {
		return err
	}
	now := time.Now()
	aired := 0
	if item := s.Cat.ScheduleItemByKey(work.Key); item != nil {
		aired = item.EpisodesAired
	} else if n, ok := s.Meta.AiredOfficial(r.Context(), work.Key, now); ok {
		aired = n
	} else if item, _ := s.Cat.ItemFor(work, now); item != nil {
		aired = item.EpisodesAired
	}
	today := now.In(time.FixedZone("JST", 9*3600)).Format("2006-01-02")
	httpx.JSON(w, 200, map[string]any{"items": episodeList(aired, official, mapped, today)})
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
	list := make([]episodeFile, 0, len(files))
	cs := make([]rank.Candidate, 0, len(files))
	for _, f := range files {
		sub := policy.Subject{Infohash: f.Infohash, Title: f.Title, Files: []string{f.Path}, Team: f.Team, Trusted: f.Trusted}
		d, err := s.Policy.Decide(r.Context(), u, sub)
		if err != nil {
			return err
		}
		list = append(list, episodeFile{f, d.Access()})
		cs = append(cs, rank.Candidate{Name: f.Title + " " + f.Path, Size: f.Size, PublishedAt: f.PublishedAt, Allowed: d.Allowed})
	}
	// same ranking as the schedule card (internal/rank): items[0] is the auto-play pick
	items := make([]episodeFile, len(list))
	for i, j := range rank.Order(cs, maxBytes) {
		items[i] = list[j]
	}
	out := map[string]any{"subject": s.subjectView(r, work), "key": key.String(), "kind": key.Kind, "number": key.Number, "label": key.Label(), "items": items}
	if key.Kind == meta.KeyMain {
		out["episode"] = key.Number
	}
	httpx.JSON(w, 200, out)
	return nil
}
