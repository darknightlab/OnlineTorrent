package edge

import (
	"bytes"
	"context"
	"crypto/sha1"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/darknightlab/onlinetorrent/internal/qb"
	"github.com/darknightlab/onlinetorrent/internal/seedsrc"
	"github.com/darknightlab/onlinetorrent/internal/torrentfile"
)

// Disk serves verified pieces of the local qB's torrents straight from its download
// directory (which the edge mounts read-only).
//
//   - layout: from qB's own .torrent (torrents/export), BEP 47 padding files included;
//   - availability: torrents/pieceStates == 2 (verified), cached HaveTTL;
//   - path: save_path (torrents/properties) + file name (torrents/files), mapped from qB's
//     view to the local one (PathMap), tried as is and with qB's ".!qB" suffix for
//     unfinished files; anything that resolves outside Root is refused;
//   - pieces fully inside a read are SHA-1 checked when Verify is set.
type Disk struct {
	QB      *qb.Client
	Root    string        // only files below this directory are served (symlinks resolved)
	PathMap [][2]string   // qB path prefix → local prefix (applied in order, first match)
	Verify  bool          // check SHA-1 of whole pieces inside a read
	HaveTTL time.Duration // default 1 s

	mu   sync.Mutex
	tor  map[string]*torEntry
	have map[string]*haveEntry
}

type torEntry struct {
	l       *seedsrc.Layout
	err     error
	at      time.Time
	save    string         // qB save path
	names   map[string]int // qB file name → index (to detect presence)
	namesAt time.Time
	mu      sync.Mutex
}

type haveEntry struct {
	at    time.Time
	done  chan struct{}
	bits  *seedsrc.Bits
	known bool
	err   error
}

// ErrNotAvailable: a piece of the requested range is not verified yet.
var ErrNotAvailable = errors.New("edge: pieces not downloaded yet")

// ErrNoTorrent: qB doesn't hold the torrent (or not its metadata).
var ErrNoTorrent = errors.New("edge: torrent not on this server")

func (d *Disk) haveTTL() time.Duration {
	if d.HaveTTL > 0 {
		return d.HaveTTL
	}
	return time.Second
}

// Have returns the verified pieces of ih (cached; concurrent callers share one qB call).
func (d *Disk) Have(ctx context.Context, ih string) (*seedsrc.Bits, bool, error) {
	d.mu.Lock()
	if d.have == nil {
		d.have = map[string]*haveEntry{}
	}
	e, ok := d.have[ih]
	if ok && (e.done != nil || time.Since(e.at) < d.haveTTL()) {
		done := e.done
		d.mu.Unlock()
		if done != nil {
			select {
			case <-done:
			case <-ctx.Done():
				return nil, false, ctx.Err()
			}
		}
		d.mu.Lock()
		defer d.mu.Unlock()
		return e.bits, e.known, e.err
	}
	e = &haveEntry{done: make(chan struct{})}
	d.have[ih] = e
	if len(d.have) > 2048 {
		for k, v := range d.have {
			if v.done == nil && time.Since(v.at) > time.Minute {
				delete(d.have, k)
			}
		}
	}
	d.mu.Unlock()
	cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 8*time.Second)
	states, err := d.QB.PieceStates(cctx, ih)
	cancel()
	var bits *seedsrc.Bits
	known := false
	switch {
	case errors.Is(err, qb.ErrNoTorrent):
		err = nil
	case err == nil && len(states) > 0:
		bits, known = seedsrc.FromStates(states), true
	}
	d.mu.Lock()
	e.bits, e.known, e.err, e.at = bits, known, err, time.Now()
	close(e.done)
	e.done = nil
	d.mu.Unlock()
	return bits, known, err
}

// Layout returns the piece layout of ih (from qB's .torrent; cached).
func (d *Disk) Layout(ctx context.Context, ih string) (*seedsrc.Layout, error) {
	t, err := d.entry(ctx, ih)
	if err != nil {
		return nil, err
	}
	return t.l, nil
}

