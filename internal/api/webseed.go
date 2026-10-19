package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/darknightlab/onlinetorrent/internal/httpx"
	"github.com/darknightlab/onlinetorrent/internal/policy"
	"github.com/darknightlab/onlinetorrent/internal/store"
)

// HTTP WebSeed (BEP 19) is served only by edge nodes (internal/edge), at their own public URLs
// with per-edge access tokens; the main server has no webseed data path. It decides who may
// use the webseed (permission "GET /webseed/hash/{ih}/{path...}", registered handler-less in
// public.go, + per-torrent policy) and streams availability (webseedEvents).

// siteBase is the absolute site origin for URLs handed to webtorrent (webseed, xs=): the
// configured site URL, else https://{Host}.
func (s *Server) siteBase(r *http.Request) string {
	base := strings.TrimRight(s.St.Str(store.KeySiteBaseURL), "/")
	if base == "" {
		base = "https://" + r.Host
	}
	return base
}

// webseedAllowed caches policy decisions per (role, infohash) for a minute: webtorrent issues
// many range requests per second and the decision only changes when admins edit rules.
// Also used by GET /torrents/hash/{ih}/torrent (same per-torrent policy as playback).
func (s *Server) webseedAllowed(r *http.Request, ih string) (bool, error) {
	return s.webseedAllowedFor(r.Context(), userOf(r), ih)
}

// webseedAllowedFor is webseedAllowed for a given user (edge nodes ask on behalf of a session).
func (s *Server) webseedAllowedFor(ctx context.Context, u *store.User, ih string) (bool, error) {
	key := roleOf(u) + "|" + ih + "|" + strconv.FormatUint(s.Policy.Generation(), 10)
	if v, ok := s.wsAllow.Load(key); ok {
		e := v.(wsAllowEntry)
		if time.Now().Before(e.exp) {
			return e.ok, nil
		}
	}
	// nothing without known metadata; then policy + content scope (playDecision)
	sub, _, known := s.catalogSubject(ctx, ih)
	d := policy.Decision{}
	if known {
		var err error
		if d, err = s.playDecision(ctx, u, sub); err != nil {
			return false, err
		}
	}
	s.wsAllow.Store(key, wsAllowEntry{d.Allowed, time.Now().Add(time.Minute)})
	return d.Allowed, nil
}

type wsAllowEntry struct {
	ok  bool
	exp time.Time
}

// webseedEvents streams Server-Sent Events about webseed availability (replaces client-side
// probing, which made webtorrent hammer errors while qB was still downloading).
//
//	GET /api/v1/torrents/hash/{ih}/webseed/events[/file/{idx}]
//
// Events: see webseedStream. There is no overall deadline; a comment heartbeat is sent every
// 25s (Cloudflare closes idle streams after ~100s).
func (s *Server) webseedEvents(w http.ResponseWriter, r *http.Request) error {
	if _, _, err := s.recordedSubject(r); err != nil {
		return err
	}
	ih := strings.ToLower(r.PathValue("ih"))
	idx := -1
	if v := r.PathValue("idx"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			return httpx.E(400, "bad_request", "无效文件序号")
		}
		idx = n
	}
	key := "sse:" + ipKey(r)
	if !s.sseAcquire(key, 6) {
		return errRateLimited
	}
	defer s.sseRelease(key)

	rc := http.NewResponseController(w)
	_ = rc.SetWriteDeadline(time.Time{}) // long-lived stream: no server write timeout
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache, no-transform")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	send := func(event string, data any) bool {
		b, _ := json.Marshal(data)
		if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, b); err != nil {
			return false
		}
		return rc.Flush() == nil
	}
	ping := func() bool {
		_, err := io.WriteString(w, ": ping\n\n")
		return err == nil && rc.Flush() == nil
	}
	if _, err := io.WriteString(w, "retry: 15000\n\n"); err != nil {
		return nil
	}
	_ = rc.Flush()

	if !s.St.Bool(store.KeyWebseedEnabled) {
		send("unavailable", map[string]string{"reason": "disabled"})
		return nil
	}
	s.webseedStream(r.Context(), r, send, ping, ih, idx)
	return nil
}

func (s *Server) sseAcquire(key string, limit int) bool {
	s.sseMu.Lock()
	defer s.sseMu.Unlock()
	if s.sse == nil {
		s.sse = map[string]int{}
	}
	if s.sse[key] >= limit {
		return false
	}
	s.sse[key]++
	return true
}

func (s *Server) sseRelease(key string) {
	s.sseMu.Lock()
	defer s.sseMu.Unlock()
	if s.sse[key]--; s.sse[key] <= 0 {
		delete(s.sse, key)
	}
}
