package meta

import (
	"context"
	"math"
	"regexp"
	"strings"
)

// A part/cour ordinal is not a season. Primary titles outrank aliases that often call
// a second cour "Season 2". This only separates hints; it never proves a mapping.
var reCourPart = regexp.MustCompile(`(?i)第\s*[0-9一二三四五六七八九十壱弐参]+\s*(?:部分|部|クール)|\b(?:part|cour)\s*[-_. ]*\d+\b|\b\d+(?:st|nd|rd|th)\s+cour\b`)

func withoutCour(n string) string {
	return strings.TrimSpace(reCourPart.ReplaceAllString(normWidth(n), " "))
}
func hasCourTitle(w *Work) bool {
	return reCourPart.MatchString(normWidth(w.Title)) || reCourPart.MatchString(normWidth(w.TitleCN))
}

// officialContinuation proves a continuous integer main-chapter spine across the direct
// corresponding entries. The relation selects entries, not season/story order. No title
// similarity or Part 2 label supplies the proof.
//
// An overlapping undated chapter is only a placeholder, not a second broadcast. Undated
// fractional recap placeholders do not decide the integer spine. Dated overlaps, gaps,
// multiple prequels and unknown boundary airdates leave the continuation unproved.
func (s *Store) officialContinuation(ctx context.Context, key string) (pre string, first float64, count int, ok bool) {
	pre, ok = s.DirectPrequel(ctx, key)
	if !ok {
		return "", 0, 0, false
	}
	pe, err := s.OfficialEpisodes(ctx, pre)
	if err != nil {
		return "", 0, 0, false
	}
	ce, err := s.OfficialEpisodes(ctx, key)
	if err != nil {
		return "", 0, 0, false
	}
	p := map[float64]string{}
	lo, hi := math.Inf(1), 0.0
	for _, e := range pe {
		if e.Key.Kind != KeyMain {
			continue
		}
		n := e.Key.Number
		if n < 1 || n != math.Trunc(n) {
			continue
		}
		p[n] = e.Airdate
		if n < lo {
			lo = n
		}
		if n > hi {
			hi = n
		}
	}
	if hi < 1 || p[hi] == "" || len(p) != int(hi-lo+1) {
		return "", 0, 0, false
	}
	c := map[float64]string{}
	max := hi
	for _, e := range ce {
		if e.Key.Kind != KeyMain {
			continue
		}
		n := e.Key.Number
		if n != math.Trunc(n) {
			if e.Airdate != "" {
				return "", 0, 0, false
			}
			continue
		}
		if n <= hi {
			if _, exists := p[n]; !exists || e.Airdate != "" {
				return "", 0, 0, false
			}
			continue
		}
		c[n] = e.Airdate
		if n > max {
			max = n
		}
	}
	first = hi + 1
	if len(c) == 0 || len(c) != int(max-hi) || c[first] == "" {
		return "", 0, 0, false
	}
	// Actual broadcast boundary must follow the earlier entry, not just its sort numbers.
	if c[first] <= p[hi] {
		return "", 0, 0, false
	}
	return pre, first, len(c), true
}

// ContinuationSeasonFits checks a single-file split-entry answer. Official continuity is
// primary; the preceding entry must fit the torrent's stated season, the candidate must
// not explicitly state a different real season, and the selected episode needs an actual
// airdate and must pass the ordinary release gate. Otherwise it remains uncertain.
func (s *Store) ContinuationSeasonFits(ctx context.Context, t *ClefTask, c *ClefCandidate, stated float64, k EpisodeKey) bool {
	if stated <= 0 || k.Kind != KeyMain {
		return false
	}
	w, err := s.GetWork(ctx, c.WorkKey)
	if err != nil {
		return false
	}
	if v := workSeason(w); v > 0 && v != stated {
		return false
	}
	pre, first, _, ok := s.officialContinuation(ctx, c.WorkKey)
	if !ok || k.Number < first || s.EpisodeAirdate(ctx, c.WorkKey, k) == "" {
		return false
	}
	pc, err := s.CandidateFor(ctx, pre)
	if err != nil || pc.Season <= 0 || float64(pc.Season) != stated {
		return false
	}
	return s.ReleaseGate(ctx, t, c.WorkKey, k) == ""
}