func (d *Disk) entry(ctx context.Context, ih string) (*torEntry, error) {
	d.mu.Lock()
	if d.tor == nil {
		d.tor = map[string]*torEntry{}
	}
	t, ok := d.tor[ih]
	if !ok {
		t = &torEntry{}
		if len(d.tor) > 512 {
			clear(d.tor)
		}
		d.tor[ih] = t
	}
	d.mu.Unlock()
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.l != nil {
		return t, nil
	}
	if t.err != nil && time.Since(t.at) < 15*time.Second {
		return nil, t.err
	}
	b, err := d.QB.Export(ctx, ih)
	if err == nil {
		var mi *torrentfile.Metainfo
		if mi, err = torrentfile.ParseTorrent(b); err == nil {
			if mi.Infohash != ih {
				err = fmt.Errorf("edge: exported torrent has infohash %s", mi.Infohash)
			} else {
				t.l, err = seedsrc.NewLayout(mi)
			}
		}
	}
	if errors.Is(err, qb.ErrNoTorrent) {
		err = ErrNoTorrent
	}
	t.err, t.at = err, time.Now()
	if err != nil {
		return nil, err
	}
	return t, nil
}

// paths refreshes save path + file names (renames, moves) at most every 30 s.
func (d *Disk) paths(ctx context.Context, t *torEntry, ih string) (string, map[string]int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.names != nil && time.Since(t.namesAt) < 30*time.Second {
		return t.save, t.names, nil
	}
	props, err := d.QB.Properties(ctx, ih)
	if err != nil {
		return "", nil, err
	}
	files, err := d.QB.Files(ctx, ih)
	if err != nil {
		return "", nil, err
	}
	names := map[string]int{}
	for _, f := range files {
		names[strings.Trim(f.Name, "/")] = f.Index
	}
	t.save, t.names, t.namesAt = props.SavePath, names, time.Now()
	return t.save, t.names, nil
}

// qbName finds qB's name for layout file fi (qB may hide padding files and strip / rename
// the root folder; match by path, then by suffix).
func qbName(l *seedsrc.Layout, fi int, names map[string]int) (string, bool) {
	p := l.Files[fi].Path
	if _, ok := names[p]; ok {
		return p, true
	}
	for n := range names {
		if strings.HasSuffix(n, "/"+p) || strings.HasSuffix(p, "/"+n) {
			return n, true
		}
	}
	if len(names) == 1 && len(l.Files) == 1 {
		for n := range names {
			return n, true
		}
	}
	return "", false
}

// localPath maps a qB path to the edge's filesystem and checks it stays below Root.
func (d *Disk) localPath(qbPath string) (string, error) {
	p := path.Clean("/" + strings.ReplaceAll(qbPath, "\\", "/"))
	for _, m := range d.PathMap {
		from := strings.TrimRight(m[0], "/")
		if p == from || strings.HasPrefix(p, from+"/") {
			p = strings.TrimRight(m[1], "/") + p[len(from):]
			break
		}
	}
	return filepath.Clean(filepath.FromSlash(p)), nil
}

// open opens the file (final name, else "<name>.!qB") after checking it resolves below Root.
func (d *Disk) open(local string) (*os.File, error) {
	root, err := filepath.EvalSymlinks(d.Root)
	if err != nil {
		return nil, fmt.Errorf("edge: data root: %w", err)
	}
	for _, cand := range []string{local, local + ".!qB"} {
		real, err := filepath.EvalSymlinks(cand)
		if err != nil {
			continue
		}
		if real != root && !strings.HasPrefix(real, root+string(filepath.Separator)) {
			return nil, fmt.Errorf("edge: %q is outside the data root", cand)
		}
		f, err := os.Open(real)
		if err != nil {
			return nil, err
		}
		if st, err := f.Stat(); err != nil || !st.Mode().IsRegular() {
			f.Close()
			return nil, fmt.Errorf("edge: %q is not a regular file", cand)
		}
		return f, nil
	}
	return nil, fmt.Errorf("edge: file not found: %s", local)
}

