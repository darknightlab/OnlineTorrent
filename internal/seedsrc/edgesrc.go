package seedsrc

import (
	"context"
	"errors"
	"fmt"
	"io"
)

// EdgeClient is the main server's view of a connected edge node (internal/edge.Conn). The
// edge runs next to a qB instance, shares its download directory and serves the webseed to
// browsers itself (partial files included); the main server only asks it for availability.
type EdgeClient interface {
	// Have returns the verified pieces of ih (qB pieceStates == 2); known=false when qB
	// doesn't hold the torrent with metadata.
	Have(ctx context.Context, ih string, numPieces int) (bits *Bits, known bool, err error)
}

// ErrServedByEdge: data of edge-backed servers is served by the edge directly, never
// relayed through the main server.
var ErrServedByEdge = errors.New("seedsrc: served by the edge node directly")

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

// ReadRange implements Source: never used for data (see ErrServedByEdge).
func (e *Edge) ReadRange(context.Context, *Layout, int, int64, int64, *Bits) (io.ReadCloser, error) {
	return nil, ErrServedByEdge
}
