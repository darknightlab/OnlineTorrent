package meta

import (
	"path"
	"regexp"
	"strconv"
	"strings"
)

var videoExts = map[string]bool{".mkv": true, ".mp4": true, ".webm": true, ".mov": true, ".m4v": true, ".avi": true, ".ts": true, ".m2ts": true, ".flv": true, ".ogv": true, ".3gp": true}

// IsVideo reports whether a path looks like a video file.
func IsVideo(p string) bool { return videoExts[strings.ToLower(path.Ext(p))] }

var (
	reBrackets  = regexp.MustCompile(`[\[【(（]([^\]】)）]*)[\]】)）]`)
	reCRC       = regexp.MustCompile(`(?i)^[0-9a-f]{8}$`)
	reRes       = regexp.MustCompile(`(?i)\b(2160|1080|720|480)[pi]\b|\b\d{3,4}x\d{3,4}\b`)
	reSxxExx    = regexp.MustCompile(`(?i)\bS(\d{1,2})E(\d{1,4}(?:\.\d)?)(?:v\d)?\b`)
	reDashEp    = regexp.MustCompile(`\s[-–—]\s(\d{1,4}(?:\.\d)?)(?:v\d)?(?:\s|$|\[|\(|【)`)
	reCNEp      = regexp.MustCompile(`第\s*(\d{1,4}(?:\.\d)?)\s*[话話集]`)
	reEPWord    = regexp.MustCompile(`(?i)\b(?:EP|E|Episode|Ep\.)\s?(\d{1,4}(?:\.\d)?)(?:v\d)?\b`)
	reBracketEp = regexp.MustCompile(`^(\d{1,4}(?:\.\d)?)(?:v\d)?(?:\s?END|\s?完)?$`)
	reRange     = regexp.MustCompile(`^(\d{1,4})\s?[-~～]\s?(\d{1,4})`)
	// NCOP/NCED are unambiguous anywhere; the short tokens only count inside brackets or
	// after the last " - " (otherwise "Mob Psycho 100 Special" / "ED" in a title would match).
	reNC       = regexp.MustCompile(`(?i)\b(NCOP|NCED)\s?(\d{0,3})\b`)
	reSpecial  = regexp.MustCompile(`(?i)\b(NCOP|NCED|OP|ED|PV|CM|Preview|Menu|SP|OVA|OAD|Special|Trailer|Teaser)\s?(\d{0,3})\b`)
	reLastDash = regexp.MustCompile(`\s[-–—]\s`)
	// specials written as an uppercase token with the number attached ("Show SP02", "CM02"),
	// or a whole file name that is just a special token ("ncop.mkv", "PV 1.mkv")
	reAttached = regexp.MustCompile(`\b(SP|OVA|OAD|PV|CM)(\d{1,3})\b`)
	reWholeSP  = regexp.MustCompile(`(?i)^(NCOP|NCED|OP|ED|PV|CM|SP|OVA|OAD|Special|Trailer|Teaser)\s?(\d{0,3})$`)
	// trailing episode for plain file names ("Show 03.mp4"): exactly two digits or a
	// zero-padded number, preceded by a word, so titles like "Mob Psycho 100" don't match.
	reTrailing = regexp.MustCompile(`\S\s+(\d{2}|0\d{2,3})(?:v\d)?\s*$`)
)

// ParsedName is the result of release-name parsing.
type ParsedName struct {
	Episode    *float64
	Kind       string // episode|sp|op|ed|pv|cm|extra|other
	Confidence float64
}

func f64(s string) *float64 {
	v, err := strconv.ParseFloat(s, 64)
	if err != nil || v < 0 || v > 2000 {
		return nil
	}
	return &v
}

func isYear(v float64) bool { return v >= 1900 && v <= 2100 }

// ParseEpisode extracts an episode number from a fansub/raw release file name.
// It is intentionally conservative: it prefers explicit markers and ignores
// resolutions, CRCs, years and bracketed ranges (batch titles).
func ParseEpisode(name string) ParsedName { return parse(name, true) }

