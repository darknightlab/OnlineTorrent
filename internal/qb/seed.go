package qb

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/darknightlab/onlinetorrent/internal/magnet"
)

// SeedRequest asks the pool to download and seed some files of a torrent.
type SeedRequest struct {
	Infohash string
	Name     string
	Trackers []string
	// Torrent is the .torrent (preferred: added stopped, metadata at once); nil = magnet with
	// stopCondition=MetadataReceived and priorities applied once the metadata arrives.
	Torrent []byte
	// Want are torrent paths ("Name/sub/file", as in the .torrent) to download; other files
	// of a torrent otd adds are skipped (priority 0). Existing selections only grow.
	Want    []string
	Timeout time.Duration // per server
}

// PushResult reports which servers accepted a seed request.
type PushResult struct {
	SeededBy []string          `json:"seeded_by"`
	Errors   map[string]string `json:"errors,omitempty"`
}

// pendingWait bounds the wait for metadata of a magnet-added torrent.
const pendingWait = 30 * time.Minute

// seedJob serialises file selection per (server, torrent) and merges wanted paths of
// requests that arrive while the metadata of a magnet-added torrent is still missing.
type seedJob struct {
	mu      sync.Mutex
	want    map[string]bool
	waiting bool // a goroutine is waiting for metadata
}

func (p *Pool) job(s *Server, ih string) *seedJob {
	p.jobsMu.Lock()
	defer p.jobsMu.Unlock()
	if p.jobs == nil {
		p.jobs = map[string]*seedJob{}
	}
	k := fmt.Sprintf("%d|%s", s.Cfg.ID, ih)
	j := p.jobs[k]
	if j == nil {
		j = &seedJob{want: map[string]bool{}}
		p.jobs[k] = j
	}
	return j
}

func (p *Pool) hasJob(s *Server, ih string) bool {
	p.jobsMu.Lock()
	defer p.jobsMu.Unlock()
	_, ok := p.jobs[fmt.Sprintf("%d|%s", s.Cfg.ID, ih)]
	return ok
}

// lockJob returns the current job of (s, ih), locked. A job dropped between lookup and
// locking is retried, so at most one job per (server, torrent) applies selections.
func (p *Pool) lockJob(s *Server, ih string) *seedJob {
	for {
		j := p.job(s, ih)
		j.mu.Lock()
		p.jobsMu.Lock()
		cur := p.jobs[fmt.Sprintf("%d|%s", s.Cfg.ID, ih)] == j
		p.jobsMu.Unlock()
		if cur {
			return j
		}
		j.mu.Unlock()
	}
}

// dropJob forgets a finished job (caller may hold j.mu; lock order is j.mu → jobsMu).
func (p *Pool) dropJob(s *Server, ih string, j *seedJob) {
	p.jobsMu.Lock()
	defer p.jobsMu.Unlock()
	k := fmt.Sprintf("%d|%s", s.Cfg.ID, ih)
	if p.jobs[k] == j {
		delete(p.jobs, k)
	}
}

// Seed adds the torrent to every enabled, online server (or extends its file selection) so
// that it downloads only req.Want. Then torrents/addTrackers (qB skips ones the torrent
// already has; wss only when the server's add_wss_trackers is on — qB ≥ 5.3.0rc1 strips
// ws/wss from magnets, PR #24512). Detached from ctx cancellation (a client disconnect must
// not abort halfway); req.Timeout bounds each server.
func (p *Pool) Seed(ctx context.Context, req SeedRequest) PushResult {
	ctx = context.WithoutCancel(ctx)
	res := PushResult{Errors: map[string]string{}}
	var mu sync.Mutex
	var wg sync.WaitGroup
	var wss, other []string
	for _, t := range req.Trackers {
		if magnet.IsWebSocket(t) {
			wss = append(wss, t)
		} else {
			other = append(other, t)
		}
	}
	for _, s := range p.Servers() {
		if !s.Cfg.Enabled || !s.Status().Online {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			cctx, cancel := context.WithTimeout(ctx, req.Timeout)
			defer cancel()
			err := p.seedOn(cctx, s, req)
			add := other
			if s.Cfg.AddWSSTrackers {
				add = append(append([]string(nil), wss...), other...)
			}
			if err == nil && len(add) > 0 {
				// the torrent may need a moment to appear
				for i := 0; i < 3; i++ {
					if err2 := s.Client.AddTrackers(cctx, req.Infohash, add); err2 == nil {
						break
					} else if i == 2 {
						p.log.Warn("addTrackers failed", "server", s.Cfg.Name, "hash", req.Infohash, "err", err2)
					}
					select {
					case <-cctx.Done():
					case <-time.After(700 * time.Millisecond):
					}
				}
			}
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				res.Errors[s.Cfg.Name] = err.Error()
				return
			}
			res.SeededBy = append(res.SeededBy, s.Cfg.Name)
		}()
	}
	wg.Wait()
	sort.Strings(res.SeededBy)
	if res.SeededBy == nil {
		res.SeededBy = []string{}
	}
	return res
}

