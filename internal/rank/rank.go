// Package rank picks the file to auto-play for an episode. The episode page (items[0]) and
// the schedule card (latest torrent) use this one scoring so the card always matches what
// the episode page plays.
package rank

import (
	"regexp"
	"sort"
)

// Candidate is one file of an episode.
type Candidate struct {
	Name        string // torrent title + file path (keywords are matched on both)
	Size        int64  // file size (only the playing file is downloaded, so the torrent size doesn't matter)
	PublishedAt string // RFC3339; newer wins ties
	Allowed     bool   // the viewer may play it (policy)
}

var (
	reHEVC = regexp.MustCompile(`(?i)hevc|x\.?265|h\.?265|10-?bit|ma10p`)
	reAVC  = regexp.MustCompile(`(?i)\bavc\b|x\.?264|h\.?264`)
	reHi   = regexp.MustCompile(`(?i)1080|1920`)
	reCHS  = regexp.MustCompile(`(?i)简|chs|\bgb\b|\bsc\b`)
)

// Score: playable for the viewer +100; file ≤ maxBytes (the "auto pick size limit"; ≤0 = no
// limit) +8; H.264/AVC +3 (decodes in every browser), HEVC/H.265/10-bit −3; 1080p +2;
// Simplified Chinese subtitles +1.
func Score(c Candidate, maxBytes int64) int {
	sc := 0
	if c.Allowed {
		sc += 100
	}
	if maxBytes <= 0 || (c.Size > 0 && c.Size <= maxBytes) {
		sc += 8
	}
	if reHEVC.MatchString(c.Name) {
		sc -= 3
	} else if reAVC.MatchString(c.Name) {
		sc += 3
	}
	if reHi.MatchString(c.Name) {
		sc += 2
	}
	if reCHS.MatchString(c.Name) {
		sc++
	}
	return sc
}

// Order returns candidate indexes best first: higher score, then newer, then input order.
func Order(cs []Candidate, maxBytes int64) []int {
	idx := make([]int, len(cs))
	scores := make([]int, len(cs))
	for i := range cs {
		idx[i], scores[i] = i, Score(cs[i], maxBytes)
	}
	sort.SliceStable(idx, func(a, b int) bool {
		i, j := idx[a], idx[b]
		if scores[i] != scores[j] {
			return scores[i] > scores[j]
		}
		return cs[i].PublishedAt > cs[j].PublishedAt
	})
	return idx
}
