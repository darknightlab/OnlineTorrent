package edge

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/websocket"

	"github.com/darknightlab/onlinetorrent/internal/seedsrc"
	"github.com/darknightlab/onlinetorrent/internal/tunnel"
)

// NewToken returns a fresh edge token ("otde_" + 43 base64url chars) and its stored hash.
func NewToken() (token, hash string) {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	token = "otde_" + base64.RawURLEncoding.EncodeToString(b)
	return token, HashToken(token)
}

// HashToken is what the database stores (SHA-256 hex; tokens are random, no salt needed).
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// Hub is the main server's side: it accepts edge connections (GET /api/v1/edge/connect,
// Authorization: Bearer <edge token>), keeps one session per qB server, answers the edges'
// config requests (site origin, edge id, token-verification public keys), pushes new keys
// and offers qB passthrough + availability queries.
type Hub struct {
	// Lookup maps an edge-token hash to the qB server it is bound to.
	Lookup func(ctx context.Context, tokenHash string) (serverID int64, ok bool, err error)
	// Config returns the site origin browsers come from (CORS on the edge).
	Config func() (siteOrigin string)
	// Keys returns the public keys edges accept (base64url, current first).
	Keys func() []string
	// OnChange is called when a server's edge connects or disconnects.
	OnChange func(serverID int64, connected bool)
	Log      *slog.Logger
	Options  tunnel.Options

	mu    sync.Mutex
	conns map[int64]*Conn
}

// Conn is one connected edge.
type Conn struct {
	ServerID int64
	Since    time.Time
	Version  string
	Host     string
	Remote   string
	sess     *tunnel.Session
	hub      *Hub
}

// Status is shown in the admin UI.
type Status struct {
	Connected bool    `json:"connected"`
	Since     string  `json:"since,omitempty"`
	Version   string  `json:"version,omitempty"`
	Host      string  `json:"host,omitempty"`
	Remote    string  `json:"remote,omitempty"`
	RTTms     float64 `json:"rtt_ms,omitempty"`
	// from the edge itself (Stats)
	BytesServed int64  `json:"bytes_served"`
	Requests    int64  `json:"requests"`
	Listen      string `json:"listen,omitempty"`
	StatsError  string `json:"stats_error,omitempty"`
}

// Stats is what an edge reports about its own HTTP server.
type Stats struct {
	BytesServed int64  `json:"bytes_served"`
	Requests    int64  `json:"requests"`
	Listen      string `json:"listen"`
}

// Get returns the connected edge of a server (nil if none).
func (h *Hub) Get(serverID int64) *Conn {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.conns[serverID]
}

// Disconnect drops a server's edge (e.g. after its token was rotated).
func (h *Hub) Disconnect(serverID int64) {
	if c := h.Get(serverID); c != nil {
		c.sess.Close()
	}
}

func (h *Hub) log() *slog.Logger {
	if h.Log != nil {
		return h.Log
	}
	return slog.Default()
}

// ServeHTTP handles GET /api/v1/edge/connect. Without an Upgrade header it only checks the
// token (204 / 401) so an edge can tell an auth failure from a network error.
func (h *Hub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	tok, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if tok == "" || h.Lookup == nil {
		http.Error(w, "missing edge token", http.StatusUnauthorized)
		return
	}
	id, ok, err := h.Lookup(r.Context(), HashToken(strings.TrimSpace(tok)))
	if err != nil {
		http.Error(w, "lookup failed", http.StatusInternalServerError)
		return
	}
	if !ok {
		http.Error(w, "unknown edge token", http.StatusUnauthorized)
		return
	}
	if !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	remote := r.Header.Get("CF-Connecting-IP")
	if remote == "" {
		remote = r.RemoteAddr
	}
	srv := websocket.Server{
		Handshake: func(*websocket.Config, *http.Request) error { return nil }, // authenticated by token
		Handler: func(ws *websocket.Conn) {
			_ = ws.SetDeadline(time.Time{}) // the main http.Server's read/write timeouts don't apply to the tunnel
			sess := tunnel.NewSession(newWSConn(ws), false, h.Options)
			c := &Conn{ServerID: id, Since: time.Now(), Version: r.Header.Get("X-OTD-Edge-Version"), Host: r.Header.Get("X-OTD-Edge-Host"), Remote: remote, sess: sess, hub: h}
			h.register(c)
			go c.serveStreams()
			<-sess.Done()
			h.unregister(c)
		},
	}
	srv.ServeHTTP(w, r)
}

func (h *Hub) register(c *Conn) {
	h.mu.Lock()
	if h.conns == nil {
		h.conns = map[int64]*Conn{}
	}
	old := h.conns[c.ServerID]
	h.conns[c.ServerID] = c
	h.mu.Unlock()
	if old != nil {
		old.sess.Close() // a reconnect replaces the previous session
	}
	h.log().Info("edge connected", "server", c.ServerID, "host", c.Host, "version", c.Version, "remote", c.Remote)
	if h.OnChange != nil {
		h.OnChange(c.ServerID, true)
	}
}

func (h *Hub) unregister(c *Conn) {
	h.mu.Lock()
	cur := h.conns[c.ServerID] == c
	if cur {
		delete(h.conns, c.ServerID)
	}
	h.mu.Unlock()
	if cur {
		h.log().Info("edge disconnected", "server", c.ServerID, "err", c.sess.Err())
		if h.OnChange != nil {
			h.OnChange(c.ServerID, false)
		}
	}
}

// serveStreams answers requests the edge opens (config).
func (c *Conn) serveStreams() {
	for {
		st, err := c.sess.Accept(context.Background())
		if err != nil {
			return
		}
		go c.serveStream(st)
	}
}

