package api

// Opening a hash — the same flow for every user (guest or member; only permissions differ):
//
//   - Known with metadata (catalogue row ready, files known): /play decides with the real
//     title and files (policy + content scope) and returns the magnet (+ the mapped episode).
//   - A NEW hash: /play records the request (plays action "pending": the submission time and
//     submitter of a user torrent come from it, St.FirstPlay) and queues an interactive
//     metadata fetch (crawl metadata queue, priority 1: metadata only — nothing is downloaded
//     or seeded, so guests may trigger it). It answers {status: "pending"} WITHOUT a magnet:
//     the browser must not start P2P before the verdict. The client then listens on
//
//     GET /api/v1/torrents/hash/{ih}/resolve/events
//
//     which, once the metadata is ingested (rules mapping; uncertain files → the decision
//     model, synchronously up to 15 s), sends one of
//
//     redirect {path}           a file is mapped (applied mapping): the episode page; it
//     checks permissions itself (also when the subject is hidden)
//     ready {}                  not mapped and allowed: call /play again for the magnet
//     denied {action, reason}   not allowed (later visits get the denial from /play)
//     timeout {}                no metadata within 60 s (the queue keeps trying)
//
//     A comment heartbeat goes out every 25 s.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/darknightlab/onlinetorrent/internal/catalog"
	"github.com/darknightlab/onlinetorrent/internal/httpx"
	"github.com/darknightlab/onlinetorrent/internal/magnet"
	"github.com/darknightlab/onlinetorrent/internal/meta"
	"github.com/darknightlab/onlinetorrent/internal/policy"
	"github.com/darknightlab/onlinetorrent/internal/store"
)

// Resolve timings (variables for tests).
var (
	resolveTimeout        = 60 * time.Second
	resolveDecisionBudget = 15 * time.Second
	resolvePoll           = time.Second
)

// maxResolveStreams: concurrent resolve streams per client IP.
const maxResolveStreams = 6

// knownTorrent returns the catalogue entry of ih when its metadata (file list) is known.
func (s *Server) knownTorrent(ctx context.Context, ih string) *meta.CatalogTorrent {
	ct, err := s.Meta.CatalogByHash(ctx, ih)
	if err != nil || ct.Status != "ready" || len(ct.Files) == 0 {
		return nil
	}
	return ct
}

// catalogSubject is the policy subject of a torrent with known metadata (ok=false: unknown).
func (s *Server) catalogSubject(ctx context.Context, ih string) (policy.Subject, *catalog.Torrent, bool) {
	ct := s.knownTorrent(ctx, ih)
	if ct == nil {
		return policy.Subject{Infohash: ih}, nil, false
	}
	t := catalog.FromCatalog(ct)
	return t.Subject(), t, true
}

// playDecision is the policy decision for u plus the content scope: a torrent with any
// applied file mapping to a subject hidden for u's role is not playable by u (unmapped files:
// policy rules only).
func (s *Server) playDecision(ctx context.Context, u *store.User, sub policy.Subject) (policy.Decision, error) {
	d, err := s.Policy.Decide(ctx, u, sub)
	if err != nil || !d.Allowed || sub.Infohash == "" {
		return d, err
	}
	if s.hiddenFor(ctx, roleOf(u), sub.Infohash) {
		return s.scopeDenial(ctx, u, sub.Infohash), nil
	}
	return d, nil
}

func (s *Server) hiddenFor(ctx context.Context, role, ih string) bool {
	h, err := s.Meta.HiddenByScope(ctx, []string{ih}, s.scopeFor(role))
	if err != nil {
		s.Log.Warn("content scope", "ih", ih, "err", err)
		return true // fail closed
	}
	return h[ih]
}

// scopeDenial: a higher role whose scope shows the torrent is what the user would need
// (guest → login, → vip); otherwise it is out of reach.
func (s *Server) scopeDenial(ctx context.Context, u *store.User, ih string) policy.Decision {
	rank := store.RoleRank(roleOf(u))
	if rank < store.RoleRank(store.RoleMember) && !s.hiddenFor(ctx, store.RoleMember, ih) {
		return policy.Decision{Action: policy.Login, Reason: "登录后即可观看", Source: "scope"}
	}
	if rank < store.RoleRank(store.RoleVIP) && !s.hiddenFor(ctx, store.RoleVIP, ih) {
		return policy.Decision{Action: policy.VIP, Reason: "该资源仅限大会员观看", Source: "scope"}
	}
	return policy.Decision{Action: policy.Block, Label: "scope", Reason: "该资源不在当前账号可观看的内容范围内", Source: "scope"}
}

// playPending answers /play for a hash without metadata: record the request, queue an
// interactive metadata fetch, no magnet.
func (s *Server) playPending(w http.ResponseWriter, r *http.Request, ih string) error {
	ctx := r.Context()
	s.St.AddPlay(ctx, store.Play{Infohash: ih, IP: httpx.ClientIP(r), Action: "pending"}, uid(userOf(r)))
	if s.Crawl != nil {
		if err := s.Crawl.RequestMetadata(ctx, ih); err != nil {
			return err
		}
	}
	httpx.JSON(w, 200, playResp{Status: "pending", Access: policy.Access{Action: "pending"}, Infohash: ih, SeededBy: []string{}, Webseeds: []string{}})
	return nil
}

