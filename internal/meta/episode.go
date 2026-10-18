package meta

import (
	"regexp"
	"strconv"
	"strings"
)

// Episode keys address one episode of a subject in URLs and APIs:
//
//	main episodes: the number as written      "12", "12.5", "0"
//	specials:      lowercase kind + number     "sp1", "ncop1", "nced2", "pv1", "cm1"
//
// Unnumbered specials get number 1 ("ncop" is accepted as an alias of "ncop1"). Parsing is
// case-insensitive and ignores leading zeros ("SP01" → "sp1", "01" → "1"); String() is the
// canonical form. The web client has the same grammar in web/src/lib/episode.ts.

// Episode key kinds (KeyMain is a regular episode).
const (
	KeyMain = "main"
	KeySP   = "sp"
	KeyNCOP = "ncop"
	KeyNCED = "nced"
	KeyPV   = "pv"
	KeyCM   = "cm"
)

// KeyKinds is the display order of the episode list: main episodes, then specials.
var KeyKinds = []string{KeyMain, KeySP, KeyNCOP, KeyNCED, KeyPV, KeyCM}

// key kind ↔ torrent_files.kind
var keyToFileKind = map[string]string{KeyMain: "episode", KeySP: "sp", KeyNCOP: "op", KeyNCED: "ed", KeyPV: "pv", KeyCM: "cm"}
var fileToKeyKind = map[string]string{"episode": KeyMain, "sp": KeySP, "op": KeyNCOP, "ed": KeyNCED, "pv": KeyPV, "cm": KeyCM}

// MaxMainEpisode / MaxSpecialNumber bound the numbers accepted in keys.
const (
	MaxMainEpisode   = 5000
	MaxSpecialNumber = 999
)

// EpisodeKey is a parsed episode key.
type EpisodeKey struct {
	Kind   string  // main|sp|ncop|nced|pv|cm
	Number float64 // ≥ 0; may be fractional (12.5)
}

var reEpisodeKey = regexp.MustCompile(`^(?i)(sp|ncop|nced|pv|cm)?(\d{1,4}(?:\.\d{1,3})?)?$`)

// ParseEpisodeKey parses an episode key (see the package comment above).
func ParseEpisodeKey(s string) (EpisodeKey, bool) {
	m := reEpisodeKey.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil || (m[1] == "" && m[2] == "") {
		return EpisodeKey{}, false
	}
	k := EpisodeKey{Kind: KeyMain, Number: 1}
	if m[1] != "" {
		k.Kind = strings.ToLower(m[1])
	}
	if m[2] != "" {
		v, err := strconv.ParseFloat(m[2], 64)
		if err != nil {
			return EpisodeKey{}, false
		}
		k.Number = v
	}
	limit := float64(MaxSpecialNumber)
	if k.Kind == KeyMain {
		limit = MaxMainEpisode
	}
	if k.Number < 0 || k.Number > limit {
		return EpisodeKey{}, false
	}
	return k, true
}

func fmtNum(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) }

// String is the canonical key ("12.5", "ncop1").
func (k EpisodeKey) String() string {
	if k.Kind == KeyMain {
		return fmtNum(k.Number)
	}
	return k.Kind + fmtNum(k.Number)
}

// Short is the chip text: "12.5", "SP1", "NCOP1".
func (k EpisodeKey) Short() string {
	if k.Kind == KeyMain {
		return fmtNum(k.Number)
	}
	return strings.ToUpper(k.Kind) + fmtNum(k.Number)
}

// Label is the display name: "第 12 话", "SP1", "NCOP1".
func (k EpisodeKey) Label() string {
	if k.Kind == KeyMain {
		return "第 " + fmtNum(k.Number) + " 话"
	}
	return k.Short()
}

// FileKind is the torrent_files.kind holding this key's files (episode|sp|op|ed|pv|cm).
func (k EpisodeKey) FileKind() string { return keyToFileKind[k.Kind] }

// KeyOfFile returns the episode key of a mapped file (kind from torrent_files + episode);
// ok is false for kinds that are never mapped to an episode (extra, other) or no number.
func KeyOfFile(fileKind string, episode *float64) (EpisodeKey, bool) {
	kk, ok := fileToKeyKind[fileKind]
	if !ok || episode == nil || *episode < 0 {
		return EpisodeKey{}, false
	}
	return EpisodeKey{Kind: kk, Number: *episode}, true
}

// MappableKind reports whether files of this torrent_files.kind are mapped to a work.
func MappableKind(fileKind string) bool { _, ok := fileToKeyKind[fileKind]; return ok }

// kindOrder is the sort rank of a key kind.
func kindOrder(kind string) int {
	for i, k := range KeyKinds {
		if k == kind {
			return i
		}
	}
	return len(KeyKinds)
}

// LessKey orders keys: main episodes by number, then sp, ncop, nced, pv, cm by number.
func LessKey(a, b EpisodeKey) bool {
	if a.Kind != b.Kind {
		return kindOrder(a.Kind) < kindOrder(b.Kind)
	}
	return a.Number < b.Number
}
