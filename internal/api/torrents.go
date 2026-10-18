package api

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/darknightlab/onlinetorrent/internal/crawl"
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
	Episode *float64       `json:"episode,omitempty"`     // main episodes only (kind episode)
	Key     string         `json:"episode_key,omitempty"` // "12", "12.5", "sp1", "ncop1"… (with subject)
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
					if f.Kind == "episode" {
						sf.Episode = f.Episode
					}
					sf.Key = f.EpisodeKey
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

// torrentFileKey is the route permission of GET /api/v1/torrents/hash/{ih}/torrent.
const (
	torrentFilePath = "/api/v1/torrents/hash/{ih}/torrent"
	torrentFileKey  = "GET " + torrentFilePath
)

// GET /api/v1/torrents/hash/{ih}/torrent — the raw .torrent (metadata over HTTP: magnet
// xs=, so browsers don't need a WebRTC peer for ut_metadata). Stored copy, else a live qB
// export / bangumi.moe download (then stored). Same per-torrent policy as playback (403).
func (s *Server) getTorrentFile(w http.ResponseWriter, r *http.Request) error {
	ih, ok := magnet.NormalizeInfohash(r.PathValue("ih"))
	if !ok {
		return httpx.E(400, "bad_request", "infohash 无效")
	}
	if !s.limiter.allow("torrentfile:"+ipKey(r), 30, 2*time.Second) {
		return errRateLimited
	}
	allowed, err := s.webseedAllowed(r, ih)
	if err != nil {
		return err
	}
	if !allowed {
		return httpx.E(http.StatusForbidden, "forbidden", "当前账号无权获取该种子")
	}
	if s.Crawl == nil {
		return httpx.E(404, "not_found", "没有该种子的种子文件")
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	b, err := s.Crawl.Metainfo(ctx, ih)
	if errors.Is(err, crawl.ErrNoMetainfo) || errors.Is(err, context.DeadlineExceeded) {
		return httpx.E(404, "not_found", "没有该种子的种子文件")
	}
	if err != nil {
		return err
	}
	w.Header().Set("Content-Type", "application/x-bittorrent")
	w.Header().Set("Content-Disposition", `attachment; filename="`+ih+`.torrent"`)
	w.Header().Set("Cache-Control", "private, max-age=3600")
	w.Header().Set("Content-Length", strconv.Itoa(len(b)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(b)
	return nil
}

type xsEntry struct {
	ok  bool
	exp time.Time
}

// metainfoObtainable decides whether /play should add xs= for ih: a stored .torrent (or a
// bangumi.moe item), else a qB server holding the metadata (checked with a short timeout,
// cached: positive 5 min, negative 15 s).
func (s *Server) metainfoObtainable(ctx context.Context, ih string) bool {
	if s.Crawl == nil {
		return false
	}
	if s.Crawl.MetainfoAvailable(ctx, ih) {
		return true
	}
	if s.Pool == nil {
		return false
	}
	now := time.Now()
	if v, ok := s.xsCache.Load(ih); ok && now.Before(v.(xsEntry).exp) {
		return v.(xsEntry).ok
	}
	cctx, cancel := context.WithTimeout(ctx, 1500*time.Millisecond)
	ok := s.Pool.HasMetadata(cctx, ih)
	cancel()
	ttl := 15 * time.Second
	if ok {
		ttl = 5 * time.Minute
	}
	if n := s.xsCount.Add(1); n%256 == 0 {
		s.xsCache.Range(func(k, v any) bool {
			if now.After(v.(xsEntry).exp) {
				s.xsCache.Delete(k)
			}
			return true
		})
	}
	s.xsCache.Store(ih, xsEntry{ok, now.Add(ttl)})
	return ok
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