// GET /api/v1/torrents/hash/{ih}/resolve/events
func (s *Server) resolveEvents(w http.ResponseWriter, r *http.Request) error {
	ih, ok := magnet.NormalizeInfohash(r.PathValue("ih"))
	if !ok {
		return httpx.E(400, "bad_request", "infohash 无效")
	}
	key := "sse-resolve:" + ipKey(r)
	if !s.sseAcquire(key, maxResolveStreams) {
		return errRateLimited
	}
	defer s.sseRelease(key)
	send, ok := sseStart(w)
	if !ok {
		return nil
	}
	ctx := r.Context()
	deadline := time.NewTimer(resolveTimeout)
	defer deadline.Stop()
	poll := time.NewTicker(resolvePoll)
	defer poll.Stop()
	beat := time.NewTicker(25 * time.Second)
	defer beat.Stop()
	for {
		if ct := s.knownTorrent(ctx, ih); ct != nil {
			ev, data := s.resolveResult(ctx, r, ih, ct)
			send(ev, data)
			return nil
		}
		select {
		case <-ctx.Done():
			return nil
		case <-deadline.C:
			send("timeout", map[string]any{})
			return nil
		case <-beat.C:
			if !send("", nil) {
				return nil
			}
		case <-poll.C:
		}
	}
}

// resolveResult decides a freshly resolved hash for the requesting user.
func (s *Server) resolveResult(ctx context.Context, r *http.Request, ih string, ct *meta.CatalogTorrent) (string, any) {
	s.decideNow(ctx, ih)
	if tm, err := s.Meta.GetTorrentMap(ctx, ih); err == nil {
		if path := mappedEpisodePath(tm); path != "" {
			return "redirect", map[string]string{"path": path}
		}
	}
	u := userOf(r)
	t := catalog.FromCatalog(ct)
	d, err := s.playDecision(ctx, u, t.Subject())
	if err != nil {
		s.Log.Warn("resolve: policy", "ih", ih, "err", err)
		return "denied", map[string]string{"action": policy.Block, "reason": "暂时无法判定播放权限，请稍后重试"}
	}
	if !d.Allowed {
		s.St.AddPlay(ctx, store.Play{Infohash: ih, Title: t.Title, IP: httpx.ClientIP(r), Action: d.Action}, uid(u))
		return "denied", map[string]string{"action": d.Action, "reason": d.Reason}
	}
	return "ready", map[string]any{}
}

// decideNow gives the decision model up to resolveDecisionBudget for the uncertain files of
// a just-ingested torrent (the job keeps running in the background beyond that).
func (s *Server) decideNow(ctx context.Context, ih string) {
	if s.Clef == nil {
		return
	}
	done := make(chan struct{})
	s.bg.Go(func() {
		defer close(done)
		jctx, cancel := context.WithTimeout(s.BaseCtx, 3*time.Minute)
		defer cancel()
		if err := s.Clef.ProcessNow(jctx, ih); err != nil && jctx.Err() == nil {
			s.Log.Warn("resolve: decision model", "ih", ih, "err", err)
		}
	})
	t := time.NewTimer(resolveDecisionBudget)
	defer t.Stop()
	select {
	case <-done:
	case <-t.C:
	case <-ctx.Done():
	}
}

// mappedEpisodePath is the episode page of the torrent's first mapped file (applied
// mappings only; main episodes first): /subject/{bgm|id}/{id}/episode/{key}/hash/{ih}/file/{idx}.
func mappedEpisodePath(tm *meta.TorrentMap) string {
	files := tm.Files[:0:0]
	for _, f := range tm.Files {
		if f.Status != "rejected" {
			files = append(files, f)
		}
	}
	m := playMetaOf(&meta.TorrentMap{Files: files}, -1)
	if m == nil {
		return ""
	}
	provider, id := "id", fmt.Sprint(m.SubjectID)
	if m.BgmID != "" && m.BgmID != "0" {
		provider, id = "bgm", m.BgmID
	}
	return fmt.Sprintf("/subject/%s/%s/episode/%s/hash/%s/file/%d", provider, url.PathEscape(id), url.PathEscape(m.Key), tm.Infohash, m.FileIndex)
}

// sseStart sets up a long-lived Server-Sent Events response. send(event, data) writes one
// event (event "" = a heartbeat comment) and reports whether the client is still there.
func sseStart(w http.ResponseWriter) (send func(event string, data any) bool, ok bool) {
	rc := http.NewResponseController(w)
	_ = rc.SetWriteDeadline(time.Time{}) // long-lived stream: no server write timeout
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache, no-transform")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	send = func(event string, data any) bool {
		var err error
		if event == "" {
			_, err = io.WriteString(w, ": ping\n\n")
		} else {
			b, _ := json.Marshal(data)
			_, err = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, b)
		}
		return err == nil && rc.Flush() == nil
	}
	if _, err := io.WriteString(w, "retry: 15000\n\n"); err != nil {
		return send, false
	}
	_ = rc.Flush()
	return send, true
}
