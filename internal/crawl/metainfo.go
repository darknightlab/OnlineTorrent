package crawl

import (
	"bytes"
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"
)

// A minimal bencode decoder: enough to read .torrent metainfo (BEP 3) and compute the v1
// infohash (SHA-1 of the raw bencoded "info" dictionary). Hybrid v1+v2 torrents carry a v1
// info dict too; pure v2 torrents (no "pieces") are rejected.

// MaxTorrentBytes caps .torrent downloads.
const MaxTorrentBytes = 5 << 20

const maxDepth = 64

type bdecoder struct {
	b     []byte
	pos   int
	depth int
	// raw byte span of the top-level "info" value
	infoStart, infoEnd int
}

var errBencode = errors.New("invalid bencode")

func (d *bdecoder) value(top bool) (any, error) {
	if d.pos >= len(d.b) {
		return nil, errBencode
	}
	switch c := d.b[d.pos]; {
	case c == 'i':
		end := bytes.IndexByte(d.b[d.pos:], 'e')
		if end < 0 {
			return nil, errBencode
		}
		n, err := strconv.ParseInt(string(d.b[d.pos+1:d.pos+end]), 10, 64)
		if err != nil {
			return nil, errBencode
		}
		d.pos += end + 1
		return n, nil
	case c >= '0' && c <= '9':
		colon := bytes.IndexByte(d.b[d.pos:], ':')
		if colon < 0 || colon > 10 {
			return nil, errBencode
		}
		n, err := strconv.Atoi(string(d.b[d.pos : d.pos+colon]))
		if err != nil || n < 0 || d.pos+colon+1+n > len(d.b) {
			return nil, errBencode
		}
		s := d.b[d.pos+colon+1 : d.pos+colon+1+n]
		d.pos += colon + 1 + n
		return s, nil
	case c == 'l':
		if d.depth++; d.depth > maxDepth {
			return nil, errBencode
		}
		d.pos++
		var out []any
		for {
			if d.pos >= len(d.b) {
				return nil, errBencode
			}
			if d.b[d.pos] == 'e' {
				d.pos++
				d.depth--
				return out, nil
			}
			v, err := d.value(false)
			if err != nil {
				return nil, err
			}
			out = append(out, v)
		}
	case c == 'd':
		if d.depth++; d.depth > maxDepth {
			return nil, errBencode
		}
		d.pos++
		out := map[string]any{}
		for {
			if d.pos >= len(d.b) {
				return nil, errBencode
			}
			if d.b[d.pos] == 'e' {
				d.pos++
				d.depth--
				return out, nil
			}
			k, err := d.value(false)
			if err != nil {
				return nil, err
			}
			ks, ok := k.([]byte)
			if !ok {
				return nil, errBencode
			}
			start := d.pos
			v, err := d.value(false)
			if err != nil {
				return nil, err
			}
			if top && string(ks) == "info" {
				d.infoStart, d.infoEnd = start, d.pos
			}
			out[string(ks)] = v
		}
	}
	return nil, errBencode
}

// TorrentFile is one file of a metainfo.
type TorrentFile struct {
	Path string
	Size int64
}

// Metainfo is the parsed part of a .torrent we use.
type Metainfo struct {
	Infohash string // v1, 40 lower-case hex
	Name     string
	Files    []TorrentFile
	Size     int64
}

// ParseTorrent decodes a .torrent file.
func ParseTorrent(b []byte) (*Metainfo, error) {
	if len(b) > MaxTorrentBytes {
		return nil, errors.New("torrent file too large")
	}
	d := &bdecoder{b: b}
	if len(b) == 0 || b[0] != 'd' {
		return nil, errors.New("not a torrent file")
	}
	v, err := d.value(true)
	if err != nil {
		return nil, err
	}
	root, _ := v.(map[string]any)
	info, ok := root["info"].(map[string]any)
	if !ok || d.infoEnd <= d.infoStart {
		return nil, errors.New("torrent has no info dictionary")
	}
	if _, ok := info["pieces"].([]byte); !ok {
		return nil, errors.New("v2-only torrent (no v1 infohash)")
	}
	sum := sha1.Sum(b[d.infoStart:d.infoEnd])
	m := &Metainfo{Infohash: hex.EncodeToString(sum[:])}
	m.Name = str(info, "name.utf-8")
	if m.Name == "" {
		m.Name = str(info, "name")
	}
	if files, ok := info["files"].([]any); ok {
		for _, fv := range files {
			f, ok := fv.(map[string]any)
			if !ok {
				continue
			}
			size, _ := f["length"].(int64)
			parts, ok := f["path.utf-8"].([]any)
			if !ok {
				parts, _ = f["path"].([]any)
			}
			var segs []string
			for _, p := range parts {
				if s, ok := p.([]byte); ok {
					segs = append(segs, cleanSeg(string(s)))
				}
			}
			if len(segs) == 0 {
				segs = []string{"_"}
			}
			// BEP 47 padding files are kept so file indexes match WebTorrent and qB (both count them);
			// classification marks them "other".
			p := strings.Join(segs, "/")
			if m.Name != "" {
				p = cleanSeg(m.Name) + "/" + p // qB / WebTorrent file paths include the torrent name
			}
			m.Files = append(m.Files, TorrentFile{Path: p, Size: size})
			m.Size += size
		}
	} else {
		size, ok := info["length"].(int64)
		if !ok {
			return nil, errors.New("torrent has neither length nor files")
		}
		m.Files = []TorrentFile{{Path: cleanSeg(m.Name), Size: size}}
		m.Size = size
	}
	if len(m.Files) == 0 {
		return nil, errors.New("torrent has no files")
	}
	return m, nil
}

func str(m map[string]any, k string) string {
	b, _ := m[k].([]byte)
	return string(b)
}

// cleanSeg makes a path segment safe for display/storage (valid UTF-8, no separators).
func cleanSeg(s string) string {
	if !utf8.ValidString(s) {
		s = strings.ToValidUTF8(s, "�")
	}
	return strings.NewReplacer("/", "_", "\\", "_", "\x00", "").Replace(s)
}

// String is for logs.
func (m *Metainfo) String() string {
	return fmt.Sprintf("%s %q (%d files, %d bytes)", m.Infohash, m.Name, len(m.Files), m.Size)
}
