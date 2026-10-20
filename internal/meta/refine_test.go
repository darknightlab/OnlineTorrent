package meta

import (
	"context"
	"testing"
	"time"
)

// seedWork inserts a bgm work with main episodes sorts[i] airing airs[i] ("" = no date).
func seedWork(t *testing.T, s *Store, bgm int, title, cn, aliases, typ, begin, end string, sorts []float64, airs []string) string {
	t.Helper()
	key := "bgm:" + itoaT(bgm)
	if _, err := s.W.Exec(`INSERT INTO works(id,key,title,title_cn,aliases,type,begin_at,end_at,eps,updated_at) VALUES(?,?,?,?,?,?,?,?,?, 'x')`,
		bgm, key, title, cn, aliases, typ, begin, end, len(sorts)); err != nil {
		t.Fatal(err)
	}
	_, _ = s.W.Exec(`INSERT INTO work_ids VALUES('bgm', ?, ?)`, itoaT(bgm), key)
	for i, so := range sorts {
		a := ""
		if i < len(airs) {
			a = airs[i]
		}
		if _, err := s.W.Exec(`INSERT INTO episodes(bgm_id,work_key,type,sort,airdate) VALUES(?,?,0,?,?)`, bgm*1000+i, key, so, a); err != nil {
			t.Fatal(err)
		}
	}
	return key
}

func itoaT(v int) string {
	if v == 0 {
		return "0"
	}
	var b []byte
	for v > 0 {
		b = append([]byte{byte('0' + v%10)}, b...)
		v /= 10
	}
	return string(b)
}

func relate(t *testing.T, s *Store, subj, rel, typ int) {
	t.Helper()
	if _, err := s.W.Exec(`INSERT INTO subject_relations(subject_id,related_id,relation_type) VALUES(?,?,?)`, subj, rel, typ); err != nil {
		t.Fatal(err)
	}
}

func seq(from, to float64) []float64 {
	var out []float64
	for v := from; v <= to; v++ {
		out = append(out, v)
	}
	return out
}

// weekly airdates from a start day
func weekly(start string, n int) []string {
	d, _ := time.Parse("2006-01-02", start)
	out := make([]string, n)
	for i := range out {
		out[i] = d.AddDate(0, 0, 7*i).Format("2006-01-02")
	}
	return out
}

