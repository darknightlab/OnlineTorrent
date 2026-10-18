package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/darknightlab/onlinetorrent/internal/crawl"
	"github.com/darknightlab/onlinetorrent/internal/httpx"
	"github.com/darknightlab/onlinetorrent/internal/store"
)

// Crawl source admin (shape mirrors YotsubaNews' /api/v1/admin/sources: list, schema,
// probe, create — which probes first and defaults the name to the feed title —, update
// (re-probes when the URL changes), plus delete, run-now and the metadata queue view).

type sourceView struct {
	store.Source
	Running bool `json:"running"`
	// next poll time (interval × failure backoff), null when disabled or never run
	NextRunAt *string `json:"next_run_at"`
}

func (s *Server) sourceView(src store.Source) sourceView {
	v := sourceView{Source: src, Running: s.Crawl.Running(src.ID)}
	if src.Enabled && src.LastRunAt != nil {
		if t, err := time.Parse(time.RFC3339, *src.LastRunAt); err == nil {
			n := t.Add(store.SourceBackoff(src)).UTC().Format(time.RFC3339)
			v.NextRunAt = &n
		}
	}
	return v
}

func (s *Server) adminListSources(w http.ResponseWriter, r *http.Request) error {
	srcs, err := s.St.ListSources(r.Context())
	if err != nil {
		return err
	}
	out := make([]sourceView, 0, len(srcs))
	for _, src := range srcs {
		out = append(out, s.sourceView(src))
	}
	list(w, out)
	return nil
}

func (s *Server) adminSourceSchema(w http.ResponseWriter, r *http.Request) error {
	httpx.JSON(w, 200, map[string]any{"kinds": crawl.Kinds(), "min_interval_min": store.MinSourceIntervalMin, "default_interval_min": store.DefaultSourceIntervalMin})
	return nil
}

func sourceError(err error) error {
	switch {
	case errors.Is(err, store.ErrNotFound):
		return httpx.E(404, "not_found", "抓取源不存在")
	case errors.Is(err, store.ErrSourceExists):
		return httpx.E(409, "source_exists", err.Error())
	case errors.Is(err, store.ErrInvalidSource):
		return httpx.Bad(err)
	}
	return err
}

func (s *Server) probe(ctx context.Context, kind, url string, proxyDisabled bool) (*crawl.Probe, error) {
	pctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	p, err := s.Crawl.ProbeSource(pctx, kind, url, proxyDisabled)
	if err != nil {
		return p, httpx.E(400, "source_probe_failed", "抓取测试失败："+err.Error())
	}
	return p, nil
}

// POST /api/v1/admin/sources/probe {kind, url, proxy_disabled} → Probe
func (s *Server) adminProbeSource(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		Kind          string `json:"kind"`
		URL           string `json:"url"`
		ProxyDisabled bool   `json:"proxy_disabled"`
	}
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	if in.Kind == "" {
		in.Kind = store.SourceKindRSS
	}
	src := store.Source{Name: "probe", Kind: in.Kind, URL: in.URL}
	if err := store.ValidateSource(&src); err != nil {
		return httpx.Bad(err)
	}
	p, err := s.probe(r.Context(), src.Kind, src.URL, in.ProxyDisabled)
	if err != nil {
		return err
	}
	httpx.JSON(w, 200, p)
	return nil
}

type sourceInput struct {
	Name          *string `json:"name"`
	Kind          *string `json:"kind"`
	URL           *string `json:"url"`
	IntervalMin   *int    `json:"interval_min"`
	Enabled       *bool   `json:"enabled"`
	ProxyDisabled *bool   `json:"proxy_disabled"`
}

func (in sourceInput) apply(src *store.Source) {
	if in.Name != nil {
		src.Name = *in.Name
	}
	if in.Kind != nil {
		src.Kind = *in.Kind
	}
	if in.URL != nil {
		src.URL = *in.URL
	}
	if in.IntervalMin != nil {
		src.IntervalMin = *in.IntervalMin
	}
	if in.Enabled != nil {
		src.Enabled = *in.Enabled
	}
	if in.ProxyDisabled != nil {
		src.ProxyDisabled = *in.ProxyDisabled
	}
}