// IsStopped reports a stopped (qB 5) / paused (qB 4) state.
func IsStopped(state string) bool {
	return strings.HasPrefix(state, "stopped") || strings.HasPrefix(state, "paused")
}

// seedOn handles one server.
func (p *Pool) seedOn(ctx context.Context, s *Server, req SeedRequest) error {
	ih := req.Infohash
	j := p.lockJob(s, ih)
	defer func() {
		if !j.waiting {
			p.dropJob(s, ih, j) // the selection is in qB now (Wanted reads it from there)
		}
		j.mu.Unlock()
	}()
	for _, w := range req.Want {
		j.want[w] = true
	}
	ts, err := s.Client.Info(ctx, "", ih)
	if err != nil {
		return err
	}
	if len(ts) == 0 {
		return p.addNew(ctx, s, req, j)
	}
	t := ts[0]
	switch {
	case t.Category == s.MetaCategory():
		// a crawler metadata fetch (stopCondition=MetadataReceived, nothing downloaded): adopt it
		// for seeding with the initial selection, so the crawler no longer deletes it
		if err := s.Client.SetCategory(ctx, ih, s.Cfg.Category); err != nil {
			return fmt.Errorf("adopt metadata torrent: %w", err)
		}
		_ = s.Client.SetShareLimits(ctx, ih, s.Cfg.RatioLimit, s.Cfg.SeedingTimeLimitMin)
		p.log.Info("adopted metadata torrent for seeding", "server", s.Cfg.Name, "hash", ih)
		if !t.HasMetadata {
			// pending: after an otd restart the next request still applies the initial selection
			_ = s.Client.AddTags(ctx, ih, PendingTag)
			return p.waitAndApply(s, ih, j, true) // its stop condition holds it until then
		}
		return p.applyAndStart(ctx, s, ih, j, true, true)
	case t.HasTag(PendingTag):
		// added by otd, selection not applied yet (metadata missing, or otd restarted)
		if !t.HasMetadata {
			return p.waitAndApply(s, ih, j, true)
		}
		return p.applyAndStart(ctx, s, ih, j, true, true)
	case !t.HasMetadata:
		// added by someone else / older otd while metadata is missing: raise once it arrives
		return p.waitAndApply(s, ih, j, false)
	default:
		// already seeding: only raise newly wanted files; start only then (a torrent stopped by
		// its seeding limit is not restarted for files it already has)
		return p.applyAndStart(ctx, s, ih, j, false, false)
	}
}

// addNew adds the torrent stopped (.torrent) or held at metadata (magnet), tagged pending.
func (p *Pool) addNew(ctx context.Context, s *Server, req SeedRequest, j *seedJob) error {
	params := AddParams{Category: s.Cfg.Category, SeedingTimeLimitMin: s.Cfg.SeedingTimeLimitMin, RatioLimit: s.Cfg.RatioLimit, Tags: PendingTag}
	if req.Torrent != nil {
		params.Stopped = true
		if err := s.Client.AddTorrent(ctx, req.Torrent, params); err != nil {
			return err
		}
		s.Client.LearnRecent(ctx) // a hybrid torrent is known by its v2 id at once (ids.go)
		// qB lists it right away (with metadata); retry briefly while it is being registered
		var err error
		for i := 0; i < 10; i++ {
			if err = p.applyAndStart(ctx, s, req.Infohash, j, true, true); !errors.Is(err, ErrNoFiles) && !errors.Is(err, ErrNoTorrent) && !isMissing(err) {
				return err
			}
			s.Client.LearnRecent(ctx)
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(300 * time.Millisecond):
			}
		}
		return err
	}
	params.URLs = magnet.Build(req.Infohash, req.Name, req.Trackers)
	params.StopCondition = "MetadataReceived"
	if err := s.Client.Add(ctx, params); err != nil {
		return err
	}
	return p.waitAndApply(s, req.Infohash, j, true)
}