func TestNameEvidenceRules(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	// the " / " in a torrent title separates titles, it is not a path
	mushen := seedWork(t, s, 712000, "牧神记 伍", "牧神记5", `["牧神记5"]`, "web", "2026-10-17", "", seq(105, 107), nil)
	if !s.NameEvidence(ctx, mushen, []string{"[黒ネズミたち] 牧神记 / Tales of Herding Gods - 105 (B-Global Donghua 1920x1080 HEVC AAC MKV)", "[Dynamis One] Tales of Herding Gods - 105.mkv"}) {
		t.Error("牧神记 before ' / '")
	}
	if s.NameEvidence(ctx, mushen, []string{"[黒ネズミたち] 某作品 / Tales of Herding Gods - 105"}) {
		t.Error("unrelated title named 牧神记")
	}
	// two-character names: only as a whole bracketed part
	zhetian := seedWork(t, s, 637591, "遮天 第四季", "遮天 第四季", `["遮天 年番4"]`, "web", "2026-04-07", "", seq(157, 160), nil)
	if !s.NameEvidence(ctx, zhetian, []string{"[GM-Team][国漫][遮天][Shrouding the Heavens][2023][186][AVC][GB][1080P]"}) {
		t.Error("[遮天]")
	}
	for _, x := range []string{"[GM-Team][国漫][遮天下][186]", "[GM-Team] 遮天 186 [1080P]", "[GM-Team][国漫][遮天大圣][186]"} {
		if s.NameEvidence(ctx, zhetian, []string{x}) {
			t.Errorf("short name matched %q", x)
		}
	}
	// franchise names: a direct prequel's names count, other relations don't
	p := seedWork(t, s, 517027, "夜桜さんちの大作戦(第2期)", "夜樱家的大作战 第二季", `["Yozakura-san Chi no Daisakusen Season 2"]`, "tv", "2026-04-12", "", seq(28, 39), nil)
	w := seedWork(t, s, 664389, "夜桜さんちの大作戦(第2期)第2クール", "夜樱家的大作战 第二季 第2部分", `["夜櫻家大作戰 第二季 第2部分"]`, "tv", "2026-10-11", "", seq(40, 51), nil)
	_ = p
	relate(t, s, 664389, 517027, 2)
	if !s.NameEvidence(ctx, w, []string{"[Doomdos] - Yozakura-san Chi no Daisakusen - S03E41 [1080p LFTLNET WEB-DL]"}) {
		t.Error("prequel name")
	}
	side := seedWork(t, s, 900001, "外伝", "外传", `["Gaiden Side Story Zero"]`, "ova", "2026-01-01", "", seq(1, 2), nil)
	_ = side
	relate(t, s, 664389, 900001, 6) // 番外篇: not a direct prequel / sequel
	if s.NameEvidence(ctx, w, []string{"[G] Gaiden Side Story Zero - 41"}) {
		t.Error("side story name counted")
	}
	// an English name derived from related works "<title> <subtitle>" / "<name>: <subtitle>"
	conan := seedWork(t, s, 899, "名探偵コナン", "名侦探柯南", `["名侦探柯南"]`, "tv", "1996-01-07", "", seq(1214, 1216), nil)
	seedWork(t, s, 2968, "名探偵コナン 時計じかけの摩天楼", "名侦探柯南 引爆摩天楼", `["Detective Conan: The Time-Bombed Skyscraper"]`, "movie", "1997-04-18", "", seq(1, 1), nil)
	relate(t, s, 899, 2968, 6)
	if s.NameEvidence(ctx, conan, []string{"[APTX-Fansub] Detective Conan - 1216 FHD [C008AEA4].mp4"}) {
		t.Error("derived from a single related work")
	}
	seedWork(t, s, 2969, "名探偵コナン 14番目の標的", "名侦探柯南 第14号目标", `["Detective Conan: The Fourteenth Target"]`, "movie", "1998-04-17", "", seq(1, 1), nil)
	relate(t, s, 899, 2969, 6)
	s.resetCandidateIndex()
	if !s.NameEvidence(ctx, conan, []string{"[APTX-Fansub] Detective Conan - 1216 FHD [C008AEA4].mp4"}) {
		t.Errorf("derived name: %v", s.WorkNames(ctx, conan))
	}
	// related works whose title doesn't start with the work's title give nothing
	seedWork(t, s, 2970, "ルパン三世VS名探偵コナン", "", `["Lupin III vs. Detective Conan: The Movie", "Lupin III vs. Detective Conan: Special"]`, "movie", "2013-12-06", "", seq(1, 1), nil)
	relate(t, s, 899, 2970, 7)
	for _, n := range s.WorkNames(ctx, conan) {
		if n == "Lupin III vs. Detective Conan" {
			t.Error("derived from a crossover title")
		}
	}
}