// POST /api/v1/admin/sources {name?, kind, url, interval_min?, enabled?, proxy_disabled?}
func (s *Server) adminCreateSource(w http.ResponseWriter, r *http.Request) error {
	var in sourceInput
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	src := store.Source{Kind: store.SourceKindRSS, Enabled: true, IntervalMin: store.DefaultSourceIntervalMin}
	in.apply(&src)
	named := src.Name != ""
	if !named {
		src.Name = "-" // validated below, replaced by the feed title
	}
	if err := store.ValidateSource(&src); err != nil {
		return httpx.Bad(err)
	}
	p, err := s.probe(r.Context(), src.Kind, src.URL, src.ProxyDisabled)
	if err != nil {
		return err
	}
	if !named {
		src.Name = store.Trunc(p.Title, store.MaxSourceNameBytes)
		if src.Name == "" {
			src.Name = src.URL
		}
	}
	id, err := s.St.AddSource(r.Context(), src)
	if err != nil {
		return sourceError(err)
	}
	s.audit(r, "source.create", fmt.Sprint(id), src.Name+" "+src.URL)
	got, err := s.St.GetSource(r.Context(), id)
	if err != nil {
		return err
	}
	httpx.JSON(w, 201, s.sourceView(*got))
	return nil
}

// PUT /api/v1/admin/sources/id/{id} — partial update: only the fields present change.
func (s *Server) adminUpdateSource(w http.ResponseWriter, r *http.Request) error {
	cur, err := s.St.GetSource(r.Context(), pathID(r))
	if err != nil {
		return sourceError(err)
	}
	var in sourceInput
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	next := *cur
	in.apply(&next)
	if err := store.ValidateSource(&next); err != nil {
		return httpx.Bad(err)
	}
	if next.URL != cur.URL || next.Kind != cur.Kind {
		if _, err := s.probe(r.Context(), next.Kind, next.URL, next.ProxyDisabled); err != nil {
			return err
		}
	}
	if err := s.St.UpdateSource(r.Context(), next); err != nil {
		return sourceError(err)
	}
	s.audit(r, "source.update", fmt.Sprint(next.ID), fmt.Sprintf("%s %s enabled=%v interval=%d", next.Name, next.URL, next.Enabled, next.IntervalMin))
	got, err := s.St.GetSource(r.Context(), next.ID)
	if err != nil {
		return err
	}
	httpx.JSON(w, 200, s.sourceView(*got))
	return nil
}

func (s *Server) adminDeleteSource(w http.ResponseWriter, r *http.Request) error {
	if err := s.St.DeleteSource(r.Context(), pathID(r)); err != nil {
		return sourceError(err)
	}
	s.audit(r, "source.delete", r.PathValue("id"), "")
	return s.adminListSources(w, r)
}

// POST /api/v1/admin/sources/id/{id}/run — starts a crawl now (202); the result shows up
// in the list (running → last_run_at / last_new / last_error).
func (s *Server) adminRunSource(w http.ResponseWriter, r *http.Request) error {
	id := pathID(r)
	if err := s.Crawl.RunNow(s.BaseCtx, id); err != nil {
		if errors.Is(err, crawl.ErrBusy) {
			return httpx.E(409, "busy", err.Error())
		}
		return sourceError(err)
	}
	s.audit(r, "source.run", fmt.Sprint(id), "")
	src, err := s.St.GetSource(r.Context(), id)
	if err != nil {
		return err
	}
	httpx.JSON(w, 202, s.sourceView(*src))
	return nil
}

// GET /api/v1/admin/metadata-jobs — the qB metadata queue (latest 200).
func (s *Server) adminListMetadataJobs(w http.ResponseWriter, r *http.Request) error {
	jobs, err := s.St.ListMetadataJobs(r.Context(), 200)
	if err != nil {
		return err
	}
	list(w, jobs)
	return nil
}
