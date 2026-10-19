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

// Scope returns the content scope of a request (nil: unrestricted).
type Scope func(r *http.Request) *store.ScopePlan

// FileFilter returns, per request, which episode files may be listed (nil func: all).
// otd hides files of torrents the user may not play (content policy).
type FileFilter func(r *http.Request) func(EpisodeFile) bool

// Handler returns the /meta/v1 HTTP API. scope (may be nil) limits which subjects reads
// expose: subjects outside it are 404, left out of search, and hidden from file mappings.
// files (may be nil) filters the episode file lists and counts.
func (s *Store) Handler(authz Authz, scope Scope, files FileFilter) http.Handler {
	if scope == nil {
		scope = func(*http.Request) *store.ScopePlan { return nil }
	}
	if files == nil {
		files = func(*http.Request) func(EpisodeFile) bool { return nil }
	}
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
		tm.HideWorks(scope(r))
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
		ws, err := s.SearchWorksFTS(r.Context(), qs, 50, scope(r))
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
		if err == nil && !scope(r).Visible(wk) {
			err = store.ErrNotFound // outside the content scope: don't reveal it exists
		}
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
	// episode keys with mapped files: [{key, kind, number, label, files}]
	mux.HandleFunc("GET /meta/v1/subjects/{provider}/{id}/episodes", func(w http.ResponseWriter, r *http.Request) {
		wk, ok := subject(w, r)
		if !ok {
			return
		}
		keys, err := s.EpisodeKeysWhere(r.Context(), wk.Key, files(r))
		if err != nil {
			httpx.Fail(w, err)
			return
		}
		items := make([]map[string]any, 0, len(keys))
		for _, k := range keys {
			items = append(items, map[string]any{"key": k.Key.String(), "kind": k.Key.Kind, "number": k.Key.Number, "label": k.Key.Label(), "files": k.Files})
		}
		httpx.JSON(w, 200, map[string]any{"items": items})
	})
	// {ep} is an episode key: "12", "12.5", "sp1", "ncop1", …
	mux.HandleFunc("GET /meta/v1/subjects/{provider}/{id}/episodes/{ep}/files", func(w http.ResponseWriter, r *http.Request) {
		wk, ok := subject(w, r)
		if !ok {
			return
		}
		key, okEp := ParseEpisodeKey(r.PathValue("ep"))
		if !okEp {
			httpx.Fail(w, httpx.E(400, "bad_request", "无效集数"))
			return
		}
		list, err := s.EpisodeFiles(r.Context(), wk.Key, key)
		if err != nil {
			httpx.Fail(w, err)
			return
		}
		if keep := files(r); keep != nil {
			kept := list[:0]
			for _, f := range list {
				if keep(f) {
					kept = append(kept, f)
				}
			}
			list = kept
		}
		httpx.JSON(w, 200, map[string]any{"key": key.String(), "items": list})
	})
	return mux
}
