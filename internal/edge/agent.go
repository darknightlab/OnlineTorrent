package edge

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"mime"
	"net"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/net/websocket"

	"github.com/darknightlab/onlinetorrent/internal/edgetoken"
	"github.com/darknightlab/onlinetorrent/internal/qb"
	"github.com/darknightlab/onlinetorrent/internal/seedsrc"
	"github.com/darknightlab/onlinetorrent/internal/tunnel"
)

// Config of an edge node (`otd edge`, see cmd/otd for the environment variables).
type Config struct {
	Main   string // wss://www.projectk.org/api/v1/edge/connect
	Token  string
	QBURL  string // http://127.0.0.1:8888
	QBUser string
	QBPass string

	DataRoot string      // only files below are served (default /Data/Torrent)
	PathMap  [][2]string // qB path prefix → local prefix
	Verify   bool        // SHA-1 check of whole pieces

	Listen      string // plain HTTP listen address, e.g. ":8890" (TLS is terminated by Cloudflare)
	TrustProxy  bool   // client IP from CF-Connecting-IP (only behind Cloudflare)
	AllowOrigin string // CORS origin override (default: the site origin reported by the main server)

	MaxConcurrent int // concurrent webseed reads (default 64)
	Tunnel        tunnel.Options
	Version       string
	Host          string
	Log           *slog.Logger
}

// Agent is a running edge node.
type Agent struct {
	cfg  Config
	disk *Disk
	pass *http.Client
	log  *slog.Logger

	mu     sync.Mutex
	sess   *tunnel.Session
	origin string
	edgeID int64
	keys   []ed25519.PublicKey // token-verification keys from the main server (current first)

	bytes  atomic.Int64
	reqs   atomic.Int64
	sem    chan struct{}
	limits *ipLimiter
}

// BuildVersion is the VCS revision of this binary (for the admin UI).
func BuildVersion() string {
	if bi, ok := debug.ReadBuildInfo(); ok {
		for _, s := range bi.Settings {
			if s.Key == "vcs.revision" && len(s.Value) >= 12 {
				return s.Value[:12]
			}
		}
		return bi.Main.Version
	}
	return "dev"
}

// New creates an agent (nothing runs until Run).
func New(cfg Config) (*Agent, error) {
	if cfg.Main == "" || cfg.Token == "" || cfg.QBURL == "" {
		return nil, errors.New("edge: OTD_MAIN, OTD_EDGE_TOKEN and QB_URL are required")
	}
	if cfg.DataRoot == "" {
		cfg.DataRoot = "/Data/Torrent"
	}
	if cfg.Listen == "" {
		cfg.Listen = ":8890"
	}
	if cfg.MaxConcurrent <= 0 {
		cfg.MaxConcurrent = 64
	}
	if cfg.Log == nil {
		cfg.Log = slog.Default()
	}
	if cfg.Version == "" {
		cfg.Version = BuildVersion()
	}
	cfg.QBURL = strings.TrimRight(cfg.QBURL, "/")
	a := &Agent{cfg: cfg, log: cfg.Log, sem: make(chan struct{}, cfg.MaxConcurrent), limits: &ipLimiter{m: map[string]*bucket{}}}
	a.disk = &Disk{QB: qb.NewClient(cfg.QBURL, cfg.QBUser, cfg.QBPass, ""), Root: cfg.DataRoot, PathMap: cfg.PathMap, Verify: cfg.Verify}
	// passthrough: no cookie jar (the main server's qB client manages the login cookie),
	// no redirects, no overall timeout (downloadFile streams)
	a.pass = &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		Transport: &http.Transport{Proxy: nil, ResponseHeaderTimeout: 60 * time.Second, MaxIdleConnsPerHost: 16}}
	return a, nil
}

// Disk exposes the disk server (tests).
func (a *Agent) Disk() *Disk { return a.disk }

// Run serves HTTP and keeps the tunnel to the main server until ctx is done.
func (a *Agent) Run(ctx context.Context) error {
	hs := &http.Server{Addr: a.cfg.Listen, Handler: a.Handler(), ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 120 * time.Second}
	errc := make(chan error, 1)
	go func() {
		a.log.Info("edge http listening", "addr", a.cfg.Listen)
		if err := hs.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errc <- err
		}
	}()
	go a.TunnelLoop(ctx)
	select {
	case <-ctx.Done():
	case err := <-errc:
		return err
	}
	sctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return hs.Shutdown(sctx)
}

