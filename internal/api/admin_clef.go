package api

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/darknightlab/onlinetorrent/internal/clef"
	"github.com/darknightlab/onlinetorrent/internal/httpx"
	"github.com/darknightlab/onlinetorrent/internal/meta"
	"github.com/darknightlab/onlinetorrent/internal/store"
)

// Decision model (Clef) admin: status + queue, dry run, decisions (audit / review queue).

func (s *Server) adminClefRoutes(mux *http.ServeMux) {
	adm := func(method, path, label string, h handler) {
		s.route(mux, method, path, AccessAdmin, "后台", label, nil, h)
	}
	adm("GET", "/api/v1/admin/clef", "决策模型 · 查看", s.adminClefStatus)
	adm("POST", "/api/v1/admin/clef/test", "决策模型 · 测试", s.adminClefTest)
	adm("GET", "/api/v1/admin/clef/decisions", "决策模型 · 查看", s.adminClefDecisions)
	adm("POST", "/api/v1/admin/clef/decisions/id/{id}/accept", "决策模型 · 审核", s.adminClefAccept)
	adm("POST", "/api/v1/admin/clef/decisions/id/{id}/reject", "决策模型 · 审核", s.adminClefReject)
	adm("POST", "/api/v1/admin/clef/jobs/hash/{ih}/run", "决策模型 · 测试", s.adminClefRequeue)
}

func (s *Server) clefSvc() (*clef.Service, error) {
	if s.Clef == nil {
		return nil, httpx.E(503, "unavailable", "决策模型未启用")
	}
	return s.Clef, nil
}

// GET /api/v1/admin/clef
func (s *Server) adminClefStatus(w http.ResponseWriter, r *http.Request) error {
	c, err := s.clefSvc()
	if err != nil {
		return err
	}
	v, err := c.Status(r.Context())
	if err != nil {
		return err
	}
	httpx.JSON(w, 200, v)
	return nil
}

func validIH(ih string) (string, bool) {
	ih = strings.ToLower(strings.TrimSpace(ih))
	if len(ih) != 40 {
		return "", false
	}
	for _, c := range ih {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return "", false
		}
	}
	return ih, true
}

// POST /api/v1/admin/clef/test {infohash} → dry run (nothing written; counts toward the
// daily cap): what the rules do now, the candidates, the requests, answers and decisions.
func (s *Server) adminClefTest(w http.ResponseWriter, r *http.Request) error {
	c, err := s.clefSvc()
	if err != nil {
		return err
	}
	var in struct {
		Infohash string `json:"infohash"`
	}
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	ih, ok := validIH(in.Infohash)
	if !ok {
		return httpx.E(400, "bad_request", "infohash 无效（40 位十六进制）")
	}
	_, rules, err := s.Meta.RuleMapping(r.Context(), ih)
	if errors.Is(err, store.ErrNotFound) {
		return httpx.E(404, "not_found", "本地库中没有这个种子")
	}
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(r.Context(), 50*time.Second)
	defer cancel()
	out, err := c.Decide(ctx, c.Config(), ih, true)
	resp := map[string]any{"rules": rules, "outcome": out}
	if err != nil {
		resp["error"] = err.Error()
		if errors.Is(err, clef.ErrNotConfigured) {
			return httpx.E(400, "not_configured", err.Error())
		}
	}
	s.audit(r, "clef.test", ih, "")
	httpx.JSON(w, 200, resp)
	return nil
}

// GET /api/v1/admin/clef/decisions?status=&ih=&page=
func (s *Server) adminClefDecisions(w http.ResponseWriter, r *http.Request) error {
	status := r.URL.Query().Get("status")
	switch status {
	case "", clef.StatusApplied, clef.StatusPending, clef.StatusRejected, clef.StatusDiscarded:
	default:
		return httpx.E(400, "bad_request", "status 无效")
	}
	ih := ""
	if q := r.URL.Query().Get("ih"); q != "" {
		v, ok := validIH(q)
		if !ok {
			return httpx.E(400, "bad_request", "infohash 无效")
		}
		ih = v
	}
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	page = max(page, 1)
	const size = 30
	items, total, err := s.Meta.ListClefDecisions(r.Context(), status, ih, (page-1)*size, size)
	if err != nil {
		return err
	}
	paged(w, items, page, total, size)
	return nil
}

func (s *Server) clefDecide(w http.ResponseWriter, r *http.Request, accept bool) error {
	c, err := s.clefSvc()
	if err != nil {
		return err
	}
	by := "admin"
	if u := userOf(r); u != nil {
		by = u.Username
	}
	var d *meta.ClefDecision
	if accept {
		d, err = c.Accept(r.Context(), pathID(r), by)
	} else {
		d, err = c.Reject(r.Context(), pathID(r), by)
	}
	switch {
	case errors.Is(err, store.ErrNotFound):
		return httpx.E(404, "not_found", "决策不存在")
	case errors.Is(err, clef.ErrState), errors.Is(err, meta.ErrClefConflict):
		return httpx.E(409, "conflict", err.Error())
	case err != nil:
		return err
	}
	action := "clef.reject"
	if accept {
		action = "clef.accept"
	}
	s.audit(r, action, d.Infohash+"/"+strconv.Itoa(d.FileIndex), d.WorkKey+" "+d.EpisodeKey)
	httpx.JSON(w, 200, d)
	return nil
}

// POST …/decisions/id/{id}/accept — apply a pending decision
func (s *Server) adminClefAccept(w http.ResponseWriter, r *http.Request) error {
	return s.clefDecide(w, r, true)
}

// POST …/decisions/id/{id}/reject — reject a pending decision / revert an applied one
func (s *Server) adminClefReject(w http.ResponseWriter, r *http.Request) error {
	return s.clefDecide(w, r, false)
}

// POST /api/v1/admin/clef/jobs/hash/{ih}/run — (re)queue a torrent now
func (s *Server) adminClefRequeue(w http.ResponseWriter, r *http.Request) error {
	c, err := s.clefSvc()
	if err != nil {
		return err
	}
	ih, ok := validIH(r.PathValue("ih"))
	if !ok {
		return httpx.E(400, "bad_request", "infohash 无效")
	}
	if _, known := s.Meta.KnownTorrent(r.Context(), ih); !known {
		return httpx.E(404, "not_found", "本地库中没有这个种子")
	}
	if err := s.Meta.RequeueClef(r.Context(), ih); err != nil {
		return err
	}
	c.Wake()
	s.audit(r, "clef.requeue", ih, "")
	v, err := c.Status(r.Context())
	if err != nil {
		return err
	}
	httpx.JSON(w, 202, v)
	return nil
}