func (c *Conn) serveStream(st *tunnel.Stream) {
	defer st.Close()
	var req Req
	if err := jsonUnmarshal(st.Header, &req); err != nil {
		st.Reset(err)
		return
	}
	var resp Resp
	switch req.Op {
	case OpConfig:
		resp.EdgeID = c.ServerID
		if c.hub.Config != nil {
			resp.SiteOrigin = c.hub.Config()
		}
		if c.hub.Keys != nil {
			resp.Keys = c.hub.Keys()
		}
	default:
		resp.Error = "unknown op " + req.Op
	}
	_ = WriteMsg(st, resp)
}

// Status of a server's edge; stats are fetched from the edge (short timeout).
func (h *Hub) Status(ctx context.Context, serverID int64) Status {
	c := h.Get(serverID)
	if c == nil {
		return Status{}
	}
	s := Status{Connected: true, Since: c.Since.UTC().Format(time.RFC3339), Version: c.Version, Host: c.Host, Remote: c.Remote,
		RTTms: float64(c.sess.RTT().Microseconds()) / 1000}
	cctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if st, err := c.Stats(cctx); err != nil {
		s.StatsError = err.Error()
	} else {
		s.BytesServed, s.Requests, s.Listen = st.BytesServed, st.Requests, st.Listen
	}
	return s
}

// call opens a stream with req, sends body (may be nil) and reads the answer header.
func (c *Conn) call(ctx context.Context, req Req, body io.Reader, bulk bool) (*tunnel.Stream, Resp, error) {
	hdr, err := jsonMarshal(req)
	if err != nil {
		return nil, Resp{}, err
	}
	st, err := c.sess.Open(ctx, hdr, bulk)
	if err != nil {
		return nil, Resp{}, err
	}
	st.WithContext(ctx)
	if body != nil {
		go func() {
			if _, err := io.Copy(st, body); err != nil {
				st.Reset(err)
				return
			}
			_ = st.CloseWrite()
		}()
	} else {
		_ = st.CloseWrite()
	}
	var resp Resp
	if err := ReadMsg(st, &resp); err != nil {
		st.Reset(err)
		return nil, Resp{}, fmt.Errorf("edge: %w", err)
	}
	if resp.Error != "" {
		st.Close()
		return nil, resp, errors.New("edge: " + resp.Error)
	}
	return st, resp, nil
}

// Have implements seedsrc.EdgeClient.
func (c *Conn) Have(ctx context.Context, ih string, numPieces int) (*seedsrc.Bits, bool, error) {
	st, resp, err := c.call(ctx, Req{Op: OpHave, IH: ih, Pieces: numPieces}, nil, false)
	if err != nil {
		return nil, false, err
	}
	st.Close()
	if !resp.Known {
		return nil, false, nil
	}
	if resp.N <= 0 || len(resp.Bits) != (resp.N+7)/8 {
		return nil, false, errors.New("edge: malformed bitfield")
	}
	return &seedsrc.Bits{N: resp.N, B: resp.Bits}, true, nil
}

// Stats asks the edge for its HTTP server counters.
func (c *Conn) Stats(ctx context.Context) (Stats, error) {
	st, err := c.sess.Open(ctx, []byte(`{"op":"`+OpStats+`"}`), false)
	if err != nil {
		return Stats{}, err
	}
	st.WithContext(ctx)
	defer st.Close()
	_ = st.CloseWrite()
	var s Stats
	err = ReadMsg(st, &s)
	return s, err
}

// PushKeys sends the current public keys to every connected edge (after a rotation).
func (h *Hub) PushKeys(ctx context.Context) {
	if h.Keys == nil {
		return
	}
	keys := h.Keys()
	h.mu.Lock()
	conns := make([]*Conn, 0, len(h.conns))
	for _, c := range h.conns {
		conns = append(conns, c)
	}
	h.mu.Unlock()
	for _, c := range conns {
		cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		if st, _, err := c.call(cctx, Req{Op: OpKeys, Keys: keys}, nil, false); err != nil {
			h.log().Warn("push keys to edge", "server", c.ServerID, "err", err)
		} else {
			st.Close()
		}
		cancel()
	}
}

// RoundTrip forwards a qB WebUI request through the edge (implements http.RoundTripper).
func (c *Conn) RoundTrip(req *http.Request) (*http.Response, error) {
	bulk := strings.Contains(req.URL.Path, "downloadFile")
	var body io.Reader
	if req.Body != nil && req.Body != http.NoBody {
		body = req.Body
		defer req.Body.Close()
	}
	st, resp, err := c.call(req.Context(), Req{Op: OpHTTP, Method: req.Method, Path: req.URL.RequestURI(), Header: req.Header.Clone()}, body, bulk)
	if err != nil {
		return nil, err
	}
	h := resp.Header
	if h == nil {
		h = http.Header{}
	}
	cl := int64(-1)
	if v := h.Get("Content-Length"); v != "" {
		fmt.Sscan(v, &cl)
	}
	return &http.Response{Status: fmt.Sprintf("%d %s", resp.Status, http.StatusText(resp.Status)), StatusCode: resp.Status, Proto: "HTTP/1.1", ProtoMajor: 1, ProtoMinor: 1,
		Header: h, Body: st, ContentLength: cl, Request: req}, nil
}

// Transport routes requests for serverID through its edge (error while it is disconnected).
func (h *Hub) Transport(serverID int64) http.RoundTripper {
	return rtFunc(func(req *http.Request) (*http.Response, error) {
		c := h.Get(serverID)
		if c == nil {
			return nil, errors.New("edge node not connected")
		}
		return c.RoundTrip(req)
	})
}

type rtFunc func(*http.Request) (*http.Response, error)

func (f rtFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
