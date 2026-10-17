package qb

import (
	"context"
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/darknightlab/onlinetorrent/internal/magnet"
	"github.com/darknightlab/onlinetorrent/internal/store"
)

// Status is the live state of a server.
type Status struct {
	Online      bool   `json:"online"`
	Version     string `json:"version"`
	Libtorrent  string `json:"libtorrent"`
	LastError   string `json:"last_error"`
	LastCheckAt string `json:"last_check_at"`
	Torrents    int    `json:"torrents"`
}

// Server is a configured qB instance plus status. Cfg is immutable after creation
// (Reload builds new Server values), so it can be read without locking.
type Server struct {
	Cfg    store.QBServer
	Client *Client

	mu     sync.RWMutex
	status Status
}

func (s *Server) Status() Status {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.status
}

func (s *Server) setStatus(st Status) {
	s.mu.Lock()
	s.status = st
	s.mu.Unlock()
}

// Pool manages all configured servers.
type Pool struct {
	st  *store.Store
	log *slog.Logger

	mu      sync.RWMutex
	servers []*Server
}

// NewPool creates a pool; call Reload to load servers.
func NewPool(st *store.Store, log *slog.Logger) *Pool { return &Pool{st: st, log: log} }

// Reload rebuilds servers from the DB, keeping status for unchanged ones.
func (p *Pool) Reload(ctx context.Context) error {
	cfgs, err := p.st.ListQBServers(ctx)
	if err != nil {
		return err
	}
	p.mu.Lock()
	old := map[int64]*Server{}
	for _, s := range p.servers {
		old[s.Cfg.ID] = s
	}
	var next []*Server
	for _, c := range cfgs {
		if o, ok := old[c.ID]; ok && o.Cfg.URL == c.URL && o.Cfg.Username == c.Username && o.Cfg.Password == c.Password && o.Cfg.APIKey == c.APIKey {
			// same credentials: keep the logged-in client and last status, but never mutate
			// the live Server (other goroutines read Cfg without locks)
			next = append(next, &Server{Cfg: c, Client: o.Client, status: o.Status()})
			continue
		}
		next = append(next, &Server{Cfg: c, Client: NewClient(c.URL, c.Username, c.Password, c.APIKey)})
	}
	p.servers = next
	p.mu.Unlock()
	go p.CheckAll(context.Background())
	return nil
}

// Servers returns a snapshot.
func (p *Pool) Servers() []*Server {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return append([]*Server(nil), p.servers...)
}

// Get returns a server by id.
func (p *Pool) Get(id int64) *Server {
	for _, s := range p.Servers() {
		if s.Cfg.ID == id {
			return s
		}
	}
	return nil
}

// Check probes one server.
func (p *Pool) Check(ctx context.Context, s *Server) Status {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	st := Status{LastCheckAt: store.Now()}
	v, err := s.Client.Version(ctx)
	if err != nil {
		st.LastError = err.Error()
		s.setStatus(st)
		return st
	}
	st.Online, st.Version = true, v
	if bi, err := s.Client.BuildInfo(ctx); err == nil {
		if lt, ok := bi["libtorrent"].(string); ok {
			st.Libtorrent = lt
		}
	}
	_ = s.Client.CreateCategory(ctx, s.Cfg.Category)
	if ts, err := s.Client.Info(ctx, s.Cfg.Category); err == nil {
		st.Torrents = len(ts)
	}
	s.setStatus(st)
	return st
}

// CheckAll probes every enabled server concurrently.
func (p *Pool) CheckAll(ctx context.Context) {
	var wg sync.WaitGroup
	for _, s := range p.Servers() {
		if !s.Cfg.Enabled {
			s.setStatus(Status{LastCheckAt: store.Now(), LastError: "已停用"})
			continue
		}
		wg.Add(1)
		go func() { defer wg.Done(); p.Check(ctx, s) }()
	}
	wg.Wait()
}

// PushResult is the outcome of pushing a torrent.
type PushResult struct {
	SeededBy []string          `json:"seeded_by"`
	Errors   map[string]string `json:"errors,omitempty"`
}