// ---------------------------------------------------------------- tunnel

// TunnelLoop connects to the main server and reconnects with backoff (1 s doubling to
// 60 s, jittered; 5 min after the token was rejected) until ctx is done.
func (a *Agent) TunnelLoop(ctx context.Context) {
	backoff := time.Second
	for ctx.Err() == nil {
		t0 := time.Now()
		err := a.connectOnce(ctx)
		if ctx.Err() != nil {
			return
		}
		wait := backoff
		switch {
		case errors.Is(err, ErrUnauthorized):
			wait = 5 * time.Minute
			a.log.Error("edge token rejected by the main server; check OTD_EDGE_TOKEN", "retry_in", wait)
		case time.Since(t0) > time.Minute:
			backoff, wait = time.Second, time.Second // the session was healthy: reconnect quickly
			a.log.Warn("tunnel closed, reconnecting", "err", err)
		default:
			a.log.Warn("tunnel connect failed", "err", err, "retry_in", wait)
			backoff = min(backoff*2, time.Minute)
		}
		wait += time.Duration(rand.Int64N(int64(wait/4) + 1))
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
	}
}

func httpURL(wsURL string) string {
	switch {
	case strings.HasPrefix(wsURL, "wss://"):
		return "https://" + wsURL[len("wss://"):]
	case strings.HasPrefix(wsURL, "ws://"):
		return "http://" + wsURL[len("ws://"):]
	}
	return wsURL
}

func (a *Agent) connectOnce(ctx context.Context) error {
	hu := httpURL(a.cfg.Main)
	// token check first: tells an auth failure apart from network trouble (the WebSocket
	// handshake only reports "bad status")
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, hu, nil)
	req.Header.Set("Authorization", "Bearer "+a.cfg.Token)
	hc := &http.Client{Timeout: 15 * time.Second}
	resp, err := hc.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized {
		return ErrUnauthorized
	}
	if resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("edge: main server answered %d", resp.StatusCode)
	}
	u, err := url.Parse(hu)
	if err != nil {
		return err
	}
	wcfg, err := websocket.NewConfig(a.cfg.Main, u.Scheme+"://"+u.Host)
	if err != nil {
		return err
	}
	wcfg.Header.Set("Authorization", "Bearer "+a.cfg.Token)
	wcfg.Header.Set("X-OTD-Edge-Version", a.cfg.Version)
	wcfg.Header.Set("X-OTD-Edge-Host", a.cfg.Host)
	wcfg.Dialer = &net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second}
	ws, err := wcfg.DialContext(ctx)
	if err != nil {
		return err
	}
	sess := tunnel.NewSession(newWSConn(ws), true, a.cfg.Tunnel)
	defer sess.Close()
	a.mu.Lock()
	a.sess = sess
	a.mu.Unlock()
	defer func() {
		a.mu.Lock()
		if a.sess == sess {
			a.sess = nil
		}
		a.mu.Unlock()
	}()
	a.log.Info("tunnel connected", "main", a.cfg.Main)
	go a.fetchConfig(ctx, sess)
	go func() {
		<-ctx.Done()
		sess.Close()
	}()
	for {
		st, err := sess.Accept(ctx)
		if err != nil {
			return err
		}
		go a.serveStream(st)
	}
}

func (a *Agent) fetchConfig(ctx context.Context, sess *tunnel.Session) {
	cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	resp, err := a.request(cctx, sess, Req{Op: OpConfig})
	if err != nil {
		a.log.Warn("config from main server", "err", err)
		return
	}
	a.mu.Lock()
	a.origin, a.edgeID = resp.SiteOrigin, resp.EdgeID
	a.mu.Unlock()
	a.setKeys(resp.Keys)
	a.log.Info("config from main server", "site_origin", resp.SiteOrigin, "edge_id", resp.EdgeID, "keys", len(resp.Keys))
}

