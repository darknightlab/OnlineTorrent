package meta

import (
	"context"
	"strings"
	"testing"

	"github.com/darknightlab/onlinetorrent/internal/torrentfile"
)

func TestMetainfoAnnounce(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	raw := []byte("d8:announce18:udp://a:1/announce4:infod6:lengthi5e4:name5:a.mkv12:piece lengthi16384e6:pieces20:" + strings.Repeat("x", 20) + "ee")
	mi, err := torrentfile.ParseTorrent(raw)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.PutMetainfo(ctx, mi.Infohash, raw, MetainfoCrawl); err != nil {
		t.Fatal(err)
	}
	if got := s.Announce(ctx, mi.Infohash); len(got) != 1 || got[0] != "udp://a:1/announce" {
		t.Fatalf("announce %v", got)
	}
	// rows stored before migration 8 (announce NULL) are backfilled
	if _, err := s.W.ExecContext(ctx, `UPDATE torrent_metainfo SET announce = NULL`); err != nil {
		t.Fatal(err)
	}
	m := s.AnnounceMany(ctx, []string{mi.Infohash, "00"})
	if len(m) != 1 || m[mi.Infohash][0] != "udp://a:1/announce" {
		t.Fatalf("backfill %v", m)
	}
	var ann string
	_ = s.R.QueryRowContext(ctx, `SELECT announce FROM torrent_metainfo`).Scan(&ann)
	if ann != `["udp://a:1/announce"]` {
		t.Fatalf("stored %q", ann)
	}
}
