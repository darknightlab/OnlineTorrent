// Package seedsrc abstracts the servers that hold torrent data for the HTTP WebSeed
// (today: qBittorrent instances; later: a "torrent client gateway" agent with
// qB / libtorrent / Go / WebTorrent backends). A Source reports which pieces it has
// (Have) and reads byte ranges of files (ReadRange); the webseed handler only serves a
// range when every piece it covers is complete on the chosen source.
package seedsrc

import (
	"errors"
	"strconv"
	"strings"

	"github.com/darknightlab/onlinetorrent/internal/torrentfile"
)

// LFile is one file of a torrent layout, in metainfo order (BEP 47 padding files included,
// so offsets match WebTorrent and libtorrent).
type LFile struct {
	Path   string // "name/dir/file" (single-file torrent: "name")
	Offset int64  // offset in the torrent's concatenated data
	Size   int64
	Pad    bool
}

// Layout maps file byte ranges to pieces.
type Layout struct {
	Infohash    string
	PieceLength int64
	NumPieces   int
	Size        int64
	Files       []LFile
	Hashes      []byte // 20 bytes per piece (SHA-1), for verifying pieces read from disk (edge)
	Single      bool   // single-file torrent (webseed URL without a file path)
}

// NewLayout builds the layout of a parsed .torrent.
func NewLayout(mi *torrentfile.Metainfo) (*Layout, error) {
	if mi == nil || mi.PieceLength <= 0 || len(mi.Files) == 0 {
		return nil, errors.New("seedsrc: metainfo without piece length or files")
	}
	l := &Layout{Infohash: mi.Infohash, PieceLength: mi.PieceLength, Hashes: mi.Pieces}
	for _, f := range mi.Files {
		l.Files = append(l.Files, LFile{Path: f.Path, Offset: l.Size, Size: f.Size, Pad: f.Pad})
		l.Size += f.Size
	}
	l.NumPieces = int((l.Size + l.PieceLength - 1) / l.PieceLength)
	if len(l.Hashes) != 20*l.NumPieces {
		return nil, errors.New("seedsrc: piece hashes don't match the torrent size")
	}
	l.Single = len(mi.Files) == 1 && !strings.Contains(mi.Files[0].Path, "/")
	return l, nil
}

// PieceSize is the length of piece i (the last one may be shorter).
func (l *Layout) PieceSize(i int) int64 {
	if i == l.NumPieces-1 {
		return l.Size - l.PieceLength*int64(l.NumPieces-1)
	}
	return l.PieceLength
}

// Pieces returns the inclusive piece range covering bytes [start, end] of file fi.
func (l *Layout) Pieces(fi int, start, end int64) (first, last int) {
	f := l.Files[fi]
	return int((f.Offset + start) / l.PieceLength), int((f.Offset + end) / l.PieceLength)
}

// FilePieces is the inclusive piece range of the whole file (ok=false for empty files).
func (l *Layout) FilePieces(fi int) (first, last int, ok bool) {
	if l.Files[fi].Size == 0 {
		return 0, 0, false
	}
	first, last = l.Pieces(fi, 0, l.Files[fi].Size-1)
	return first, last, true
}

// Match finds the file a BEP 19 webseed request path refers to. WebTorrent requests
// "<url>/<name>/<dir>/<file>" for multi-file torrents and "<url>" for single-file ones.
func (l *Layout) Match(reqPath string) (int, bool) {
	reqPath = strings.Trim(reqPath, "/")
	if reqPath == "" {
		if len(l.Files) == 1 {
			return 0, true
		}
		return 0, false
	}
	for i, f := range l.Files {
		if f.Path == reqPath {
			return i, true
		}
	}
	// tolerate a missing / different root folder (clients that strip the torrent name)
	for i, f := range l.Files {
		if strings.HasSuffix(f.Path, "/"+reqPath) || strings.HasSuffix(reqPath, "/"+f.Path) {
			return i, true
		}
	}
	return 0, false
}

// PieceHash returns the expected SHA-1 of piece i.
func (l *Layout) PieceHash(i int) []byte { return l.Hashes[20*i : 20*i+20] }

// ParseRange parses a single "bytes=" range of a resource of size n. ok=false → 416.
// No header → whole resource (partial=false).
func ParseRange(h string, n int64) (start, end int64, partial, ok bool) {
	if h == "" {
		return 0, n - 1, false, true
	}
	spec, found := strings.CutPrefix(h, "bytes=")
	if !found || strings.Contains(spec, ",") {
		return 0, 0, false, false
	}
	a, b, found := strings.Cut(strings.TrimSpace(spec), "-")
	if !found {
		return 0, 0, false, false
	}
	switch {
	case a == "": // suffix
		k, err := strconv.ParseInt(b, 10, 64)
		if err != nil || k <= 0 {
			return 0, 0, false, false
		}
		return max(0, n-k), n - 1, true, n > 0
	default:
		st, err := strconv.ParseInt(a, 10, 64)
		if err != nil || st < 0 || st >= n {
			return 0, 0, false, false
		}
		en := n - 1
		if b != "" {
			e, err := strconv.ParseInt(b, 10, 64)
			if err != nil || e < st {
				return 0, 0, false, false
			}
			en = min(e, n-1)
		}
		return st, en, true, true
	}
}
