package api

import (
	"context"
	"net/http"
	"slices"
	"sort"
	"strings"
	"sync"

	"github.com/darknightlab/onlinetorrent/internal/httpx"
	"github.com/darknightlab/onlinetorrent/internal/store"
)

// Route-level permissions (like YotsubaNews): every API endpoint is registered with a
// permission key "METHOD /path/pattern", an access kind and default grants. Grants for
// configurable routes live in SQLite (role_permissions) and are edited in the admin panel.
//
// Roles are ordered guest < member < vip < admin. A grant to a role also applies to every
// higher role; admin passes every route.

// Access kinds.
const (
	AccessPublic       = "public"       // everyone, not configurable (site info, login…)
	AccessConfigurable = "configurable" // guest/member/vip grants editable
	AccessUser         = "user"         // requires login; member/vip grants editable
	AccessAdmin        = "admin"        // admin only, not configurable
)

// RouteSpec describes one permission-checked endpoint.
type RouteSpec struct {
	Key          string   `json:"key"`
	Method       string   `json:"method"`
	Path         string   `json:"path"`
	Group        string   `json:"group"`
	Label        string   `json:"label"`
	Access       string   `json:"access"`
	DefaultRoles []string `json:"default_roles"`
}

type permState struct {
	mu     sync.RWMutex
	specs  []RouteSpec
	byKey  map[string]*RouteSpec
	grants map[string]map[string]bool // role -> route -> granted
}

func (p *permState) add(spec RouteSpec) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.byKey == nil {
		p.byKey = map[string]*RouteSpec{}
	}
	p.specs = append(p.specs, spec)
	p.byKey[spec.Key] = &p.specs[len(p.specs)-1]
	// re-point after possible slice growth
	for i := range p.specs {
		p.byKey[p.specs[i].Key] = &p.specs[i]
	}
}

// route registers a handler under its permission key.
func (s *Server) route(mux *http.ServeMux, method, pattern, access, group, label string, defaults []string, h handler) {
	key := method + " " + pattern
	s.perms.add(RouteSpec{Key: key, Method: method, Path: pattern, Group: group, Label: label, Access: access, DefaultRoles: defaults})
	mux.HandleFunc(key, s.checkPerm(key, s.h(h)))
}

// checkPerm enforces a route permission before calling next.
func (s *Server) checkPerm(key string, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u := userOf(r)
		if !s.allowed(roleOf(u), key) {
			if u == nil {
				httpx.Fail(w, httpx.E(http.StatusUnauthorized, "login_required", "请先登录"))
			} else {
				httpx.Fail(w, httpx.E(http.StatusForbidden, "forbidden", "当前账号无权使用该功能"))
			}
			return
		}
		next(w, r)
	}
}

func roleOf(u *store.User) string {
	if u == nil {
		return store.RoleGuest
	}
	return u.EffectiveRole()
}

// allowed reports whether role may call route key.
func (s *Server) allowed(role, key string) bool {
	s.perms.mu.RLock()
	defer s.perms.mu.RUnlock()
	spec := s.perms.byKey[key]
	if spec == nil {
		return false
	}
	if role == store.RoleAdmin || spec.Access == AccessPublic {
		return true
	}
	switch spec.Access {
	case AccessAdmin:
		return false
	case AccessUser:
		if role == store.RoleGuest {
			return false
		}
	}
	rank := store.RoleRank(role)
	for _, g := range store.GrantableRoles {
		if store.RoleRank(g) > rank {
			continue
		}
		if spec.Access == AccessUser && g == store.RoleGuest {
			continue
		}
		if s.perms.grants[g][key] {
			return true
		}
	}
	return false
}

// loadPerms seeds defaults for new routes and loads grants into memory.
func (s *Server) loadPerms(ctx context.Context) error {
	defaults := map[string][]string{}
	s.perms.mu.RLock()
	for _, sp := range s.perms.specs {
		if sp.Access == AccessConfigurable || sp.Access == AccessUser {
			defaults[sp.Key] = sp.DefaultRoles
		}
	}
	s.perms.mu.RUnlock()
	if err := s.St.SeedRoutePermissions(ctx, defaults); err != nil {
		return err
	}
	g, err := s.St.RolePermissions(ctx)
	if err != nil {
		return err
	}
	s.perms.mu.Lock()
	s.perms.grants = g
	s.perms.mu.Unlock()
	return nil
}

// permissionsFor lists the route keys a role may call (for /me, so the UI can adapt).
func (s *Server) permissionsFor(role string) []string {
	s.perms.mu.RLock()
	keys := make([]string, 0, len(s.perms.specs))
	for _, sp := range s.perms.specs {
		if sp.Access != AccessAdmin && sp.Access != AccessPublic {
			keys = append(keys, sp.Key)
		}
	}
	s.perms.mu.RUnlock()
	out := []string{}
	for _, k := range keys {
		if s.allowed(role, k) {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

type permRow struct {
	RouteSpec
	Granted map[string]bool `json:"granted"` // explicit grants per grantable role
}

func (s *Server) adminGetPermissions(w http.ResponseWriter, r *http.Request) error {
	s.perms.mu.RLock()
	rows := make([]permRow, 0, len(s.perms.specs))
	for _, sp := range s.perms.specs {
		row := permRow{RouteSpec: sp, Granted: map[string]bool{}}
		for _, g := range store.GrantableRoles {
			row.Granted[g] = s.perms.grants[g][sp.Key]
		}
		rows = append(rows, row)
	}
	s.perms.mu.RUnlock()
	httpx.JSON(w, 200, map[string]any{"roles": store.GrantableRoles, "items": rows})
	return nil
}

// adminPutPermissions body: {"grants": {"GET /api/v1/schedule": ["guest"], ...}}
func (s *Server) adminPutPermissions(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		Grants map[string][]string `json:"grants"`
	}
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	for key, roles := range in.Grants {
		s.perms.mu.RLock()
		spec := s.perms.byKey[key]
		s.perms.mu.RUnlock()
		if spec == nil {
			return httpx.E(400, "bad_request", "未知接口: "+key)
		}
		if spec.Access != AccessConfigurable && spec.Access != AccessUser {
			return httpx.E(400, "bad_request", "该接口权限不可修改: "+key)
		}
		if spec.Access == AccessUser && slices.Contains(roles, store.RoleGuest) {
			return httpx.E(400, "bad_request", "该接口需要登录，不能授权给游客: "+key)
		}
		if err := s.St.SetRouteRoles(r.Context(), key, roles); err != nil {
			return httpx.Bad(err)
		}
		s.audit(r, "permission.update", key, strings.Join(roles, ","))
	}
	if err := s.loadPerms(r.Context()); err != nil {
		return err
	}
	return s.adminGetPermissions(w, r)
}
