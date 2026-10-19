package rank

import "testing"

func TestOrder(t *testing.T) {
	cs := []Candidate{
		{Name: "[A] Show - 03 [1080p][HEVC 10bit][CHS].mkv", Size: 300 << 20, PublishedAt: "2026-10-18T10:00:00Z", Allowed: true},
		{Name: "[B] Show - 03 [1080p][AVC][CHT].mp4", Size: 400 << 20, PublishedAt: "2026-10-18T09:00:00Z", Allowed: true},
		{Name: "[C] Show - 03 [1080p][AVC][CHS].mp4", Size: 3 << 30, PublishedAt: "2026-10-18T11:00:00Z", Allowed: true}, // over the size limit
		{Name: "[D] Show - 03 [1080p][AVC][CHS].mp4", Size: 400 << 20, PublishedAt: "2026-10-18T12:00:00Z", Allowed: false},
		{Name: "[E] Show - 03 [1080p][AVC][CHT].mp4", Size: 400 << 20, PublishedAt: "2026-10-18T09:30:00Z", Allowed: true}, // ties B, newer
	}
	got := Order(cs, 2<<30)
	want := []int{4, 1, 0, 2, 3}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("order %v, want %v", got, want)
		}
	}
}

func TestReCHS(t *testing.T) {
	for s, want := range map[string]bool{
		"[LoliHouse] Foo - 01 [简繁内封]":    true,
		"[Sub] Foo - 01 [CHS][1080p]":    true,
		"[Sub] Foo - 01 [GB][1080p]":     true,
		"[Sub] Foo - 01 [SC][1080p]":     true,
		"[Sub] Foo [BD Disc 1][1080p]":   false,
		"[Sub] Foo - 01 [BIG5][1080p]":   false,
		"[Sub] Foo - 01 [Sci-Fi][1080p]": false,
	} {
		if got := reCHS.MatchString(s); got != want {
			t.Errorf("%q: got %v want %v", s, got, want)
		}
	}
}
