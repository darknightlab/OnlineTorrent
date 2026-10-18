package meta

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/darknightlab/onlinetorrent/internal/store"
	"github.com/darknightlab/onlinetorrent/internal/torrentfile"
)

// Raw .torrent files (table torrent_metainfo, migration 7). Every stored file is a bencoded
// metainfo ≤ 5 MB whose v1 infohash equals its key, so it can be handed to browsers as-is
// (GET /api/v1/torrents/hash/{ih}/torrent, magnet xs=).

// Metainfo sources.
const (
	MetainfoCrawl = "crawl"       // .torrent downloaded from a feed
	MetainfoQB    = "qb"          // qBittorrent torrents/export
	MetainfoMoe   = "bangumi.moe" // bangumi.moe /download/torrent/{id}
)

// ErrMetainfoMismatch: the data is not a torrent for the given infohash.
var ErrMetainfoMismatch = errors.New("torrent file does not match the infohash")

// PutMetainfo validates and stores a raw .torrent for ih (an existing row is kept: the info
// dict is identical by construction, only the outer keys such as trackers could differ).
func (s *Store) PutMetainfo(ctx context.Context, ih string, data []byte, source string) error {
	if len(data) > torrentfile.MaxTorrentBytes {
		return fmt.Errorf("%w: too large", ErrMetainfoMismatch)
	}
	mi, err := torrentfile.ParseTorrent(data)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrMetainfoMismatch, err)
	}
	if mi.Infohash != ih {
		return fmt.Errorf("%w: got %s", ErrMetainfoMismatch, mi.Infohash)
	}
	_, err = s.W.ExecContext(ctx, `INSERT INTO torrent_metainfo(infohash, data, source, created_at) VALUES (?, ?, ?, ?)
		ON CONFLICT(infohash) DO NOTHING`, ih, data, source, store.Now())
	return err
}

// GetMetainfo returns the stored .torrent of ih (store.ErrNotFound if none).
func (s *Store) GetMetainfo(ctx context.Context, ih string) ([]byte, error) {
	var b []byte
	err := s.R.QueryRowContext(ctx, `SELECT data FROM torrent_metainfo WHERE infohash = ?`, ih).Scan(&b)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, store.ErrNotFound
	}
	return b, err
}

// HasMetainfo reports whether a .torrent of ih is stored (cheap; no blob read).
func (s *Store) HasMetainfo(ctx context.Context, ih string) bool {
	var one int
	return s.R.QueryRowContext(ctx, `SELECT 1 FROM torrent_metainfo WHERE infohash = ?`, ih).Scan(&one) == nil
}
