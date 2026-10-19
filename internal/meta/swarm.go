package meta

import (
	"context"
	"database/sql"
	"strings"

	"github.com/darknightlab/onlinetorrent/internal/store"
)

// Swarm statistics (table swarm_stats, migration 8), written by internal/swarm.

// SwarmStats is one row. Nil counts are unknown (no tracker of that kind answered yet).
type SwarmStats struct {
	Infohash       string
	BTSeeders      *int
	BTLeechers     *int
	BTCompleted    *int
	WTSeeders      *int
	WTLeechers     *int
	ServerComplete bool
	ServerProgress float64
	Source         string // scrape | rss
	CheckedAt      string // counts observed at ('' = never)
	ScrapedAt      string // last scrape attempt ('' = never scraped)
	Error          string
}

// SwarmView is the public JSON shape (API field "swarm"). Nil counts are unknown;
// CheckedAt is nil when no tracker / feed numbers are known yet.
type SwarmView struct {
	BTSeeders      *int    `json:"bt_seeders"`
	BTLeechers     *int    `json:"bt_leechers"`
	WTSeeders      *int    `json:"wt_seeders"`
	WTLeechers     *int    `json:"wt_leechers"`
	ServerComplete bool    `json:"server_complete"`
	CheckedAt      *string `json:"checked_at"`
}

// View converts a row (nil-safe).
func (st *SwarmStats) View() *SwarmView {
	if st == nil {
		return nil
	}
	v := &SwarmView{BTSeeders: st.BTSeeders, BTLeechers: st.BTLeechers, WTSeeders: st.WTSeeders, WTLeechers: st.WTLeechers, ServerComplete: st.ServerComplete}
	if st.CheckedAt != "" {
		c := st.CheckedAt
		v.CheckedAt = &c
	}
	return v
}

// Swarm sources.
const (
	SwarmScrape = "scrape"
	SwarmRSS    = "rss"
)

func placeholders(n int) string {
	if n <= 0 {
		return ""
	}
	return strings.Repeat("?,", n-1) + "?"
}

func nullInt(p *int) any {
	if p == nil {
		return nil
	}
	return *p
}

func intPtr(n sql.NullInt64) *int {
	if !n.Valid {
		return nil
	}
	v := int(n.Int64)
	return &v
}

