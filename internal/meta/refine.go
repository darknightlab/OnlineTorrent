package meta

// Catalogue lookups of the decision model's post-checks (clef.Refine): prequel numbering,
// the batch rule's unique-fit and year checks.

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
		c := s.candidateOf(ctx, pw, "rule")
		if c.Type != "tv" && c.Type != "web" {
			continue
		}
		if found != "" && found != pw.Key {
			return "", false
		}
		found = pw.Key
	}
	return found, found != ""
}

// UniqueFit reports whether workKey is the only work, among others and the work's family
// (one relation step), that is named by texts (NameEvidence), fits the stated season and
// lists every main episode number of nums (as written, split-cour shift applied). The work
// itself must list them too (its season is checked by the caller).
func (s *Store) UniqueFit(ctx context.Context, workKey string, others []string, nums []float64, season float64, texts []string) bool {
	lists := func(key string) bool {
		for _, n := range nums {
			if _, ok := s.ValidEpisode(ctx, key, EpisodeKey{Kind: KeyMain, Number: n}); !ok {
				return false
			}
		}
		return true
	}
	if !lists(workKey) {
		return false
	}
	set := map[string]bool{}
	for _, o := range others {
		set[o] = true
	}
	if w, err := s.GetWork(ctx, workKey); err == nil {
		if id := bgmIDOf(w); id > 0 {
			for _, rid := range s.relatedBgm(ctx, id, familyRelations, 1, 200) {
				if rw, err := s.WorkByExternal(ctx, "bgm", strconv.FormatInt(rid, 10)); err == nil {
					set[rw.Key] = true
				}
			}
		}
	}
	delete(set, workKey)
	for o := range set {
		c, err := s.CandidateFor(ctx, o)
		if err != nil || !c.SeasonFits(season) || !lists(o) || !s.NameEvidence(ctx, o, texts) {
			continue
		}
		return false
	}
	return true
}

// YearsFit: when texts write 4-digit years (1900–2099), at least one of them must lie within
// the work's airing years (begin … end); no year written: true.
func (s *Store) YearsFit(ctx context.Context, workKey string, texts []string) bool {
	var years []int
	for _, t := range texts {
		years = append(years, yearsIn(t)...)
	}
	if len(years) == 0 {
		return true
	}
	w, err := s.GetWork(ctx, workKey)
	if err != nil {
		return false
	}
	b, e := parseDay(w.Begin), parseDay(w.End)
	if b.IsZero() {
		return false
	}
	from, to := b.In(jst).Year(), b.In(jst).Year()
	if !e.IsZero() {
		to = e.In(jst).Year()
	}
	for _, y := range years {
		if y >= from && y <= to {
			return true
		}
	}
	return false
}
