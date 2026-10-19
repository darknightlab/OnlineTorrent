package api

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/darknightlab/onlinetorrent/internal/httpx"
	"github.com/darknightlab/onlinetorrent/internal/magnet"
	"github.com/darknightlab/onlinetorrent/internal/policy"
	"github.com/darknightlab/onlinetorrent/internal/store"
)

// HTTP WebSeed (BEP 19) backed by qBittorrent's torrents/downloadFile (qB ≥ 5.3).
//
//	/webseed/hash/{infohash}/              single-file torrent (webtorrent requests the URL as-is)
//	/webseed/hash/{infohash}/{file path}   multi-file torrent (webtorrent appends "root/sub/file")
//
// The URL is fixed (no signature / expiry). Every request is authorised with the login cookie
// (same origin, so webtorrent's fetches carry it): the route permission
// "GET /webseed/hash/{ih}/" (admin-editable, default members) plus the per-torrent policy.
// Only files a qB server has fully downloaded are served; responses are not cacheable.

// webseedURLs returns the webseed URL for a torrent, or nil if webseeds are disabled.
func (s *Server) webseedURLs(r *http.Request, ih string) []string {
	if !s.St.Bool(store.KeyWebseedEnabled) {
		return nil
	}
	return []string{s.siteBase(r) + "/webseed/hash/" + ih + "/"}
}

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
	u := userOf(r)
	key := roleOf(u) + "|" + ih + "|" + strconv.FormatUint(s.Policy.Generation(), 10)
	if v, ok := s.wsAllow.Load(key); ok {
		e := v.(wsAllowEntry)
		if time.Now().Before(e.exp) {
			return e.ok, nil
		}
	}
	// nothing without known metadata; then policy + content scope (playDecision)
	sub, _, known := s.catalogSubject(r.Context(), ih)
	d := policy.Decision{}
	if known {
		var err error
		if d, err = s.playDecision(r.Context(), u, sub); err != nil {
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

// serveWebseed handles GET/HEAD /webseed/hash/{ih}/{path...} (permission already checked).
func (s *Server) serveWebseed(w http.ResponseWriter, r *http.Request) error {
	fail := func(code int, msg string) error { http.Error(w, msg, code); return nil }
	if !s.St.Bool(store.KeyWebseedEnabled) {
		return fail(http.StatusNotFound, "webseed disabled")
	}
	ih, ok := magnet.NormalizeInfohash(r.PathValue("ih"))
	if !ok {
		return fail(http.StatusNotFound, "invalid infohash")
	}
	if !s.limiter.allow("webseed:"+ipKey(r), 600, 50*time.Millisecond) {
		return fail(http.StatusTooManyRequests, "rate limited")
	}
	allowed, err := s.webseedAllowed(r, ih)
	if err != nil {
		return err
	}
	if !allowed {
		return fail(http.StatusForbidden, "forbidden")
	}
	srv, f, found := s.Pool.FindFile(r.Context(), ih, r.PathValue("path"))
	if !found {
		return fail(http.StatusNotFound, "file not available on any seed server yet")
	}
	resp, err := srv.Client.DownloadFile(r.Context(), ih, f.Index, r.Header.Get("Range"))
	if err != nil {
		return fail(http.StatusBadGateway, "upstream error")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
		return fail(http.StatusBadGateway, "upstream status "+strconv.Itoa(resp.StatusCode))
	}
	for _, h := range []string{"Content-Type", "Content-Length", "Content-Range", "Accept-Ranges", "Last-Modified", "ETag"} {
		if v := resp.Header.Get(h); v != "" {
			w.Header().Set(h, v)
		}
	}
	w.Header().Set("Cache-Control", "private, no-store")
	w.WriteHeader(resp.StatusCode)
	if r.Method == http.MethodHead {
		return nil
	}
	_, _ = io.Copy(w, resp.Body)
	return nil
}

// webseedEvents streams Server-Sent Events until the requested file is fully available on a
// seed server (then sends `ready` with freshly signed webseed URLs). This replaces client-side
// probing, which made webtorrent hammer 404s while qB was still downloading.
//
//	GET /api/v1/torrents/hash/{ih}/webseed/events[/file/{idx}]
//
// Events: waiting {progress} (on change), ready {webseeds}, unavailable {reason}, timeout {}.
// A comment heartbeat is sent every 25s (Cloudflare closes idle streams after ~100s).
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
	if _, err := io.WriteString(w, "retry: 15000\n\n"); err != nil {
		return nil
	}
	_ = rc.Flush()

	if !s.St.Bool(store.KeyWebseedEnabled) {
		send("unavailable", map[string]string{"reason": "disabled"})
		return nil
	}
	ctx := r.Context()
	check := time.NewTicker(5 * time.Second)
	beat := time.NewTicker(25 * time.Second)
	defer check.Stop()
	defer beat.Stop()
	deadline := time.After(30 * time.Minute)
	lastProgress := -1.0
	notFound := 0
	for {
		ready, progress, found := s.Pool.FileProgress(ctx, ih, idx)
		switch {
		case ready:
			urls := s.webseedURLs(r, ih)
			if urls == nil {
				send("unavailable", map[string]string{"reason": "disabled"})
			} else {
				send("ready", map[string]any{"webseeds": urls})
			}
			return nil
		case !found:
			// no server holds the torrent (e.g. nobody asked it to seed): give up after ~1 min
			if notFound++; notFound >= 12 {
				send("unavailable", map[string]string{"reason": "not_seeded"})
				return nil
			}
		default:
			notFound = 0
			if progress != lastProgress {
				lastProgress = progress
				if !send("waiting", map[string]float64{"progress": progress}) {
					return nil
				}
			}
		}
		select {
		case <-ctx.Done():
			return nil
		case <-deadline:
			send("timeout", map[string]any{})
			return nil
		case <-beat.C:
			if _, err := io.WriteString(w, ": ping\n\n"); err != nil || rc.Flush() != nil {
				return nil
			}
			<-check.C
		case <-check.C:
		}
	}
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
