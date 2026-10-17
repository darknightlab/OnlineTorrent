// Package api implements the /api/v1 HTTP API and serves the SPA.
package api

import (
	"context"
	"io/fs"
	"log/slog"
	"net/http"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/darknightlab/onlinetorrent/internal/catalog"
	"github.com/darknightlab/onlinetorrent/internal/httpx"
	"github.com/darknightlab/onlinetorrent/internal/meta"
	"github.com/darknightlab/onlinetorrent/internal/policy"
	"github.com/darknightlab/onlinetorrent/internal/qb"
	"github.com/darknightlab/onlinetorrent/internal/store"
)

const sessionCookie = "ot_session"

// Server wires dependencies.
type Server struct {
	St      *store.Store
	Meta    *meta.Store
	Cat     *catalog.Catalog
	Pool    *qb.Pool
	Policy  *policy.Engine
	Log     *slog.Logger
	WebFS   fs.FS // built SPA (may be nil in dev)
	BaseCtx context.Context

	limiter *limiter
}

type ctxKey int

const userKey ctxKey = 1

func userOf(r *http.Request) *store.User {
	u, _ := r.Context().Value(userKey).(*store.User)
	return u
}

func uid(u *store.User) *int64 {
	if u == nil {
		return nil
	}
	return &u.ID
}

// Handler builds the full HTTP handler.
func (s *Server) Handler() http.Handler {
	s.limiter = &limiter{m: map[string]*bucket{}}
	mux := http.NewServeMux()
	s.routes(mux)
	mux.Handle("/meta/v1/", s.Meta.Handler(func(r *http.Request) (string, bool) {
		u := userOf(r)
		if u == nil || u.Role != store.RoleAdmin || !csrfOK(r) {
			return "", false
		}
		return u.Username, true
	}))
	mux.Handle("/", s.spa())
	return s.withUser(mux)
}

func csrfOK(r *http.Request) bool {
	return r.Header.Get("X-Requested-With") == "fetch"
}

func (s *Server) withUser(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if c, err := r.Cookie(sessionCookie); err == nil && c.Value != "" {
			if u, err := s.St.SessionUser(r.Context(), c.Value); err == nil {
				r = r.WithContext(context.WithValue(r.Context(), userKey, u))
			}
		}
		// CSRF: every state-changing API call must carry the custom header (not settable cross-site without CORS).
		if r.Method != http.MethodGet && r.Method != http.MethodHead && (strings.HasPrefix(r.URL.Path, "/api/") || strings.HasPrefix(r.URL.Path, "/meta/")) && !csrfOK(r) {
			httpx.Fail(w, httpx.E(http.StatusForbidden, "csrf", "缺少 X-Requested-With 请求头"))
			return
		}
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")
		next.ServeHTTP(w, r)
	})
}

type handler func(w http.ResponseWriter, r *http.Request) error

func (s *Server) h(f handler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := f(w, r); err != nil {
			httpx.Fail(w, err)
		}
	}
}

// admin wraps a handler requiring role admin.
func (s *Server) admin(f handler) http.HandlerFunc {
	return s.h(func(w http.ResponseWriter, r *http.Request) error {
		u := userOf(r)
		if u == nil {
			return httpx.E(401, "login_required", "请先登录")
		}
		if u.Role != store.RoleAdmin {
			return httpx.E(403, "forbidden", "需要管理员权限")
		}
		return f(w, r)
	})
}

func (s *Server) authed(f handler) http.HandlerFunc {
	return s.h(func(w http.ResponseWriter, r *http.Request) error {
		if userOf(r) == nil {
			return httpx.E(401, "login_required", "请先登录")
		}
		return f(w, r)
	})
}

func (s *Server) audit(r *http.Request, action, target, detail string) {
	s.St.Audit(r.Context(), uid(userOf(r)), action, target, detail, httpx.ClientIP(r))
}

// spa serves the embedded frontend with index.html fallback.
func (s *Server) spa() http.Handler {
	if s.WebFS == nil {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "frontend not built", http.StatusNotFound)
		})
	}
	files := http.FileServerFS(s.WebFS)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") {
			httpx.Fail(w, httpx.E(404, "not_found", "接口不存在"))
			return
		}
		p := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		if p == "" {
			p = "index.html"
		}
		if st, err := fs.Stat(s.WebFS, p); err == nil && !st.IsDir() {
			switch {
			case strings.HasPrefix(p, "assets/"):
				w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			case p == "sw.min.js" || p == "index.html":
				w.Header().Set("Cache-Control", "no-cache")
			default:
				w.Header().Set("Cache-Control", "public, max-age=3600")
			}
			files.ServeHTTP(w, r)
			return
		}
		w.Header().Set("Cache-Control", "no-cache")
		r2 := r.Clone(r.Context())
		r2.URL.Path = "/"
		files.ServeHTTP(w, r2)
	})
}

// ---- tiny per-IP rate limiter for auth endpoints ----

type bucket struct {
	tokens float64
	last   time.Time
}

type limiter struct {
	mu sync.Mutex
	m  map[string]*bucket
}

// allow permits `burst` requests then one every `every`.
func (l *limiter) allow(key string, burst float64, every time.Duration) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	b, ok := l.m[key]
	if !ok {
		if len(l.m) > 10000 {
			l.m = map[string]*bucket{}
		}
		b = &bucket{tokens: burst, last: now}
		l.m[key] = b
	}
	b.tokens = min(burst, b.tokens+now.Sub(b.last).Seconds()/every.Seconds())
	b.last = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}
