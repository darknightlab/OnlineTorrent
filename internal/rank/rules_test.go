package rank

import (
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"
)

func TestDefaultValid(t *testing.T) {
	r, err := Parse(DefaultJSON())
	if err != nil {
		t.Fatal(err)
	}
	a := r.Availability
	if a.ServerComplete != 2 || a.BTCap != 10 || a.Dead != -100 || a.Curve != CurveLog || len(r.Keywords) != 6 || r.Oversize != -10 || r.Size.Max != 16 {
		t.Fatalf("%+v", r)
	}
	// the dead penalty stays decisive: more than every bonus together
	sum := a.ServerComplete + a.WTCap + a.BTCap + r.Size.Max
	groups := map[string]float64{}
	for _, k := range r.Keywords {
		if k.Group == "" {
			sum += k.Score
		} else {
			groups[k.Group] = max(groups[k.Group], k.Score)
		}
	}
	for _, v := range groups {
		sum += v
	}
	if -a.Dead <= sum {
		t.Fatalf("dead %v not decisive vs max bonus %v", a.Dead, sum)
	}
	// 1080p beats 720p unless the 720p file is ≥ 500 MB smaller (size: 2 points / 100 MB)
	res := map[string]float64{}
	for _, k := range r.Keywords {
		res[k.Label] = k.Score
	}
	slope := r.Size.Max / float64(r.Size.ZeroAboveMB-r.Size.FullBelowMB) * 100
	if slope < 2 || res["1080p"]-res["720p"] < 5*slope {
		t.Fatalf("size slope %.2f/100MB, 1080p−720p %.0f", slope, res["1080p"]-res["720p"])
	}
}

// testdata/default.json is the shared golden copy: web/src/lib/rank.test.ts checks that
// DEFAULT_RANK_RULES equals it, so Go and TS defaults can't drift apart.
func TestDefaultGolden(t *testing.T) {
	b, err := os.ReadFile("testdata/default.json")
	if err != nil {
		t.Fatal(err)
	}
	var got, want any
	_ = json.Unmarshal(DefaultJSON(), &got)
	if err := json.Unmarshal(b, &want); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Default() differs from testdata/default.json:\n%s", DefaultJSON())
	}
}

func TestLegacyDefault(t *testing.T) {
	for i, l := range legacyDefaults {
		if !IsLegacyDefault([]byte(l)) {
			t.Fatalf("legacy %d literal", i+1)
		}
		if _, err := Parse([]byte(l)); err != nil {
			t.Fatalf("legacy %d: %v", i+1, err)
		}
	}
	if IsLegacyDefault(DefaultJSON()) {
		t.Fatal("current default is not legacy")
	}
	// v1 with a single edit is a customised rule set: kept
	edited := strings.Replace(legacyDefaults[0], `"server_complete":30`, `"server_complete":10`, 1)
	if IsLegacyDefault([]byte(edited)) || IsLegacyDefault([]byte("{")) {
		t.Fatal("edited / invalid")
	}
	// legacy rules still parse (no curve / size / group): linear, size off
	r, err := Parse([]byte(legacyDefaults[0]))
	if err != nil || r.Availability.Curve != CurveLinear || r.Size.Max != 0 {
		t.Fatalf("%+v %v", r, err)
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
