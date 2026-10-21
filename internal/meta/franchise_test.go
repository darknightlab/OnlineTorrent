package meta

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

// matchOne runs Match on a torrent (crawled, published at pub) and returns the work key and
// episode key of each file ("" = not mapped).
func matchOne(t *testing.T, s *Store, ih, title, pub string, paths ...string) []string {
	t.Helper()
	ctx := context.Background()
	var files []FileInput
	for i, p := range paths {
		files = append(files, FileInput{Index: i, Path: p, Size: 1})
	}
	if err := s.Match(ctx, TorrentInput{Infohash: ih, Title: title, Source: "Nyaa", SourceKind: "rss", PublishedAt: pub, Files: files}); err != nil {
		t.Fatal(err)
	}
	tm, err := s.GetTorrentMap(ctx, ih)
	if err != nil {
		t.Fatal(err)
	}
	out := make([]string, len(paths))
	for _, f := range tm.Files {
		if f.Work != nil {
			out[f.Index] = f.Work.Key + " " + f.EpisodeKey
		}
	}
	return out
}

func TestFranchiseSeasons(t *testing.T) {
	s := openTest(t)
	seedWork(t, s, 1014, "とある魔術の禁書目録", "魔法禁书目录", `["Toaru Majutsu no Index"]`, "tv", "2008-10-04", "2009-03-19", seq(1, 24), nil)
	seedWork(t, s, 7843, "とある魔術の禁書目録Ⅱ", "魔法禁书目录 第二季", `["Toaru Majutsu no Index II"]`, "tv", "2010-10-08", "2011-04-01", seq(1, 24), nil)
	seedWork(t, s, 226540, "とある魔術の禁書目録Ⅲ", "魔法禁书目录 第三季", `["Toaru Majutsu no Index III", "Toaru Majutsu no Index"]`, "tv", "2018-10-05", "2019-04-05", seq(1, 26), nil)
	relate(t, s, 1014, 7843, 3)
	relate(t, s, 7843, 226540, 3)
	s.resetCandidateIndex()
	ep := func(title string) string { return "[G] " + title + " [1080p].mkv" }
	cases := []struct {
		title, pub, want string
	}{
		{"Toaru Majutsu no Index III - 05", "2018-11-05T00:00:00Z", "bgm:226540 5"},
		{"Toaru Majutsu no Index S3 - 05", "2018-11-05T00:00:00Z", "bgm:226540 5"},
		{"Toaru Majutsu no Index 3rd Season - 05", "2018-11-05T00:00:00Z", "bgm:226540 5"},
		{"Toaru Majutsu no Index Dai San Ki - 05", "2018-11-05T00:00:00Z", "bgm:226540 5"},
		{"Toaru Majutsu no Index - 05", "2008-11-05T00:00:00Z", "bgm:1014 5"}, // no marker: season 1
		{"Toaru Majutsu no Index - 30", "2009-05-01T00:00:00Z", ""},           // not in S1's list
		{"Toaru Majutsu no Index - 05", "2007-01-01T00:00:00Z", ""},           // before S1 began
		{"Toaru Majutsu no Index S4 - 05", "2018-11-05T00:00:00Z", ""},        // no 4th season
	}
	for i, c := range cases {
		got := matchOne(t, s, fmt.Sprintf("i%02d", i), "[G] "+c.title+" [1080p]", c.pub, ep(c.title))
		if got[0] != c.want {
			t.Errorf("%s (%s): %q, want %q", c.title, c.pub[:10], got[0], c.want)
		}
	}
}

