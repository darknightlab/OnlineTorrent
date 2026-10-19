// Package edge implements the edge node ("边缘节点"): an `otd edge` process next to a
// qBittorrent instance that shares its download directory and serves the HTTP WebSeed
// directly to browsers — partial files included (verified pieces only), straight from disk.
//
// The edge keeps one outbound WebSocket to the main server (internal/tunnel multiplexes it).
// The tunnel carries control traffic only, never webseed data:
//
//	main → edge  http    qB WebUI request passthrough (the main server's qb.Client works unchanged)
//	main → edge  have    verified pieces of a torrent (for the availability SSE)
//	main → edge  keys    new token-verification public keys (after a rotation)
//	main → edge  stats   the edge's HTTP counters (admin UI)
//	edge → main  config  site origin (CORS), the edge's id (token audience), public keys
//
// Browsers authenticate to the edge with short-lived tokens signed by the main server
// (internal/edgetoken), verified offline by the edge.
//
// Every stream starts with a JSON header (the tunnel's open frame); the answer is a
// length-prefixed JSON message, optionally followed by a body.
package edge

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"

	"golang.org/x/net/websocket"
)

// Operations.
const (
	OpHTTP   = "http"
	OpHave   = "have"
	OpKeys   = "keys"
	OpStats  = "stats"
	OpConfig = "config"
)

// Req is the header of a stream.
type Req struct {
	Op string `json:"op"`
	// http
	Method string      `json:"method,omitempty"`
	Path   string      `json:"path,omitempty"` // "/api/v2/…?query"
	Header http.Header `json:"header,omitempty"`
	// have
	IH     string `json:"ih,omitempty"`
	Pieces int    `json:"pieces,omitempty"`
	// keys: base64url Ed25519 public keys (current first)
	Keys []string `json:"keys,omitempty"`
}

// Resp is the first message of an answer.
type Resp struct {
	Error string `json:"error,omitempty"`
	// http
	Status int         `json:"status,omitempty"`
	Header http.Header `json:"header,omitempty"`
	// have
	Known bool   `json:"known,omitempty"`
	Bits  []byte `json:"bits,omitempty"` // BitTorrent-order bitfield
	N     int    `json:"n,omitempty"`
	// config
	SiteOrigin string   `json:"site_origin,omitempty"`
	EdgeID     int64    `json:"edge_id,omitempty"`
	Keys       []string `json:"keys,omitempty"`
}

const maxMsg = 4 << 20

// WriteMsg writes a length-prefixed JSON message.
func WriteMsg(w io.Writer, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	var hdr [4]byte
	binary.BigEndian.PutUint32(hdr[:], uint32(len(b)))
	if _, err := w.Write(append(hdr[:], b...)); err != nil {
		return err
	}
	return nil
}

// ReadMsg reads a length-prefixed JSON message.
func ReadMsg(r io.Reader, v any) error {
	var hdr [4]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return err
	}
	n := binary.BigEndian.Uint32(hdr[:])
	if n > maxMsg {
		return fmt.Errorf("edge: message too large (%d)", n)
	}
	b := make([]byte, n)
	if _, err := io.ReadFull(r, b); err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}

// ErrUnauthorized: the main server rejected the edge token.
var ErrUnauthorized = errors.New("edge: token rejected by the main server")

// wsConn adapts a golang.org/x/net/websocket connection to tunnel.MsgConn.
type wsConn struct {
	ws  *websocket.Conn
	wmu sync.Mutex
}

func newWSConn(ws *websocket.Conn) *wsConn {
	ws.PayloadType = websocket.BinaryFrame
	ws.MaxPayloadBytes = 1 << 20
	return &wsConn{ws: ws}
}

func (c *wsConn) ReadMsg() ([]byte, error) {
	var b []byte
	err := websocket.Message.Receive(c.ws, &b)
	return b, err
}

func (c *wsConn) WriteMsg(b []byte) error {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	return websocket.Message.Send(c.ws, b)
}

func (c *wsConn) Close() error { return c.ws.Close() }

func jsonMarshal(v any) ([]byte, error) { return json.Marshal(v) }

func jsonUnmarshal(b []byte, v any) error {
	if len(b) > maxMsg {
		return errors.New("edge: header too large")
	}
	return json.Unmarshal(b, v)
}
