package qb

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/darknightlab/onlinetorrent/internal/magnet"
	"github.com/darknightlab/onlinetorrent/internal/store"
)

// MetaCategory is the qB category used for metadata-only fetches of crawled magnets
// (torrents there are stopped after metadata arrives and deleted by the crawler).
func MetaCategory(category string) string { return category + "-meta" }

// MetaCategory of this server.
func (s *Server) MetaCategory() string { return MetaCategory(s.Cfg.Category) }

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

	// EdgeTransport, when set, carries the WebUI API of servers configured with EdgeAPI
	// through their edge node's tunnel (internal/edge.Hub.Transport).
	EdgeTransport func(serverID int64) http.RoundTripper

	mu      sync.RWMutex
	servers []*Server

	progMu      sync.Mutex
	prog        map[string]float64 // infohash → best progress over online servers
	progAt      time.Time
	progLoading bool
}

// progressTTL: how long a torrents/info snapshot is reused.
const progressTTL = time.Minute

// Progress returns infohash → best download progress (0…1) over every enabled, online
// server (all categories: a torrent seeded there serves WebRTC peers and WebSeed either
// way), refreshing the snapshot when it is older than a minute. nil if no server answered.
func (p *Pool) Progress(ctx context.Context) map[string]float64 {
	p.progMu.Lock()
	if p.prog != nil && time.Since(p.progAt) < progressTTL {
		m := p.prog
		p.progMu.Unlock()
		return m
	}
	p.progMu.Unlock()
	return p.loadProgress(ctx)
}

// ProgressCached returns the last snapshot without blocking (ok = there is one) and
// refreshes it in the background when stale.
func (p *Pool) ProgressCached() (map[string]float64, bool) {
	p.progMu.Lock()
	m, at, loading := p.prog, p.progAt, p.progLoading
	if time.Since(at) >= progressTTL && !loading {
		p.progLoading = true
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			p.loadProgress(ctx)
		}()
	}
	p.progMu.Unlock()
	return m, m != nil
}

