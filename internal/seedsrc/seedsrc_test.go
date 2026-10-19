package seedsrc

import (
	"context"
	"crypto/sha1"
	"math/rand"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/darknightlab/onlinetorrent/internal/torrentfile"
)

// testTorrent builds a layout (and the torrent data) from file specs; pad files are zeros.
func testTorrent(t *testing.T, pieceLen int64, files []torrentfile.TorrentFile) (*Layout, []byte) {
	t.Helper()
	var data []byte
	rng := rand.New(rand.NewSource(1))
	for _, f := range files {
		b := make([]byte, f.Size)
		if !f.Pad {
			rng.Read(b)
		}
		data = append(data, b...)
	}
	var hashes []byte
	for off := int64(0); off < int64(len(data)); off += pieceLen {
		sum := sha1.Sum(data[off:min(off+pieceLen, int64(len(data)))])
		hashes = append(hashes, sum[:]...)
	}
	mi := &torrentfile.Metainfo{Infohash: strings.Repeat("ab", 20), Name: "T", Files: files, PieceLength: pieceLen, Pieces: hashes}
	l, err := NewLayout(mi)
	if err != nil {
		t.Fatal(err)
	}
	return l, data
}

func TestLayoutMappingWithPadding(t *testing.T) {
	// piece 100 B: a(150) | pad(50) | b(230) | c(0) | d(20)  → 450 B, 5 pieces
	l, _ := testTorrent(t, 100, []torrentfile.TorrentFile{
		{Path: "T/a.mkv", Size: 150}, {Path: "T/.pad/50", Size: 50, Pad: true}, {Path: "T/b.mkv", Size: 230}, {Path: "T/c.txt", Size: 0}, {Path: "T/d.srt", Size: 20},
	})
	if l.NumPieces != 5 || l.Size != 450 || l.PieceSize(4) != 50 || l.PieceSize(0) != 100 {
		t.Fatalf("layout %+v", l)
	}
	cases := []struct {
		fi          int
		start, end  int64
		first, last int
	}{
		{0, 0, 0, 0, 0},
		{0, 99, 100, 0, 1},
		{0, 149, 149, 1, 1},
		{2, 0, 0, 2, 2},     // b starts at 200 (after the pad)
		{2, 229, 229, 4, 4}, // b ends at 429
		{4, 0, 19, 4, 4},    // d 430..449
	}
	for _, c := range cases {
		f, la := l.Pieces(c.fi, c.start, c.end)
		if f != c.first || la != c.last {
			t.Errorf("Pieces(%d,%d,%d) = %d..%d, want %d..%d", c.fi, c.start, c.end, f, la, c.first, c.last)
		}
	}
	if _, _, ok := l.FilePieces(3); ok {
		t.Error("empty file has pieces")
	}
	for p, want := range map[string]int{"T/b.mkv": 2, "b.mkv": 2, "/T/d.srt": 4, "X/T/a.mkv": 0, "T/.pad/50": 1} {
		if i, ok := l.Match(p); !ok || i != want {
			t.Errorf("Match(%q) = %d,%v want %d", p, i, ok, want)
		}
	}
	if _, ok := l.Match(""); ok {
		t.Error("empty path matched a multi-file torrent")
	}
	single, _ := testTorrent(t, 64, []torrentfile.TorrentFile{{Path: "movie.mp4", Size: 1000}})
	if i, ok := single.Match(""); !ok || i != 0 || !single.Single {
		t.Error("single-file torrent")
	}
}

func TestBitsRanges(t *testing.T) {
	b := NewBits(12)
	for _, i := range []int{0, 1, 2, 7, 10, 11} {
		b.Set(i)
	}
	if got := b.Ranges(); !reflect.DeepEqual(got, []int{0, 2, 7, 7, 10, 11}) {
		t.Fatalf("Ranges = %v", got)
	}
	old := FromStates([]int{2, 2, 0, 0, 0, 0, 0, 0, 0, 0, 1, 2})
	if got := b.RangesNotIn(old); !reflect.DeepEqual(got, []int{2, 2, 7, 7, 10, 10}) {
		t.Fatalf("RangesNotIn = %v", got)
	}
	if !b.All(0, 2) || b.All(0, 3) || b.Full() || b.Count() != 6 {
		t.Fatal("All/Full/Count")
	}
	if got := NewBits(3).Ranges(); len(got) != 0 {
		t.Fatal("empty ranges")
	}
}

type fakeSource struct {
	id    string
	avail Avail
	calls atomic.Int32
}

func (f *fakeSource) ID() string   { return f.id }
func (f *fakeSource) Name() string { return f.id }
func (f *fakeSource) Have(context.Context, *Layout) (Avail, error) {
	f.calls.Add(1)
	return f.avail, nil
}
func TestSetCache(t *testing.T) {
	l, _ := testTorrent(t, 100, []torrentfile.TorrentFile{{Path: "T/a", Size: 300}, {Path: "T/b", Size: 200}})
	partial := &fakeSource{id: "partial", avail: Avail{Known: true, Pieces: FromStates([]int{2, 2, 0, 2, 2})}}
	fullA := &fakeSource{id: "fullA", avail: Avail{Known: true, Pieces: FromStates([]int{2, 2, 2, 0, 0})}}
	none := &fakeSource{id: "none"}
	set := &Set{Sources: func() []Source { return []Source{partial, none, fullA} }, TTL: time.Hour}
	ctx := context.Background()
	if all := set.All(ctx, l); len(all) != 3 || !all[0].Avail.Known || all[1].Avail.Known {
		t.Fatalf("All = %+v", all)
	}
	u, known := set.Union(ctx, l)
	if !known || !u.Full() {
		t.Fatalf("Union = %v %v", u.Ranges(), known)
	}
	// cache: concurrent callers share one Have call per source
	set2 := &Set{Sources: func() []Source { return []Source{partial} }, TTL: time.Hour}
	before := partial.calls.Load()
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); set2.Union(ctx, l) }()
	}
	wg.Wait()
	if n := partial.calls.Load() - before; n != 1 {
		t.Fatalf("Have called %d times, want 1", n)
	}
}

func TestParseRange(t *testing.T) {
	cases := []struct {
		h       string
		n       int64
		s, e    int64
		partial bool
		ok      bool
	}{
		{"", 100, 0, 99, false, true},
		{"bytes=0-9", 100, 0, 9, true, true},
		{"bytes=90-", 100, 90, 99, true, true},
		{"bytes=90-500", 100, 90, 99, true, true},
		{"bytes=-10", 100, 90, 99, true, true},
		{"bytes=-500", 100, 0, 99, true, true},
		{"bytes=100-", 100, 0, 0, false, false},
		{"bytes=5-2", 100, 0, 0, false, false},
		{"bytes=0-1,5-6", 100, 0, 0, false, false},
		{"items=0-1", 100, 0, 0, false, false},
	}
	for _, c := range cases {
		s, e, p, ok := ParseRange(c.h, c.n)
		if ok != c.ok || (ok && (s != c.s || e != c.e || p != c.partial)) {
			t.Errorf("parseRange(%q,%d) = %d,%d,%v,%v", c.h, c.n, s, e, p, ok)
		}
	}
}
