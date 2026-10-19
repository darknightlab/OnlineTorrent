package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/darknightlab/onlinetorrent/internal/httpx"
	"github.com/darknightlab/onlinetorrent/internal/store"
)

// Content scopes (admin "内容范围"), modelled on YotsubaNews api/handlers_roles.go: each of
// guest/member/vip has a tag-based scope (store/content_scope.go); admin is unrestricted.
// Compiled plans live in memory and are replaced after every write.

// loadScopes compiles every role's scope into the cache.
func (s *Server) loadScopes(ctx context.Context) error {
	scopes, err := s.St.ContentScopes(ctx)
	if err != nil {
		return err
	}
	next := make(map[string]*store.ScopePlan, len(scopes))
	for role, sc := range scopes {
		next[role] = store.CompileScope(sc)
	}
	s.scopeMu.Lock()
	s.scopes = next
	s.scopeMu.Unlock()
	return nil
}

func (s *Server) replaceScope(role string, plan *store.ScopePlan) {
	s.scopeMu.Lock()
	defer s.scopeMu.Unlock()
	if s.scopes == nil {
		s.scopes = map[string]*store.ScopePlan{}
	}
	s.scopes[role] = plan
}

// scopeFor returns the compiled plan of role (admin: unrestricted). If the scopes could not
// be loaded it retries, and fails closed (sees nothing) while the database is unavailable.
func (s *Server) scopeFor(role string) *store.ScopePlan {
	if role == store.RoleAdmin {
		return store.UnrestrictedScopePlan()
	}
	s.scopeMu.RLock()
	scopes := s.scopes
	s.scopeMu.RUnlock()
	if scopes == nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		err := s.loadScopes(ctx)
		cancel()
		if err != nil {
			s.Log.Warn("load content scopes", "err", err)
			return store.DenyAllScopePlan()
		}
		s.scopeMu.RLock()
		scopes = s.scopes
		s.scopeMu.RUnlock()
	}
	if p := scopes[role]; p != nil {
		return p
	}
	return store.UnrestrictedScopePlan() // role without a row: default scope
}

// scopeOf is the content scope of the requesting user.
func (s *Server) scopeOf(r *http.Request) *store.ScopePlan { return s.scopeFor(roleOf(userOf(r))) }

// GET /api/v1/admin/content-scopes
func (s *Server) adminListContentScopes(w http.ResponseWriter, r *http.Request) error {
	scopes, err := s.St.ContentScopes(r.Context())
	if err != nil {
		return err
	}
	items := make([]store.ContentScope, 0, len(store.ScopeRoles))
	for _, role := range store.ScopeRoles {
		items = append(items, scopes[role])
	}
	httpx.JSON(w, 200, map[string]any{"roles": store.ScopeRoles, "items": items})
	return nil
}

// PUT /api/v1/admin/content-scopes/role/{role}
func (s *Server) adminSetContentScope(w http.ResponseWriter, r *http.Request) error {
	role := r.PathValue("role")
	if role == store.RoleAdmin {
		return httpx.E(400, "bad_request", "管理员不受内容范围限制")
	}
	var raw json.RawMessage
	if err := httpx.Decode(r, &raw); err != nil {
		return err
	}
	// strict: unknown fields (typos, removed options) are an error, not silently ignored
	var in store.ContentScope
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil {
		return httpx.E(400, "bad_request", "请求格式错误："+err.Error())
	}
	s.scopeWrites.Lock()
	defer s.scopeWrites.Unlock()
	updated, err := s.St.SetContentScope(r.Context(), role, in)
	switch {
	case errors.Is(err, store.ErrNotFound):
		return httpx.E(404, "not_found", "角色不存在")
	case errors.Is(err, store.ErrScopeInvalid):
		return httpx.E(400, "bad_request", strings.TrimPrefix(err.Error(), store.ErrScopeInvalid.Error()+": "))
	case err != nil:
		return err
	}
	s.replaceScope(role, store.CompileScope(updated))
	detail, _ := json.Marshal(map[string]any{"tag_mode": updated.TagMode,
		"expression": store.TagExpressionString(updated.Tags, updated.TagOperators), "exclude_tags": updated.ExcludeTags})
	s.audit(r, "content_scope.update", role, string(detail))
	httpx.JSON(w, 200, updated)
	return nil
}

const subjectTagLimit = 30

// GET /api/v1/admin/subject-tags?q= — tag autocomplete (works.tags ∪ meta_tags, with counts)
func (s *Server) adminSubjectTags(w http.ResponseWriter, r *http.Request) error {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if utf8.RuneCountInString(q) > store.MaxScopeTagRune {
		return httpx.E(400, "bad_request", "关键词过长")
	}
	tags, err := s.Meta.SubjectTags(r.Context(), q, subjectTagLimit)
	if err != nil {
		return err
	}
	list(w, tags)
	return nil
}
