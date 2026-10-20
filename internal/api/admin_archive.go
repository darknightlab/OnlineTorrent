package api

import (
	"net/http"

	"github.com/darknightlab/onlinetorrent/internal/httpx"
)

// GET /api/v1/admin/bgm-archive — bangumi Archive sync status
func (s *Server) adminArchiveStatus(w http.ResponseWriter, r *http.Request) error {
	httpx.JSON(w, 200, s.Meta.ArchiveState(r.Context()))
	return nil
}

// POST /api/v1/admin/bgm-archive/run — start a sync now (re-imports even if unchanged);
// 202 with the live status, 409 if a sync is already running.
func (s *Server) adminArchiveRun(w http.ResponseWriter, r *http.Request) error {
	if st := s.Meta.ArchiveState(r.Context()); st.Running {
		return httpx.E(409, "busy", "bangumi 数据正在同步")
	}
	go func() { _, _, _ = s.Cat.SyncArchive(s.BaseCtx, true) }()
	s.audit(r, "bgm_archive.run", "", "")
	st := s.Meta.ArchiveState(r.Context())
	st.Running = true
	httpx.JSON(w, 202, st)
	return nil
}

// GET /api/v1/admin/bangumi-data — bangumi-data sync status
func (s *Server) adminBangumiDataStatus(w http.ResponseWriter, r *http.Request) error {
	httpx.JSON(w, 200, s.Cat.BangumiDataState())
	return nil
}

// POST /api/v1/admin/bangumi-data/run — sync now; 202 with the live status, 409 if running.
func (s *Server) adminBangumiDataRun(w http.ResponseWriter, r *http.Request) error {
	if s.Cat.BangumiDataState().Running {
		return httpx.E(409, "busy", "bangumi-data 正在同步")
	}
	go s.Cat.SyncBangumiData(s.BaseCtx)
	s.audit(r, "bangumi_data.run", "", "")
	st := s.Cat.BangumiDataState()
	st.Running = true
	httpx.JSON(w, 202, st)
	return nil
}
