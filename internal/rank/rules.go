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
	"regexp"
	"strings"
	"unicode/utf8"
)

// Availability scores (swarm stats).
type Availability struct {
	ServerComplete int `json:"server_complete"` // a qB server has the whole torrent
	WTSeeder       int `json:"wt_seeder"`       // per WebTorrent seeder …
	WTCap          int `json:"wt_cap"`          // … at most
	BTSeeder       int `json:"bt_seeder"`       // per BT seeder (only viewers who can make the server seed) …
	BTCap          int `json:"bt_cap"`          // … at most
	Dead           int `json:"dead"`            // no reachable source for this viewer
}

// Keyword adds Score when Pattern (case-insensitive regex) matches title + file path.
type Keyword struct {
	Label   string `json:"label"`
	Pattern string `json:"pattern"`
	Score   int    `json:"score"`
}

// Team adds Score for an exact team name.
type Team struct {
	Name  string `json:"name"`
	Score int    `json:"score"`
}

// Rules is the whole rule set.
type Rules struct {
	Availability Availability `json:"availability"`
	Keywords     []Keyword    `json:"keywords"`
	Teams        []Team       `json:"teams"`
	Oversize     int          `json:"oversize"` // file larger than play.max_auto_size_mb
}

// Limits.
const (
	MaxKeywords   = 30
	MaxTeams      = 200
	MaxLabelLen   = 40
	MaxPatternLen = 200
	MaxTeamLen    = 80
	MaxScore      = 1000
)

// Default returns the built-in rules.
func Default() Rules {
	return Rules{
		Availability: Availability{ServerComplete: 30, WTSeeder: 5, WTCap: 30, BTSeeder: 1, BTCap: 20, Dead: -100},
		Keywords: []Keyword{
			{Label: "HEVC", Pattern: `hevc|[hx]\.?265`, Score: 3},
			{Label: "1080p", Pattern: `1080|1920`, Score: 3},
			{Label: "4K", Pattern: `2160|\b4k\b`, Score: 1},
			{Label: "简体", Pattern: `简|chs|\bsc\b|\bgb\b`, Score: 2},
			{Label: "繁体", Pattern: `繁|cht|\btc\b|big5`, Score: 1},
		},
		Teams:    []Team{},
		Oversize: -10,
	}
}

// DefaultJSON is Default encoded (the setting's default value).
func DefaultJSON() json.RawMessage {
	b, _ := json.Marshal(Default())
	return b
}

// Parse decodes and validates rules (unknown fields are an error) and returns them
// normalised (trimmed strings, non-nil lists).
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

func score(name string, v int) error {
	if v < -MaxScore || v > MaxScore {
		return fmt.Errorf("%s 须在 %d 到 %d 之间", name, -MaxScore, MaxScore)
	}
	return nil
}

func (r *Rules) normalize() error {
	a := r.Availability
	for _, c := range []struct {
		n string
		v int
	}{{"服务器已缓存加分", a.ServerComplete}, {"每个 WebTorrent 做种者加分", a.WTSeeder}, {"每个 BT 做种者加分", a.BTSeeder}, {"无人做种扣分", a.Dead}, {"超过大小上限扣分", r.Oversize}} {
		if err := score(c.n, c.v); err != nil {
			return err
		}
	}
	if a.WTCap < 0 || a.WTCap > MaxScore || a.BTCap < 0 || a.BTCap > MaxScore {
		return fmt.Errorf("加分上限须在 0 到 %d 之间", MaxScore)
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
