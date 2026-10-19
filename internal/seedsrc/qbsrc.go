package seedsrc

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/darknightlab/onlinetorrent/internal/qb"
)

// QB is a qBittorrent server without an edge node: torrents/downloadFile only serves files
// that are 100 % downloaded (HTTP 409 "File not fully downloaded" otherwise, even for ranges
// whose pieces are verified), so this source only offers pieces of complete files.
//
//   - Have: torrents/pieceStates → pieces of complete files (CompleteFiles).
//   - ReadRange: torrents/downloadFile with a Range header.
type QB struct {
	Server *qb.Server

	mu    sync.Mutex
	files map[string]qbFiles // ih → torrents/files (name → qB file index)
}

type qbFiles struct {
	byName map[string]int
	at     time.Time
}

func (q *QB) ID() string   { return fmt.Sprintf("qb:%d", q.Server.Cfg.ID) }
func (q *QB) Name() string { return q.Server.Cfg.Name }

// Have implements Source.
func (q *QB) Have(ctx context.Context, l *Layout) (Avail, error) {
	states, err := q.Server.Client.PieceStates(ctx, l.Infohash)
	if errors.Is(err, qb.ErrNoTorrent) {
		return Avail{}, nil
	}
	if err != nil {
		return Avail{}, err
	}
	if len(states) == 0 {
		return Avail{}, nil // no metadata yet
	}
	if len(states) != l.NumPieces {
		return Avail{}, fmt.Errorf("seedsrc: %s reports %d pieces, torrent has %d", q.Name(), len(states), l.NumPieces)
	}
	return Avail{Known: true, Pieces: CompleteFiles(l, FromStates(states))}, nil
}

// CompleteFiles keeps the pieces of have that torrents/downloadFile can serve: pieces whose
// every (non-padding) file is complete. A boundary piece shared with an incomplete file is
// left out — a webseed request for it also covers bytes of that file.
func CompleteFiles(l *Layout, have *Bits) *Bits {
	out := have.Clone()
	for fi, f := range l.Files {
		first, last, ok := l.FilePieces(fi)
		if !ok || f.Pad || have.All(first, last) {
			continue
		}
		for i := first; i <= last; i++ {
			out.B[i>>3] &^= 0x80 >> (i & 7)
		}
	}
	return out
}

// ReadRange implements Source (complete files only, see Have).
func (q *QB) ReadRange(ctx context.Context, l *Layout, fi int, start, end int64, _ *Bits) (io.ReadCloser, error) {
	idx, err := q.fileIndex(ctx, l, fi)
	if err != nil {
		return nil, err
	}
	resp, err := q.Server.Client.DownloadFile(ctx, l.Infohash, idx, fmt.Sprintf("bytes=%d-%d", start, end))
	if err != nil {
		return nil, err
	}
	want := end - start + 1
	if resp.StatusCode != http.StatusPartialContent && !(resp.StatusCode == http.StatusOK && start == 0 && resp.ContentLength == want) {
		resp.Body.Close()
		return nil, fmt.Errorf("seedsrc: downloadFile HTTP %d", resp.StatusCode)
	}
	return struct {
		io.Reader
		io.Closer
	}{io.LimitReader(resp.Body, want), resp.Body}, nil
}

// fileIndex maps layout file fi to qB's file index (qB may hide BEP 47 padding files, so
// indexes can differ; match by path).
func (q *QB) fileIndex(ctx context.Context, l *Layout, fi int) (int, error) {
	q.mu.Lock()
	e, ok := q.files[l.Infohash]
	q.mu.Unlock()
	if !ok || time.Since(e.at) > 10*time.Minute {
		fs, err := q.Server.Client.Files(ctx, l.Infohash)
		if err != nil {
			return 0, err
		}
		e = qbFiles{byName: map[string]int{}, at: time.Now()}
		for _, f := range fs {
			e.byName[strings.Trim(f.Name, "/")] = f.Index
		}
		q.mu.Lock()
		if q.files == nil || len(q.files) > 512 {
			q.files = map[string]qbFiles{}
		}
		q.files[l.Infohash] = e
		q.mu.Unlock()
	}
	p := l.Files[fi].Path
	if i, ok := e.byName[p]; ok {
		return i, nil
	}
	for name, i := range e.byName { // single-file torrents / renamed roots
		if strings.HasSuffix(name, "/"+p) || strings.HasSuffix(p, "/"+name) || (len(l.Files) == 1 && len(e.byName) == 1) {
			return i, nil
		}
	}
	return 0, fmt.Errorf("seedsrc: %s has no file %q", q.Name(), p)
}

// PoolSources adapts the qB pool: every enabled server is a Source — an Edge (availability
// only; browsers fetch from the edge's public URL) when its edge node is connected and has a
// public URL, otherwise, when online, the qB WebUI (QB: complete files only, data proxied by
// the main server). Source values are reused while unchanged (they cache file lists).
func PoolSources(pool *qb.Pool, edges func(serverID int64) EdgeClient) func() []Source {
	var mu sync.Mutex
	plain := map[*qb.Server]*QB{}
	viaEdge := map[EdgeClient]*Edge{}
	return func() []Source {
		servers := pool.Servers()
		mu.Lock()
		defer mu.Unlock()
		nextPlain := map[*qb.Server]*QB{}
		nextEdge := map[EdgeClient]*Edge{}
		var out []Source
		for _, s := range servers {
			if !s.Cfg.Enabled {
				continue
			}
			if edges != nil && s.Cfg.EdgeURL != "" {
				if ec := edges(s.Cfg.ID); ec != nil {
					e, ok := viaEdge[ec]
					if !ok || e.BaseURL != s.Cfg.EdgeURL {
						e = &Edge{ServerID: s.Cfg.ID, ServerName: s.Cfg.Name, Client: ec, BaseURL: s.Cfg.EdgeURL}
					}
					nextEdge[ec] = e
					out = append(out, e)
					continue
				}
			}
			if !s.Status().Online {
				continue
			}
			q, ok := plain[s]
			if !ok {
				q = &QB{Server: s}
			}
			nextPlain[s] = q
			out = append(out, q)
		}
		plain, viaEdge = nextPlain, nextEdge
		return out
	}
}
