//go:build live

package edge

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/darknightlab/onlinetorrent/internal/qb"
	"github.com/darknightlab/onlinetorrent/internal/store"
	"github.com/darknightlab/onlinetorrent/internal/torrentfile"
)

// TestLiveFileSelection runs against a real qBittorrent (web/e2e/fileselect/run.py starts
// qbittorrent-nox and a libtorrent seeder): otd's Pool.Seed adds the torrent with only
// LIVE_WANT selected, qB downloads it, then the edge's Disk serves the wanted file and the
// boundary pieces shared with skipped files — their skipped bytes from libtorrent's real
// partfile — verified against the seed copy.
//
//	QB_URL LIVE_TORRENT LIVE_WANT LIVE_SAVE (qB's download dir) LIVE_SEED (seeder's dir)
//	optional: QB_USER QB_PASS, LIVE_CATEGORY (default fsel-live), LIVE_WANT2 (requested later:
//	raised, nothing lowered), LIVE_SAVE="" (remote qB: no disk checks)
func TestLiveFileSelection(t *testing.T) {
	env := func(k string) string {
		v := os.Getenv(k)
		if v == "" {
			t.Skip(k + " not set")
		}
		return v
	}
	qbURL, torPath, want := env("QB_URL"), env("LIVE_TORRENT"), env("LIVE_WANT")
	save, seedDir, want2 := os.Getenv("LIVE_SAVE"), os.Getenv("LIVE_SEED"), os.Getenv("LIVE_WANT2")
	cat := os.Getenv("LIVE_CATEGORY")
	if cat == "" {
		cat = "fsel-live"
	}
	tor, err := os.ReadFile(torPath)
	if err != nil {
		t.Fatal(err)
	}
	mi, err := torrentfile.ParseTorrent(tor)
	if err != nil {
		t.Fatal(err)
	}
	ih := mi.Infohash
	ctx := context.Background()
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	pool := qb.NewPool(nil, log)
	client := qb.NewClient(qbURL, os.Getenv("QB_USER"), os.Getenv("QB_PASS"), "")
	client.SetIDCategories(cat)
	pool.SetServersForTest([]*qb.Server{qb.NewServerForTest(store.QBServer{ID: 1, Name: "live", Category: cat, Enabled: true}, client)})
	if ts, err := client.Info(ctx, "", ih); err != nil || len(ts) > 0 {
		t.Fatalf("torrent already on this qB (or qB unreachable): %d %v — not touching it", len(ts), err)
	}
	t.Cleanup(func() {
		if os.Getenv("LIVE_KEEP") == "" {
			_ = client.Delete(context.Background(), []string{ih})
			t.Logf("deleted the test torrent")
		}
	})

	res := pool.Seed(ctx, qb.SeedRequest{Infohash: ih, Name: mi.Name, Torrent: tor, Want: []string{want}, Timeout: 20 * time.Second})
	if len(res.SeededBy) != 1 {
		t.Fatalf("seed: %+v", res)
	}
	var wantIdx = -1
	for i, f := range mi.Files {
		if f.Path == want {
			wantIdx = i
		}
	}
	deadline := time.Now().Add(90 * time.Second)
	for {
		ts, err := client.Info(ctx, "", ih)
		if err == nil && len(ts) == 1 && ts[0].Progress >= 1 {
			t.Logf("qB: state %s, progress %.2f, size (wanted) %d of %d, id %s", ts[0].State, ts[0].Progress, ts[0].Size, ts[0].TotalSize, ts[0].ID)
			if ts[0].Size >= ts[0].TotalSize {
				t.Fatalf("qB downloads the whole torrent (size %d, total %d)", ts[0].Size, ts[0].TotalSize)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("qB never completed the wanted file: %+v %v", ts, err)
		}
		time.Sleep(time.Second)
	}
	files, _ := client.Files(ctx, ih)
	for _, f := range files {
		t.Logf("  qB file %d %s prio %d progress %.3f", f.Index, f.Name, f.Priority, f.Progress)
		if (strings.Trim(f.Name, "/") == want) != (f.Priority > 0) {
			t.Fatalf("priorities: %+v", files)
		}
	}
	if want2 != "" {
		// another file requested later: raised and downloaded, the first one stays wanted
		res := pool.Seed(ctx, qb.SeedRequest{Infohash: ih, Name: mi.Name, Torrent: tor, Want: []string{want2}, Timeout: 20 * time.Second})
		if len(res.SeededBy) != 1 {
			t.Fatalf("second seed: %+v", res)
		}
		deadline := time.Now().Add(90 * time.Second)
		for {
			files, _ = client.Files(ctx, ih)
			done := 0
			for _, f := range files {
				n := strings.Trim(f.Name, "/")
				if (n == want || n == want2) != (f.Priority > 0) {
					t.Fatalf("priorities after the second request: %+v", files)
				}
				if (n == want || n == want2) && f.Progress >= 1 {
					done++
				}
			}
			ts, _ := client.Info(ctx, "", ih)
			if done == 2 && len(ts) == 1 && ts[0].Progress >= 1 {
				t.Logf("second file downloaded too: state %s, wanted size %d of %d", ts[0].State, ts[0].Size, ts[0].TotalSize)
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("second file never completed: %+v", files)
			}
			time.Sleep(time.Second)
		}
	}
	if save == "" {
		return // remote qB: no disk access
	}
	// what is on disk: only the wanted file (+ partfile for v1 boundary pieces)
	_ = filepath.Walk(save, func(p string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() {
			t.Logf("  disk: %s %d", strings.TrimPrefix(p, save), info.Size())
		}
		return nil
	})

	d := &Disk{QB: client, Root: save, Verify: true}
	seedData := func(i int) []byte {
		b, err := os.ReadFile(filepath.Join(seedDir, filepath.FromSlash(mi.Files[i].Path)))
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	read := func(fi int, s, e int64) ([]byte, error) {
		rc, err := d.ReadRange(ctx, ih, fi, s, e)
		if err != nil {
			return nil, err
		}
		defer rc.Close()
		return io.ReadAll(rc)
	}
	f := mi.Files[wantIdx]
	got, err := read(wantIdx, 0, f.Size-1)
	if err != nil || !bytes.Equal(got, seedData(wantIdx)) {
		t.Fatalf("wanted file via the edge: %v (%d bytes)", err, len(got))
	}
	t.Logf("edge served the whole wanted file (%d bytes), every piece SHA-1 verified", len(got))
	// WebTorrent's per-file ranges of the boundary pieces: the skipped neighbours' parts
	layout := d
	l, _ := layout.Layout(ctx, ih)
	first, last, _ := l.FilePieces(wantIdx)
	boundary := 0
	for k, nf := range mi.Files {
		if k == wantIdx || nf.Pad {
			continue
		}
		for _, pi := range []int{first, last} {
			ps, pe := int64(pi)*l.PieceLength, int64(pi)*l.PieceLength+l.PieceSize(pi)
			s, e := max(ps, l.Files[k].Offset), min(pe, l.Files[k].Offset+nf.Size)
			if s >= e {
				continue
			}
			got, err := read(k, s-l.Files[k].Offset, e-l.Files[k].Offset-1)
			want := seedData(k)[s-l.Files[k].Offset : e-l.Files[k].Offset]
			if err != nil || !bytes.Equal(got, want) {
				t.Fatalf("skipped file %s, piece %d part: %v", nf.Path, pi, err)
			}
			boundary++
			t.Logf("edge served %s's %d bytes of boundary piece %d (from the partfile)", nf.Path, len(got), pi)
		}
	}
	if os.Getenv("LIVE_EXPECT_BOUNDARY") == "1" && boundary == 0 {
		t.Fatal("expected boundary pieces shared with skipped files")
	}
	// a piece only in a skipped file is refused
	for k, nf := range mi.Files {
		if k == wantIdx || nf.Pad || nf.Size < 3*l.PieceLength {
			continue
		}
		if _, err := read(k, nf.Size/2, nf.Size/2); err != ErrNotAvailable {
			t.Fatalf("piece inside skipped %s: %v", nf.Path, err)
		}
		break
	}
}
