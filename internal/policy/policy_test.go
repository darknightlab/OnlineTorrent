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
