package edge

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/darknightlab/onlinetorrent/internal/seedsrc"
)

// libtorrent's partfile (src/part_file.cpp, libtorrent 2.0): bytes of pieces that belong to
// files with priority 0 — e.g. the part of a boundary piece shared between the file being
// played and a skipped neighbour — are written to "<save path>/.<infohash hex>.parts"
// instead of the (non-existent) skipped file. Layout, verified against qBittorrent 5.2.4 /
// libtorrent 2.0.13 (web/e2e/fileselect):
//
//	u32 BE max_pieces | u32 BE piece_size | max_pieces × i32 BE slot (-1 = none)
//	padded to a multiple of 1024 bytes, then slot i at header + i*piece_size;
//	within a slot the bytes sit at their offset inside the piece.
//
// The file is named after the torrent's "best" infohash: v1 for v1-only torrents. Hybrid
// v1+v2 torrents are piece-aligned with BEP 47 padding, so no piece is shared between
// files and their partfile is never needed (we still try the v1 name).

type partFile struct {
	f      *os.File
	pieces int
	psize  int64
	header int64
	slots  []int32
}

var errNoPart = errors.New("edge: no partfile")

// openPart opens and validates the partfile of torrent ih in save (edge-local path).
func (d *Disk) openPart(save, ih string, l *seedsrc.Layout) (*partFile, error) {
	p := filepath.Join(save, "."+ih+".parts")
	fh, err := d.open(p) // root check, symlinks resolved
	if err != nil {
		return nil, errNoPart
	}
	var hdr [8]byte
	if _, err := io.ReadFull(fh, hdr[:]); err != nil {
		fh.Close()
		return nil, fmt.Errorf("edge: partfile header: %w", err)
	}
	n := int(binary.BigEndian.Uint32(hdr[0:4]))
	psz := int64(binary.BigEndian.Uint32(hdr[4:8]))
	if n != l.NumPieces || psz != l.PieceLength {
		fh.Close()
		return nil, fmt.Errorf("edge: partfile is for %d pieces of %d bytes, torrent has %d of %d", n, psz, l.NumPieces, l.PieceLength)
	}
	raw := make([]byte, 4*n)
	if _, err := io.ReadFull(fh, raw); err != nil {
		fh.Close()
		return nil, fmt.Errorf("edge: partfile slots: %w", err)
	}
	slots := make([]int32, n)
	for i := range slots {
		slots[i] = int32(binary.BigEndian.Uint32(raw[4*i:]))
	}
	return &partFile{f: fh, pieces: n, psize: psz, header: (int64(2+n)*4 + 1023) &^ 1023, slots: slots}, nil
}

// read fills seg with the bytes at offset off inside piece pi.
func (p *partFile) read(pi int, seg []byte, off int64) error {
	if pi < 0 || pi >= p.pieces || p.slots[pi] < 0 {
		return fmt.Errorf("edge: piece %d not in the partfile", pi)
	}
	at := p.header + int64(p.slots[pi])*p.psize + off
	if _, err := p.f.ReadAt(seg, at); err != nil {
		return fmt.Errorf("edge: partfile read: %w", err)
	}
	return nil
}
