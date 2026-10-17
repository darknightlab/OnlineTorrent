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
	mux.HandleFunc("GET /meta/v1/torrents/{ih}", func(w http.ResponseWriter, r *http.Request) {
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
	mux.HandleFunc("PUT /meta/v1/torrents/{ih}/files/{idx}", func(w http.ResponseWriter, r *http.Request) {
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
	mux.HandleFunc("GET /meta/v1/works/{key}", func(w http.ResponseWriter, r *http.Request) {
		wk, err := s.GetWork(r.Context(), r.PathValue("key"))
		if errors.Is(err, store.ErrNotFound) {
			httpx.Fail(w, httpx.E(404, "not_found", "作品不存在"))
			return
		}
		if err != nil {
			httpx.Fail(w, err)
			return
		}
		httpx.JSON(w, 200, wk)
	})
	mux.HandleFunc("GET /meta/v1/works/{key}/episodes/{ep}/files", func(w http.ResponseWriter, r *http.Request) {
		ep, ok := atof(r.PathValue("ep"))
		if !ok {
			httpx.Fail(w, httpx.E(400, "bad_request", "无效集数"))
			return
		}
		files, err := s.EpisodeFiles(r.Context(), r.PathValue("key"), ep)
		if err != nil {
			httpx.Fail(w, err)
			return
		}
		httpx.JSON(w, 200, map[string]any{"items": files})
	})
	mux.HandleFunc("GET /meta/v1/works", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if ext := q.Get("external"); ext != "" {
			p, id, ok := parseExternal(ext)
			if !ok {
				httpx.Fail(w, httpx.E(400, "bad_request", "external 格式为 provider:id"))
				return
			}
			wk, err := s.WorkByExternal(r.Context(), p, id)
			if errors.Is(err, store.ErrNotFound) {
				httpx.JSON(w, 200, map[string]any{"items": []any{}})
				return
			}
			if err != nil {
				httpx.Fail(w, err)
				return
			}
			httpx.JSON(w, 200, map[string]any{"items": []*Work{wk}})
			return
		}
		qs := strings.TrimSpace(q.Get("q"))
		if qs == "" {
			httpx.Fail(w, httpx.E(400, "bad_request", "需要 q 或 external 参数"))
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
	return mux
}