// SwarmStatsMany returns the stored rows of the given infohashes (absent ones are missing).
func (s *Store) SwarmStatsMany(ctx context.Context, ihs []string) (map[string]*SwarmStats, error) {
	out := map[string]*SwarmStats{}
	for start := 0; start < len(ihs); start += 500 {
		part := ihs[start:min(len(ihs), start+500)]
		args := make([]any, len(part))
		for i, ih := range part {
			args[i] = ih
		}
		rows, err := s.R.QueryContext(ctx, `SELECT infohash, bt_seeders, bt_leechers, bt_completed, wt_seeders, wt_leechers,
			server_complete, server_progress, source, checked_at, scraped_at, error FROM swarm_stats WHERE infohash IN (`+placeholders(len(part))+`)`, args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var st SwarmStats
			var bs, bl, bc, ws, wl sql.NullInt64
			var sc int
			if err := rows.Scan(&st.Infohash, &bs, &bl, &bc, &ws, &wl, &sc, &st.ServerProgress, &st.Source, &st.CheckedAt, &st.ScrapedAt, &st.Error); err != nil {
				rows.Close()
				return nil, err
			}
			st.BTSeeders, st.BTLeechers, st.BTCompleted, st.WTSeeders, st.WTLeechers = intPtr(bs), intPtr(bl), intPtr(bc), intPtr(ws), intPtr(wl)
			st.ServerComplete = sc != 0
			out[st.Infohash] = &st
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

// PutSwarmScrape records scrape results. Nil BT / WT counts keep the previous values (no
// tracker of that kind answered); ScrapedAt is always updated so failing torrents also
// wait for their next turn. CheckedAt is updated when the row carries any count.
func (s *Store) PutSwarmScrape(ctx context.Context, rows []SwarmStats) error {
	if len(rows) == 0 {
		return nil
	}
	tx, err := s.W.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	stmt, err := tx.PrepareContext(ctx, `INSERT INTO swarm_stats(infohash, bt_seeders, bt_leechers, bt_completed, wt_seeders, wt_leechers,
			server_complete, server_progress, source, checked_at, scraped_at, error)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(infohash) DO UPDATE SET
			bt_seeders = CASE WHEN excluded.bt_seeders IS NULL THEN bt_seeders ELSE excluded.bt_seeders END,
			bt_leechers = CASE WHEN excluded.bt_seeders IS NULL THEN bt_leechers ELSE excluded.bt_leechers END,
			bt_completed = CASE WHEN excluded.bt_seeders IS NULL THEN bt_completed ELSE excluded.bt_completed END,
			wt_seeders = CASE WHEN excluded.wt_seeders IS NULL THEN wt_seeders ELSE excluded.wt_seeders END,
			wt_leechers = CASE WHEN excluded.wt_seeders IS NULL THEN wt_leechers ELSE excluded.wt_leechers END,
			server_complete = excluded.server_complete, server_progress = excluded.server_progress,
			source = CASE WHEN excluded.checked_at != '' THEN excluded.source ELSE source END,
			checked_at = CASE WHEN excluded.checked_at != '' THEN excluded.checked_at ELSE checked_at END,
			scraped_at = excluded.scraped_at, error = excluded.error`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, r := range rows {
		checked := r.CheckedAt
		if checked == "" && (r.BTSeeders != nil || r.WTSeeders != nil) {
			checked = r.ScrapedAt
		}
		if _, err := stmt.ExecContext(ctx, r.Infohash, nullInt(r.BTSeeders), nullInt(r.BTLeechers), nullInt(r.BTCompleted), nullInt(r.WTSeeders), nullInt(r.WTLeechers),
			boolInt(r.ServerComplete), r.ServerProgress, SwarmScrape, checked, r.ScrapedAt, store.Trunc(r.Error, 300)); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// PutSwarmInitial stores feed-provided counts (Nyaa RSS nyaa:seeders / nyaa:leechers /
// nyaa:downloads) unless the torrent has been scraped already.
func (s *Store) PutSwarmInitial(ctx context.Context, ih string, seeders, leechers, completed *int) error {
	if seeders == nil && leechers == nil {
		return nil
	}
	_, err := s.W.ExecContext(ctx, `INSERT INTO swarm_stats(infohash, bt_seeders, bt_leechers, bt_completed, source, checked_at)
		VALUES (?,?,?,?,?,?)
		ON CONFLICT(infohash) DO UPDATE SET bt_seeders = excluded.bt_seeders, bt_leechers = excluded.bt_leechers,
			bt_completed = excluded.bt_completed, source = excluded.source, checked_at = excluded.checked_at
		WHERE swarm_stats.scraped_at = ''`, ih, nullInt(seeders), nullInt(leechers), nullInt(completed), SwarmRSS, store.Now())
	return err
}

// SwarmCounts summarises the table for the admin page.
type SwarmCounts struct {
	Torrents int `json:"torrents"` // catalogue torrents
	Scraped  int `json:"scraped"`  // scraped at least once
	Known    int `json:"known"`    // with any count (scrape or feed)
	Dead     int `json:"dead"`     // known, 0 BT and 0 WebTorrent seeders, not on a server
}

// SwarmSummary counts catalogue torrents and their stats.
func (s *Store) SwarmSummary(ctx context.Context) (SwarmCounts, error) {
	var c SwarmCounts
	err := s.R.QueryRowContext(ctx, `SELECT (SELECT COUNT(*) FROM torrents),
		COUNT(*) FILTER (WHERE s.scraped_at != ''),
		COUNT(*) FILTER (WHERE s.checked_at != ''),
		COUNT(*) FILTER (WHERE s.checked_at != '' AND COALESCE(s.bt_seeders,0) = 0 AND COALESCE(s.wt_seeders,0) = 0 AND s.server_complete = 0)
		FROM swarm_stats s JOIN torrents t ON t.infohash = s.infohash`).Scan(&c.Torrents, &c.Scraped, &c.Known, &c.Dead)
	return c, err
}
