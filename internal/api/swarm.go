package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/darknightlab/onlinetorrent/internal/catalog"
	"github.com/darknightlab/onlinetorrent/internal/httpx"
	"github.com/darknightlab/onlinetorrent/internal/meta"
	"github.com/darknightlab/onlinetorrent/internal/rank"
	"github.com/darknightlab/onlinetorrent/internal/store"
	"github.com/darknightlab/onlinetorrent/internal/swarm"
)

// swarmBudget bounds how long a listing waits for an on-demand scrape of stale torrents.
const swarmBudget = 1500 * time.Millisecond

// swarmFor returns the swarm view of each infohash (absent = null: never seen by any
// tracker / feed and not on a qB server).
func (s *Server) swarmFor(ctx context.Context, ihs []string) map[string]*meta.SwarmView {
	out := map[string]*meta.SwarmView{}
	if len(ihs) == 0 {
		return out
	}
	var rows map[string]*meta.SwarmStats
	if s.Swarm != nil {
		rows = s.Swarm.Lookup(ctx, ihs, swarmBudget)
	} else {
		rows, _ = s.Meta.SwarmStatsMany(ctx, ihs)
	}
	for ih, st := range rows {
		if v := st.View(); v != nil && (v.CheckedAt != nil || v.ServerComplete) {
			out[ih] = v
		}
	}
	return out
}

// attachSwarm sets the swarm field (and the legacy seeders / leechers = BT counts).
func (s *Server) attachSwarm(ctx context.Context, ts []catalog.Torrent) {
	ihs := make([]string, len(ts))
	for i := range ts {
		ihs[i] = ts[i].Infohash
	}
	views := s.swarmFor(ctx, ihs)
	for i := range ts {
		if v := views[ts[i].Infohash]; v != nil {
			ts[i].Swarm = v
			if v.BTSeeders != nil {
				ts[i].Seeders = *v.BTSeeders
			}
			if v.BTLeechers != nil {
				ts[i].Leechers = *v.BTLeechers
			}
		}
	}
}

type swarmAdminResp struct {
	Items []swarm.Health      `json:"items"`
	Swarm swarm.Info          `json:"swarm"`
	Stats *meta.SwarmCounts   `json:"stats"`
	Lists map[string][]string `json:"lists"`
}

func (s *Server) swarmAdmin(ctx context.Context, items []swarm.Health) swarmAdminResp {
	out := swarmAdminResp{Items: items, Lists: map[string][]string{
		"webtorrent": nonNil(s.St.List(store.KeyTrackers)), "bt": nonNil(s.St.List(store.KeyBTTrackers))}}
	if out.Items == nil {
		out.Items = []swarm.Health{}
	}
	if s.Swarm != nil {
		out.Swarm = s.Swarm.Status()
	}
	if c, err := s.Meta.SwarmSummary(ctx); err == nil {
		out.Stats = &c
	}
	return out
}

// GET /api/v1/admin/trackers — tracker state as of its last real use (passive; no probes)
// + on-demand scraping counters.
func (s *Server) adminTrackers(w http.ResponseWriter, r *http.Request) error {
	var items []swarm.Health
	if s.Swarm != nil {
		items = s.Swarm.Health()
	}
	httpx.JSON(w, 200, s.swarmAdmin(r.Context(), items))
	return nil
}

type rankRulesResp struct {
	Rules    json.RawMessage `json:"rules"`
	Defaults rank.Rules      `json:"defaults"`
}

// GET /api/v1/admin/rank-rules
func (s *Server) adminGetRankRules(w http.ResponseWriter, r *http.Request) error {
	httpx.JSON(w, 200, rankRulesResp{Rules: s.rankRules(), Defaults: rank.Default()})
	return nil
}

// PUT /api/v1/admin/rank-rules — body: the rules object (validated, 400 on errors).
func (s *Server) adminPutRankRules(w http.ResponseWriter, r *http.Request) error {
	var raw json.RawMessage
	if err := httpx.Decode(r, &raw); err != nil {
		return err
	}
	stored, err := s.St.SetJSON(r.Context(), store.KeyRankRules, raw)
	if err != nil {
		var he *httpx.Error
		if errors.As(err, &he) {
			return err
		}
		return httpx.E(400, "bad_request", err.Error())
	}
	s.audit(r, "rank_rules.update", "", "")
	httpx.JSON(w, 200, rankRulesResp{Rules: stored, Defaults: rank.Default()})
	return nil
}

// rankRules is the stored rule set, normalised (all fields present; defaults when unset
// or invalid).
func (s *Server) rankRules() json.RawMessage {
	r, err := rank.Parse(s.St.Raw(store.KeyRankRules))
	if err != nil {
		return rank.DefaultJSON()
	}
	b, _ := json.Marshal(r)
	return b
}
