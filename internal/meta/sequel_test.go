package meta

import "testing"

func TestSequelNumber(t *testing.T) {
	cases := map[string]float64{
		"Cyberpunk: Edgerunners 2": 2,
		"赛博朋克：边缘行者 2":              2,
		"サイバーパンク: エッジランナーズ２":       2,
		"Sword Art Online 3":       3,
		"Cyberpunk: Edgerunners":   0,
		"Kaiju No. 8":              0,
		"怪兽8号":                     0,
		"Mob Psycho 100":           0,
		"86 -エイティシックス-":            0,
		"Attack on Titan Part 2":   0,
		"Show Vol. 2":              0,
		"Area 88":                  0,
		"#2":                       0,
		"第2":                       0,
		"らんま1/2":                   0,
		"今日の5の2":                   0,
		"極主夫道 パート2":                0,
		"Lv2":                      0,
		"Overlord IV":              0, // roman numerals are nameHint's job
	}
	for in, want := range cases {
		if got := sequelNumber(in); got != want {
			t.Errorf("sequelNumber(%q) = %v, want %v", in, got, want)
		}
	}
	w := &Work{Title: "Cyberpunk: Edgerunners 2", TitleCN: "赛博朋克：边缘行者 2"}
	if got := workSeason(w); got != 2 {
		t.Fatalf("workSeason = %v, want 2", got)
	}
	if !titleSeasonFits("Cyberpunk.Edgerunners.S02", w) {
		t.Fatal("S02 torrent should fit 'Edgerunners 2'")
	}
}
