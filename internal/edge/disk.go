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
//   - every piece a read touches is assembled whole across its files — wanted files from disk,
//     files qB skips (priority 0, otd downloads only the files being played) from libtorrent's
//     partfile ".<infohash>.parts" (where a boundary piece's bytes of a skipped file live),
//     BEP 47 padding as zeros — SHA-1 checked when Verify is set, then sliced.
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
	names   map[string]int // qB file name → priority (0 = skipped)
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
		names[strings.Trim(f.Name, "/")] = f.Priority
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
	saveLocal, err := d.localPath(filepath.ToSlash(save))
	if err != nil {
		return nil, err
	}
	return &pieceReader{d: d, l: l, ih: ih, fi: fi, pos: start, end: end, verify: d.Verify, save: save, saveLocal: saveLocal, names: names, open: map[int]*os.File{}}, nil
}

// pieceReader delivers [pos, end] of file fi piece by piece; each piece is assembled whole
// (pieceBytes) and verified before any of its bytes are returned.
type pieceReader struct {
	d         *Disk
	l         *seedsrc.Layout
	ih        string
	fi        int
	pos       int64 // next file offset to deliver
	end       int64
	verify    bool
	save      string         // qB's save path
	saveLocal string         // the same, on the edge's filesystem
	names     map[string]int // qB name → priority
	open      map[int]*os.File
	missing   map[int]bool // layout files not on disk
	part      *partFile
	partTried bool
	buf       []byte
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
	piece, err := r.pieceBytes(pi)
	if err != nil {
		return err
	}
	pe := ps + int64(len(piece)) - 1
	segEnd := min(pe-f.Offset, r.end) // file coordinates
	r.buf = piece[abs-ps : f.Offset+segEnd-ps+1]
	r.pos = segEnd + 1
	return nil
}

// pieceBytes assembles piece pi from every file it spans.
func (r *pieceReader) pieceBytes(pi int) ([]byte, error) {
	l := r.l
	ps := int64(pi) * l.PieceLength
	buf := make([]byte, l.PieceSize(pi))
	pe := ps + int64(len(buf)) // exclusive
	for k, f := range l.Files {
		s, e := max(ps, f.Offset), min(pe, f.Offset+f.Size)
		if s >= e {
			continue
		}
		seg := buf[s-ps : e-ps]
		if f.Pad {
			continue // zeros
		}
		name, ok := qbName(l, k, r.names)
		if !ok {
			return nil, fmt.Errorf("edge: qB has no file %q", f.Path)
		}
		skipped := r.names[name] == 0
		// a skipped file's bytes are in the partfile; a wanted one's in the file — fall back
		// to the other (priorities change: a raised file gets its partfile bytes moved over)
		readers := []func() error{func() error { return r.readFile(k, name, seg, s-f.Offset) }, func() error { return r.readPart(pi, seg, s-ps) }}
		if skipped {
			readers[0], readers[1] = readers[1], readers[0]
		}
		var err error
		if err = readers[0](); err != nil {
			if err2 := readers[1](); err2 != nil {
				return nil, fmt.Errorf("piece %d, %s: %w (fallback: %v)", pi, f.Path, err, err2)
			}
		}
	}
	if r.verify {
		if sum := sha1.Sum(buf); !bytes.Equal(sum[:], l.PieceHash(pi)) {
			return nil, fmt.Errorf("edge: piece %d failed hash check", pi)
		}
	}
	return buf, nil
}

func (r *pieceReader) readFile(k int, name string, seg []byte, off int64) error {
	if r.missing[k] {
		return errFileMissing
	}
	fh := r.open[k]
	if fh == nil {
		local, err := r.d.localPath(path.Join(filepath.ToSlash(r.save), name))
		if err != nil {
			return err
		}
		if fh, err = r.d.open(local); err != nil {
			if r.missing == nil {
				r.missing = map[int]bool{}
			}
			r.missing[k] = true
			return err
		}
		r.open[k] = fh
	}
	if _, err := fh.ReadAt(seg, off); err != nil {
		if errors.Is(err, io.EOF) {
			return fmt.Errorf("edge: file shorter than expected")
		}
		return err
	}
	return nil
}

var errFileMissing = errors.New("edge: file not on disk")

func (r *pieceReader) readPart(pi int, seg []byte, off int64) error {
	if !r.partTried {
		r.partTried = true
		pf, err := r.d.openPart(r.saveLocal, r.ih, r.l)
		if err != nil {
			return err
		}
		r.part = pf
	}
	if r.part == nil {
		return errNoPart
	}
	return r.part.read(pi, seg, off)
}

func (r *pieceReader) Close() error {
	for _, f := range r.open {
		f.Close()
	}
	if r.part != nil {
		r.part.f.Close()
	}
	return nil
}
