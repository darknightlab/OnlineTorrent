package store

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// A stored rule set equal to the v1 built-in default is upgraded on start; edited ones stay.
func TestRankRulesLegacyUpgrade(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	v1 := `{"availability":{"server_complete":30,"wt_seeder":5,"wt_cap":30,"bt_seeder":1,"bt_cap":20,"dead":-100},"keywords":[{"label":"HEVC","pattern":"hevc|[hx]\\.?265","score":3},{"label":"1080p","pattern":"1080|1920","score":3},{"label":"4K","pattern":"2160|\\b4k\\b","score":1},{"label":"简体","pattern":"简|chs|\\bsc\\b|\\bgb\\b","score":2},{"label":"繁体","pattern":"繁|cht|\\btc\\b|big5","score":1}],"teams":[],"oversize":-10}`
	put := func(v string) {
		if _, err := st.W.ExecContext(ctx, `UPDATE settings SET value = ? WHERE key = ?`, v, KeyRankRules); err != nil {
			t.Fatal(err)
		}
	}
	put(v1)
	if err := st.seedSettings(ctx); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(st.Raw(KeyRankRules)), `"curve":"tanh"`) {
		t.Fatalf("not upgraded: %s", st.Raw(KeyRankRules))
	}
	edited := strings.Replace(v1, `"server_complete":30`, `"server_complete":10`, 1)
	put(edited)
	_ = st.seedSettings(ctx)
	if string(st.Raw(KeyRankRules)) != edited {
		t.Fatalf("customised rules replaced: %s", st.Raw(KeyRankRules))
	}
}

func TestTrackerAndRankSettings(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	if got := st.List(KeyBTTrackers); len(got) != 6 || !strings.HasPrefix(got[0], "udp://") {
		t.Fatalf("default bt trackers %v", got)
	}
	set := func(k string, v any) error {
		b, _ := json.Marshal(v)
		return st.UpdateSettings(ctx, map[string]json.RawMessage{k: b})
	}
	// tracker lists: schemes are checked, blanks / duplicates dropped
	if err := set(KeyBTTrackers, []string{"wss://x"}); err == nil {
		t.Error("wss in bt list accepted")
	}
	if err := set(KeyBTTrackers, []string{"udp://x/announce"}); err == nil {
		t.Error("udp without port accepted")
	}
	if err := set(KeyTrackers, []string{"udp://x:1"}); err == nil {
		t.Error("udp in webtorrent list accepted")
	}
	if err := set(KeyBTTrackers, []string{" udp://a:1/announce ", "", "udp://a:1/announce", "https://b/announce"}); err != nil {
		t.Fatal(err)
	}
	if got := st.List(KeyBTTrackers); strings.Join(got, ",") != "udp://a:1/announce,https://b/announce" {
		t.Fatalf("normalized %v", got)
	}

	// rank rules: hidden from the generic settings API, own validated setter
	for _, it := range st.SettingItems() {
		if it.Key == KeyRankRules {
			t.Fatal("rank.rules listed in settings")
		}
	}
	if err := set(KeyRankRules, map[string]any{}); err == nil {
		t.Error("rank.rules writable via UpdateSettings")
	}
	var def map[string]any
	if err := json.Unmarshal(st.Raw(KeyRankRules), &def); err != nil || def["availability"] == nil {
		t.Fatalf("default rank rules %s", st.Raw(KeyRankRules))
	}
	if _, err := st.SetJSON(ctx, KeyRankRules, json.RawMessage(`{"keywords":[{"label":"x","pattern":"(?<=a)b","score":1}]}`)); err == nil {
		t.Error("lookbehind accepted")
	}
	// server_complete (removed) is accepted and dropped
	out, err := st.SetJSON(ctx, KeyRankRules, json.RawMessage(`{"availability":{"server_complete":10,"bt_seeder":3},"teams":[{"name":" A ","score":3}]}`))
	if err != nil {
		t.Fatal(err)
	}
	raw := string(st.Raw(KeyRankRules))
	if !strings.Contains(string(out), `"name":"A"`) || !strings.Contains(raw, `"bt_seeder":3`) || strings.Contains(raw, "server_complete") {
		t.Fatalf("stored %s", out)
	}
}