// airing order, not bgm's story order: a later-made prequel is released as "S2"
func TestFranchiseAiringOrder(t *testing.T) {
	s := openTest(t)
	seedWork(t, s, 500, "ショーサーガ", "秀传", `["Show Saga"]`, "tv", "2015-04-05", "2015-06-21", seq(1, 12), nil)
	seedWork(t, s, 501, "ショーサーガ 黎明篇", "秀传 黎明篇", `["Show Saga"]`, "tv", "2018-04-08", "2018-06-24", seq(1, 12), nil)
	relate(t, s, 500, 501, 2) // 501 is the story prequel of 500
	s.resetCandidateIndex()
	if got := matchOne(t, s, "a1", "[G] Show Saga S2 - 03 [1080p]", "2018-04-30T00:00:00Z", "[G] Show Saga S2 - 03 [1080p].mkv"); got[0] != "bgm:501 3" {
		t.Errorf("S2 = second aired: %q", got[0])
	}
	if got := matchOne(t, s, "a2", "[G] Show Saga - 03 [1080p]", "2015-04-25T00:00:00Z", "[G] Show Saga - 03 [1080p].mkv"); got[0] != "bgm:500 3" {
		t.Errorf("no marker = first aired: %q", got[0])
	}
	if got := matchOne(t, s, "a3", "[G] Show Saga S3 - 03 [1080p]", "2018-04-30T00:00:00Z", "[G] Show Saga S3 - 03 [1080p].mkv"); got[0] != "" {
		t.Errorf("S3 doesn't exist: %q", got[0])
	}
	// the second-aired entry released as S2 before it began: not certain
	if got := matchOne(t, s, "a4", "[G] Show Saga S2 - 03 [1080p]", "2017-01-01T00:00:00Z", "[G] Show Saga S2 - 03 [1080p].mkv"); got[0] != "" {
		t.Errorf("S2 before airing: %q", got[0])
	}
	// two entries starting the same day: the position is ambiguous
	seedWork(t, s, 502, "ショーサーガ 外伝", "秀传 外传", `["Show Saga"]`, "tv", "2018-04-08", "2018-06-24", seq(1, 12), nil)
	s.aliasMu.Lock()
	s.aliases = nil // works added behind the catalog's back
	s.aliasMu.Unlock()
	if got := matchOne(t, s, "a5", "[G] Show Saga S2 - 04 [1080p]", "2018-05-07T00:00:00Z", "[G] Show Saga S2 - 04 [1080p].mkv"); got[0] != "" {
		t.Errorf("tied start dates: %q", got[0])
	}
}

// the rule path's name index covers all eras: an old show by its romaji alias
func TestAliasIndexAllWorks(t *testing.T) {
	s := openTest(t)
	seedWork(t, s, 10440, "あの日見た花の名前を僕達はまだ知らない。", "我们仍未知道那天所看见的花的名字。", `["Ano Hi Mita Hana no Namae o Bokutachi wa Mada Shiranai"]`, "tv", "2011-04-14", "2011-06-23", seq(1, 11), nil)
	got := matchOne(t, s, "ah", "[G] Ano Hi Mita Hana no Namae o Bokutachi wa Mada Shiranai [BD 1080p]", "2026-10-20T00:00:00Z",
		"AH/[G] Ano Hi Mita Hana no Namae o Bokutachi wa Mada Shiranai - 01 [BD 1080p].mkv", "AH/[G] Ano Hi Mita Hana no Namae o Bokutachi wa Mada Shiranai - 02 [BD 1080p].mkv")
	if got[0] != "bgm:10440 1" || got[1] != "bgm:10440 2" {
		t.Errorf("old show: %v", got)
	}
	// the alias guess (substring) reaches old works too, as a hint only
	w, method, _, certain := s.resolveWork(context.Background(), TorrentInput{Title: "[G] Ano Hi Mita Hana no Namae o Bokutachi wa Mada Shiranai Movie 01"})
	if w == nil || w.Key != "bgm:10440" || certain || !strings.HasPrefix(method, "alias") {
		t.Errorf("alias guess: %v %s %v", w, method, certain)
	}
	idx, _ := s.aliasIndex(context.Background())
	if ws := idx.longest("[g] ano hi mita hana no namae o bokutachi wa mada shiranai - 01"); len(ws) != 1 || ws[0].key != "bgm:10440" {
		t.Errorf("longest: %+v", ws)
	}
}

