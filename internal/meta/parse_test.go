package meta

import (
	"sort"
	"strings"
	"testing"
)

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
		{"[Team] Show NCOP1 [1080p].mkv", 1, "op"},
		{"[Team] Show [2024][1080p].mkv", -1, "other"},
		{"Show 03.mp4", 3, "episode"},
		{"Mob Psycho 100 [BDRip].mkv", -1, "other"},
		{"[Team] Show - 01 [NCOP ver][1080p].mkv", 1, "episode"},
		{"[Team] Show - 2024 [1080p].mkv", -1, "other"},
		{"[Team] Show - NCED2 [1080p].mkv", 2, "ed"},
		{"[Team] Show [SP02][1080p].mkv", 2, "sp"},
		{"[Team] Special Agent Show [05][1080p].mkv", 5, "episode"},
		{"86.mkv", -1, "other"},
		// specials keep their number (unnumbered = 1)
		{"[Team] Show - SP01 [1080p].mkv", 1, "sp"},
		{"[Team] Show - OVA [1080p].mkv", 1, "sp"},
		{"[Team] Show NCOP1.mkv", 1, "op"},
		{"Show - NCED [BD].mkv", 1, "ed"},
		{"Show - 12.5 [1080p].mkv", 12.5, "episode"},
		{"Show - 00.mkv", 0, "episode"},
		{"SPs/Show - PV01.mkv", 1, "pv"},
		{"CM02.mkv", 2, "cm"},
		{"[Team] Show CM02 [1080p].mkv", 2, "cm"},
		{"[Team] Show - Trailer [1080p].mkv", 1, "pv"},
		{"[Team] Show [Menu01][1080p].mkv", -1, "extra"},
		{"[Team] SPY x FAMILY - 03 [1080p].mkv", 3, "episode"},
		{"ncop.mkv", 1, "op"},
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

func TestEpisodeKey(t *testing.T) {
	cases := []struct {
		in, canon, kind, label, short string
		num                           float64
	}{
		{"12", "12", KeyMain, "第 12 话", "12", 12},
		{"012", "12", KeyMain, "第 12 话", "12", 12},
		{"12.5", "12.5", KeyMain, "第 12.5 话", "12.5", 12.5},
		{"0", "0", KeyMain, "第 0 话", "0", 0},
		{"sp1", "sp1", KeySP, "SP1", "SP1", 1},
		{"SP01", "sp1", KeySP, "SP1", "SP1", 1},
		{"ncop", "ncop1", KeyNCOP, "NCOP1", "NCOP1", 1},
		{"ncop2", "ncop2", KeyNCOP, "NCOP2", "NCOP2", 2},
		{"nced1", "nced1", KeyNCED, "NCED1", "NCED1", 1},
		{"pv3", "pv3", KeyPV, "PV3", "PV3", 3},
		{"cm1", "cm1", KeyCM, "CM1", "CM1", 1},
	}
	for _, c := range cases {
		k, ok := ParseEpisodeKey(c.in)
		if !ok || k.String() != c.canon || k.Kind != c.kind || k.Number != c.num || k.Label() != c.label || k.Short() != c.short {
			t.Errorf("%q: ok=%v %+v %q %q", c.in, ok, k, k.String(), k.Label())
		}
		if back, ok := ParseEpisodeKey(k.String()); !ok || back != k {
			t.Errorf("%q: round trip %+v", c.in, back)
		}
	}
	for _, bad := range []string{"", "-1", "abc", "op1", "sp-1", "1e3", "12.", ".5", "5001", "sp1000", "12 ", "ncop1x", "１２"} {
		if k, ok := ParseEpisodeKey(bad); ok && bad != "12 " {
			t.Errorf("%q: accepted as %+v", bad, k)
		}
	}
	if k, ok := KeyOfFile("op", ptr(2)); !ok || k.String() != "ncop2" || k.FileKind() != "op" {
		t.Errorf("KeyOfFile op: %+v", k)
	}
	if _, ok := KeyOfFile("extra", ptr(1)); ok {
		t.Error("extra must not have a key")
	}
	ks := []EpisodeKey{{KeyCM, 1}, {KeyNCOP, 1}, {KeyMain, 12.5}, {KeySP, 2}, {KeyMain, 2}, {KeyNCED, 1}, {KeySP, 1}, {KeyPV, 1}, {KeyMain, 0}}
	sort.Slice(ks, func(i, j int) bool { return LessKey(ks[i], ks[j]) })
	var got []string
	for _, k := range ks {
		got = append(got, k.String())
	}
	if strings.Join(got, ",") != "0,2,12.5,sp1,sp2,ncop1,nced1,pv1,cm1" {
		t.Errorf("order: %v", got)
	}
}

func ptr(v float64) *float64 { return &v }

func TestJapaneseTextless(t *testing.T) {
	for in, want := range map[string]string{
		"[BD] Show ノンテロップOP.mkv":     "ncop1",
		"[BD] Show ノンクレジットED2.mkv":   "nced2",
		"[BD] Show ノンテロップ ED 01.mkv": "nced1",
	} {
		p := ParseEpisode(in)
		k, _ := KeyOfFile(p.Kind, p.Episode)
		if got := k.String(); got != want {
			t.Errorf("%s: %q want %q (%+v)", in, got, want, p)
		}
	}
}
