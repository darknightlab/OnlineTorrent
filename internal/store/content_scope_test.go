package store

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

type tagged struct{ tags, meta []string }

func (t tagged) ScopeFacts() ([]string, []string) { return t.tags, t.meta }

func sub(tags ...string) tagged { return tagged{tags: tags} }

func TestCompileScopePrecedence(t *testing.T) {
	// 原创 AND 校园 OR 战斗 == (原创 ∧ 校园) ∨ 战斗
	p := CompileScope(ContentScope{TagMode: ScopeSelected, Tags: []string{"原创", "校园", "战斗"}, TagOperators: []string{"and", "or"}})
	if !reflect.DeepEqual(p.Groups, [][]string{{"原创", "校园"}, {"战斗"}}) {
		t.Fatalf("groups %v", p.Groups)
	}
	cases := []struct {
		s    tagged
		want bool
	}{
		{sub("原创", "校园"), true},
		{sub("战斗"), true},
		{sub("原创"), false},
		{sub("校园"), false},
		{tagged{tags: []string{"原创"}, meta: []string{"校园"}}, true}, // union of tags and meta_tags
		{sub(), false},
	}
	for _, c := range cases {
		if got := p.Visible(c.s); got != c.want {
			t.Errorf("%v: got %v", c.s, got)
		}
	}
	// 原创 OR 校园 AND 战斗 == 原创 ∨ (校园 ∧ 战斗) — not ((原创 ∨ 校园) ∧ 战斗)
	p = CompileScope(ContentScope{TagMode: ScopeSelected, Tags: []string{"原创", "校园", "战斗"}, TagOperators: []string{"or", "and"}})
	if !p.Visible(sub("原创")) || p.Visible(sub("校园")) || !p.Visible(sub("校园", "战斗")) || p.Visible(sub("战斗")) {
		t.Errorf("or-and precedence wrong: %v", p.Groups)
	}
	if got := TagExpressionString([]string{"原创", "校园", "战斗"}, []string{"and", "or"}); got != "(原创 AND 校园) OR 战斗" {
		t.Errorf("expr %q", got)
	}
}

func TestScopeExclude(t *testing.T) {
	p := CompileScope(ContentScope{TagMode: ScopeAll, ExcludeTags: []string{"里番", "R18"}})
	if !p.Restricted() || p.Visible(sub("原创", "里番")) || p.Visible(tagged{meta: []string{"R18"}}) || !p.Visible(sub("原创")) || !p.Visible(sub()) {
		t.Error("exclude in all mode")
	}
	// exclusion wins over a matching expression
	p = CompileScope(ContentScope{TagMode: ScopeSelected, Tags: []string{"原创"}, ExcludeTags: []string{"里番"}})
	if p.Visible(sub("原创", "里番")) || !p.Visible(sub("原创")) {
		t.Error("exclude in selected mode")
	}
	if CompileScope(DefaultContentScope(RoleGuest)).Restricted() || !(*ScopePlan)(nil).Visible(sub()) {
		t.Error("default / nil plans must be unrestricted")
	}
	if DenyAllScopePlan().Visible(sub("x")) {
		t.Error("deny all")
	}
}

func TestNormalizeContentScope(t *testing.T) {
	long := strings.Repeat("长", 61)
	many := make([]string, 51)
	for i := range many {
		many[i] = "t"
	}
	bad := []ContentScope{
		{TagMode: "some"},
		{TagMode: ScopeSelected},                           // empty expression
		{TagMode: ScopeSelected, Tags: []string{"a", "b"}}, // missing operator
		{TagMode: ScopeSelected, Tags: []string{"a", "b"}, TagOperators: []string{"xor"}}, // bad operator
		{TagMode: ScopeSelected, Tags: []string{"a"}, TagOperators: []string{"and"}},      // too many operators
		{TagMode: ScopeSelected, Tags: []string{" "}},                                     // empty tag
		{TagMode: ScopeSelected, Tags: []string{long}},                                    // > 60 chars
		{TagMode: ScopeAll, ExcludeTags: many},                                            // > 50 tags
		{TagMode: ScopeAll, ExcludeTags: []string{""}},                                    // empty exclude
	}
	for i, sc := range bad {
		if _, err := NormalizeContentScope(sc); !errors.Is(err, ErrScopeInvalid) {
			t.Errorf("case %d: want ErrScopeInvalid, got %v", i, err)
		}
	}
	ok, err := NormalizeContentScope(ContentScope{TagMode: ScopeAll, Tags: []string{"a"}, ExcludeTags: []string{" b ", "a", "b"}})
	if err != nil || len(ok.Tags) != 0 || !reflect.DeepEqual(ok.ExcludeTags, []string{"a", "b"}) {
		t.Errorf("normalize: %+v %v", ok, err)
	}
	if _, err := NormalizeContentScope(ContentScope{TagMode: ScopeSelected, Tags: []string{strings.Repeat("长", 60)}}); err != nil {
		t.Errorf("60 chars allowed: %v", err)
	}
}

func TestContentScopePersistence(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "otd.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	all, err := st.ContentScopes(ctx)
	if err != nil || len(all) != 3 || all[RoleGuest].TagMode != ScopeAll {
		t.Fatalf("defaults: %+v %v", all, err)
	}
	in := ContentScope{TagMode: ScopeSelected, Tags: []string{"原创", "校园", "战斗", "原创"}, TagOperators: []string{"and", "or", "and"}, ExcludeTags: []string{"里番"}}
	if _, err := st.SetContentScope(ctx, RoleGuest, in); err != nil {
		t.Fatal(err)
	}
	got, err := st.ContentScope(ctx, RoleGuest)
	if err != nil || !reflect.DeepEqual(got.Tags, in.Tags) || !reflect.DeepEqual(got.TagOperators, in.TagOperators) || !reflect.DeepEqual(got.ExcludeTags, []string{"里番"}) {
		t.Fatalf("round trip: %+v %v", got, err)
	}
	if m, _ := st.ContentScope(ctx, RoleMember); m.TagMode != ScopeAll {
		t.Error("member untouched")
	}
	if _, err := st.SetContentScope(ctx, RoleAdmin, in); !errors.Is(err, ErrScopeInvalid) {
		t.Errorf("admin: %v", err)
	}
	if _, err := st.SetContentScope(ctx, "nobody", in); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown: %v", err)
	}
	// back to all: expression cleared
	if _, err := st.SetContentScope(ctx, RoleGuest, ContentScope{TagMode: ScopeAll}); err != nil {
		t.Fatal(err)
	}
	if got, _ := st.ContentScope(ctx, RoleGuest); len(got.Tags) != 0 || len(got.ExcludeTags) != 0 || got.TagMode != ScopeAll {
		t.Errorf("reset: %+v", got)
	}
}
