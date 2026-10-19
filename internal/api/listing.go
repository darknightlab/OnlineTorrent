package api

import (
	"net/http"
	"strings"

	"github.com/darknightlab/onlinetorrent/internal/catalog"
	"github.com/darknightlab/onlinetorrent/internal/meta"
	"github.com/darknightlab/onlinetorrent/internal/policy"
	"github.com/darknightlab/onlinetorrent/internal/store"
)

// Torrents the content policy doesn't let the current user play are not listed at all
// (episode items, subject torrent lists, search, episode source counts, /meta/v1 episode
// files). That includes the content scope: a torrent with any applied file mapping to a
// subject hidden for the user's role is not playable (playDecision) and not listed. Admins
// see everything, with access attached. Opening a torrent directly (play, .torrent,
// webseed, seed) is checked by playDecision as well.

// decider evaluates the policy for one request, memoised per (infohash, files).
type decider struct {
	s      *Server
	r      *http.Request
	u      *store.User
	admin  bool
	memo   map[string]policy.Decision
	scope  *store.ScopePlan
	hidden map[string]bool // infohash → hidden by the content scope (filled lazily / prefetch)
}

func (s *Server) decider(r *http.Request) *decider {
	u := userOf(r)
	return &decider{s: s, r: r, u: u, admin: u != nil && u.Role == store.RoleAdmin, memo: map[string]policy.Decision{},
		scope: s.scopeFor(roleOf(u)), hidden: map[string]bool{}}
}

// prefetch looks the content scope up for many torrents at once.
func (d *decider) prefetch(ihs []string) {
	if !d.scope.Restricted() {
		return
	}
	var todo []string
	for _, ih := range ihs {
		if _, ok := d.hidden[ih]; !ok {
			todo = append(todo, ih)
		}
	}
	h, err := d.s.Meta.HiddenByScope(d.r.Context(), todo, d.scope)
	for _, ih := range todo {
		d.hidden[ih] = err != nil || h[ih] // fail closed
	}
}

func (d *decider) hiddenByScope(ih string) bool {
	if !d.scope.Restricted() || ih == "" {
		return false
	}
	if _, ok := d.hidden[ih]; !ok {
		d.prefetch([]string{ih})
	}
	return d.hidden[ih]
}

// decide returns the decision; ok = the subject may be listed for this user. Policy errors
// fail closed (not listed) except for admins.
func (d *decider) decide(sub policy.Subject) (policy.Decision, bool) {
	key := sub.Infohash + "\x00" + strings.Join(sub.Files, "\x00")
	dec, seen := d.memo[key]
	if !seen {
		var err error
		dec, err = d.s.Policy.Decide(d.r.Context(), d.u, sub)
		if err != nil {
			d.s.Log.Warn("policy decide", "ih", sub.Infohash, "err", err)
			dec = policy.Decision{Action: policy.Block}
		}
		if dec.Allowed && d.hiddenByScope(sub.Infohash) {
			dec = d.s.scopeDenial(d.r.Context(), d.u, sub.Infohash)
		}
		d.memo[key] = dec
	}
	return dec, d.admin || dec.Allowed
}

func episodeSubject(f meta.EpisodeFile) policy.Subject {
	return policy.Subject{Infohash: f.Infohash, Title: f.Title, Files: []string{f.Path}, Team: f.Team}
}

// fileFilter is the per-request filter for episode files (nil for admins: no filtering).
func (s *Server) fileFilter(r *http.Request) func(meta.EpisodeFile) bool {
	d := s.decider(r)
	if d.admin {
		return nil
	}
	return func(f meta.EpisodeFile) bool {
		_, ok := d.decide(episodeSubject(f))
		return ok
	}
}

// listTorrents attaches access and swarm stats and drops torrents the user may not play.
func (s *Server) listTorrents(r *http.Request, ts []*catalog.Torrent) []catalog.Torrent {
	out := s.listTorrentsNoSwarm(r, ts)
	s.attachSwarm(r.Context(), out)
	return out
}

// listTorrentsNoSwarm is listTorrents without swarm stats (for paginating first).
func (s *Server) listTorrentsNoSwarm(r *http.Request, ts []*catalog.Torrent) []catalog.Torrent {
	d := s.decider(r)
	ihs := make([]string, len(ts))
	for i, t := range ts {
		ihs[i] = t.Infohash
	}
	d.prefetch(ihs)
	out := make([]catalog.Torrent, 0, len(ts))
	for _, t := range ts {
		c := *t
		dec, ok := d.decide(c.Subject())
		if !ok {
			continue
		}
		a := dec.Access()
		c.Access = &a
		out = append(out, c)
	}
	return out
}
