package meta

import (
	"context"
	"database/sql"
	"log/slog"
	"strings"

	"github.com/darknightlab/onlinetorrent/internal/store"
)

// stateReclassify is the meta_state flag of the one-off re-classification of stored files.
// Bump it whenever the file-name parser changes in a way that should reach existing rows:
//
//	reclassify_specials_v1  specials keep their number and are mapped (migration 5)
//	reclassify_parser_v2    token-elimination parser + torrent-level cross-checks (ClassifyFiles)
//	reclassify_match_v3     per-file work routing (route.go), title-fragment anchor match
//
// The flag is inserted as 'pending' from Go (INSERT OR IGNORE), not by a migration.
const stateReclassify = "reclassify_match_v3"

// ReclassifyOnce runs Reclassify if it has not completed on this database yet (flag in
// meta_state). Safe to call in a goroutine at startup.
func (s *Store) ReclassifyOnce(ctx context.Context, log *slog.Logger) {
	if _, err := s.W.ExecContext(ctx, `INSERT OR IGNORE INTO meta_state(key, value) VALUES (?, 'pending')`, stateReclassify); err != nil {
		log.Warn("meta: reclassify flag", "err", err)
		return
	}
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
	case strings.HasPrefix(method, "title"):
		return 0.6
	}
	return 0.7
}

// Reclassify re-runs the file mapping (mapFiles: classification + per-file routing) over
// the stored paths of every ready torrent and rewrites automatic rows (status auto) whose
// kind, episode or work changed. The anchor is the work the torrent-level match gave
// before (most files mapped to it without routing); torrents without one are re-matched
// (resolveWork). Returns the number of rows changed.
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
	votes := map[string]int{}       // files mapped to a work
	anchorVotes := map[string]int{} // … by the torrent-level match (not routed)
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
			if !isRoutedMethod(f.method) {
				anchorVotes[f.work.String]++
			}
			if methodOf[f.work.String] == "" || f.status == "auto" {
				methodOf[f.work.String] = baseMethod(f.method)
			}
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	paths := make([]string, len(files))
	for i, f := range files {
		paths[i] = f.path
	}

	var work *Work
	method, wconf := "", 0.0
	if len(anchorVotes) > 0 {
		votes = anchorVotes
	}
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
	} else if in, ok := s.GetTorrentInput(ctx, ih); ok {
		work, method, wconf = s.resolveWork(ctx, in)
	}

	n := 0
	now := store.Now()
	res := s.mapFiles(ctx, paths, title, work, method, wconf)
	for i, f := range files {
		if f.status != "auto" {
			continue
		}
		kind, wk, ep, conf, m := res[i].kind, res[i].work, res[i].ep, res[i].conf, res[i].method
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
