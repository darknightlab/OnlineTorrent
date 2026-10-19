package store

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

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
	out, err := st.SetJSON(ctx, KeyRankRules, json.RawMessage(`{"availability":{"server_complete":10},"teams":[{"name":" A ","score":3}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), `"name":"A"`) || !strings.Contains(string(st.Raw(KeyRankRules)), `"server_complete":10`) {
		t.Fatalf("stored %s", out)
	}
}
