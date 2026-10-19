// Package rank defines the admin-editable recommendation rules (setting "rank.rules",
// public via GET /api/v1/site as rank_rules). The scoring itself runs in the browser
// (web/src/lib/rank.ts): it depends on the viewer's permissions and live swarm stats, and
// the episode page sorts its resource list and auto-plays the first entry with it. The
// backend only validates and stores the rules.
package rank

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"reflect"
	"regexp"
	"strings"
	"unicode/utf8"
)

// Availability scores (swarm stats).
type Availability struct {
	ServerComplete float64 `json:"server_complete"` // a qB server has the whole torrent
	WTSeeder       float64 `json:"wt_seeder"`       // WebTorrent seeders: coefficient …
	WTCap          float64 `json:"wt_cap"`          // … and cap
	BTSeeder       float64 `json:"bt_seeder"`       // BT seeders (only viewers who can make the server seed): coefficient …
	BTCap          float64 `json:"bt_cap"`          // … and cap
	// Curve: "linear" = coefficient per seeder, "log" = coefficient × log2(1 + seeders)
	// (1 → 1×, 3 → 2×, 7 → 3×). Missing = linear (rules saved before the field existed).
	Curve string  `json:"curve"`
	Dead  float64 `json:"dead"` // no reachable source for this viewer
}

// Seeder curves.
const (
	CurveLinear = "linear"
	CurveLog    = "log"
)

// Keyword adds Score when Pattern (case-insensitive regex) matches title + file path.
// Keywords sharing a non-empty Group only count once: the highest matching score.
type Keyword struct {
	Label   string  `json:"label"`
	Pattern string  `json:"pattern"`
	Score   float64 `json:"score"`
	Group   string  `json:"group"`
}

// Team adds Score for an exact team name.
type Team struct {
	Name  string  `json:"name"`
	Score float64 `json:"score"`
}

// SizeBonus favours small files (P2P and server traffic): Max for files ≤ FullBelowMB,
// 0 from ZeroAboveMB, linear in between; unknown sizes get nothing. Max 0 = off.
type SizeBonus struct {
	Max         float64 `json:"max"`
	FullBelowMB int     `json:"full_below_mb"`
	ZeroAboveMB int     `json:"zero_above_mb"`
}

// Rules is the whole rule set.
type Rules struct {
	Availability Availability `json:"availability"`
	Keywords     []Keyword    `json:"keywords"`
	Teams        []Team       `json:"teams"`
	Size         SizeBonus    `json:"size"`
	Oversize     float64      `json:"oversize"` // file larger than play.max_auto_size_mb
}

// Limits.
const (
	MaxKeywords   = 30
	MaxTeams      = 200
	MaxLabelLen   = 40
	MaxPatternLen = 200
	MaxGroupLen   = 20
	MaxTeamLen    = 80
	MaxScore      = 1000
	MaxSizeMB     = 1 << 20
)

// Default returns the built-in rules (rationale and the scenarios they were tuned on:
// docs/research/rank-rules.md, web/src/lib/rank.scenarios.test.ts). Keep in sync with
// DEFAULT_RANK_RULES in web/src/lib/rank.ts.
func Default() Rules {
	return Rules{
		Availability: Availability{ServerComplete: 2, WTSeeder: 2, WTCap: 6, BTSeeder: 1, BTCap: 4, Curve: CurveLog, Dead: -100},
		Keywords: []Keyword{
			{Label: "简体中文", Pattern: `简|chs|\bsc\b|\bgb\b|zh-?hans`, Score: 25, Group: "字幕"},
			{Label: "繁体中文", Pattern: `繁|cht|\btc\b|big5|zh-?hant`, Score: 12, Group: "字幕"},
			{Label: "1080p", Pattern: `1080[pi]?\b|1920x1080`, Score: 10, Group: "分辨率"},
			{Label: "4K", Pattern: `2160p?\b|3840x2160|\b4k\b`, Score: 5, Group: "分辨率"},
			{Label: "720p", Pattern: `720p\b|1280x720`, Score: 4, Group: "分辨率"},
			{Label: "HEVC", Pattern: `hevc|[hx]\.?265`, Score: 2},
		},
		Teams:    []Team{},
		Size:     SizeBonus{Max: 6, FullBelowMB: 300, ZeroAboveMB: 1500},
		Oversize: -10,
	}
}

