package seedsrc

import (
	"context"
	"fmt"
	"sync"

	"github.com/darknightlab/onlinetorrent/internal/qb"
)

// EdgeClient is the main server's view of a connected edge node (internal/edge.Conn). The
// edge runs next to a qB instance, shares its download directory and serves the webseed to
// browsers itself (partial files included); the main server only asks it for availability.
type EdgeClient interface {
	// Have returns the verified pieces of ih (qB pieceStates == 2); known=false when qB
	// doesn't hold the torrent with metadata.
	Have(ctx context.Context, ih string, numPieces int) (bits *Bits, known bool, err error)
}

// Edge is a qB server with a connected edge node. It only reports availability (the
// browser fetches from the edge's own URL, BaseURL).
type Edge struct {
	BaseURL    string // public base URL of the edge, e.g. https://edge-ams.projectk.org
	ServerID   int64
	ServerName string
	Client     EdgeClient
}

func (e *Edge) ID() string   { return fmt.Sprintf("edge:%d", e.ServerID) }
func (e *Edge) Name() string { return e.ServerName + "（边缘节点）" }

// Have implements Source: every verified piece is servable from disk.
func (e *Edge) Have(ctx context.Context, l *Layout) (Avail, error) {
	bits, known, err := e.Client.Have(ctx, l.Infohash, l.NumPieces)
	if err != nil || !known {
		return Avail{}, err
	}
	if bits.N != l.NumPieces {
		return Avail{}, fmt.Errorf("seedsrc: edge %s reports %d pieces, torrent has %d", e.ServerName, bits.N, l.NumPieces)
	}
	return Avail{Known: true, Pieces: bits}, nil
}

// PoolSources adapts the qB pool: every enabled server whose edge node is connected (edges
// returns non-nil) and has a public URL is a Source. Servers without an edge offer no
// webseed. Source values are reused while unchanged.
func PoolSources(pool *qb.Pool, edges func(serverID int64) EdgeClient) func() []Source {
	var mu sync.Mutex
	known := map[EdgeClient]*Edge{}
	return func() []Source {
		servers := pool.Servers()
		mu.Lock()
		defer mu.Unlock()
		next := map[EdgeClient]*Edge{}
		var out []Source
		for _, s := range servers {
			if !s.Cfg.Enabled || s.Cfg.EdgeURL == "" || edges == nil {
				continue
			}
			ec := edges(s.Cfg.ID)
			if ec == nil {
				continue
			}
			e, ok := known[ec]
			if !ok || e.BaseURL != s.Cfg.EdgeURL || e.ServerName != s.Cfg.Name {
				e = &Edge{ServerID: s.Cfg.ID, ServerName: s.Cfg.Name, Client: ec, BaseURL: s.Cfg.EdgeURL}
			}
			next[ec] = e
			out = append(out, e)
		}
		known = next
		return out
	}
}
