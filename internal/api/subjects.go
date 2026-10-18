package api

import (
	"errors"
	"net/http"
	"regexp"
	"sort"
	"strconv"
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
	if work.Summary == "" && work.External["bgm"] != "" {
		s.fetchBgmDetail(r.Context(), work)
	}
	item, _ := s.Cat.ItemFor(work, time.Now())
	if item == nil {
		item = &catalog.ScheduleItem{Key: work.Key, SubjectID: work.ID, Title: work.Title, TitleCN: work.TitleCN, Cover: s.Cat.CoverFor(work)}
	}
	return subjectOut{ScheduleItem: item, Summary: work.Summary, Type: work.Type, Official: work.Official, External: work.External, Tags: []string{}}
}

// GET /api/v1/subjects/{provider}/{id}
func (s *Server) getSubject(w http.ResponseWriter, r *http.Request) error {
	work, err := s.resolveSubject(r)
	if err != nil {
		return err
	}
	var list []*catalog.Torrent
	if id := work.External["bangumi_moe"]; id != "" {
		list, _, err = s.Cat.ByTag(r.Context(), id, 1)
	} else {
		q := work.TitleCN
		if q == "" {
			q = work.Title
		}
		list, _, err = s.Cat.Search(r.Context(), q, 1)
	}
	if err != nil {
		list = nil
	}
	httpx.JSON(w, 200, map[string]any{"subject": s.subjectView(r, work), "torrents": s.withAccess(r, list)})
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

// GET /api/v1/subjects/{provider}/{id}/episodes/{n}
// Lists every known file of that episode with per-user access; items[0] is the
// automatic pick (playable for this user, browser friendly codec, sensible size).
func (s *Server) getEpisode(w http.ResponseWriter, r *http.Request) error {
	work, err := s.resolveSubject(r)
	if err != nil {
		return err
	}
	ep, err := strconv.ParseFloat(r.PathValue("n"), 64)
	if err != nil || ep < 0 || ep > 5000 {
		return httpx.E(400, "bad_request", "无效集数")
	}
	files, err := s.Meta.EpisodeFiles(r.Context(), work.Key, ep)
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
		sub := policy.Subject{Infohash: f.Infohash, Title: f.Title, Files: []string{f.Path}, Team: f.Team, Trusted: f.SourceID != ""}
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
	httpx.JSON(w, 200, map[string]any{"subject": s.subjectView(r, work), "episode": ep, "items": items})
	return nil
}
