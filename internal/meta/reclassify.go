package meta

import (
	"context"
	"database/sql"
	"log/slog"
	"strings"

	"github.com/darknightlab/onlinetorrent/internal/store"
)

// stateReclassify is the meta_state flag of the one-off re-classification that gives
// specials (SP/NCOP/NCED/PV/CM) their number and maps them to the work.
const stateReclassify = "reclassify_specials_v1"

// ReclassifyOnce runs Reclassify if it has not completed on this database yet (flag in
// meta_state, set by migration 5). Safe to call in a goroutine at startup.
func (s *Store) ReclassifyOnce(ctx context.Context, log *slog.Logger) {
	var v string
	if err := s.R.QueryRowContext(ctx, `SELECT value FROM meta_state WHERE key = ?`, stateReclassify).Scan(&v); err != nil || v == "done" {
		return
	}
	n, err := s.Reclassify(ctx)
	if err != nil {
		log.Warn("meta: reclassify files", "err", err, "changed", n)
		return
	}
	if _, err := s.W.ExecContext(ctx, `UPDATE meta_state SET value = 'done' WHERE key = ?`, stateReclassify); err != nil {
		log.Warn("meta: reclassify flag", "err", err)
	}
	log.Info("meta: reclassified stored files", "changed", n)
}

type storedFile struct {
	index   int
	path    string
	kind    string
	work    sql.NullString
	episode sql.NullFloat64
	status  string
	method  string
}

// workConf approximates the work-match confidence of a stored method ("moe_tag+name").
func workConf(method string) float64 {
	switch {
	case strings.HasPrefix(method, "moe_tag"):
		return 0.95
	case strings.HasPrefix(method, "manual"):
		return 0.9
	}
	return 0.7
}

// Reclassify re-runs ClassifyFile over the stored paths of every ready torrent and
// rewrites automatic rows (status auto) whose kind, episode or work changed. The work is
// the one already mapped to the torrent's files; torrents without one are only re-matched
// (resolveWork) when a file became mappable (e.g. an NCOP that used to be unmapped).
// Returns the number of rows changed.
func (s *Store) Reclassify(ctx context.Context) (int, error) {
	changed := 0
	var seq int64
	for {
		rows, err := s.R.QueryContext(ctx, `SELECT seq, infohash, title FROM torrents WHERE status = 'ready' AND seq > ? ORDER BY seq LIMIT 500`, seq)
		if err != nil {
			return changed, err
		}
		type tor struct{ ih, title string }
		var batch []tor
		for rows.Next() {
			var t tor
			if err := rows.Scan(&seq, &t.ih, &t.title); err != nil {
				rows.Close()
				return changed, err
			}
			batch = append(batch, t)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return changed, err
		}
		if len(batch) == 0 {
			return changed, nil
		}
		for _, t := range batch {
			n, err := s.reclassifyTorrent(ctx, t.ih, t.title)
			if err != nil {
				return changed, err
			}
			changed += n
		}
	}
}

func (s *Store) reclassifyTorrent(ctx context.Context, ih, title string) (int, error) {
	rows, err := s.R.QueryContext(ctx, `SELECT file_index, path, kind, work_key, episode, status, method FROM torrent_files WHERE infohash = ? ORDER BY file_index`, ih)
	if err != nil {
		return 0, err
	}
	var files []storedFile
	votes := map[string]int{}
	methodOf := map[string]string{}
	for rows.Next() {
		var f storedFile
		if err := rows.Scan(&f.index, &f.path, &f.kind, &f.work, &f.episode, &f.status, &f.method); err != nil {
			rows.Close()
			return 0, err
		}
		files = append(files, f)
		if f.work.Valid && f.status != "rejected" {
			votes[f.work.String]++
			if methodOf[f.work.String] == "" || f.status == "auto" {
				methodOf[f.work.String] = strings.TrimSuffix(f.method, "+name")
			}
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	videos := 0
	for _, f := range files {
		if IsVideo(f.path) {
			videos++
		}
	}

	var work *Work
	method, wconf := "", 0.0
	best := ""
	for k, n := range votes {
		if n > votes[best] || (n == votes[best] && k < best) {
			best = k
		}
	}
	if best != "" {
		if w, err := s.GetWork(ctx, best); err == nil {
			work, method, wconf = w, methodOf[best], workConf(methodOf[best])
		}
	} else {
		// re-match only if a file newly became mappable
		for _, f := range files {
			if k, _ := classify(f.path, title, videos, nil); f.status == "auto" && MappableKind(k) && !MappableKind(f.kind) {
				if in, ok := s.GetTorrentInput(ctx, ih); ok {
					work, method, wconf = s.resolveWork(ctx, in)
				}
				break
			}
		}
	}

	n := 0
	now := store.Now()
	for _, f := range files {
		if f.status != "auto" {
			continue
		}
		kind, p := classify(f.path, title, videos, work)
		wk, ep, conf, m := fileMapping(work, method, wconf, kind, p)
		oldWK := ""
		if f.work.Valid {
			oldWK = f.work.String
		}
		newWK, _ := wk.(string)
		sameEp := (ep == nil && !f.episode.Valid) || (ep != nil && f.episode.Valid && *ep == f.episode.Float64)
		if kind == f.kind && newWK == oldWK && sameEp {
			continue
		}
		if _, err := s.W.ExecContext(ctx, `UPDATE torrent_files SET kind=?, work_key=?, episode=?, confidence=?, method=?, updated_at=? WHERE infohash=? AND file_index=? AND status='auto'`,
			kind, wk, ep, conf, m, now, ih, f.index); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}