// ParseTitle parses a torrent title (not a file name): like ParseEpisode but without the
// bare trailing-number fallback, which is too ambiguous for titles.
func ParseTitle(title string) ParsedName { return parse(title+".mkv", false) }

func parse(name string, allowTrailing bool) ParsedName {
	base := path.Base(name)
	ext := path.Ext(base)
	stem := strings.TrimSuffix(base, ext)
	clean := reRes.ReplaceAllString(stem, " ")

	// 1. strong episode markers
	if m := reSxxExx.FindStringSubmatch(clean); m != nil {
		return episode(f64(m[2]), 0.9)
	}
	if m := reCNEp.FindStringSubmatch(clean); m != nil {
		return episode(f64(m[1]), 0.9)
	}
	if m := reDashEp.FindStringSubmatch(clean + " "); m != nil {
		v := f64(m[1])
		if v != nil && isYear(*v) {
			return ParsedName{Kind: "other"} // "Show - 2024 [1080p]" is a year, not an episode
		}
		return episode(v, 0.9)
	}
	// bracketed number: [02], 【02】, [02v2], [12 END]
	brackets := reBrackets.FindAllStringSubmatch(clean, -1)
	for _, m := range brackets {
		inner := strings.TrimSpace(m[1])
		if reCRC.MatchString(inner) || reRange.MatchString(inner) {
			continue
		}
		if mm := reBracketEp.FindStringSubmatch(inner); mm != nil {
			if v := f64(mm[1]); v != nil && !isYear(*v) {
				return episode(v, 0.85)
			}
		}
	}

	// 2. specials (only without a strong episode marker)
	if p, ok := special(reNC.FindStringSubmatch(clean)); ok {
		return p
	}
	for _, m := range brackets {
		if p, ok := special(reSpecial.FindStringSubmatch(m[1])); ok {
			return p
		}
	}
	if locs := reLastDash.FindAllStringIndex(clean, -1); len(locs) > 0 {
		tail := clean[locs[len(locs)-1][1]:]
		if p, ok := special(reSpecial.FindStringSubmatch(tail)); ok {
			return p
		}
	}
	noBr := strings.TrimSpace(reBrackets.ReplaceAllString(clean, " "))
	if p, ok := special(reWholeSP.FindStringSubmatch(noBr)); ok {
		return p
	}
	if p, ok := special(reAttached.FindStringSubmatch(noBr)); ok {
		return p
	}

	// 3. weaker markers
	if m := reEPWord.FindStringSubmatch(clean); m != nil {
		return episode(f64(m[1]), 0.8)
	}
	if allowTrailing {
		if m := reTrailing.FindStringSubmatch(noBr); m != nil {
			return episode(f64(m[1]), 0.6)
		}
	}
	return ParsedName{Kind: "other"}
}

func episode(v *float64, conf float64) ParsedName {
	if v == nil {
		return ParsedName{Kind: "other"}
	}
	return ParsedName{Kind: "episode", Episode: v, Confidence: conf}
}

func special(m []string) (ParsedName, bool) {
	if m == nil {
		return ParsedName{}, false
	}
	k := strings.ToUpper(m[1])
	// specials keep their number; unnumbered ones are number 1
	num := f64(m[2])
	if num == nil {
		one := 1.0
		num = &one
	}
	switch {
	case strings.HasPrefix(k, "NCOP") || k == "OP":
		return ParsedName{Kind: "op", Episode: num, Confidence: 0.6}, true
	case strings.HasPrefix(k, "NCED") || k == "ED":
		return ParsedName{Kind: "ed", Episode: num, Confidence: 0.6}, true
	case k == "SP" || k == "OVA" || k == "OAD" || k == "SPECIAL":
		return ParsedName{Kind: "sp", Episode: num, Confidence: 0.5}, true
	case k == "PV" || k == "TRAILER" || k == "TEASER":
		return ParsedName{Kind: "pv", Episode: num, Confidence: 0.5}, true
	case k == "CM":
		return ParsedName{Kind: "cm", Episode: num, Confidence: 0.5}, true
	}
	return ParsedName{Kind: "extra", Confidence: 0.5}, true // Menu, Preview (予告)
}