// setKeys installs the token-verification public keys (kept across reconnects: the edge keeps
// verifying tokens while the main server is unreachable).
func (a *Agent) setKeys(enc []string) {
	var keys []ed25519.PublicKey
	for _, k := range enc {
		if pk, err := edgetoken.DecodePublic(k); err == nil {
			keys = append(keys, pk)
		}
	}
	if len(keys) == 0 {
		return
	}
	a.mu.Lock()
	a.keys = keys
	a.mu.Unlock()
}

// SetIdentity sets the edge id and verification keys directly (tests).
func (a *Agent) SetIdentity(id int64, origin string, keys []ed25519.PublicKey) {
	a.mu.Lock()
	a.edgeID, a.origin, a.keys = id, origin, keys
	a.mu.Unlock()
}

// request sends a control request to the main server and reads its answer.
func (a *Agent) request(ctx context.Context, sess *tunnel.Session, req Req) (Resp, error) {
	hdr, _ := jsonMarshal(req)
	st, err := sess.Open(ctx, hdr, false)
	if err != nil {
		return Resp{}, err
	}
	st.WithContext(ctx)
	defer st.Close()
	_ = st.CloseWrite()
	var resp Resp
	if err := ReadMsg(st, &resp); err != nil {
		return Resp{}, err
	}
	if resp.Error != "" {
		return resp, errors.New("main: " + resp.Error)
	}
	return resp, nil
}

func (a *Agent) session() *tunnel.Session {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.sess
}

// Connected reports whether the tunnel is up.
func (a *Agent) Connected() bool { return a.session() != nil }

