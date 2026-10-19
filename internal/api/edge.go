package api

import (
	"context"
	"crypto/ed25519"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/darknightlab/onlinetorrent/internal/edge"
	"github.com/darknightlab/onlinetorrent/internal/edgetoken"
	"github.com/darknightlab/onlinetorrent/internal/httpx"
	"github.com/darknightlab/onlinetorrent/internal/seedsrc"
	"github.com/darknightlab/onlinetorrent/internal/store"
)

// Edge nodes ("边缘节点", internal/edge): an `otd edge` next to a qB server connects OUT to
// GET /api/v1/edge/connect (Authorization: Bearer <edge token>, WebSocket) and serves the
// webseed to browsers itself, from qB's download directory. The main server never relays
// webseed data for edge-backed servers: it reports availability in the webseed SSE and hands
// out the edge's public URL together with a short-lived access token per edge
// (internal/edgetoken, Ed25519, verified offline by the edge) — issued only after the same
// route permission + policy + content-scope check as its own webseed.

// webseedRouteKey is the "may use WebSeed" permission (handler-less on the main server; the
// edges serve the data).
const webseedRouteKey = "GET /webseed/hash/{ih}/{path...}"

// NewEdgeHub creates the hub for edge connections, wired to this server (call before Handler).
// It loads (or creates) the token-signing key.
func (s *Server) NewEdgeHub() (*edge.Hub, error) {
	if _, err := s.edgeKeyRing(s.BaseCtx); err != nil {
		return nil, err
	}
	h := &edge.Hub{
		Lookup: s.St.QBServerByEdgeToken,
		Config: s.siteOrigin,
		Keys:   s.edgePublicKeys,
		OnChange: func(id int64, connected bool) {
			// servers whose qB API goes through the edge come online / go offline with it
			if s.Pool == nil {
				return
			}
			if sv := s.Pool.Get(id); sv != nil && sv.Cfg.EdgeAPI {
				go s.Pool.Check(context.WithoutCancel(s.BaseCtx), sv)
			}
		},
		Log: s.Log,
	}
	s.Edges = h
	if s.Pool != nil {
		s.Pool.EdgeTransport = h.Transport
		// servers whose API goes through their edge get the edge transport
		if err := s.Pool.Reload(s.BaseCtx); err != nil {
			return nil, err
		}
	}
	return h, nil
}

// siteOrigin is the origin browsers load the site from (CORS on the edges).
func (s *Server) siteOrigin() string {
	base := strings.TrimRight(s.St.Str(store.KeySiteBaseURL), "/")
	if u, err := url.Parse(base); err == nil && u.Scheme != "" && u.Host != "" {
		return u.Scheme + "://" + u.Host
	}
	return ""
}

// ---- token signing keys ----

type edgeKeyRing struct {
	cur  ed25519.PrivateKey
	prev ed25519.PublicKey // accepted by edges until the next rotation
}

type edgeKeysJSON struct {
	Current   string `json:"current,omitempty"`  // base64url Ed25519 seed
	Previous  string `json:"previous,omitempty"` // base64url public key
	RotatedAt string `json:"rotated_at,omitempty"`
}

// edgeKeyRing returns the signing keys, creating the first key on first use.
func (s *Server) edgeKeyRing(ctx context.Context) (*edgeKeyRing, error) {
	s.edgeKeysMu.Lock()
	defer s.edgeKeysMu.Unlock()
	if s.edgeKeys != nil {
		return s.edgeKeys, nil
	}
	var stored edgeKeysJSON
	_ = json.Unmarshal(s.St.Raw(store.KeyEdgeSigningKeys), &stored)
	ring := &edgeKeyRing{}
	if seed, err := base64.RawURLEncoding.DecodeString(stored.Current); err == nil && len(seed) == ed25519.SeedSize {
		ring.cur = ed25519.NewKeyFromSeed(seed)
		ring.prev, _ = edgetoken.DecodePublic(stored.Previous)
	} else {
		_, priv, err := ed25519.GenerateKey(nil)
		if err != nil {
			return nil, err
		}
		ring.cur = priv
		if err := s.saveEdgeKeys(ctx, ring); err != nil {
			return nil, err
		}
		s.Log.Info("created the edge token signing key")
	}
	s.edgeKeys = ring
	return ring, nil
}

func (s *Server) saveEdgeKeys(ctx context.Context, ring *edgeKeyRing) error {
	v := edgeKeysJSON{Current: base64.RawURLEncoding.EncodeToString(ring.cur.Seed()), RotatedAt: store.Now()}
	if ring.prev != nil {
		v.Previous = edgetoken.EncodePublic(ring.prev)
	}
	b, _ := json.Marshal(v)
	_, err := s.St.SetJSON(ctx, store.KeyEdgeSigningKeys, b)
	return err
}

// edgePublicKeys are the keys edges accept (current first, then the previous one).
func (s *Server) edgePublicKeys() []string {
	ring, err := s.edgeKeyRing(s.BaseCtx)
	if err != nil {
		return nil
	}
	out := []string{edgetoken.EncodePublic(ring.cur.Public().(ed25519.PublicKey))}
	if ring.prev != nil {
		out = append(out, edgetoken.EncodePublic(ring.prev))
	}
	return out
}

