package meta

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/darknightlab/onlinetorrent/internal/httpx"
	"github.com/darknightlab/onlinetorrent/internal/store"
)

// Authz decides whether a request may modify mappings and returns an actor name.
type Authz func(r *http.Request) (actor string, ok bool)

// Handler returns the /meta/v1 HTTP API.
func (s *Store) Handler(authz Authz) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /meta/v1/torrents/hash/{ih}", func(w http.ResponseWriter, r *http.Request) {
		tm, err := s.GetTorrentMap(r.Context(), strings.ToLower(r.PathValue("ih")))
		if errors.Is(err, store.ErrNotFound) {
			httpx.Fail(w, httpx.E(404, "not_found", "未收录该种子"))
			return
		}
		if err != nil {
			httpx.Fail(w, err)
			return
		}
		httpx.JSON(w, 200, tm)
	})
	mux.HandleFunc("PUT /meta/v1/torrents/hash/{ih}/files/{idx}", func(w http.ResponseWriter, r *http.Request) {
		actor, ok := authz(r)
		if !ok {
			httpx.Fail(w, httpx.E(403, "forbidden", "需要管理员权限"))
			return
		}
		idx, err := strconv.Atoi(r.PathValue("idx"))
		if err != nil {
			httpx.Fail(w, httpx.E(400, "bad_request", "无效文件序号"))
			return
		}
		var p SetFile
		if err := httpx.Decode(r, &p); err != nil {
			httpx.Fail(w, err)
			return
		}
		if p.SubjectID != nil {
			wk, err := s.GetWorkByID(r.Context(), *p.SubjectID)
			if err != nil {
				httpx.Fail(w, httpx.E(400, "bad_request", "条目不存在"))
				return
			}
			p.WorkKey = &wk.Key
		}
		if p.WorkKey != nil && *p.WorkKey != "" {
			if _, err := s.GetWork(r.Context(), *p.WorkKey); err != nil {
				httpx.Fail(w, httpx.E(400, "bad_request", "作品不存在"))
				return
			}
		}
		ih := strings.ToLower(r.PathValue("ih"))
		if err := s.CorrectFile(r.Context(), ih, idx, p, actor); err != nil {
			if errors.Is(err, store.ErrNotFound) {
				httpx.Fail(w, httpx.E(404, "not_found", "文件不存在"))
				return
			}
			httpx.Fail(w, httpx.Bad(err))
			return
		}
		tm, err := s.GetTorrentMap(r.Context(), ih)
		if err != nil {
			httpx.Fail(w, err)
			return
		}
		httpx.JSON(w, 200, tm)
	})
	mux.HandleFunc("GET /meta/v1/subjects/search", func(w http.ResponseWriter, r *http.Request) {
		qs := strings.TrimSpace(r.URL.Query().Get("q"))
		if qs == "" || len([]rune(qs)) > 100 {
			httpx.Fail(w, httpx.E(400, "bad_request", "需要 q 参数（≤100 字）"))
			return
		}
		ws, err := s.SearchWorks(r.Context(), qs, 50)
		if err != nil {
			httpx.Fail(w, err)
			return
		}
		if ws == nil {
			ws = []*Work{}
		}
		httpx.JSON(w, 200, map[string]any{"items": ws})
	})
	subject := func(w http.ResponseWriter, r *http.Request) (*Work, bool) {
		wk, err := s.ResolveSubject(r.Context(), r.PathValue("provider"), r.PathValue("id"))
		if errors.Is(err, store.ErrNotFound) {
			httpx.Fail(w, httpx.E(404, "not_found", "条目不存在"))
			return nil, false
		}
		if err != nil {
			httpx.Fail(w, err)
			return nil, false
		}
		return wk, true
	}
	// /meta/v1/subjects/id/{id} (our id) or /meta/v1/subjects/{bgm|mal|anidb|anilist|tmdb|…}/{id}
	mux.HandleFunc("GET /meta/v1/subjects/{provider}/{id}", func(w http.ResponseWriter, r *http.Request) {
		if wk, ok := subject(w, r); ok {
			httpx.JSON(w, 200, wk)
		}
	})
	mux.HandleFunc("GET /meta/v1/subjects/{provider}/{id}/episodes/{ep}/files", func(w http.ResponseWriter, r *http.Request) {
		wk, ok := subject(w, r)
		if !ok {
			return
		}
		ep, okEp := atof(r.PathValue("ep"))
		if !okEp {
			httpx.Fail(w, httpx.E(400, "bad_request", "无效集数"))
			return
		}
		files, err := s.EpisodeFiles(r.Context(), wk.Key, ep)
		if err != nil {
			httpx.Fail(w, err)
			return
		}
		httpx.JSON(w, 200, map[string]any{"items": files})
	})
	return mux
}