// legacyDefaults are earlier built-in defaults as stored in the settings table. A stored
// value equal to one of them was never edited by an admin and is upgraded to Default().
var legacyDefaults = []string{
	// v1 (2026-10, 7d2effb): server_complete +30 dominated, no size bonus
	`{"availability":{"server_complete":30,"wt_seeder":5,"wt_cap":30,"bt_seeder":1,"bt_cap":20,"dead":-100},"keywords":[{"label":"HEVC","pattern":"hevc|[hx]\\.?265","score":3},{"label":"1080p","pattern":"1080|1920","score":3},{"label":"4K","pattern":"2160|\\b4k\\b","score":1},{"label":"简体","pattern":"简|chs|\\bsc\\b|\\bgb\\b","score":2},{"label":"繁体","pattern":"繁|cht|\\btc\\b|big5","score":1}],"teams":[],"oversize":-10}`,
}

// IsLegacyDefault reports whether raw equals (as JSON) an earlier built-in default.
func IsLegacyDefault(raw []byte) bool {
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return false
	}
	for _, l := range legacyDefaults {
		var d any
		if json.Unmarshal([]byte(l), &d) == nil && reflect.DeepEqual(v, d) {
			return true
		}
	}
	return false
}

// DefaultJSON is Default encoded (the setting's default value).
func DefaultJSON() json.RawMessage {
	b, _ := json.Marshal(Default())
	return b
}

// Parse decodes and validates rules (unknown fields are an error) and returns them
// normalised (trimmed strings, non-nil lists, curve set).
func Parse(raw []byte) (Rules, error) {
	var r Rules
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&r); err != nil {
		return Rules{}, fmt.Errorf("规则格式错误：%v", err)
	}
	if dec.More() {
		return Rules{}, errors.New("规则格式错误：多余的内容")
	}
	return r, r.normalize()
}

func score(name string, v float64) error {
	if math.IsNaN(v) || v < -MaxScore || v > MaxScore {
		return fmt.Errorf("%s 须在 %d 到 %d 之间", name, -MaxScore, MaxScore)
	}
	return nil
}