// Shapes found in the replays that must not become certain franchise mappings.
func TestFranchiseNegatives(t *testing.T) {
	if franchiseBase("JoJo no Kimyou na Bouken (2012)") == franchiseBase("JoJo no Kimyou na Bouken") {
		t.Error("a (year) qualifier was dropped")
	}
	if franchiseBase("Toaru Majutsu no Index III") != franchiseBase("Toaru Majutsu no Index S3") {
		t.Error("season markers kept")
	}
	s := openTest(t)
	// a single entry whose Ⅱ belongs to its name ("actⅡ"), titled "… Act II Season 2": no
	// franchise to resolve
	seedWork(t, s, 267615, "ダイヤのA actII", "钻石王牌 act2", `["Ace of Diamond actⅡ"]`, "tv", "2019-04-02", "2020-03-31", seq(1, 52), nil)
	// two different series named by two fragments of one title (2014 "Keroro", 2026
	// "KERORO 軍曹☆"): not one franchise
	seedWork(t, s, 90005, "ケロロ", "Keroro", `["Keroro"]`, "tv", "2014-03-21", "2014-09-26", seq(1, 26), nil)
	seedWork(t, s, 617273, "ケロロ軍曹☆", "Keroro军曹☆", `["KERORO 軍曹☆"]`, "tv", "2026-10-03", "", seq(1, 12), nil)
	// a shared name plus a subtitle naming a part that isn't a candidate
	seedWork(t, s, 43558, "ジョジョの奇妙な冒険", "JOJO的奇妙冒险", `["JoJo's Bizarre Adventure", "JoJo no Kimyou na Bouken (2012)"]`, "tv", "2012-10-06", "2013-04-06", seq(1, 26), nil)
	seedWork(t, s, 150490, "ジョジョの奇妙な冒険 ダイヤモンドは砕けない", "JOJO的奇妙冒险 不灭钻石", `["JoJo's Bizarre Adventure"]`, "tv", "2016-04-02", "2016-12-24", seq(1, 39), nil)
	s.resetCandidateIndex()
	for _, c := range []struct{ title, pub string }{
		{"【悠哈璃羽字幕社】[JOJO的奇妙冒险 飙马野郎/JoJo's Bizarre Adventure: Steel Ball Run][05][1080p HEVC]", "2026-10-20T00:00:00Z"},
		{"[Doomdos] - Diamond no Ace ActSecond Season Part 2 - S02E14 [1080p] [Ace of Diamond Act II Season 2 Part 2]", "2026-10-20T00:00:00Z"},
		{"[ANi] Keroro /  KERORO 軍曹☆ - 03 [1080P]", "2026-10-17T00:00:00Z"},
	} {
		w, _, _, certain := s.resolveWork(context.Background(), TorrentInput{Title: c.title, Files: []FileInput{{Path: c.title + ".mkv"}}})
		if certain && (w.Key == "bgm:267615" || w.Key == "bgm:90005" || w.Key == "bgm:43558") {
			t.Errorf("%s → certain %s", c.title, w.Key)
		}
	}
}

// A file whose own name names another work is not mapped by rule; files naming the work
// by any of its names, or no work at all, are.
func TestFileNamesOtherWork(t *testing.T) {
	s := openTest(t)
	seedWork(t, s, 555710, "时光代理人Ⅲ Part One", "时光代理人 第三季 Part 1", `["时光代理人 第三季 Part 1", "Link Click Season 3"]`, "web", "2026-08-13", "", seq(1, 12), nil)
	seedWork(t, s, 324295, "时光代理人", "时光代理人", `["Link Click"]`, "web", "2021-04-30", "2021-07-09", seq(1, 11), nil)
	seedWork(t, s, 361196, "时光代理人 第二季", "时光代理人 第二季", `["Link Click Season 2"]`, "web", "2023-07-14", "2023-09-22", seq(1, 12), nil)
	seedWork(t, s, 507530, "灵境行者", "灵境行者", `["Spirit Realm Walker"]`, "web", "2026-03-01", "", seq(1, 16), nil)
	const dir = "[GM-Team][时光代理人 第3季][Link Click S03][08-11][AVC][4K SDR+][NoLogo]/"
	got := matchOne(t, s, "lc", "[GM-Team][时光代理人 第3季][Link Click S03][08-11][AVC][4K SDR+][NoLogo]", "2026-10-20T00:00:00Z",
		dir+"[GM-Team][国漫][灵境行者][Spirit Realm Walker][2026][08][HEVC][GB][4K].mp4",
		dir+"[GM-Team][国漫][灵境行者][Spirit Realm Walker][2026][09][HEVC][GB][4K].mp4",
		dir+"[GM-Team][国漫][时光代理人 第3季][Link Click S03][2026][10][AVC][4K].mp4",
		dir+"[GM-Team][11][AVC][4K].mp4")
	if got[0] != "" || got[1] != "" {
		t.Errorf("files naming 灵境行者 mapped: %v", got)
	}
	if got[2] != "bgm:555710 10" || got[3] != "bgm:555710 11" {
		t.Errorf("real files: %v", got)
	}
	// a pack whose file names use another alias / romaji of the same work
	seedWork(t, s, 10440, "あの日見た花の名前を僕達はまだ知らない。", "我们仍未知道那天所看见的花的名字。", `["Ano Hi Mita Hana no Namae o Bokutachi wa Mada Shiranai", "AnoHana"]`, "tv", "2011-04-14", "2011-06-23", seq(1, 11), nil)
	s.aliasMu.Lock()
	s.aliases = nil
	s.aliasMu.Unlock()
	got = matchOne(t, s, "ah", "[G] 我们仍未知道那天所看见的花的名字。 [BD 1080p]", "2026-10-20T00:00:00Z",
		"我们仍未知道那天所看见的花的名字/[G] AnoHana - 01 [BD 1080p].mkv",
		"我们仍未知道那天所看见的花的名字/[G] Ano Hi Mita Hana no Namae o Bokutachi wa Mada Shiranai - 02 [BD 1080p].mkv")
	if got[0] != "bgm:10440 1" || got[1] != "bgm:10440 2" {
		t.Errorf("same work under other names: %v", got)
	}
}