type zeroReader struct{}

func (zeroReader) Read(p []byte) (int, error) {
	clear(p)
	return len(p), nil
}

// ReadRange returns bytes [start, end] of file fi of torrent ih, refusing ranges with
// unverified pieces (ErrNotAvailable).
func (d *Disk) ReadRange(ctx context.Context, ih string, fi int, start, end int64) (io.ReadCloser, error) {
	t, err := d.entry(ctx, ih)
	if err != nil {
		return nil, err
	}
	l := t.l
	if fi < 0 || fi >= len(l.Files) {
		return nil, fmt.Errorf("edge: no file %d", fi)
	}
	f := l.Files[fi]
	if start < 0 || end < start || end >= f.Size {
		return nil, fmt.Errorf("edge: range %d-%d outside file of %d bytes", start, end, f.Size)
	}
	if f.Pad {
		return io.NopCloser(io.LimitReader(zeroReader{}, end-start+1)), nil
	}
	first, last := l.Pieces(fi, start, end)
	have, known, err := d.Have(ctx, ih)
	if err != nil {
		return nil, err
	}
	if !known {
		return nil, ErrNoTorrent
	}
	if !have.All(first, last) {
		return nil, ErrNotAvailable
	}
	save, names, err := d.paths(ctx, t, ih)
	if err != nil {
		return nil, err
	}
	name, ok := qbName(l, fi, names)
	if !ok {
		return nil, fmt.Errorf("edge: qB has no file %q", f.Path)
	}
	local, err := d.localPath(path.Join(filepath.ToSlash(save), name))
	if err != nil {
		return nil, err
	}
	fh, err := d.open(local)
	if err != nil {
		return nil, err
	}
	return &pieceReader{f: fh, l: l, fi: fi, pos: start, end: end, verify: d.Verify}, nil
}

// pieceReader reads [pos, end] of one file piece by piece; pieces that lie entirely inside
// the read (and inside this file) are hashed before any of their bytes are returned.
type pieceReader struct {
	f      *os.File
	l      *seedsrc.Layout
	fi     int
	pos    int64 // next file offset to deliver
	end    int64
	verify bool
	buf    []byte
}

func (r *pieceReader) Read(p []byte) (int, error) {
	if len(r.buf) == 0 {
		if r.pos > r.end {
			return 0, io.EOF
		}
		if err := r.fill(); err != nil {
			return 0, err
		}
	}
	n := copy(p, r.buf)
	r.buf = r.buf[n:]
	return n, nil
}

func (r *pieceReader) fill() error {
	f := r.l.Files[r.fi]
	abs := f.Offset + r.pos
	pi := int(abs / r.l.PieceLength)
	ps := int64(pi) * r.l.PieceLength
	pe := ps + r.l.PieceSize(pi) - 1                      // piece, torrent coordinates
	segEnd := min(pe-f.Offset, r.end)                     // file coordinates
	whole := ps >= f.Offset+r.pos && pe <= f.Offset+r.end // piece fully inside this read
	n := segEnd - r.pos + 1
	buf := make([]byte, n)
	if _, err := r.f.ReadAt(buf, r.pos); err != nil {
		if errors.Is(err, io.EOF) {
			return fmt.Errorf("edge: file shorter than expected (piece %d)", pi)
		}
		return err
	}
	if r.verify && whole {
		if sum := sha1.Sum(buf); !bytes.Equal(sum[:], r.l.PieceHash(pi)) {
			return fmt.Errorf("edge: piece %d failed hash check", pi)
		}
	}
	r.buf = buf
	r.pos = segEnd + 1
	return nil
}

func (r *pieceReader) Close() error { return r.f.Close() }
