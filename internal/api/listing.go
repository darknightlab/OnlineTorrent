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
// files). Admins see everything, with access attached. Opening a torrent directly (play,
// .torrent, webseed, status by infohash) is still checked by the policy as before.

// decider evaluates the policy for one request, memoised per (infohash, files).
type decider struct {
	s     *Server
	r     *http.Request
	u     *store.User
	admin bool
	memo  map[string]policy.Decision
}

func (s *Server) decider(r *http.Request) *decider {
	u := userOf(r)
	return &decider{s: s, r: r, u: u, admin: u != nil && u.Role == store.RoleAdmin, memo: map[string]policy.Decision{}}
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
		d.memo[key] = dec
	}
	return dec, d.admin || dec.Allowed
}

func episodeSubject(f meta.EpisodeFile) policy.Subject {
	return policy.Subject{Infohash: f.Infohash, Title: f.Title, Files: []string{f.Path}, Team: f.Team, Trusted: f.Trusted}
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
