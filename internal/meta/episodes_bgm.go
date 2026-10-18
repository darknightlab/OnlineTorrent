package meta

import (
	"context"
	"database/sql"
	"math"
	"sync"
	"time"
)

// Official episodes come from the bangumi Archive (table episodes).

// OfficialEpisode is one bgm episode of a work.
type OfficialEpisode struct {
	Key      EpisodeKey
	BgmID    int64
	Type     int // 0 main, 1 SP, 2 OP, 3 ED, 4 PV, 5 MAD, 6 other
	Name     string
	NameCN   string
	Airdate  string // YYYY-MM-DD or ""
	Duration string
}

// bgm episode type → episode key kind (5 MAD / 6 other are not listed)
var bgmTypeKind = map[int]string{0: KeyMain, 1: KeySP, 2: KeyNCOP, 3: KeyNCED, 4: KeyPV}

// OfficialEpisodes lists the listable official episodes of a work (main + SP/OP/ED/PV),
// sorted by LessKey.
func (s *Store) OfficialEpisodes(ctx context.Context, workKey string) ([]OfficialEpisode, error) {
	rows, err := s.R.QueryContext(ctx, `SELECT bgm_id,type,sort,name,name_cn,airdate,duration FROM episodes
		WHERE work_key = ? AND type IN (0,1,2,3,4) ORDER BY type, sort`, workKey)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []OfficialEpisode
	seen := map[EpisodeKey]bool{}
	for rows.Next() {
		var e OfficialEpisode
		var sort float64
		if err := rows.Scan(&e.BgmID, &e.Type, &sort, &e.Name, &e.NameCN, &e.Airdate, &e.Duration); err != nil {
			return nil, err
		}
		kind := bgmTypeKind[e.Type]
		if kind == "" || sort < 0 || sort > 5000 {
			continue
		}
		if kind != KeyMain && sort == 0 {
			sort = 1 // unnumbered specials
		}
		e.Key = EpisodeKey{Kind: kind, Number: sort}
		if seen[e.Key] {
			continue // duplicate sort (wiki data glitch): keep the first
		}
		seen[e.Key] = true
		out = append(out, e)
	}
	return out, rows.Err()
}

// AiredOfficial counts official main episodes aired by now (airdate ≤ today in JST);
// ok=false when the work has no official episode data.
func (s *Store) AiredOfficial(ctx context.Context, workKey string, now time.Time) (aired int, ok bool) {
	var total, dated, past int
	today := now.In(jst).Format("2006-01-02")
	err := s.R.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(SUM(airdate != ''),0), COALESCE(SUM(airdate != '' AND airdate <= ?),0)
		FROM episodes WHERE work_key = ? AND type = 0`, today, workKey).Scan(&total, &dated, &past)
	if err != nil || total == 0 {
		return 0, false
	}
	if dated == 0 {
		return total, true // no air dates at all (old/OVA entries): treat as released
	}
	return past, true
}

// Split-cour second seasons are often numbered on bangumi as a continuation (13–24) while
// fansub files say 01–12 (some say 13). Mapped files keep the number written in the file;
// when reading, a main-episode number n below the work's first official sort m is shifted to
// m+n-1 ("canonical" number), so both numberings land on the official episode.

type offsetEntry struct {
	min, count float64 // first official main sort (0 = no shift), number of main episodes
	exp        time.Time
}

var episodeOffsets sync.Map // workKey → offsetEntry

func (s *Store) resetEpisodeOffsets() {
	episodeOffsets.Range(func(k, _ any) bool { episodeOffsets.Delete(k); return true })
}

// episodeOffset returns (first official main sort, main episode count) when the work's main
// episodes start above 1 at an integer; (0, 0) when no shift applies.
func (s *Store) episodeOffset(ctx context.Context, workKey string) (float64, float64) {
	if v, ok := episodeOffsets.Load(workKey); ok {
		if e := v.(offsetEntry); time.Now().Before(e.exp) {
			return e.min, e.count
		}
	}
	var mn sql.NullFloat64
	var n float64
	_ = s.R.QueryRowContext(ctx, `SELECT MIN(sort), COUNT(*) FROM episodes WHERE work_key = ? AND type = 0`, workKey).Scan(&mn, &n)
	e := offsetEntry{exp: time.Now().Add(10 * time.Minute)}
	if mn.Valid && mn.Float64 > 1 && mn.Float64 == math.Trunc(mn.Float64) {
		e.min, e.count = mn.Float64, n
	}
	episodeOffsets.Store(workKey, e)
	return e.min, e.count
}

// CanonicalKey maps a key as written in a file to the official numbering of the work.
func (s *Store) CanonicalKey(ctx context.Context, workKey string, k EpisodeKey) EpisodeKey {
	if k.Kind != KeyMain || k.Number < 1 {
		return k
	}
	m, n := s.episodeOffset(ctx, workKey)
	if m > 1 && k.Number < m && k.Number <= n {
		k.Number += m - 1
	}
	return k
}

// fileNumbers lists the file-side numbers that map to canonical key k (k itself, plus the
// unshifted number for split-cour works).
func (s *Store) fileNumbers(ctx context.Context, workKey string, k EpisodeKey) []float64 {
	nums := []float64{k.Number}
	if k.Kind != KeyMain {
		return nums
	}
	if m, n := s.episodeOffset(ctx, workKey); m > 1 && k.Number >= m && k.Number-m+1 <= n {
		nums = append(nums, k.Number-m+1)
	}
	return nums
}
