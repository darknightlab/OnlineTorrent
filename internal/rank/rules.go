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
	// ServerComplete (a bonus when our qB had the file) was removed in v5 — users must not
	// learn what our servers cache. Still accepted on input so older saved rules parse,
	// and dropped.
	ServerComplete *float64 `json:"server_complete,omitempty"`
	WTSeeder       float64  `json:"wt_seeder"` // WebTorrent seeders: coefficient …
	WTCap          float64  `json:"wt_cap"`    // … cap …
	WTScale        float64  `json:"wt_scale"`  // … and scale (tanh curve)
	BTSeeder       float64  `json:"bt_seeder"` // BT seeders (only viewers who can make the server seed): coefficient …
	BTCap          float64  `json:"bt_cap"`    // … cap …
	BTScale        float64  `json:"bt_scale"`  // … and scale (tanh curve)
	// Curve: "linear" = coefficient per seeder; "log" = coefficient × log2(1 + seeders);
	// "tanh" = coefficient × tanh(seeders / scale) (≈ coefficient·n/scale for few seeders,
	// saturating at coefficient). The cap applies to every curve. Missing = linear (rules
	// saved before the field existed).
	Curve string  `json:"curve"`
	Dead  float64 `json:"dead"` // no reachable source for this viewer
}

// Seeder curves.
const (
	CurveLinear = "linear"
	CurveLog    = "log"
	CurveTanh   = "tanh"
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

// SizeRule scores the file size along a line through (FromMB, FromScore) and (ToMB,
// ToScore): FromScore up to FromMB, linear in between, ToScore from ToMB on — or, with
// Extend, the line continues beyond ToMB without bound. Unknown sizes score 0.
//
// Rules saved before this shape used {max, full_below_mb, zero_above_mb} (a small-file
// bonus): they are read as (full_below_mb, max) → (zero_above_mb, 0), not extended.
type SizeRule struct {
	FromMB    float64 `json:"from_mb"`
	FromScore float64 `json:"from_score"`
	ToMB      float64 `json:"to_mb"`
	ToScore   float64 `json:"to_score"`
	Extend    bool    `json:"extend"`
}

// UnmarshalJSON accepts the current and the legacy shape (unknown keys are an error).
func (s *SizeRule) UnmarshalJSON(b []byte) error {
	var v struct {
		FromMB      *float64 `json:"from_mb"`
		FromScore   *float64 `json:"from_score"`
		ToMB        *float64 `json:"to_mb"`
		ToScore     *float64 `json:"to_score"`
		Extend      *bool    `json:"extend"`
		Max         *float64 `json:"max"`
		FullBelowMB *float64 `json:"full_below_mb"`
		ZeroAboveMB *float64 `json:"zero_above_mb"`
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&v); err != nil {
		return err
	}
	get := func(p *float64) float64 {
		if p == nil {
			return 0
		}
		return *p
	}
	legacy := v.Max != nil || v.FullBelowMB != nil || v.ZeroAboveMB != nil
	current := v.FromMB != nil || v.FromScore != nil || v.ToMB != nil || v.ToScore != nil || v.Extend != nil
	if legacy && current {
		return errors.New("size: 新旧两种写法不能混用")
	}
	if legacy {
		*s = SizeRule{FromMB: get(v.FullBelowMB), FromScore: get(v.Max), ToMB: get(v.ZeroAboveMB)}
		return nil
	}
	*s = SizeRule{FromMB: get(v.FromMB), FromScore: get(v.FromScore), ToMB: get(v.ToMB), ToScore: get(v.ToScore), Extend: v.Extend != nil && *v.Extend}
	return nil
}

// Rules is the whole rule set.
type Rules struct {
	Availability Availability `json:"availability"`
	Keywords     []Keyword    `json:"keywords"`
	Teams        []Team       `json:"teams"`
	Size         SizeRule     `json:"size"`
	// Oversize (−10 for files over play.max_auto_size_mb) was removed in v4; still accepted
	// on input so older saved rules parse, and dropped.
	Oversize *float64 `json:"oversize,omitempty"`
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
	MaxScale      = 1e6
	MaxSizeMB     = 1 << 20
)

// Default returns the built-in rules (owner-specified v5 = v4 without the server-cache
// bonus; scenarios:
// docs/research/rank-rules.md, web/src/lib/rank.scenarios.test.ts). Keep in sync with
// DEFAULT_RANK_RULES in web/src/lib/rank.ts (golden file testdata/default.json).
func Default() Rules {
	return Rules{
		Availability: Availability{WTSeeder: 20, WTCap: 20, WTScale: 20, BTSeeder: 10, BTCap: 10, BTScale: 20, Curve: CurveTanh, Dead: -100},
		Keywords: []Keyword{
			{Label: "简体中文", Pattern: `简|chs|\bsc\b|\bgb\b|zh-?hans|jpsc`, Score: 25, Group: "字幕"},
			{Label: "繁体中文", Pattern: `繁|cht|\btc\b|big5|zh-?hant|jptc`, Score: 20, Group: "字幕"},
			{Label: "日文", Pattern: `\bjpn?\b|\bjapanese\b|日字|日文|日[语語]|[简繁中]日|ch[st][_&.-]?jp|jp[_&.-]?ch[st]`, Score: 10, Group: "字幕"},
			{Label: "4K 及以上", Pattern: `2160p?\b|3840x2160|4320p?\b|7680x4320|\b[48]k\b`, Score: 30, Group: "分辨率"},
			{Label: "1080p", Pattern: `1080[pi]?\b|1920x1080`, Score: 20, Group: "分辨率"},
			{Label: "720p", Pattern: `720p\b|1280x720`, Score: 10, Group: "分辨率"},
			{Label: "HEVC", Pattern: `hevc|[hx]\.?265`, Score: 2},
		},
		Teams: []Team{},
		Size:  SizeRule{FromMB: 200, FromScore: 0, ToMB: 1000, ToScore: -16, Extend: true},
	}
}

// legacyDefaults are earlier built-in defaults as stored in the settings table. A stored
// value equal to one of them was never edited by an admin and is upgraded to Default().
var legacyDefaults = []string{
	// v1 (2026-10, 7d2effb): server_complete +30 dominated, no size bonus
	`{"availability":{"server_complete":30,"wt_seeder":5,"wt_cap":30,"bt_seeder":1,"bt_cap":20,"dead":-100},"keywords":[{"label":"HEVC","pattern":"hevc|[hx]\\.?265","score":3},{"label":"1080p","pattern":"1080|1920","score":3},{"label":"4K","pattern":"2160|\\b4k\\b","score":1},{"label":"简体","pattern":"简|chs|\\bsc\\b|\\bgb\\b","score":2},{"label":"繁体","pattern":"繁|cht|\\btc\\b|big5","score":1}],"teams":[],"oversize":-10}`,
	// v2 (2026-10, e23a33a): BT cap 4, 繁体 12, size bonus 6 over 300–1500 MB
	`{"availability":{"server_complete":2,"wt_seeder":2,"wt_cap":6,"bt_seeder":1,"bt_cap":4,"curve":"log","dead":-100},"keywords":[{"label":"简体中文","pattern":"简|chs|\\bsc\\b|\\bgb\\b|zh-?hans","score":25,"group":"字幕"},{"label":"繁体中文","pattern":"繁|cht|\\btc\\b|big5|zh-?hant","score":12,"group":"字幕"},{"label":"1080p","pattern":"1080[pi]?\\b|1920x1080","score":10,"group":"分辨率"},{"label":"4K","pattern":"2160p?\\b|3840x2160|\\b4k\\b","score":5,"group":"分辨率"},{"label":"720p","pattern":"720p\\b|1280x720","score":4,"group":"分辨率"},{"label":"HEVC","pattern":"hevc|[hx]\\.?265","score":2,"group":""}],"teams":[],"size":{"max":6,"full_below_mb":300,"zero_above_mb":1500},"oversize":-10}`,
	// v3 (2026-10, 708decc): BT cap 10, 繁体 20, small-file bonus 16 over 200–1000 MB
	`{"availability":{"server_complete":2,"wt_seeder":2,"wt_cap":6,"bt_seeder":1,"bt_cap":10,"curve":"log","dead":-100},"keywords":[{"label":"简体中文","pattern":"简|chs|\\bsc\\b|\\bgb\\b|zh-?hans","score":25,"group":"字幕"},{"label":"繁体中文","pattern":"繁|cht|\\btc\\b|big5|zh-?hant","score":20,"group":"字幕"},{"label":"1080p","pattern":"1080[pi]?\\b|1920x1080","score":15,"group":"分辨率"},{"label":"4K","pattern":"2160p?\\b|3840x2160|\\b4k\\b","score":8,"group":"分辨率"},{"label":"720p","pattern":"720p\\b|1280x720","score":5,"group":"分辨率"},{"label":"HEVC","pattern":"hevc|[hx]\\.?265","score":2,"group":""}],"teams":[],"size":{"max":16,"full_below_mb":200,"zero_above_mb":1000},"oversize":-10}`,
	// v4 (2026-10, deployed with the tanh curves): server_complete +2
	`{"availability":{"server_complete":2,"wt_seeder":20,"wt_cap":20,"wt_scale":20,"bt_seeder":10,"bt_cap":10,"bt_scale":20,"curve":"tanh","dead":-100},"keywords":[{"label":"简体中文","pattern":"简|chs|\\bsc\\b|\\bgb\\b|zh-?hans|jpsc","score":25,"group":"字幕"},{"label":"繁体中文","pattern":"繁|cht|\\btc\\b|big5|zh-?hant|jptc","score":20,"group":"字幕"},{"label":"日文","pattern":"\\bjpn?\\b|\\bjapanese\\b|日字|日文|日[语語]|[简繁中]日|ch[st][_&.-]?jp|jp[_&.-]?ch[st]","score":10,"group":"字幕"},{"label":"4K 及以上","pattern":"2160p?\\b|3840x2160|4320p?\\b|7680x4320|\\b[48]k\\b","score":30,"group":"分辨率"},{"label":"1080p","pattern":"1080[pi]?\\b|1920x1080","score":20,"group":"分辨率"},{"label":"720p","pattern":"720p\\b|1280x720","score":10,"group":"分辨率"},{"label":"HEVC","pattern":"hevc|[hx]\\.?265","score":2,"group":""}],"teams":[],"size":{"from_mb":200,"from_score":0,"to_mb":1000,"to_score":-16,"extend":true}}`,
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
// normalised (trimmed strings, non-nil lists, curve set, legacy fields converted/dropped).
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
	r.Oversize = nil
	r.Availability.ServerComplete = nil
	a := &r.Availability
	for _, c := range []struct {
		n string
		v float64
	}{{"WebTorrent 做种系数", a.WTSeeder}, {"BT 做种系数", a.BTSeeder}, {"无人做种扣分", a.Dead},
		{"文件大小起点分值", r.Size.FromScore}, {"文件大小终点分值", r.Size.ToScore}} {
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
	case CurveTanh:
		if !(a.WTScale > 0 && a.WTScale <= MaxScale && a.BTScale > 0 && a.BTScale <= MaxScale) {
			return errors.New("tanh 曲线的尺度须大于 0")
		}
	default:
		return errors.New("做种人数曲线须为 linear、log 或 tanh")
	}
	if a.WTScale < 0 || a.BTScale < 0 || a.WTScale > MaxScale || a.BTScale > MaxScale || math.IsNaN(a.WTScale) || math.IsNaN(a.BTScale) {
		return errors.New("做种人数尺度无效")
	}
	sz := r.Size
	if math.IsNaN(sz.FromMB) || math.IsNaN(sz.ToMB) || sz.FromMB < 0 || sz.ToMB < 0 || sz.FromMB > MaxSizeMB || sz.ToMB > MaxSizeMB {
		return fmt.Errorf("文件大小规则的大小须在 0 到 %d MB 之间", MaxSizeMB)
	}
	if (sz.FromScore != 0 || sz.ToScore != 0) && sz.ToMB <= sz.FromMB {
		return errors.New("文件大小规则：终点大小须大于起点大小")
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
