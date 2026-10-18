package meta

import (
	"bytes"
	"compress/flate"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"hash"
	"hash/crc32"
	"io"
	"net/http"
	"strconv"
)

// Remote zip reading over HTTP Range requests: only the central directory and the entries we
// need are downloaded, and entries are inflated as a stream (no temp file, constant memory).
// GitHub release downloads honour absolute ranges ("bytes=a-b") but not suffix ranges.

type zipEntry struct {
	Name        string
	Method      uint16
	CRC32       uint32
	CompSize    uint64
	Size        uint64
	LocalOffset uint64
}

type rangeZip struct {
	hc   *http.Client
	url  string
	size int64
	ua   string
}

func (z *rangeZip) get(ctx context.Context, from, to int64) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, z.url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Range", "bytes="+strconv.FormatInt(from, 10)+"-"+strconv.FormatInt(to, 10))
	if z.ua != "" {
		req.Header.Set("User-Agent", z.ua)
	}
	resp, err := z.hc.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusPartialContent {
		resp.Body.Close()
		return nil, fmt.Errorf("range %d-%d: HTTP %d (want 206)", from, to, resp.StatusCode)
	}
	return resp, nil
}

func (z *rangeZip) read(ctx context.Context, from, to int64) ([]byte, error) {
	resp, err := z.get(ctx, from, to)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, to-from+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) != to-from+1 {
		return nil, fmt.Errorf("range %d-%d: short read %d", from, to, len(b))
	}
	return b, nil
}

// entries reads the central directory (zip64 aware).
func (z *rangeZip) entries(ctx context.Context) ([]zipEntry, error) {
	const tailLen = 64 << 10
	from := max(0, z.size-tailLen)
	tail, err := z.read(ctx, from, z.size-1)
	if err != nil {
		return nil, err
	}
	eocd := bytes.LastIndex(tail, []byte("PK\x05\x06"))
	if eocd < 0 || len(tail)-eocd < 22 {
		return nil, errors.New("zip: end of central directory not found")
	}
	le := binary.LittleEndian
	count := uint64(le.Uint16(tail[eocd+10:]))
	cdSize := uint64(le.Uint32(tail[eocd+12:]))
	cdOff := uint64(le.Uint32(tail[eocd+16:]))
	if count == 0xffff || cdSize == 0xffffffff || cdOff == 0xffffffff {
		// zip64 end of central directory locator sits right before the EOCD
		loc := eocd - 20
		if loc < 0 || !bytes.Equal(tail[loc:loc+4], []byte("PK\x06\x07")) {
			return nil, errors.New("zip64: locator not found")
		}
		rec := int64(le.Uint64(tail[loc+8:])) - from
		if rec < 0 || rec+56 > int64(len(tail)) || !bytes.Equal(tail[rec:rec+4], []byte("PK\x06\x06")) {
			return nil, errors.New("zip64: end record not in tail")
		}
		count, cdSize, cdOff = le.Uint64(tail[rec+32:]), le.Uint64(tail[rec+40:]), le.Uint64(tail[rec+48:])
	}
	var cd []byte
	if int64(cdOff) >= from && int64(cdOff+cdSize) <= z.size {
		cd = tail[int64(cdOff)-from : int64(cdOff)-from+int64(cdSize)]
	} else if cd, err = z.read(ctx, int64(cdOff), int64(cdOff+cdSize)-1); err != nil {
		return nil, err
	}
	out := make([]zipEntry, 0, count)
	for p := 0; p+46 <= len(cd) && uint64(len(out)) < count; {
		if !bytes.Equal(cd[p:p+4], []byte("PK\x01\x02")) {
			return nil, errors.New("zip: bad central directory entry")
		}
		e := zipEntry{Method: le.Uint16(cd[p+10:]), CRC32: le.Uint32(cd[p+16:]),
			CompSize: uint64(le.Uint32(cd[p+20:])), Size: uint64(le.Uint32(cd[p+24:])), LocalOffset: uint64(le.Uint32(cd[p+42:]))}
		nl, xl, cl := int(le.Uint16(cd[p+28:])), int(le.Uint16(cd[p+30:])), int(le.Uint16(cd[p+32:]))
		if p+46+nl+xl+cl > len(cd) {
			return nil, errors.New("zip: truncated central directory")
		}
		e.Name = string(cd[p+46 : p+46+nl])
		// zip64 extra field: values present only for fields saturated in the header
		for x := cd[p+46+nl : p+46+nl+xl]; len(x) >= 4; {
			id, sz := le.Uint16(x), int(le.Uint16(x[2:]))
			if 4+sz > len(x) {
				break
			}
			if id == 1 {
				d := x[4 : 4+sz]
				if e.Size == 0xffffffff && len(d) >= 8 {
					e.Size, d = le.Uint64(d), d[8:]
				}
				if e.CompSize == 0xffffffff && len(d) >= 8 {
					e.CompSize, d = le.Uint64(d), d[8:]
				}
				if e.LocalOffset == 0xffffffff && len(d) >= 8 {
					e.LocalOffset = le.Uint64(d)
				}
			}
			x = x[4+sz:]
		}
		out = append(out, e)
		p += 46 + nl + xl + cl
	}
	return out, nil
}

// open streams one entry's uncompressed content. Close verifies size and CRC32.
func (z *rangeZip) open(ctx context.Context, e zipEntry) (io.ReadCloser, error) {
	if e.Method != 0 && e.Method != 8 {
		return nil, fmt.Errorf("zip: %s: unsupported method %d", e.Name, e.Method)
	}
	// local header is 30 bytes + name + extra (its extra may differ from the central one)
	hdr, err := z.read(ctx, int64(e.LocalOffset), int64(e.LocalOffset)+29)
	if err != nil {
		return nil, err
	}
	if !bytes.Equal(hdr[:4], []byte("PK\x03\x04")) {
		return nil, errors.New("zip: bad local header")
	}
	start := int64(e.LocalOffset) + 30 + int64(binary.LittleEndian.Uint16(hdr[26:])) + int64(binary.LittleEndian.Uint16(hdr[28:]))
	resp, err := z.get(ctx, start, start+int64(e.CompSize)-1)
	if err != nil {
		return nil, err
	}
	var r io.Reader = io.LimitReader(resp.Body, int64(e.CompSize))
	var fr io.ReadCloser
	if e.Method == 8 {
		fr = flate.NewReader(r)
		r = fr
	}
	h := crc32.NewIEEE()
	return &zipStream{r: io.TeeReader(r, h), h: h, e: e, body: resp.Body, fr: fr}, nil
}

type zipStream struct {
	r    io.Reader
	h    hash.Hash32
	e    zipEntry
	n    uint64
	body io.Closer
	fr   io.Closer
	eof  bool
}

func (s *zipStream) Read(p []byte) (int, error) {
	n, err := s.r.Read(p)
	s.n += uint64(n)
	if err == io.EOF {
		s.eof = true
		if s.n != s.e.Size {
			return n, fmt.Errorf("zip: %s: size %d, want %d", s.e.Name, s.n, s.e.Size)
		}
		if s.h.Sum32() != s.e.CRC32 {
			return n, fmt.Errorf("zip: %s: CRC32 mismatch", s.e.Name)
		}
	}
	return n, err
}

func (s *zipStream) Close() error {
	if s.fr != nil {
		s.fr.Close()
	}
	return s.body.Close()
}
