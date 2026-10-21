package api

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"time"

	"github.com/darknightlab/onlinetorrent/internal/httpx"
	"github.com/darknightlab/onlinetorrent/internal/meta"
)

// danmaku collections started per client (stored danmaku is not limited)
const (
	danmakuFetchBurst = 10
	danmakuFetchEvery = 30 * time.Second
)

// GET /api/v1/subjects/{provider}/{id}/episodes/{n}/danmaku → {"items": [[time, mode, colour, text], …]}
// (mode: 1 scroll, 4 bottom, 5 top; colour 0xRRGGBB). Official main episodes only; an
// episode without stored danmaku is collected now (internal/danmaku), otherwise the list is
// empty. No source / platform information is returned.
func (s *Server) getDanmaku(w http.ResponseWriter, r *http.Request) error {
	work, err := s.resolveSubject(r)
	if err != nil {
		return err
	}
	key, ok := meta.ParseEpisodeKey(r.PathValue("n"))
	if !ok {
		return httpx.E(400, "bad_request", "无效集数")
	}
	key = s.Meta.CanonicalKey(r.Context(), work.Key, key)
	var items []meta.Comment
	if s.Danmaku != nil {
		mayFetch := func() bool { return s.limiter.allow("danmaku:"+ipKey(r), danmakuFetchBurst, danmakuFetchEvery) }
		items, err = s.Danmaku.Get(r.Context(), s.BaseCtx, work, key, mayFetch)
		if err != nil {
			return err
		}
	}
	if items == nil {
		items = []meta.Comment{}
	}
	body, err := json.Marshal(map[string]any{"items": items})
	if err != nil {
		return err
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	if len(items) == 0 {
		w.Header().Set("Cache-Control", "no-store") // may be collected later
	} else {
		// stored danmaku never changes; revalidate (permissions are per user)
		sum := sha256.Sum256(body)
		etag := `"` + hex.EncodeToString(sum[:12]) + `"`
		w.Header().Set("Cache-Control", "private, no-cache")
		w.Header().Set("ETag", etag)
		if r.Header.Get("If-None-Match") == etag {
			w.WriteHeader(http.StatusNotModified)
			return nil
		}
	}
	_, err = w.Write(body)
	return err
}
