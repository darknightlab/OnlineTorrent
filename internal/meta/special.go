package meta

import (
	"regexp"
	"strings"
	"unicode"
)

// IsSpecialKind accepts file kinds and canonical chapter kinds.
func IsSpecialKind(k string) bool {
	switch k {
	case "sp", "op", "ed", "ncop", "nced", "pv", "cm":
		return true
	}
	return false
}

// UnnumberedSpecial distinguishes absent numbering from ambiguous explicit numbering.
// No torrent-title inference is used.
func UnnumberedSpecial(p, kind string) bool {
	r, info := classifyOneKind(p, "", 2)
	return IsSpecialKind(kind) && (r.Kind == kind || r.Kind == "other") && r.Episode == nil && info.noNum
}

var (
	reSpBracket     = regexp.MustCompile(`^[\[\(【〔「『{〖]|[\]\)】〕」』}〗]$`)
	reSpTrailingGrp = regexp.MustCompile(`(?i)-[A-Za-z0-9_.]{2,20}$`)
	reSpYear        = regexp.MustCompile(`^(?:19|20)\d\d$`)
)

// DeriveSpecialTitle extracts a clean descriptive title for a special episode from its
// mapped file path when bangumi has no official chapter title for it, stripping the
// release group, work title, technical tags, and hashes.
func DeriveSpecialTitle(p string, workNames []string) string {
	base := stem(p)
	if strings.HasPrefix(strings.TrimSpace(base), "[") || strings.HasPrefix(strings.TrimSpace(base), "【") {
		if title := deriveBracketSpecialTitle(base, workNames); title != "" {
			return title
		}
	}
	return deriveDotSpecialTitle(base, workNames)
}

func deriveBracketSpecialTitle(base string, workNames []string) string {
	var groups []string
	var cur strings.Builder
	depth := 0
	for _, r := range normWidth(base) {
		if _, ok := bracketPairs[r]; ok {
			if depth == 0 && cur.Len() > 0 {
				groups = append(groups, strings.TrimSpace(cur.String()))
				cur.Reset()
			}
			depth++
			if depth == 1 {
				continue
			}
		} else if isCloser(r) {
			if depth == 1 {
				groups = append(groups, strings.TrimSpace(cur.String()))
				cur.Reset()
				depth = 0
				continue
			}
			if depth > 0 {
				depth--
			}
		}
		cur.WriteRune(r)
	}
	if s := strings.TrimSpace(cur.String()); s != "" {
		groups = append(groups, s)
	}
	var kept []string
	for i, g := range groups {
		if i == 0 {
			continue // release group
		}
		if g == "" || isWorkTitleGroup(g, workNames) || isTechGroup(g) {
			continue
		}
		c := strings.ReplaceAll(g, "_", " ")
		words := stripLeadingWorkWords(strings.Fields(c), workNames)
		for len(words) > 0 && isDashText(words[0]) {
			words = words[1:]
		}
		for len(words) > 0 && isDashText(words[len(words)-1]) {
			words = words[:len(words)-1]
		}
		c = strings.Join(words, " ")
		if u := strings.ToUpper(c); u == "SP" || u == "SPS" || u == "SPECIAL" || u == "SPECIALS" {
			continue
		}
		if c != "" {
			kept = append(kept, c)
		}
	}
	return strings.Join(kept, " ")
}

func deriveDotSpecialTitle(base string, workNames []string) string {
	s := normWidth(base)
	s = reSpTrailingGrp.ReplaceAllString(s, "")
	if strings.HasPrefix(strings.TrimSpace(s), "[") {
		if idx := strings.IndexByte(s, ']'); idx > 0 {
			s = s[idx+1:]
		}
	}
	s = reProtect.ReplaceAllStringFunc(s, func(m string) string {
		if reDecimalCh.MatchString(m) || reAudioCh.MatchString(strings.ToUpper(m)) {
			return " "
		}
		return m
	})
	s = strings.NewReplacer(".", " ", "_", " ").Replace(s)
	words := strings.Fields(s)
	var filtered []string
	for _, w := range words {
		wClean := reSpBracket.ReplaceAllString(w, "")
		if wClean == "" {
			continue
		}
		tok := classifyWord(wClean)
		if tok.cat == cIdent || tok.cat == cSeason || tok.cat == cVersion || reSxxExx.MatchString(strings.ToUpper(wClean)) || reSpYear.MatchString(wClean) {
			continue
		}
		filtered = append(filtered, wClean)
	}
	filtered = stripLeadingWorkWords(filtered, workNames)
	for len(filtered) > 0 && isDashText(filtered[0]) {
		filtered = filtered[1:]
	}
	for len(filtered) > 0 && isDashText(filtered[len(filtered)-1]) {
		filtered = filtered[:len(filtered)-1]
	}
	return strings.Join(filtered, " ")
}

func isWorkTitleGroup(g string, workNames []string) bool {
	ng := normName(g)
	sg := seasonless(g)
	if ng == "" {
		return true
	}
	for _, wn := range workNames {
		nw := normName(wn)
		sw := seasonless(wn)
		if nw != "" && (ng == nw || sg == sw) {
			return true
		}
	}
	return false
}

func isTechGroup(g string) bool {
	toks := tokenize("[_][" + g + "]")
	for _, t := range toks {
		if t.group == 1 {
			continue
		}
		if t.cat != cIdent && t.cat != cYear && t.cat != cVersion && !reDecimalCh.MatchString(t.text) &&
			!(reCRC.MatchString(strings.ToUpper(t.text)) && strings.ContainsAny(t.text, "0123456789") && strings.ContainsAny(strings.ToUpper(t.text), "ABCDEF")) {
			return false
		}
	}
	return len(toks) > 0
}

func stripLeadingWorkWords(words []string, workNames []string) []string {
	bestDrop := 0
	var acc strings.Builder
	for i, w := range words {
		if isDashText(w) {
			continue
		}
		for _, r := range strings.ToLower(normWidth(w)) {
			if unicode.IsLetter(r) || unicode.IsDigit(r) {
				acc.WriteRune(r)
			}
		}
		cur := acc.String()
		if cur == "" {
			continue
		}
		for _, wn := range workNames {
			nw := normName(wn)
			sw := seasonless(wn)
			if nw != "" && (cur == nw || cur == sw) {
				if i+1 > bestDrop {
					bestDrop = i + 1
				}
			}
		}
	}
	return words[bestDrop:]
}
