package meta

// Candidate retrieval for the decision model: an in-memory index of every work's names
// (title, Chinese title, all aliases, and an offline romaji transliteration of kana titles)
// in one normalised form — lower case, no punctuation / spaces, macrons and long vowels
// folded (ou/oo → o, uu → u), "wo" → "o", season / part markers removed — scored against
// the torrent title and file names by exact fragment, containment and token / bigram
// coverage, with a boost for works airing around the publish date.

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"
)

type idxName struct {
	norm    string   // matching form
	latin   int      // ASCII letters/digits in norm
	bigrams []string // CJK bigrams of norm
}

type idxWork struct {
	key        string
	begin, end time.Time
	names      []idxName
}

type candIndex struct {
	works []idxWork
	built time.Time
}

const candIndexTTL = 30 * time.Minute

var foldReplacer = strings.NewReplacer("ā", "a", "ī", "i", "ū", "u", "ē", "e", "ō", "o", "â", "a", "î", "i", "û", "u", "ê", "e", "ô", "o")

// matchNorm is the matching form of a name or query text.
func matchNorm(s string) string {
	s = strings.ToLower(normWidth(s))
	s = foldReplacer.Replace(s)
	s = normName(reSeasonWords.ReplaceAllString(s, " "))
	return foldVowels(s)
}

var vowelFolder = strings.NewReplacer("ou", "o", "oo", "o", "uu", "u", "wo", "o", "ei", "e")

func foldVowels(s string) string { return vowelFolder.Replace(s) }

func cjkBigrams(s string) []string {
	var out []string
	var run []rune
	flush := func() {
		for i := 0; i+1 < len(run); i++ {
			out = append(out, string(run[i:i+2]))
		}
		run = run[:0]
	}
	for _, r := range s {
		if r >= utf8.RuneSelf && unicode.IsLetter(r) {
			run = append(run, r)
		} else {
			flush()
		}
	}
	flush()
	return out
}

func newIdxName(raw string) (idxName, bool) {
	n := matchNorm(raw)
	if n == "" {
		return idxName{}, false
	}
	latin := 0
	for i := 0; i < len(n); i++ {
		if n[i] < utf8.RuneSelf {
			latin++
		}
	}
	in := idxName{norm: n, latin: latin, bigrams: cjkBigrams(n)}
	if latin < 4 && len(in.bigrams) == 0 {
		return idxName{}, false // too short to say anything
	}
	return in, true
}

func parseDay(s string) time.Time {
	if len(s) < 10 {
		return time.Time{}
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t
	}
	t, _ := time.Parse("2006-01-02", s[:10])
	return t
}