func TestCheckDateNotAiredYet(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	// 归还者 S2: 1–12 weekly from 2026-10-07, 13 and 14 listed without a date
	w := seedWork(t, s, 471844, "帰還者の魔法は特別です(第二期)", "归还者的魔法要特别 第二季", `[]`, "tv", "2026-10-07", "",
		seq(1, 14), weekly("2026-10-07", 12))
	at := func(d string) time.Time { v, _ := time.Parse("2006-01-02", d); return v }
	if c := s.CheckDateFull(ctx, w, EpisodeKey{Kind: KeyMain, Number: 14}, at("2026-10-19"), "published"); c.OK || c.Late {
		t.Errorf("ep 14 before ep 3 aired: %+v", c)
	}
	if c := s.CheckDateFull(ctx, w, EpisodeKey{Kind: KeyMain, Number: 14}, at("2027-01-05"), "published"); !c.OK {
		t.Errorf("ep 14 after ep 12 aired: %+v", c)
	}
	if c := s.CheckDateFull(ctx, w, EpisodeKey{Kind: KeyMain, Number: 2}, at("2026-10-19"), "published"); !c.OK {
		t.Errorf("ep 2: %+v", c)
	}
	// late vs early on a dated episode
	if c := s.CheckDateFull(ctx, w, EpisodeKey{Kind: KeyMain, Number: 1}, at("2027-06-01"), "published"); c.OK || !c.Late {
		t.Errorf("late: %+v", c)
	}
	if c := s.CheckDateFull(ctx, w, EpisodeKey{Kind: KeyMain, Number: 5}, at("2026-10-19"), "published"); c.OK || c.Late {
		t.Errorf("early: %+v", c)
	}
}

func TestMakuSeason(t *testing.T) {
	for p, want := range map[string]float64{
		"[WZF]Tokyo_Majin_Gakuen_Kenpuchou_Tou~Dai_Ni_Maku~_-_Capitulo_01[DVDRip][X264-AAC][848x480][Sub_Esp][621D5434].mp4": 2,
		"Tokyo Majin Dai San Maku - 03.mkv":                      3,
		"東京魔人學園剣風帖 龖 第弐幕 - 01.mkv":                               2,
		"[WZF]Tokyo_Majin_Gakuen_Kenpuchou_Tou-_Capitulo_14.mp4": 0,
		"Daisuki Makoto - 02.mkv":                                0,
		"Dai Ni Makuhari - 02.mkv":                               0,
	} {
		if got := StatedSeason(p, 0); got != want {
			t.Errorf("%s: %v want %v", p, got, want)
		}
	}
	if v := workSeason(&Work{Title: "東京魔人學園剣風帖 龖 第弐幕", TitleCN: "东京魔人学园剑风帖 龙龙 第二幕"}); v != 2 {
		t.Errorf("work season %v", v)
	}
	if v := workSeason(&Work{Title: "東京魔人學園剣風帖 龖", TitleCN: "东京魔人学园剑风帖 龙龙"}); v != 0 {
		t.Errorf("first act %v", v)
	}
}

func TestUniqueFitAndYears(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	s1 := seedWork(t, s, 100, "スパイ", "间谍", `["Spy x Family"]`, "tv", "2022-04-09", "2022-12-24", seq(1, 25), nil)
	s3 := seedWork(t, s, 300, "スパイ Season 3", "间谍 第三季", `["Spy x Family Season 3"]`, "tv", "2025-10-04", "2025-12-27", seq(38, 50), nil)
	relate(t, s, 300, 100, 2)
	texts := []string{"[G] Spy x Family S03 01-13 BDrip", "Spy x Family 2025 S03E01.mkv"}
	if !s.UniqueFit(ctx, s3, []string{s1}, seq(1, 13), 3, texts) {
		t.Error("season 3 stated: unique")
	}
	// no season stated: season 1 (1–25, named too) fits as well
	if s.UniqueFit(ctx, s3, []string{s1}, seq(1, 13), 0, []string{"[G] Spy x Family 01-13 BDrip"}) {
		t.Error("ambiguous without a season")
	}
	// a number the work doesn't list
	if s.UniqueFit(ctx, s3, nil, seq(1, 14), 3, texts) {
		t.Error("14 not listed")
	}
	if !s.YearsFit(ctx, s3, texts) || s.YearsFit(ctx, s3, []string{"Spy x Family 2023 S03E01.mkv"}) || !s.YearsFit(ctx, s3, []string{"no year"}) {
		t.Error("years")
	}
	if k, ok := s.DirectPrequel(ctx, s3); !ok || k != s1 {
		t.Errorf("prequel %q %v", k, ok)
	}
	if _, ok := s.DirectPrequel(ctx, s1); ok {
		t.Error("season 1 has no prequel")
	}
}