// rotateEdgeKey makes a new signing key; the old public key stays valid until the next
// rotation (tokens live minutes, so a rotation never cuts off running playback).
func (s *Server) rotateEdgeKey(ctx context.Context) error {
	old, err := s.edgeKeyRing(ctx)
	if err != nil {
		return err
	}
	_, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		return err
	}
	ring := &edgeKeyRing{cur: priv, prev: old.cur.Public().(ed25519.PublicKey)}
	if err := s.saveEdgeKeys(ctx, ring); err != nil {
		return err
	}
	s.edgeKeysMu.Lock()
	s.edgeKeys = ring
	s.edgeKeysMu.Unlock()
	if s.Edges != nil {
		s.Edges.PushKeys(ctx)
	}
	return nil
}

// edgeTokenTTL is the lifetime of browser tokens (setting, minutes; 1 min … 24 h).
func (s *Server) edgeTokenTTL() time.Duration {
	if s.EdgeTokenTTL > 0 {
		return s.EdgeTokenTTL
	}
	m := s.St.Num(store.KeyEdgeTokenMinutes)
	if m < 1 {
		m = 10
	}
	return time.Duration(min(m, 24*60) * float64(time.Minute))
}

// issueEdgeToken signs an access token for edge edgeID and torrent ih. The caller must have
// checked the permission (route permission + policy + content scope).
func (s *Server) issueEdgeToken(ctx context.Context, u *store.User, ih string, edgeID int64) (string, time.Time, error) {
	ring, err := s.edgeKeyRing(ctx)
	if err != nil {
		return "", time.Time{}, err
	}
	exp := time.Now().Add(s.edgeTokenTTL()).Truncate(time.Second)
	tok, err := edgetoken.Sign(ring.cur, edgetoken.Claims{Role: roleOf(u), IH: ih, Aud: edgeID, Exp: exp})
	return tok, exp, err
}

// adminRotateEdgeKey: POST /api/v1/admin/edge/rotate-key
func (s *Server) adminRotateEdgeKey(w http.ResponseWriter, r *http.Request) error {
	if err := s.rotateEdgeKey(r.Context()); err != nil {
		return err
	}
	s.audit(r, "edge.rotate_key", "", "")
	httpx.JSON(w, 200, map[string]any{"keys": len(s.edgePublicKeys())})
	return nil
}

// edgeClient returns the connected edge of a qB server (nil if none).
func (s *Server) edgeClient(serverID int64) seedsrc.EdgeClient {
	if s.Edges == nil {
		return nil
	}
	if c := s.Edges.Get(serverID); c != nil {
		return c
	}
	return nil
}

// adminEdgeToken generates (or rotates) the edge token of a qB server; the plain token is
// returned once, only its hash is stored. A connected edge with the old token is dropped.
func (s *Server) adminEdgeToken(w http.ResponseWriter, r *http.Request) error {
	id := pathID(r)
	tok, hash := edge.NewToken()
	if err := s.St.SetQBEdgeToken(r.Context(), id, hash); errors.Is(err, sql.ErrNoRows) {
		return httpx.E(404, "not_found", "服务器不存在")
	} else if err != nil {
		return err
	}
	s.audit(r, "qb.edge_token", r.PathValue("id"), "")
	if s.Edges != nil {
		s.Edges.Disconnect(id)
	}
	if err := s.Pool.Reload(r.Context()); err != nil {
		return err
	}
	httpx.JSON(w, 200, map[string]any{"token": tok, "connect_url": s.edgeConnectURL(r)})
	return nil
}

// adminEdgeUnbind removes the edge token (the edge is disconnected and can't reconnect).
func (s *Server) adminEdgeUnbind(w http.ResponseWriter, r *http.Request) error {
	id := pathID(r)
	if err := s.St.SetQBEdgeToken(r.Context(), id, ""); errors.Is(err, sql.ErrNoRows) {
		return httpx.E(404, "not_found", "服务器不存在")
	} else if err != nil {
		return err
	}
	s.audit(r, "qb.edge_unbind", r.PathValue("id"), "")
	if s.Edges != nil {
		s.Edges.Disconnect(id)
	}
	if err := s.Pool.Reload(r.Context()); err != nil {
		return err
	}
	return s.adminListQB(w, r)
}

// edgeConnectURL is the OTD_MAIN value for edges (wss://{site}/api/v1/edge/connect).
func (s *Server) edgeConnectURL(r *http.Request) string {
	base := s.siteBase(r)
	switch {
	case strings.HasPrefix(base, "https://"):
		base = "wss://" + base[len("https://"):]
	case strings.HasPrefix(base, "http://"):
		base = "ws://" + base[len("http://"):]
	}
	return base + "/api/v1/edge/connect"
}

// edgeStatus is the admin view of a server's edge.
func (s *Server) edgeStatus(ctx context.Context, id int64) edge.Status {
	if s.Edges == nil {
		return edge.Status{}
	}
	cctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	return s.Edges.Status(cctx, id)
}