func (a *Agent) serveStream(st *tunnel.Stream) {
	defer st.Close()
	var req Req
	if err := jsonUnmarshal(st.Header, &req); err != nil {
		st.Reset(err)
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	st.WithContext(ctx)
	switch req.Op {
	case OpHTTP:
		a.servePassthrough(ctx, st, req)
	case OpHave:
		bits, known, err := a.disk.Have(ctx, strings.ToLower(req.IH))
		resp := Resp{Known: known}
		if err != nil {
			resp.Error = err.Error()
		} else if known {
			resp.Bits, resp.N = bits.B, bits.N
		}
		_ = WriteMsg(st, resp)
	case OpStats:
		_ = WriteMsg(st, Stats{BytesServed: a.bytes.Load(), Requests: a.reqs.Load(), Listen: a.cfg.Listen})
	case OpKeys:
		a.setKeys(req.Keys)
		a.log.Info("verification keys updated", "keys", len(req.Keys))
		_ = WriteMsg(st, Resp{})
	default:
		_ = WriteMsg(st, Resp{Error: "unknown op " + req.Op})
	}
}

var hopHeaders = []string{"Connection", "Keep-Alive", "Proxy-Authenticate", "Proxy-Authorization", "Te", "Trailer", "Transfer-Encoding", "Upgrade", "Host"}

// servePassthrough forwards a qB WebUI request from the main server to the local qB.
func (a *Agent) servePassthrough(ctx context.Context, st *tunnel.Stream, req Req) {
	if !strings.HasPrefix(req.Path, "/api/v2/") || strings.Contains(req.Path, "..") {
		_ = WriteMsg(st, Resp{Error: "path not allowed"})
		return
	}
	// request bodies are small forms / .torrent uploads: buffer them so qB gets a Content-Length
	body, err := io.ReadAll(io.LimitReader(st, 32<<20))
	if err != nil {
		_ = WriteMsg(st, Resp{Error: err.Error()})
		return
	}
	hr, err := http.NewRequestWithContext(ctx, req.Method, a.cfg.QBURL+req.Path, bytes.NewReader(body))
	if err != nil {
		_ = WriteMsg(st, Resp{Error: err.Error()})
		return
	}
	for k, v := range req.Header {
		hr.Header[k] = v
	}
	for _, h := range hopHeaders {
		hr.Header.Del(h)
	}
	// qB's CSRF protection compares Referer / Origin with its own host
	hr.Header.Set("Referer", a.cfg.QBURL)
	hr.Header.Del("Origin")
	resp, err := a.pass.Do(hr)
	if err != nil {
		_ = WriteMsg(st, Resp{Error: err.Error()})
		return
	}
	defer resp.Body.Close()
	h := resp.Header.Clone()
	for _, k := range hopHeaders {
		h.Del(k)
	}
	if resp.ContentLength >= 0 {
		h.Set("Content-Length", strconv.FormatInt(resp.ContentLength, 10))
	}
	st.SetBulk(strings.Contains(req.Path, "downloadFile"))
	if err := WriteMsg(st, Resp{Status: resp.StatusCode, Header: h}); err != nil {
		return
	}
	_, _ = io.Copy(st, resp.Body)
}

// ---------------------------------------------------------------- auth

// tokenSkew is the clock difference tolerated when checking token expiry.
const tokenSkew = 60 * time.Second

// verify checks the Authorization: Bearer token of a request for ih.
func (a *Agent) verify(r *http.Request, ih string) error {
	tok, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok || tok == "" {
		return errors.New("missing token")
	}
	a.mu.Lock()
	keys, id := a.keys, a.edgeID
	a.mu.Unlock()
	if len(keys) == 0 || id == 0 {
		return errNoKeys
	}
	_, err := edgetoken.Verify(strings.TrimSpace(tok), keys, id, ih, time.Now(), tokenSkew)
	return err
}

var errNoKeys = errors.New("edge: no verification keys yet (never connected to the main server)")

// ---------------------------------------------------------------- HTTP (browsers)

var ihRE = regexp.MustCompile(`^[0-9a-fA-F]{40}$`)

func (a *Agent) allowedOrigin() string {
	if a.cfg.AllowOrigin != "" {
		return strings.TrimRight(a.cfg.AllowOrigin, "/")
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return strings.TrimRight(a.origin, "/")
}

func (a *Agent) clientIP(r *http.Request) string {
	if a.cfg.TrustProxy {
		if ip := strings.TrimSpace(r.Header.Get("CF-Connecting-IP")); ip != "" {
			return ip
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// cors sets the CORS headers when the request comes from the site; false = foreign origin.
func (a *Agent) cors(w http.ResponseWriter, r *http.Request) bool {
	w.Header().Add("Vary", "Origin")
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true // not a CORS request (e.g. curl, same-origin)
	}
	allowed := a.allowedOrigin()
	if allowed == "" || origin != allowed {
		return false
	}
	h := w.Header()
	h.Set("Access-Control-Allow-Origin", origin) // no credentials: browsers send a bearer token, not cookies
	h.Set("Access-Control-Expose-Headers", "Content-Range, Content-Length, Accept-Ranges, Retry-After")
	return true
}

var corsHeaders = map[string]bool{"authorization": true, "range": true, "cache-control": true, "user-agent": true, "if-range": true, "pragma": true}

// Handler is the edge's HTTP server: the webseed, preflight, health.
func (a *Agent) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		if a.Connected() {
			io.WriteString(w, "ok\n")
		} else {
			w.WriteHeader(http.StatusServiceUnavailable)
			io.WriteString(w, "not connected to the main server\n")
		}
	})
	mux.HandleFunc("OPTIONS /webseed/hash/{ih}/{path...}", func(w http.ResponseWriter, r *http.Request) {
		if !a.cors(w, r) || r.Header.Get("Origin") == "" {
			http.Error(w, "origin not allowed", http.StatusForbidden)
			return
		}
		if m := r.Header.Get("Access-Control-Request-Method"); m != "GET" && m != "HEAD" {
			http.Error(w, "method not allowed", http.StatusForbidden)
			return
		}
		var allow []string
		for _, h := range strings.Split(r.Header.Get("Access-Control-Request-Headers"), ",") {
			h = strings.ToLower(strings.TrimSpace(h))
			if h == "" {
				continue
			}
			if !corsHeaders[h] {
				http.Error(w, "header not allowed: "+h, http.StatusForbidden)
				return
			}
			allow = append(allow, h)
		}
		w.Header().Set("Access-Control-Allow-Methods", "GET, HEAD")
		if len(allow) > 0 {
			w.Header().Set("Access-Control-Allow-Headers", strings.Join(allow, ", "))
		}
		w.Header().Set("Access-Control-Max-Age", "600")
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("GET /webseed/hash/{ih}/{path...}", a.serveWebseed)
	return mux
}

func (a *Agent) serveWebseed(w http.ResponseWriter, r *http.Request) {
	fail := func(code int, msg string) { http.Error(w, msg, code) }
	if !a.cors(w, r) {
		fail(http.StatusForbidden, "origin not allowed")
		return
	}
	ih := r.PathValue("ih")
	if !ihRE.MatchString(ih) {
		fail(http.StatusNotFound, "invalid infohash")
		return
	}
	ih = strings.ToLower(ih)
	if !a.limits.allow(a.clientIP(r)) {
		fail(http.StatusTooManyRequests, "rate limited")
		return
	}
	a.reqs.Add(1)
	if err := a.verify(r, ih); errors.Is(err, errNoKeys) {
		w.Header().Set("Retry-After", "10")
		fail(http.StatusServiceUnavailable, "edge not ready")
		return
	} else if err != nil {
		w.Header().Set("WWW-Authenticate", `Bearer error="invalid_token"`)
		fail(http.StatusUnauthorized, err.Error())
		return
	}
	ctx := r.Context()
	l, err := a.disk.Layout(ctx, ih)
	if errors.Is(err, ErrNoTorrent) {
		fail(http.StatusNotFound, "torrent not on this server")
		return
	}
	if err != nil {
		a.log.Warn("layout", "hash", ih, "err", err)
		fail(http.StatusBadGateway, "qBittorrent unavailable")
		return
	}
	fi, ok := l.Match(r.PathValue("path"))
	if !ok {
		fail(http.StatusNotFound, "no such file in torrent")
		return
	}
	f := l.Files[fi]
	h := w.Header()
	h.Set("Accept-Ranges", "bytes")
	h.Set("Cache-Control", "private, no-store")
	if f.Size == 0 {
		h.Set("Content-Length", "0")
		w.WriteHeader(http.StatusOK)
		return
	}
	start, end, partial, ok := seedsrc.ParseRange(r.Header.Get("Range"), f.Size)
	if !ok {
		h.Set("Content-Range", fmt.Sprintf("bytes */%d", f.Size))
		fail(http.StatusRequestedRangeNotSatisfiable, "range not satisfiable")
		return
	}
	select {
	case a.sem <- struct{}{}:
		defer func() { <-a.sem }()
	default:
		h.Set("Retry-After", "2")
		fail(http.StatusServiceUnavailable, "busy")
		return
	}
	body, err := a.disk.ReadRange(ctx, ih, fi, start, end)
	switch {
	case errors.Is(err, ErrNotAvailable):
		h.Set("Retry-After", "5")
		fail(http.StatusServiceUnavailable, "pieces not downloaded yet")
		return
	case errors.Is(err, ErrNoTorrent):
		fail(http.StatusNotFound, "torrent not on this server")
		return
	case err != nil:
		if ctx.Err() == nil {
			a.log.Warn("read failed", "hash", ih, "file", f.Path, "err", err)
		}
		h.Set("Retry-After", "5")
		fail(http.StatusServiceUnavailable, "read failed")
		return
	}
	defer body.Close()
	ct := mime.TypeByExtension(path.Ext(f.Path))
	if ct == "" {
		ct = "application/octet-stream"
	}
	h.Set("Content-Type", ct)
	h.Set("Content-Length", strconv.FormatInt(end-start+1, 10))
	if partial {
		h.Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, f.Size))
		w.WriteHeader(http.StatusPartialContent)
	} else {
		w.WriteHeader(http.StatusOK)
	}
	if r.Method == http.MethodHead {
		return
	}
	n, err := io.Copy(w, body)
	a.bytes.Add(n)
	if err != nil && ctx.Err() == nil {
		a.log.Info("webseed copy", "hash", ih, "err", err)
	}
}

// ipLimiter: token bucket per client IP (burst 600, one token per 50 ms, like the main server).
type ipLimiter struct {
	mu sync.Mutex
	m  map[string]*bucket
}

type bucket struct {
	tokens float64
	at     time.Time
}

func (l *ipLimiter) allow(ip string) bool {
	const burst, every = 600.0, 50 * time.Millisecond
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	b, ok := l.m[ip]
	if !ok {
		if len(l.m) > 50_000 {
			clear(l.m)
		}
		b = &bucket{tokens: burst, at: now}
		l.m[ip] = b
	}
	b.tokens = min(burst, b.tokens+float64(now.Sub(b.at))/float64(every))
	b.at = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}
