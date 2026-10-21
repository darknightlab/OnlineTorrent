package meta

import (
	"context"
	"regexp"
	"strings"
)

// OfficialSingleton returns the actual sort of exactly one official main chapter.
// Planned counts (works.eps) and the number of videos in a torrent cannot prove this.
func (s *Store) OfficialSingleton(ctx context.Context, key string) *float64 {
	eps, err := s.OfficialEpisodes(ctx, key)
	if err != nil {
		return nil
	}
	var n *float64
	for _, e := range eps {
		if e.Key.Kind != KeyMain {
			continue
		}
		if n != nil {
			return nil
		}
		v := e.Key.Number
		n = &v
	}
	return n
}

// UnnumberedMainVideo is deliberately narrower than "the parser returned nil": explicit
// conflicting/ambiguous numbers and parts, specials, extras and interview videos are not
// implicit main chapters. Directory context matters, but a collection's numbered season
// folders do not make an otherwise unnumbered movie/OVA numbered.
func UnnumberedMainVideo(p string) bool {
	if !IsVideo(p) {
		return false
	}
	clean := strings.ReplaceAll(p, "\\", "/")
	a := parseName(stem(clean), true)
	if !a.noNum || a.Episode != nil || (a.Kind != "other" && a.Kind != "episode") {
		return false
	}
	for i, tk := range a.toks {
		if tk.cat == cEpisode || tk.cat == cRange || tierOf(a.toks, i, true) != tierNone {
			return false // also catch ambiguous explicit markers that yielded Episode=nil
		}
	}
	for _, part := range strings.Split(clean, "/") {
		if singletonNonMain.MatchString(part) {
			return false
		}
	}
	if singletonPart.MatchString(stem(clean)) || singletonExplicit.MatchString(stem(clean)) {
		return false
	}
	r := ClassifyFile(clean, "", 2) // do not infer from torrent title or video count
	return r.Episode == nil && (r.Kind == "other" || r.Kind == "episode")
}

var singletonExplicit = regexp.MustCompile(`(?i)(?:\b|\d)(?:e|ep|episode)[\s._-]*\d`)
var singletonNonMain = regexp.MustCompile(`(?i)(?:^|[^a-z])(?:iv|interviews?|making|extras?|bonus|sample|menu|preview)(?:[^a-z]|$)|インタビュー|映像特典|花絮|访谈|訪談|特典`)
var singletonPart = regexp.MustCompile(`(?i)(?:^|[^a-z])(?:part|pt|disc|disk|cd|vol(?:ume)?)[\s._-]*(?:\d+|[ivx]+)(?:[^a-z]|$)|(?:前|後|后|上|下)[篇編编]`)
