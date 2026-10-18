package api

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/darknightlab/onlinetorrent/internal/httpx"
	"github.com/darknightlab/onlinetorrent/internal/magnet"
	"github.com/darknightlab/onlinetorrent/internal/store"
)

// seedPendingFor: after a seed request, the torrent counts as "pending" (metadata expected
// from qB) for this long unless it became ready.
const seedPendingFor = 30 * time.Minute

type statusSubject struct {
	SubjectID int64  `json:"subject_id"`
	BgmID     *int64 `json:"bgm_id,omitempty"`
	Title     string `json:"title"`
	TitleCN   string `json:"title_cn"`
}

type statusFile struct {
	Index   int            `json:"index"`
	Path    string         `json:"path"`
	Size    int64          `json:"size_bytes"`
	Kind    string         `json:"kind"`
	Subject *statusSubject `json:"subject,omitempty"`
	Episode *float64       `json:"episode,omitempty"`
}

type statusResp struct {
	Status string       `json:"status"` // unknown | pending | ready
	Title  string       `json:"title"`
	Files  []statusFile `json:"files"`
}

// GET /api/v1/torrents/hash/{ih} — catalogue status of a torrent and its scraped files
// (polled by the watch page after a seed request). Local data only, no outbound calls.
func (s *Server) getTorrentStatus(w http.ResponseWriter, r *http.Request) error {
	ih, ok := magnet.NormalizeInfohash(r.PathValue("ih"))
	if !ok {
		return httpx.E(400, "bad_request", "infohash 无效")
	}
	ctx := r.Context()
	out := statusResp{Status: "unknown", Files: []statusFile{}}
	tm, err := s.Meta.GetTorrentMap(ctx, ih)
	switch {
	case err == nil:
		out.Title = tm.Title
		if tm.Status == "ready" && len(tm.Files) > 0 {
			out.Status = "ready"
			for _, f := range tm.Files {
				sf := statusFile{Index: f.Index, Path: f.Path, Size: f.Size, Kind: f.Kind}
				if f.Work != nil {
					sf.Subject = &statusSubject{SubjectID: f.Work.ID, Title: f.Work.Title, TitleCN: f.Work.TitleCN}
					if v, err := strconv.ParseInt(f.Work.External["bgm"], 10, 64); err == nil {
						sf.Subject.BgmID = &v
					}
					sf.Episode = f.Episode
				}
				out.Files = append(out.Files, sf)
			}
		}
	case errors.Is(err, store.ErrNotFound):
		// the trusted title of a user torrent is the qB metadata name (never the dn=)
		if rec, err := s.St.GetTorrent(ctx, ih); err == nil && rec != nil {
			out.Title = rec.Title
		}
	default:
		return err
	}
	if out.Status != "ready" && s.metadataExpected(r, ih) {
		out.Status = "pending"
	}
	w.Header().Set("Cache-Control", "no-store")
	httpx.JSON(w, 200, out)
	return nil
}

// metadataExpected: something is fetching the metadata right now (crawler metadata queue,
// post-seed check, or a recent seed request).
func (s *Server) metadataExpected(r *http.Request, ih string) bool {
	if _, busy := s.postChecks.Load(ih); busy {
		return true
	}
	if v, ok := s.seededAt.Load(ih); ok && time.Since(v.(time.Time)) < seedPendingFor {
		return true
	}
	return s.Crawl != nil && s.Crawl.JobActive(r.Context(), ih)
}