func isMissing(err error) bool {
	return err != nil && strings.Contains(err.Error(), "HTTP 404")
}

// applyAndStart applies j.want (caller holds j.mu); starts the torrent when start is set or a
// priority was raised; clears the pending tag.
func (p *Pool) applyAndStart(ctx context.Context, s *Server, ih string, j *seedJob, initial, start bool) error {
	raised, err := s.Client.ApplyWanted(ctx, ih, wantList(j), initial)
	if err != nil {
		return err
	}
	if start || raised {
		if err := s.Client.Start(ctx, ih); err != nil {
			return fmt.Errorf("start: %w", err)
		}
	}
	if initial {
		if err := s.Client.RemoveTags(ctx, ih, PendingTag); err != nil {
			p.log.Warn("remove pending tag", "server", s.Cfg.Name, "hash", ih, "err", err)
		}
	}
	return nil
}

func wantList(j *seedJob) []string {
	out := make([]string, 0, len(j.want))
	for w := range j.want {
		out = append(out, w)
	}
	sort.Strings(out)
	return out
}

// waitAndApply applies the selection once qB has the metadata (in the background; requests
// arriving meanwhile only add to j.want). Caller holds j.mu.
func (p *Pool) waitAndApply(s *Server, ih string, j *seedJob, initial bool) error {
	if j.waiting {
		return nil
	}
	j.waiting = true
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), pendingWait)
		defer cancel()
		gone := 0
		for {
			select {
			case <-ctx.Done():
				p.log.Warn("seed: no metadata, selection not applied", "server", s.Cfg.Name, "hash", ih)
				j.mu.Lock()
				j.waiting = false
				p.dropJob(s, ih, j)
				j.mu.Unlock()
				return
			case <-time.After(2 * time.Second):
			}
			ts, err := s.Client.Info(ctx, "", ih)
			if err == nil && len(ts) > 0 {
				gone = 0
			}
			if err != nil || len(ts) == 0 || !ts[0].HasMetadata {
				if err == nil && len(ts) == 0 {
					if gone++; gone < 5 { // e.g. a hybrid torrent's id just switched (ids.go)
						continue
					}
					j.mu.Lock()
					j.waiting = false
					p.dropJob(s, ih, j)
					j.mu.Unlock()
					return // deleted meanwhile
				}
				continue
			}
			j.mu.Lock()
			err = p.applyAndStart(ctx, s, ih, j, initial, true)
			j.waiting = false
			p.dropJob(s, ih, j)
			j.mu.Unlock()
			if err != nil {
				p.log.Warn("seed: apply file selection", "server", s.Cfg.Name, "hash", ih, "err", err)
			}
			return
		}
	}()
	return nil
}

// Wanted returns the torrent paths some server downloads or is about to (pending selections
// included). A torrent whose selection is not applied yet counts only its pending wants
// (qB's default priorities of a just-added torrent are not a selection).
func (p *Pool) Wanted(ctx context.Context, ih string) map[string]bool {
	out := map[string]bool{}
	var jobs []*seedJob
	p.jobsMu.Lock()
	for k, j := range p.jobs {
		if strings.HasSuffix(k, "|"+ih) {
			jobs = append(jobs, j)
		}
	}
	p.jobsMu.Unlock()
	for _, j := range jobs {
		j.mu.Lock()
		for w := range j.want {
			out[w] = true
		}
		j.mu.Unlock()
	}
	for _, s := range p.Servers() {
		if !s.Cfg.Enabled || !s.Status().Online {
			continue
		}
		cctx, cancel := context.WithTimeout(ctx, 8*time.Second)
		ts, err := s.Client.Info(cctx, "", ih)
		if err == nil && len(ts) > 0 && ts[0].HasMetadata && ts[0].Category == s.Cfg.Category && !ts[0].HasTag(PendingTag) {
			if files, err := s.Client.Files(cctx, ih); err == nil {
				for _, f := range WantedFiles(files) {
					out[strings.Trim(f.Name, "/")] = true
				}
			}
		}
		cancel()
	}
	return out
}