// Push adds a torrent to every online, enabled server and, if configured per server,
// adds the wss trackers afterwards (qB ≥ 5.3.0rc1 strips ws/wss from magnets, PR #24512).
// It is detached from ctx cancellation (a client disconnect must not abort between Add
// and AddTrackers); timeout bounds each server.
func (p *Pool) Push(ctx context.Context, ih, name string, trackers []string, timeout time.Duration) PushResult {
	ctx = context.WithoutCancel(ctx)
	res := PushResult{Errors: map[string]string{}}
	var mu sync.Mutex
	var wg sync.WaitGroup
	uri := magnet.Build(ih, name, trackers)
	var wss []string
	for _, t := range trackers {
		if magnet.IsWebSocket(t) {
			wss = append(wss, t)
		}
	}
	for _, s := range p.Servers() {
		if !s.Cfg.Enabled || !s.Status().Online {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			cctx, cancel := context.WithTimeout(ctx, timeout)
			defer cancel()
			err := s.Client.Add(cctx, AddParams{URLs: uri, Category: s.Cfg.Category, SeedingTimeLimitMin: s.Cfg.SeedingTimeLimitMin, RatioLimit: s.Cfg.RatioLimit})
			if err == nil && s.Cfg.AddWSSTrackers && len(wss) > 0 {
				// the torrent may need a moment to appear
				for i := 0; i < 3; i++ {
					if err2 := s.Client.AddTrackers(cctx, ih, wss); err2 == nil {
						break
					} else if i == 2 {
						p.log.Warn("addTrackers failed", "server", s.Cfg.Name, "hash", ih, "err", err2)
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

// Remove deletes a torrent (with files) from every server.
func (p *Pool) Remove(ctx context.Context, ih string) {
	for _, s := range p.Servers() {
		if s.Status().Online {
			if err := s.Client.Delete(ctx, []string{ih}); err != nil {
				p.log.Warn("delete failed", "server", s.Cfg.Name, "err", err)
			}
		}
	}
}

// Metadata returns the name and files of a torrent from the first server that has it.
func (p *Pool) Metadata(ctx context.Context, ih string) (string, []File, bool) {
	for _, s := range p.Servers() {
		if !s.Status().Online {
			continue
		}
		ts, err := s.Client.Info(ctx, "", ih)
		if err != nil || len(ts) == 0 || !ts[0].HasMetadata {
			continue
		}
		files, err := s.Client.Files(ctx, ih)
		if err != nil || len(files) == 0 {
			continue
		}
		return ts[0].Name, files, true
	}
	return "", nil, false
}

// staleAfter: torrents in our category still incomplete (or without metadata) after this
// long are dropped — nobody is watching them any more and they only hold disk/slots.
const staleAfter = 48 * time.Hour

// Cleanup deletes, in our category, finished torrents (seeding limit reached → stopped)
// and stale ones (added > 48h ago with progress < 1 or no metadata).
func (p *Pool) Cleanup(ctx context.Context) {
	stale := time.Now().Add(-staleAfter).Unix()
	for _, s := range p.Servers() {
		if !s.Cfg.Enabled || !s.Status().Online {
			continue
		}
		ts, err := s.Client.Info(ctx, s.Cfg.Category)
		if err != nil {
			continue
		}
		var del []string
		for _, t := range ts {
			if t.Category != s.Cfg.Category {
				continue
			}
			finished := t.Progress >= 1 && (t.State == "stoppedUP" || t.State == "pausedUP")
			old := t.AddedOn > 0 && t.AddedOn < stale && (t.Progress < 1 || !t.HasMetadata)
			if finished || old {
				del = append(del, t.Hash)
			}
		}
		if len(del) > 0 {
			if err := s.Client.Delete(ctx, del); err != nil {
				p.log.Warn("cleanup failed", "server", s.Cfg.Name, "err", err)
			} else {
				p.log.Info("cleanup", "server", s.Cfg.Name, "deleted", len(del))
			}
		}
	}
}

// Run starts background health and cleanup loops until ctx is done.
func (p *Pool) Run(ctx context.Context) {
	health := time.NewTicker(time.Minute)
	clean := time.NewTicker(5 * time.Minute)
	defer health.Stop()
	defer clean.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-health.C:
			p.CheckAll(ctx)
		case <-clean.C:
			if p.st.Bool(store.KeyCleanupEnabled) {
				p.Cleanup(ctx)
			}
		}
	}
}

// OnlineCount returns (online, total enabled).
func (p *Pool) OnlineCount() (int, int) {
	on, total := 0, 0
	for _, s := range p.Servers() {
		if !s.Cfg.Enabled {
			continue
		}
		total++
		if s.Status().Online {
			on++
		}
	}
	return on, total
}
