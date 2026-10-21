package meta

// Catalogue lookups of the decision model's post-checks (clef.Refine): prequel numbering
// and season continuation.

import (
	"context"
	"database/sql"
	"strconv"
)

// CandidateFor describes a work as a decision-model candidate (season, type, episodes).
func (s *Store) CandidateFor(ctx context.Context, workKey string) (ClefCandidate, error) {
	w, err := s.GetWork(ctx, workKey)
	if err != nil {
		return ClefCandidate{}, err
	}
	return s.candidateOf(ctx, w, "rule"), nil
}

// MainRange is (lowest sort, highest sort, count) of the work's official main episodes
// (zeros: no list).
func (s *Store) MainRange(ctx context.Context, workKey string) (lo, hi float64, n int) {
	var a, b sql.NullFloat64
	_ = s.R.QueryRowContext(ctx, `SELECT MIN(sort), MAX(sort), COUNT(*) FROM episodes WHERE work_key = ? AND type = 0`, workKey).Scan(&a, &b, &n)
	return a.Float64, b.Float64, n
}

// DirectPrequel is the single direct TV/WEB prequel of a work (bgm relation 前传 from the
// work, or 续集 pointing to it). ok=false when there is none or more than one.
func (s *Store) DirectPrequel(ctx context.Context, workKey string) (string, bool) {
	w, err := s.GetWork(ctx, workKey)
	if err != nil {
		return "", false
	}
	id := bgmIDOf(w)
	if id == 0 {
		return "", false
	}
	rows, err := s.R.QueryContext(ctx, `SELECT related_id FROM subject_relations WHERE subject_id = ? AND relation_type = 2
		UNION SELECT subject_id FROM subject_relations WHERE related_id = ? AND relation_type = 3`, id, id)
	if err != nil {
		return "", false
	}
	var ids []int64
	for rows.Next() {
		var rid int64
		if rows.Scan(&rid) == nil {
			ids = append(ids, rid)
		}
	}
	rows.Close()
	found := ""
	for _, rid := range ids {
		pw, err := s.WorkByExternal(ctx, "bgm", strconv.FormatInt(rid, 10))
		if err != nil {
			continue
		}
		typ := (&famWork{w: pw}).kind()
		if typ != "tv" && typ != "web" {
			continue
		}
		if found != "" && found != pw.Key {
			return "", false
		}
		found = pw.Key
	}
	return found, found != ""
}