// candidateIndex returns (cached for 30 min) the name index of all works.
func (s *Store) candidateIndex(ctx context.Context) (*candIndex, error) {
	s.candMu.Lock()
	defer s.candMu.Unlock()
	if s.cand != nil && time.Since(s.cand.built) < candIndexTTL {
		return s.cand, nil
	}
	rows, err := s.R.QueryContext(ctx, `SELECT key, title, title_cn, aliases, begin_at, end_at FROM works`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	idx := &candIndex{built: time.Now()}
	for rows.Next() {
		var key, title, cn, aliases, begin, end string
		if err := rows.Scan(&key, &title, &cn, &aliases, &begin, &end); err != nil {
			return nil, err
		}
		var al []string
		_ = json.Unmarshal([]byte(aliases), &al)
		w := idxWork{key: key, begin: parseDay(begin), end: parseDay(end)}
		seen := map[string]bool{}
		add := func(raw string) {
			if n, ok := newIdxName(raw); ok && !seen[n.norm] {
				seen[n.norm] = true
				w.names = append(w.names, n)
			}
		}
		for _, n := range append([]string{title, cn}, al...) {
			add(n)
			if ro, ok := Romanize(n); ok {
				add(ro)
			}
		}
		if len(w.names) > 0 {
			idx.works = append(idx.works, w)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	s.cand = idx
	return idx, nil
}

func (s *Store) resetCandidateIndex() {
	s.candMu.Lock()
	s.cand = nil
	s.candMu.Unlock()
}

// query words that never name a work (release tags)
var queryStop = setOf("season", "part", "cour", "anime", "movie", "film", "series", "special", "specials", "the", "and", "with", "from", "for",
	"webrip", "webdl", "bdrip", "bluray", "remux", "hevc", "avc", "aac", "flac", "opus", "x264", "x265", "h264", "h265", "10bit", "8bit",
	"1080p", "720p", "2160p", "480p", "baha", "abema", "multi", "subfrench", "vostfr", "dual", "audio", "uncensored", "chs", "cht", "jpn",
	"mkv", "mp4", "raws", "raw", "sub", "subs", "subbed", "dub", "dubbed", "batch", "complete", "netflix", "amzn", "crunchyroll", "global",
	"ddp20", "ddp51", "eac3", "veryslow", "srtx2", "assx2")

type retrieveQuery struct {
	full   string   // matchNorm of the texts, "|"-joined
	frags  []string // matchNorm of title fragments
	tokens []string // latin words (≥ 4 letters, folded)
	// fragWords: the latin words of each title fragment (a fragment is often exactly the
	// work's name: "Kyouran Reijou Nia Liston")
	fragWords [][]string
	pub       time.Time
}

func latinWords(t string) []string {
	low := foldReplacer.Replace(strings.ToLower(normWidth(reSeasonWords.ReplaceAllString(normWidth(t), " "))))
	var out []string
	for _, w := range strings.FieldsFunc(low, func(r rune) bool { return r >= utf8.RuneSelf || !(unicode.IsLetter(r) || unicode.IsDigit(r)) }) {
		w = foldVowels(w)
		if len(w) < 4 || queryStop[w] || strings.ContainsAny(w, "0123456789") {
			continue
		}
		out = append(out, w)
	}
	return out
}

func newQuery(texts []string, published string) retrieveQuery {
	q := retrieveQuery{pub: parseDay(published)}
	var full strings.Builder
	seenTok := map[string]bool{}
	seenFrag := map[string]bool{}
	for _, t := range texts {
		if t == "" {
			continue
		}
		full.WriteString(matchNorm(t))
		full.WriteByte('|')
		for _, f := range titleFragments(stem(t)) {
			if n := matchNorm(f); n != "" && !seenFrag[n] {
				seenFrag[n] = true
				q.frags = append(q.frags, n)
				if ws := latinWords(f); len(ws) > 0 {
					q.fragWords = append(q.fragWords, ws)
				}
			}
		}
		for _, w := range latinWords(t) {
			if !seenTok[w] {
				seenTok[w] = true
				q.tokens = append(q.tokens, w)
			}
		}
	}
	q.full = full.String()
	return q
}

// score rates a work for the query (0 = no match).
func (q *retrieveQuery) score(w *idxWork) float64 {
	best, bestCov := 0.0, 0.0
	for i := range w.names {
		n := &w.names[i]
		for _, f := range q.frags {
			if f == n.norm {
				best = max(best, 100)
			}
		}
		runes := utf8.RuneCountInString(n.norm)
		if (runes >= 4 && n.latin < runes) || n.latin >= 6 {
			if strings.Contains(q.full, n.norm) {
				best = max(best, 60+float64(min(runes, 20)))
			}
		}
		// a title fragment inside the name ("Kyouran Reijou Nia Liston" of a long alias)
		for _, f := range q.frags {
			fr := utf8.RuneCountInString(f)
			if (fr >= 8 || (fr >= 3 && len(f) > fr)) && strings.Contains(n.norm, f) {
				best = max(best, 40+float64(min(fr, 20)))
			}
		}
		// a fragment's words all (or mostly) in the name ("CONAN" → detectiveconan)
		for _, ws := range q.fragWords {
			got, all := 0, 0
			for _, w := range ws {
				all += len(w)
				if strings.Contains(n.norm, w) {
					got += len(w)
				}
			}
			if all >= 5 {
				bestCov = max(bestCov, 0.9*float64(got)/float64(all))
			}
		}
		// coverage of the name by the query: latin words found in it, CJK bigrams found in
		// the query
		covered, total := 0, 0
		if n.latin > 0 {
			total += n.latin
			for _, t := range q.tokens {
				if strings.Contains(n.norm, t) {
					covered += len(t)
				}
			}
		}
		if len(n.bigrams) > 0 {
			total += len(n.bigrams)
			for _, bg := range n.bigrams {
				if strings.Contains(q.full, bg) {
					covered++
				}
			}
		}
		if total > 0 {
			bestCov = max(bestCov, min(1, float64(covered)/float64(total)))
		}
	}
	if bestCov < 0.5 && best == 0 {
		return 0
	}
	sc := best
	if bestCov >= 0.5 {
		sc += 50 * bestCov
	}
	// airing around the publish date
	if !q.pub.IsZero() && !w.begin.IsZero() && !w.begin.After(q.pub.AddDate(0, 0, 31)) {
		if (!w.end.IsZero() && !q.pub.After(w.end.AddDate(1, 0, 0))) || (w.end.IsZero() && !q.pub.After(w.begin.AddDate(2, 0, 0))) {
			sc += 10
		}
	}
	return sc
}

// retrieve returns up to limit work keys for the texts (title, file paths), best first.
func (s *Store) retrieve(ctx context.Context, texts []string, published string, limit int) []string {
	idx, err := s.candidateIndex(ctx)
	if err != nil {
		return nil
	}
	q := newQuery(texts, published)
	if q.full == "" {
		return nil
	}
	type hit struct {
		key   string
		score float64
		begin time.Time
	}
	// scan in parallel halves (the index is read-only)
	var mu sync.Mutex
	var hits []hit
	var wg sync.WaitGroup
	const parts = 2
	for p := 0; p < parts; p++ {
		wg.Add(1)
		go func(p int) {
			defer wg.Done()
			var local []hit
			for i := p; i < len(idx.works); i += parts {
				w := &idx.works[i]
				if sc := q.score(w); sc >= 30 {
					local = append(local, hit{w.key, sc, w.begin})
				}
			}
			mu.Lock()
			hits = append(hits, local...)
			mu.Unlock()
		}(p)
	}
	wg.Wait()
	sort.Slice(hits, func(i, j int) bool {
		if hits[i].score != hits[j].score {
			return hits[i].score > hits[j].score
		}
		if !hits[i].begin.Equal(hits[j].begin) {
			return hits[i].begin.After(hits[j].begin)
		}
		return hits[i].key < hits[j].key
	})
	out := make([]string, 0, limit)
	for _, h := range hits[:min(limit, len(hits))] {
		out = append(out, h.key)
	}
	return out
}
