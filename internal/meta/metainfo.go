package meta

import (
	"context"
	"database/sql"
	"encoding/json"
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
	ann, _ := json.Marshal(nonNilStrings(mi.Announce))
	_, err = s.W.ExecContext(ctx, `INSERT INTO torrent_metainfo(infohash, data, source, created_at, announce) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(infohash) DO NOTHING`, ih, data, source, store.Now(), string(ann))
	return err
}

func nonNilStrings(v []string) []string {
	if v == nil {
		return []string{}
	}
	return v
}

// Announce returns the trackers listed in the stored .torrent of ih (nil if none stored).
// Rows stored before migration 8 are parsed once and backfilled.
func (s *Store) Announce(ctx context.Context, ih string) []string {
	m := s.AnnounceMany(ctx, []string{ih})
	return m[ih]
}

// AnnounceMany is Announce for many infohashes (missing ones are absent from the map).
func (s *Store) AnnounceMany(ctx context.Context, ihs []string) map[string][]string {
	out := map[string][]string{}
	for start := 0; start < len(ihs); start += 500 {
		part := ihs[start:min(len(ihs), start+500)]
		args := make([]any, len(part))
		for i, ih := range part {
			args[i] = ih
		}
		rows, err := s.R.QueryContext(ctx, `SELECT infohash, announce FROM torrent_metainfo WHERE infohash IN (`+placeholders(len(part))+`)`, args...)
		if err != nil {
			return out
		}
		var missing []string
		for rows.Next() {
			var ih string
			var ann sql.NullString
			if rows.Scan(&ih, &ann) != nil {
				continue
			}
			if !ann.Valid {
				missing = append(missing, ih)
				continue
			}
			var l []string
			_ = json.Unmarshal([]byte(ann.String), &l)
			out[ih] = l
		}
		rows.Close()
		for _, ih := range missing {
			out[ih] = s.backfillAnnounce(ctx, ih)
		}
	}
	return out
}

func (s *Store) backfillAnnounce(ctx context.Context, ih string) []string {
	b, err := s.GetMetainfo(ctx, ih)
	if err != nil {
		return nil
	}
	var l []string
	if mi, err := torrentfile.ParseTorrent(b); err == nil {
		l = mi.Announce
	}
	ann, _ := json.Marshal(nonNilStrings(l))
	_, _ = s.W.ExecContext(ctx, `UPDATE torrent_metainfo SET announce = ? WHERE infohash = ?`, string(ann), ih)
	return l
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