var (
	// directories that hold specials/extras in batch releases ("SPs", "CDs", "Scans", "特典映像", …)
	reSPDir    = regexp.MustCompile(`(?i)^(SPs?|Specials?|OVAs?|OADs?)$`)
	// directories of one special kind (the file name may only carry a number: "PVs/Show 01.mkv")
	rePVDir   = regexp.MustCompile(`(?i)^(PVs?|Trailers?|Teasers?)$`)
	reCMDir   = regexp.MustCompile(`(?i)^CMs?$`)
	reNCOPDir = regexp.MustCompile(`(?i)^NCOPs?$`)
	reNCEDDir = regexp.MustCompile(`(?i)^NCEDs?$`)
	reExtraDir = regexp.MustCompile(`(?i)^(Extras?|Bonus|Menus?|CMs?|PVs?|Previews?|Trailers?|Samples?|Scans|NC|NCOP|NCED|NCOPED|NC[\s._-]?OP[\s._-]?ED|Others?|Making|Interview)s?$|特典|映像特典|予告|番宣|无字|NCOP|NCED`)
	// anywhere in the file name: samples and bonus material
	reExtraName = regexp.MustCompile(`(?i)\bsample\b|特典|映像特典|予告|番宣|\bmaking\b`)
)

// ClassifyFile decides the kind (episode|sp|op|ed|pv|cm|extra|other) and episode number of
// one file of a torrent. Non-video files are "other". Specials (sp, NCOP = op, NCED = ed,
// pv, cm) keep their number (unnumbered = 1) and are mapped like episodes (episode key
// "sp1", "ncop1", …); Menu, Sample, Preview and 特典 material are "extra" (never mapped).
// title is the torrent title, used when the torrent has a single video whose own name
// carries no number.
func ClassifyFile(p, title string, videos int) ParsedName {
	if !IsVideo(p) {
		return ParsedName{Kind: "other"}
	}
	clean := strings.ReplaceAll(p, "\\", "/")
	dirs := strings.Split(clean, "/")
	name := dirs[len(dirs)-1]
	dirs = dirs[:len(dirs)-1]
	r := ParseEpisode(name)
	switch r.Kind {
	case "op", "ed", "pv", "cm", "extra":
		return r
	}
	if reExtraName.MatchString(name) {
		return ParsedName{Kind: "extra", Confidence: 0.5}
	}
	for _, d := range dirs {
		d = strings.TrimSpace(d)
		dirKind := ""
		switch {
		case reSPDir.MatchString(d):
			dirKind = "sp"
		case rePVDir.MatchString(d):
			dirKind = "pv"
		case reCMDir.MatchString(d):
			dirKind = "cm"
		case reNCOPDir.MatchString(d):
			dirKind = "op"
		case reNCEDDir.MatchString(d):
			dirKind = "ed"
		}
		if dirKind != "" {
			if (r.Kind == "episode" || r.Kind == "sp") && r.Episode != nil {
				return ParsedName{Kind: dirKind, Episode: r.Episode, Confidence: 0.5}
			}
			one := 1.0
			return ParsedName{Kind: dirKind, Episode: &one, Confidence: 0.4}
		}
		if reExtraDir.MatchString(d) {
			return ParsedName{Kind: "extra", Confidence: 0.5}
		}
	}
	if r.Episode == nil && r.Kind == "other" && videos == 1 && title != "" {
		t := ParseTitle(title)
		if t.Kind == "episode" || t.Kind == "sp" {
			r = t
		}
	}
	return r
}
