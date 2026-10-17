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
	reSpecial   = regexp.MustCompile(`(?i)\b(NCOP|NCED|OP|ED|PV|CM|Preview|Menu|SP|OVA|OAD|Special|Trailer|Teaser)\s?(\d{0,3})\b`)
	reTrailing  = regexp.MustCompile(`(?:^|\s)(\d{1,4})(?:v\d)?\s*$`)
)

// ParsedName is the result of release-name parsing.
type ParsedName struct {
	Episode    *float64
	Kind       string // episode|sp|op|ed|extra|other
	Confidence float64
}

func f64(s string) *float64 {
	v, err := strconv.ParseFloat(s, 64)
	if err != nil || v < 0 || v > 2000 {
		return nil
	}
	return &v
}

// ParseEpisode extracts an episode number from a fansub/raw release file name.
// It is intentionally conservative: it prefers explicit markers and ignores
// resolutions, CRCs, years and bracketed ranges (batch titles).
func ParseEpisode(name string) ParsedName {
	base := path.Base(name)
	ext := path.Ext(base)
	stem := strings.TrimSuffix(base, ext)

	if m := reSpecial.FindStringSubmatch(stem); m != nil {
		k := strings.ToUpper(m[1])
		switch {
		case strings.HasPrefix(k, "NCOP") || k == "OP":
			return ParsedName{Kind: "op", Confidence: 0.6}
		case strings.HasPrefix(k, "NCED") || k == "ED":
			return ParsedName{Kind: "ed", Confidence: 0.6}
		case k == "SP" || k == "OVA" || k == "OAD" || k == "SPECIAL":
			return ParsedName{Kind: "sp", Episode: f64(m[2]), Confidence: 0.5}
		default:
			return ParsedName{Kind: "extra", Confidence: 0.5}
		}
	}
	clean := reRes.ReplaceAllString(stem, " ")
	if m := reSxxExx.FindStringSubmatch(clean); m != nil {
		return ParsedName{Kind: "episode", Episode: f64(m[2]), Confidence: 0.9}
	}
	if m := reCNEp.FindStringSubmatch(clean); m != nil {
		return ParsedName{Kind: "episode", Episode: f64(m[1]), Confidence: 0.9}
	}
	if m := reDashEp.FindStringSubmatch(clean + " "); m != nil {
		return ParsedName{Kind: "episode", Episode: f64(m[1]), Confidence: 0.9}
	}
	// bracketed number: [02], 【02】, [02v2], [12 END]
	for _, m := range reBrackets.FindAllStringSubmatch(clean, -1) {
		inner := strings.TrimSpace(m[1])
		if reCRC.MatchString(inner) || reRange.MatchString(inner) {
			continue
		}
		if mm := reBracketEp.FindStringSubmatch(inner); mm != nil {
			v := f64(mm[1])
			if v != nil && (*v < 1900 || *v > 2100) {
				return ParsedName{Kind: "episode", Episode: v, Confidence: 0.85}
			}
		}
	}
	if m := reEPWord.FindStringSubmatch(clean); m != nil {
		return ParsedName{Kind: "episode", Episode: f64(m[1]), Confidence: 0.8}
	}
	noBr := strings.TrimSpace(reBrackets.ReplaceAllString(clean, " "))
	if m := reTrailing.FindStringSubmatch(noBr); m != nil {
		v := f64(m[1])
		if v != nil && (*v < 1900 || *v > 2100) {
			return ParsedName{Kind: "episode", Episode: v, Confidence: 0.6}
		}
	}
	return ParsedName{Kind: "other"}
}