func (p *Pool) loadProgress(ctx context.Context) map[string]float64 {
	defer func() {
		p.progMu.Lock()
		p.progLoading = false
		p.progMu.Unlock()
	}()
	var m map[string]float64
	for _, s := range p.Servers() {
		if !s.Cfg.Enabled || !s.Status().Online {
			continue
		}
		cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		ts, err := s.Client.Info(cctx, "")
		cancel()
		if err != nil {
			continue
		}
		if m == nil {
			m = map[string]float64{}
		}
		for _, t := range ts {
			h := strings.ToLower(t.Hash)
			if t.HasMetadata || t.Progress > 0 {
				m[h] = max(m[h], t.Progress)
			} else if _, ok := m[h]; !ok {
				m[h] = 0
			}
		}
	}
	if m == nil {
		return nil
	}
	p.progMu.Lock()
	p.prog, p.progAt = m, time.Now()
	p.progMu.Unlock()
	return m
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
		if o, ok := old[c.ID]; ok && o.Cfg.URL == c.URL && o.Cfg.Username == c.Username && o.Cfg.Password == c.Password && o.Cfg.APIKey == c.APIKey && o.Cfg.EdgeAPI == c.EdgeAPI {
			// same credentials: keep the logged-in client and last status, but never mutate
			// the live Server (other goroutines read Cfg without locks)
			next = append(next, &Server{Cfg: c, Client: o.Client, status: o.Status()})
			continue
		}
		client := NewClient(c.URL, c.Username, c.Password, c.APIKey)
		if c.EdgeAPI && p.EdgeTransport != nil {
			client = NewClientTransport(c.URL, c.Username, c.Password, c.APIKey, p.EdgeTransport(c.ID))
		}
		next = append(next, &Server{Cfg: c, Client: client})
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
	_ = s.Client.CreateCategory(ctx, s.MetaCategory())
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

// Push adds a torrent to every online, enabled server, then adds the trackers with
// torrents/addTrackers (qB skips ones the torrent already has): this also gives a torrent
// that was already in qB the missing trackers. wss trackers are only added this way when
// the server's add_wss_trackers is on (qB ≥ 5.3.0rc1 strips ws/wss from magnets, PR #24512).
// It is detached from ctx cancellation (a client disconnect must not abort between Add
// and AddTrackers); timeout bounds each server.
func (p *Pool) Push(ctx context.Context, ih, name string, trackers []string, timeout time.Duration) PushResult {
	ctx = context.WithoutCancel(ctx)
	res := PushResult{Errors: map[string]string{}}
	var mu sync.Mutex
	var wg sync.WaitGroup
	uri := magnet.Build(ih, name, trackers)
	var wss, other []string
	for _, t := range trackers {
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
			cctx, cancel := context.WithTimeout(ctx, timeout)
			defer cancel()
			err := s.Client.Add(cctx, AddParams{URLs: uri, Category: s.Cfg.Category, SeedingTimeLimitMin: s.Cfg.SeedingTimeLimitMin, RatioLimit: s.Cfg.RatioLimit})
			if err == nil {
				p.adoptMeta(cctx, s, ih)
			}
			add := other
			if s.Cfg.AddWSSTrackers {
				add = append(append([]string(nil), wss...), other...)
			}
			if err == nil && len(add) > 0 {
				// the torrent may need a moment to appear
				for i := 0; i < 3; i++ {
					if err2 := s.Client.AddTrackers(cctx, ih, add); err2 == nil {
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

// adoptMeta: if the torrent already sits in our metadata category (a crawler metadata
// fetch, stopped after metadata arrived), Add was a no-op — move it to the seeding
// category, apply the share limits and start it, so the crawler no longer deletes it.
func (p *Pool) adoptMeta(ctx context.Context, s *Server, ih string) {
	ts, err := s.Client.Info(ctx, "", ih)
	if err != nil || len(ts) == 0 || ts[0].Category != s.MetaCategory() {
		return
	}
	if err := s.Client.SetCategory(ctx, ih, s.Cfg.Category); err != nil {
		p.log.Warn("adopt meta torrent: setCategory", "server", s.Cfg.Name, "hash", ih, "err", err)
		return
	}
	_ = s.Client.SetShareLimits(ctx, ih, s.Cfg.RatioLimit, s.Cfg.SeedingTimeLimitMin)
	if err := s.Client.Start(ctx, ih); err != nil {
		p.log.Warn("adopt meta torrent: start", "server", s.Cfg.Name, "hash", ih, "err", err)
	}
	p.log.Info("adopted metadata torrent for seeding", "server", s.Cfg.Name, "hash", ih)
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

// Export returns the .torrent of ih from the first online server that has its metadata.
func (p *Pool) Export(ctx context.Context, ih string) ([]byte, error) {
	err := ErrNoTorrent
	for _, s := range p.Servers() {
		if !s.Cfg.Enabled || !s.Status().Online {
			continue
		}
		b, e := s.Client.Export(ctx, ih)
		if e == nil {
			return b, nil
		}
		if !errors.Is(e, ErrNoTorrent) {
			err = e
		}
		if ctx.Err() != nil {
			break
		}
	}
	return nil, err
}

// HasMetadata reports whether some online server holds ih with metadata (one torrents/info
// call per server).
func (p *Pool) HasMetadata(ctx context.Context, ih string) bool {
	for _, s := range p.Servers() {
		if !s.Cfg.Enabled || !s.Status().Online {
			continue
		}
		if ts, err := s.Client.Info(ctx, "", ih); err == nil && len(ts) > 0 && ts[0].HasMetadata {
			return true
		}
		if ctx.Err() != nil {
			return false
		}
	}
	return false
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

type filesEntry struct {
	files []File
	exp   time.Time
}

var filesCache sync.Map // serverID|hash -> filesEntry

// FindFile locates a fully downloaded file of torrent ih whose path matches reqPath
// (BEP19 webseed request path). An empty reqPath selects the only file of a
// single-file torrent. It returns the server holding it.
func (p *Pool) FindFile(ctx context.Context, ih, reqPath string) (*Server, File, bool) {
	reqPath = strings.Trim(reqPath, "/")
	for _, s := range p.Servers() {
		if !s.Cfg.Enabled || !s.Status().Online {
			continue
		}
		key := fmt.Sprintf("%d|%s", s.Cfg.ID, ih)
		var files []File
		if e, ok := filesCache.Load(key); ok && time.Now().Before(e.(filesEntry).exp) {
			files = e.(filesEntry).files
		} else {
			cctx, cancel := context.WithTimeout(ctx, 8*time.Second)
			fs, err := s.Client.Files(cctx, ih)
			cancel()
			if err != nil {
				continue
			}
			files = fs
			filesCache.Store(key, filesEntry{files, time.Now().Add(20 * time.Second)})
		}
		for _, f := range files {
			if f.Progress < 1 {
				continue
			}
			name := strings.Trim(f.Name, "/")
			if (reqPath == "" && len(files) == 1) || name == reqPath || strings.HasSuffix(name, "/"+reqPath) || strings.HasSuffix(reqPath, "/"+name) {
				return s, f, true
			}
		}
	}
	return nil, File{}, false
}

var progressCache sync.Map // serverID|hash -> filesEntry (short TTL, shared by webseed watchers)

// FileProgress reports whether file idx (idx < 0: the only file of a single-file torrent,
// or any file of a multi-file torrent) is fully downloaded on some online server, plus the
// best progress seen. found is false when no server has the torrent at all.
func (p *Pool) FileProgress(ctx context.Context, ih string, idx int) (ready bool, progress float64, found bool) {
	for _, s := range p.Servers() {
		if !s.Cfg.Enabled || !s.Status().Online {
			continue
		}
		key := fmt.Sprintf("%d|%s", s.Cfg.ID, ih)
		var files []File
		if e, ok := progressCache.Load(key); ok && time.Now().Before(e.(filesEntry).exp) {
			files = e.(filesEntry).files
		} else {
			cctx, cancel := context.WithTimeout(ctx, 8*time.Second)
			fs, err := s.Client.Files(cctx, ih)
			cancel()
			if err != nil {
				continue
			}
			files = fs
			progressCache.Store(key, filesEntry{files, time.Now().Add(4 * time.Second)})
		}
		if len(files) == 0 {
			// torrent present but metadata not fetched yet
			found = true
			continue
		}
		found = true
		for _, f := range files {
			if idx >= 0 && f.Index != idx {
				continue
			}
			if idx < 0 && len(files) > 1 && f.Progress < 1 {
				progress = max(progress, f.Progress)
				continue
			}
			progress = max(progress, f.Progress)
			if f.Progress >= 1 {
				return true, 1, true
			}
		}
	}
	return false, progress, found
}
