package rank

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestDefaultValid(t *testing.T) {
	r, err := Parse(DefaultJSON())
	if err != nil {
		t.Fatal(err)
	}
	if r.Availability.ServerComplete != 30 || r.Availability.Dead != -100 || len(r.Keywords) != 5 || r.Oversize != -10 {
		t.Fatalf("%+v", r)
	}
	// HEVC is a bonus now, not a penalty
	if r.Keywords[0].Label != "HEVC" || r.Keywords[0].Score <= 0 {
		t.Fatal("HEVC")
	}
}

func TestCheckPattern(t *testing.T) {
	ok := []string{`hevc|[hx]\.?265`, `\b4k\b`, `简|chs`, `(?:1080|1920)p?`, `\d{3,4}p`, `[a-z]+\s\(\w\)`, `\x41`}
	for _, p := range ok {
		if err := CheckPattern(p); err != nil {
			t.Errorf("%q: %v", p, err)
		}
	}
	bad := []string{
		`(?i)x`,       // inline flags
		`(?P<n>x)`,    // named group
		`(?=x)`,       // lookahead (RE2 rejects)
		`(a)\1`,       // backreference (RE2 rejects)
		`\p{Han}`,     // unicode class (JS needs the u flag)
		`\Aabc\z`,     // RE2-only anchors
		`\Qa.b\E`,     // quoting
		`[[:alpha:]]`, // POSIX class
		`\x{4e00}`,    // RE2 hex escape
		`(`,           // syntax error
		`abc\`,        // trailing backslash
	}
	for _, p := range bad {
		if err := CheckPattern(p); err == nil {
			t.Errorf("%q accepted", p)
		}
	}
}

func TestParseValidation(t *testing.T) {
	mut := func(f func(*Rules)) []byte {
		r := Default()
		f(&r)
		b, _ := json.Marshal(r)
		return b
	}
	cases := map[string][]byte{
		"unknown field": []byte(`{"availability":{},"keywords":[],"teams":[],"oversize":0,"extra":1}`),
		"not object":    []byte(`[]`),
		"score range":   mut(func(r *Rules) { r.Availability.ServerComplete = 5000 }),
		"cap range":     mut(func(r *Rules) { r.Availability.WTCap = -1 }),
		"empty label":   mut(func(r *Rules) { r.Keywords[0].Label = " " }),
		"bad regex":     mut(func(r *Rules) { r.Keywords[0].Pattern = `(?=x)` }),
		"long pattern":  mut(func(r *Rules) { r.Keywords[0].Pattern = strings.Repeat("a", MaxPatternLen+1) }),
		"too many kw":   mut(func(r *Rules) { r.Keywords = make([]Keyword, MaxKeywords+1) }),
		"dup team":      mut(func(r *Rules) { r.Teams = []Team{{Name: "A", Score: 1}, {Name: " A ", Score: 2}} }),
		"empty team":    mut(func(r *Rules) { r.Teams = []Team{{Name: ""}} }),
		"keyword score": mut(func(r *Rules) { r.Keywords[1].Score = -1001 }),
	}
	for name, b := range cases {
		if _, err := Parse(b); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	r, err := Parse(mut(func(r *Rules) {
		r.Teams = []Team{{Name: " 喵萌奶茶屋 ", Score: 5}}
		r.Keywords[0].Label = " HEVC "
	}))
	if err != nil || r.Teams[0].Name != "喵萌奶茶屋" || r.Keywords[0].Label != "HEVC" {
		t.Fatalf("normalize: %+v %v", r, err)
	}
	r, err = Parse([]byte(`{"availability":{"server_complete":1},"oversize":-1}`))
	if err != nil || r.Keywords == nil || r.Teams == nil {
		t.Fatalf("nil lists: %+v %v", r, err)
	}
}