func (r *Rules) normalize() error {
	a := &r.Availability
	for _, c := range []struct {
		n string
		v float64
	}{{"服务器已缓存加分", a.ServerComplete}, {"WebTorrent 做种系数", a.WTSeeder}, {"BT 做种系数", a.BTSeeder}, {"无人做种扣分", a.Dead},
		{"超过大小上限扣分", r.Oversize}, {"小文件加分", r.Size.Max}} {
		if err := score(c.n, c.v); err != nil {
			return err
		}
	}
	if a.WTCap < 0 || a.WTCap > MaxScore || a.BTCap < 0 || a.BTCap > MaxScore {
		return fmt.Errorf("加分上限须在 0 到 %d 之间", MaxScore)
	}
	switch a.Curve {
	case "":
		a.Curve = CurveLinear
	case CurveLinear, CurveLog:
	default:
		return errors.New("做种人数曲线须为 linear 或 log")
	}
	if r.Size.FullBelowMB < 0 || r.Size.ZeroAboveMB < 0 || r.Size.FullBelowMB > MaxSizeMB || r.Size.ZeroAboveMB > MaxSizeMB {
		return fmt.Errorf("小文件加分的大小须在 0 到 %d MB 之间", MaxSizeMB)
	}
	if r.Size.Max != 0 && r.Size.ZeroAboveMB <= r.Size.FullBelowMB {
		return errors.New("小文件加分：「不加分的大小」须大于「满分的大小」")
	}
	if len(r.Keywords) > MaxKeywords {
		return fmt.Errorf("关键词最多 %d 个", MaxKeywords)
	}
	if r.Keywords == nil {
		r.Keywords = []Keyword{}
	}
	for i := range r.Keywords {
		k := &r.Keywords[i]
		k.Label, k.Pattern = strings.TrimSpace(k.Label), strings.TrimSpace(k.Pattern)
		n := fmt.Sprintf("第 %d 个关键词", i+1)
		if k.Label == "" || utf8.RuneCountInString(k.Label) > MaxLabelLen {
			return fmt.Errorf("%s：名称不能为空且不超过 %d 字", n, MaxLabelLen)
		}
		if k.Pattern == "" || utf8.RuneCountInString(k.Pattern) > MaxPatternLen {
			return fmt.Errorf("%s（%s）：正则不能为空且不超过 %d 字", n, k.Label, MaxPatternLen)
		}
		if err := CheckPattern(k.Pattern); err != nil {
			return fmt.Errorf("%s（%s）：%v", n, k.Label, err)
		}
		if err := score(n+"分值", k.Score); err != nil {
			return err
		}
		k.Group = strings.TrimSpace(k.Group)
		if utf8.RuneCountInString(k.Group) > MaxGroupLen {
			return fmt.Errorf("%s（%s）：分组名不超过 %d 字", n, k.Label, MaxGroupLen)
		}
	}
	if len(r.Teams) > MaxTeams {
		return fmt.Errorf("字幕组最多 %d 个", MaxTeams)
	}
	if r.Teams == nil {
		r.Teams = []Team{}
	}
	seen := map[string]bool{}
	for i := range r.Teams {
		t := &r.Teams[i]
		t.Name = strings.TrimSpace(t.Name)
		if t.Name == "" || utf8.RuneCountInString(t.Name) > MaxTeamLen {
			return fmt.Errorf("第 %d 个字幕组：名称不能为空且不超过 %d 字", i+1, MaxTeamLen)
		}
		if seen[t.Name] {
			return fmt.Errorf("字幕组重复：%s", t.Name)
		}
		seen[t.Name] = true
		if err := score("字幕组「"+t.Name+"」分值", t.Score); err != nil {
			return err
		}
	}
	return nil
}

// CheckPattern accepts the regex syntax shared by Go RE2 and JavaScript RegExp (flag "i",
// no "u"): the pattern must compile in Go (so no lookaround / backreferences, which RE2
// rejects) and must not use RE2-only syntax — inline flags and named groups "(?…)" other
// than "(?:", \A \z \Q \E \C, \p{…} / \P{…}, \x{…} and POSIX classes [[:alpha:]].
func CheckPattern(p string) error {
	if _, err := regexp.Compile("(?i)" + p); err != nil {
		return fmt.Errorf("正则无效：%v", err)
	}
	for i := 0; i < len(p); i++ {
		switch p[i] {
		case '\\':
			if i+1 >= len(p) {
				return errors.New("正则以反斜杠结尾")
			}
			c := p[i+1]
			switch c {
			case 'A', 'z', 'Q', 'E', 'C', 'p', 'P':
				return fmt.Errorf("不支持 \\%c（浏览器不兼容）", c)
			case 'x':
				if i+2 < len(p) && p[i+2] == '{' {
					return errors.New("不支持 \\x{…}，请用 \\xHH")
				}
			}
			i++
		case '(':
			if i+1 < len(p) && p[i+1] == '?' && !(i+2 < len(p) && p[i+2] == ':') {
				return errors.New("只支持 (?: 非捕获分组，不支持内联标志与命名分组")
			}
		case '[':
			if i+1 < len(p) && p[i+1] == '[' && i+2 < len(p) && p[i+2] == ':' {
				return errors.New("不支持 POSIX 字符类 [[:…:]]")
			}
			if strings.HasPrefix(p[i:], "[:") && strings.Contains(p[i:], ":]") {
				return errors.New("不支持 POSIX 字符类 [[:…:]]")
			}
		}
	}
	return nil
}
