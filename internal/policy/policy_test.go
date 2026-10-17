package policy

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/darknightlab/onlinetorrent/internal/store"
)

func newStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func user(role string, vipUntil *string) *store.User {
	return &store.User{Role: role, VIPUntil: vipUntil}
}

func TestDecide(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	must := func(_ int64, err error) {
		if err != nil {
			t.Fatal(err)
		}
	}
	must(st.SaveRule(ctx, store.Rule{Name: "vip only", Enabled: true, Field: "title", Op: "contains", Value: "[VIPTEAM]", Action: VIP}))
	must(st.SaveRule(ctx, store.Rule{Name: "porn", Enabled: true, Field: "any", Op: "regex", Value: `\bhentai\b`, Action: Login, Label: "porn"}))
	must(st.SaveRule(ctx, store.Rule{Name: "members", Enabled: true, Field: "team", Op: "equals", Value: "MemberSub", Action: Login}))
	must(st.SaveRule(ctx, store.Rule{Name: "whitelist", Enabled: true, Priority: 10, Field: "infohash", Op: "equals", Value: "1111111111111111111111111111111111111111", Action: Allow}))
	e := New(st)

	future := time.Now().Add(48 * time.Hour).UTC().Format(time.RFC3339)
	past := time.Now().Add(-48 * time.Hour).UTC().Format(time.RFC3339)
	trusted := func(title string) Subject { return Subject{Title: title, Trusted: true} }

	cases := []struct {
		name    string
		u       *store.User
		s       Subject
		action  string
		allowed bool
	}{
		{"guest plain trusted", nil, trusted("[Sub] Foo - 01"), Allow, true},
		{"guest user magnet needs login", nil, Subject{Title: "x"}, Login, false},
		{"member user magnet", user("member", nil), Subject{Title: "x"}, Login, true},
		{"vip rule guest", nil, trusted("[VIPTEAM] Foo"), VIP, false},
		{"vip rule member", user("member", nil), trusted("[VIPTEAM] Foo"), VIP, false},
		{"vip rule member with vip_until", user("member", &future), trusted("[VIPTEAM] Foo"), VIP, true},
		{"vip rule expired", user("vip", &past), trusted("[VIPTEAM] Foo"), VIP, false},
		{"vip rule permanent vip", user("vip", nil), trusted("[VIPTEAM] Foo"), VIP, true},
		{"porn label forces block even for admin", user("admin", nil), Subject{Title: "foo", Files: []string{"a hentai b.mkv"}, Trusted: true}, Block, false},
		{"team equals", nil, Subject{Title: "foo", Team: "membersub", Trusted: true}, Login, false},
		{"priority whitelist beats source floor", nil, Subject{Infohash: "1111111111111111111111111111111111111111"}, Allow, true},
	}
	for _, c := range cases {
		d, err := e.Decide(ctx, c.u, c.s)
		if err != nil {
			t.Fatal(err)
		}
		if d.Action != c.action || d.Allowed != c.allowed {
			t.Errorf("%s: got %s/%v want %s/%v (%+v)", c.name, d.Action, d.Allowed, c.action, c.allowed, d)
		}
	}

	// verdict beats everything; political label forces block
	ih := "2222222222222222222222222222222222222222"
	if err := st.SetVerdict(ctx, nil, store.Verdict{Infohash: ih, Action: Allow, Label: "political"}); err != nil {
		t.Fatal(err)
	}
	d, _ := e.Decide(ctx, user("admin", nil), Subject{Infohash: ih, Title: "[VIPTEAM]", Trusted: true})
	if d.Action != Block || d.Allowed || d.Source != "verdict" {
		t.Errorf("verdict: %+v", d)
	}

	// verdict note must not leak to the public access object
	ih3 := "3333333333333333333333333333333333333333"
	if err := st.SetVerdict(ctx, nil, store.Verdict{Infohash: ih3, Action: Block, Label: "copyright", Note: "internal: DMCA ticket #42 from foo@bar"}); err != nil {
		t.Fatal(err)
	}
	d, _ = e.Decide(ctx, nil, Subject{Infohash: ih3, Trusted: true})
	if a := d.Access(); a.Action != Block || a.Reason != reasonFor(Block, "copyright") || d.Note == "" {
		t.Errorf("verdict leak: access=%+v decision=%+v", a, d)
	}

	// default action from settings
	raw, _ := json.Marshal("vip")
	if err := st.UpdateSettings(ctx, map[string]json.RawMessage{store.KeyDefaultAction: raw}); err != nil {
		t.Fatal(err)
	}
	d, _ = e.Decide(ctx, user("member", nil), trusted("plain"))
	if d.Action != VIP || d.Allowed {
		t.Errorf("default vip: %+v", d)
	}
}

func TestHardBlockBeatsPriority(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	if _, err := st.SaveRule(ctx, store.Rule{Name: "porn-kw", Enabled: true, Priority: 0, Field: "file", Op: "contains", Value: "xxx", Action: Login, Label: "porn"}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.SaveRule(ctx, store.Rule{Name: "team allow", Enabled: true, Priority: 100, Field: "team", Op: "equals", Value: "GoodSub", Action: Allow}); err != nil {
		t.Fatal(err)
	}
	e := New(st)
	d, err := e.Decide(ctx, user("admin", nil), Subject{Title: "ok", Team: "GoodSub", Files: []string{"a xxx b.mkv"}, Trusted: true})
	if err != nil {
		t.Fatal(err)
	}
	if d.Action != Block || d.Allowed || d.Label != "porn" || d.RuleName != "porn-kw" {
		t.Errorf("porn rule vs higher-priority allow: %+v", d)
	}
	if a := d.Access(); a.Reason != reasonFor(Block, "porn") {
		t.Errorf("reason: %+v", a)
	}
}

func TestFloorVsTitleAllow(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	if _, err := st.SaveRule(ctx, store.Rule{Name: "title allow", Enabled: true, Priority: 5, Field: "title", Op: "contains", Value: "Frieren", Action: Allow}); err != nil {
		t.Fatal(err)
	}
	e := New(st)
	// untrusted (user magnet, dn= is attacker-controlled): floor (login) still applies
	d, _ := e.Decide(ctx, nil, Subject{Infohash: "4444444444444444444444444444444444444444", Title: "[x] Frieren - 01"})
	if d.Action != Login || d.Allowed || d.Source != "source" {
		t.Errorf("untrusted title allow must not lower floor: %+v", d)
	}
	if d.Access().Reason != reasonFor(Login, "") {
		t.Errorf("reason: %+v", d.Access())
	}
	// trusted: rule applies
	d, _ = e.Decide(ctx, nil, Subject{Title: "[x] Frieren - 01", Trusted: true})
	if d.Action != Allow || !d.Allowed {
		t.Errorf("trusted title allow: %+v", d)
	}
	// raising rules still raise above the floor for untrusted
	if _, err := st.SaveRule(ctx, store.Rule{Name: "vip", Enabled: true, Field: "title", Op: "contains", Value: "VIPONLY", Action: VIP}); err != nil {
		t.Fatal(err)
	}
	e.Invalidate()
	d, _ = e.Decide(ctx, user("member", nil), Subject{Title: "VIPONLY"})
	if d.Action != VIP || d.Allowed {
		t.Errorf("untrusted vip rule: %+v", d)
	}
}

func TestGeneration(t *testing.T) {
	e := New(newStore(t))
	g := e.Generation()
	if _, err := e.load(context.Background()); err != nil {
		t.Fatal(err)
	}
	e.Invalidate()
	if e.Generation() == g {
		t.Fatal("generation not bumped")
	}
}
