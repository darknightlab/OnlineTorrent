package meta

import "testing"

func TestParseEpisode(t *testing.T) {
	cases := []struct {
		in   string
		ep   float64 // -1 = none
		kind string
	}{
		{"[Dynamis One] Shinja Zero no Megami-sama - 01 (ABEMA 1920x1080 AVC AAC MKV) [7CB4C9A3].mkv", 1, "episode"},
		{"[NanakoRaws] Watashi ga Koibito ni Nareru Wake Nai jan, Muri Muri! - 17 (AT-X 1920x1080 x265 AAC).mkv", 17, "episode"},
		{"[LoliHouse] Sousou no Frieren - 28 [WebRip 1080p HEVC-10bit AAC][简繁内封字幕].mkv", 28, "episode"},
		{"【喵萌奶茶屋】★10月新番★[葬送的芙莉莲 / Sousou no Frieren][05][1080p][简日双语].mp4", 5, "episode"},
		{"[桜都字幕组] 某作品 第12话 [1080P][简体].mp4", 12, "episode"},
		{"Show.Name.S02E07.1080p.WEB-DL.mkv", 7, "episode"},
		{"[Team] Show [12v2][1080p].mkv", 12, "episode"},
		{"[Team] Show [01-12][1080p][BDRip].mkv", -1, "other"},
		{"[Team] Show - 12.5 [1080p].mkv", 12.5, "episode"},
		{"[Team] Show NCOP1 [1080p].mkv", -1, "op"},
		{"[Team] Show [2024][1080p].mkv", -1, "other"},
		{"Show 03.mp4", 3, "episode"},
		{"Mob Psycho 100 [BDRip].mkv", -1, "other"},
		{"[Team] Show - 01 [NCOP ver][1080p].mkv", 1, "episode"},
		{"[Team] Show - 2024 [1080p].mkv", -1, "other"},
		{"[Team] Show - NCED2 [1080p].mkv", -1, "ed"},
		{"[Team] Show [Menu01][1080p].mkv", -1, "extra"},
		{"[Team] Show [SP02][1080p].mkv", 2, "sp"},
		{"[Team] Special Agent Show [05][1080p].mkv", 5, "episode"},
		{"86.mkv", -1, "other"},
	}
	for _, c := range cases {
		p := ParseEpisode(c.in)
		got := -1.0
		if p.Episode != nil {
			got = *p.Episode
		}
		if got != c.ep || p.Kind != c.kind {
			t.Errorf("%q: got ep=%v kind=%s, want ep=%v kind=%s", c.in, got, p.Kind, c.ep, c.kind)
		}
	}
}

func TestParseTitleNoTrailing(t *testing.T) {
	if p := ParseTitle("Show 03"); p.Kind != "other" {
		t.Errorf("title trailing number must not parse: %+v", p)
	}
	if p := ParseTitle("[Sub] Show - 03 [1080p]"); p.Episode == nil || *p.Episode != 3 {
		t.Errorf("title dash: %+v", p)
	}
}
