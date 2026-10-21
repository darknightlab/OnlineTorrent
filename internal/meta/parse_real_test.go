package meta

import (
	"fmt"
	"testing"
)

func keyStr(p ParsedName) string {
	if p.Episode == nil {
		return p.Kind + "/-"
	}
	return fmt.Sprintf("%s/%v", p.Kind, *p.Episode)
}

// Real file names from the dev mapping DB (torrent_files.path, base names) plus common
// naming patterns. want is "kind/episode" ("sp/-" = special without a number).
func TestParseEpisodeRealNames(t *testing.T) {
	cases := []struct{ in, want string }{
		// fansub " - NN" with metadata brackets
		{"[ANi] 至高之力 - 03 [1080P][Baha][WEB-DL][AAC AVC][CHT].mp4", "episode/3"},
		{"[Dynamis One] Onegai AiPri - 28 (ABEMA 1920x1080 AVC AAC MKV) [BE1C4491].mkv", "episode/28"},
		{"[Dynamis One] Jukishi - 16 (B-Global 1920x1080 HEVC AAC MKV) [57CA7BB4].mkv", "episode/16"},
		{"[ANi] 我家的弟弟們真是讓您費心了 - 12.5 [1080P][Baha][WEB-DL][AAC AVC][CHT].mp4", "episode/12.5"},
		{"[Dynamis One] Detective Conan - 1217 (CR 1920x1080 AVC AAC MKV) [6F5CCFFF].mkv", "episode/1217"},
		{"[Dynamis One] Black Clover 2nd Season - 173 (Baha 1920x1080 AVC AAC MP4) [2E4C4606].mp4", "episode/173"},
		{"[Dynamis One] Tenipuri: U-17 Kesshou Member Ketteisen - 03 (ABEMA 1920x1080 AVC AAC MKV) [5E1ED797].mkv", "episode/3"},
		{"[Dynamis One] #Zombie Sagashitemasu - 03 (CR 1920x1080 AVC AAC MKV) [10EB646B].mkv", "episode/3"},
		{"[Dynamis One] Ojisan wa Kawaii Mono ga Osuki. - 02 (CR 1920x1080 AVC AAC MKV) [F1F684E2].mkv", "episode/2"},
		{"[Dynamis One] Magical★Explorer - 03 (ABEMA 1280x720 AVC AAC MKV) [2DED463F].mkv", "episode/3"},
		{"[jibaketa]Meitantei Precure! - 29 (WEB 1920x1080 AVC AAC YUE).mp4", "episode/29"},
		{"[jibaketa]Kamen Rider My-Th - 06 (WEB 1920x1080 AVC AACx2 SRT+PGS ViuTV CHT).mkv", "episode/6"},
		{"[Tsukigakirei] Kanojo no Tomodachi - 01v3 [WebRip 1080P AVC-8bit AAC][CHS&JPN][Uncencored].mp4", "episode/1"},
		{"[Tsukigakirei] Ao no Hako - 27 [WebRip 1080P AVC-8bit AAC][CHT&JPN].mp4", "episode/27"},
		{"[Prejudice-Studio] 青之箱 Ao no Hako - 01 [Bilibili WEB-DL 1080P AVC 8bit AAC MP4][简日内嵌].mp4", "episode/1"},
		{"[星塔汉化组] 冷淡的佐藤同学只对我撒娇 Shiotaiou no Satou-san ga Ore ni dake Amai - 02 [WebRip 1080P AVC 8Bit AAC MP4][简日内嵌].mp4", "episode/2"},
		{"[樱桃花字幕组]FX战士久留美 FX Senshi Kurumi-chan - 3 [1080p][简日内嵌].mp4", "episode/3"},
		{"[Ñ] Boruto - Naruto Next Generations - 086 [BD 1080p] [794B5C2B].mkv", "episode/86"},
		{"[Doomdos] - 초능력 순경! 쵸죠 선배 - 02 [1080P LFTLNET WEB-DL].mkv", "episode/2"},
		{"[Doomdos] - 야생의 라스트 보스가 나타났다! 2기 - 16 [1080P LFTLNET WEB-DL].mkv", "episode/16"},
		{"[Doomdos] - (자막) 블랙 클로버 New Chapter - 03 [1080P LFTLNET WEB-DL].mkv", "episode/3"},
		{"[KiyoshiiSubs] Black Clover S2 - 03 [1080p][H.265 - 10Bit].mkv", "episode/3"},
		{"Tensei Shitara Ken Deshita 2 - 01 (WEBRip 1920x1080 x264 AAC Rus + Jap).mkv", "episode/1"},
		{"[smzase] Tensei Shitara Ken Deshita - S02E01 - [CHT_JPN][WebRip H264 8bit 1080P AAC][V2].mp4", "episode/1"},
		{"[EdensFansub] Blue Archive the Animation - 01 (BD 1080p) [DB262706].mkv", "episode/1"},
		{"[Skymoon-Raws] Yozakurasan Chi no Daisakusen - 41 [ViuTV][WEB-DL][CHT][SRT][1080p][AVC AAC].mkv", "episode/41"},
		{"[NKWT] Shuiro no Kamen - 01 [1080p].mkv", "episode/1"},
		// common international fansub/scene patterns
		{"[HorribleSubs] Boku no Hero Academia - 88 [1080p].mkv", "episode/88"},
		{"[SubsPlease] Show - 12v2 (1080p) [ABCD1234].mkv", "episode/12"},
		{"[Erai-raws] Show - 01 [1080p][Multiple Subtitle][ABCD1234].mkv", "episode/1"},
		{"[Judas] Show - S01E05 [1080p][HEVC x265 10bit][Multi-Subs].mkv", "episode/5"},
		{"Show - 01 (BD 1080p Hi10 FLAC) [Dual Audio].mkv", "episode/1"},
		{"[Sub] Show - 05 (176) [1080p].mkv", "episode/5"},
		{"[Sub] Show - 05 END [1080p].mkv", "episode/5"},
		{"[Sub] Show 2 [05][1080p].mkv", "episode/5"},
		{"Show 2 05.mkv", "episode/5"},
		{"[Sub] Show 01-02 [1080p].mkv", "other/-"},
		{"[Sub] Show EP01-02 [1080p].mkv", "other/-"},
		{"Show 第01集.mp4", "episode/1"},
		{"[Sub] Show 第二季 - 05 [1080p].mkv", "episode/5"},
		{"[Sub] Show the Movie [BD 1080p].mkv", "other/-"},
		// isolated bracket numbers
		{"[Sakurato] FX Senshi Kurumi-chan [02][HEVC-10bit 1080P AAC][CHS&CHT].mkv", "episode/2"},
		{"[Sakurato] Chitose-kun wa Ramune Bin no Naka (2026) [01][HEVC-10bit 1080P AAC][CHS&CHT].mkv", "episode/1"},
		{"[KTXP][Toaru_ITEM][02][GB_CN][AV1_opus][1080p].mkv", "episode/2"},
		{"[Studio GreenTea] FX Senshi Kurumi-chan [01v2][WebRip][HEVC-10bit 1080p AAC ASSx2].mkv", "episode/1"},
		{"[UHA-WINGS][Seitokai ni mo Ana wa Aru][02][1080p HEVC][CHS&CHT].mkv", "episode/2"},
		{"[orion origin] Tempal：Item no Chikara [01] [1080p] [H265 AAC] [CHT_JPN].mp4", "episode/1"},
		{"[orion origin] Ao Ashi S2 [02] [1080p] [H265 AAC] [CHT_JPN].mp4", "episode/2"},
		{"[hyakuhuyu][BanG Dream Yumemita][13END][1080p AVC AAC][CHT].mp4", "episode/13"},
		{"[GM-Team][国漫][光阴之外][Beyond Time's Gaze][2025][41][GB][4K HEVC 10Bit].mp4", "episode/41"},
		{"[GM-Team][国漫][斗破苍穹 第5季][Fights Break Sphere Ⅴ][2022][212][HEVC][GB][4K].mp4", "episode/212"},
		{"[GM-Team][国漫][一念永恒 第4季][Yi Nian Yong Heng 4th Season][2026][12][AVC][GB][1080P].mp4", "episode/12"},
		{"[Nekomoe kissaten][Ranma ½ 2024][27][1080p][JPTC].mp4", "episode/27"},
		{"[Nekomoe kissaten][Sasaki to Pii-chan S2][01][1080p][JPTC].mp4", "episode/1"},
		{"[咪路fans制作组]蜡笔小新 Crayonshinchan [227][1997.05.02][1080P][简日双语][数码修复版][MP4].mp4", "episode/227"},
		{"[DBFC][DBSuperBeerus][001][jp_cn][webrip][x264_aac][1080P].mp4", "episode/1"},
		{"[SBSUB][CONAN][1217][WEBRIP][1080P][HEVC_AAC][CHS_CHT_JP][PGS](EF97B07B).mkv", "episode/1217"},
		{"[FLsnow.feat.PO][Onegai_Aipri][1080P][29].mkv", "episode/29"},
		{"[FLsnow][Star-Detective_Precure][38][TVer_1080p].mkv", "episode/38"},
		{"[TSDM]FX Senshi Kurumi Chan[01][WebRip][1080P][HEVC 10bit][AAC][CHS_JP].mp4", "episode/1"},
		{"[MingY] Romelia Senki [02][WebRip][JPCN].mkv", "episode/2"},
		{"【喵萌奶茶屋】★10月新番★[葬送的芙莉莲 / Sousou no Frieren][05][1080p][简日双语].mp4", "episode/5"},
		// two numbers: season-relative episode + absolute number
		{"[BeanSub][Kusuriya no Hitorigoto S3][03_51][CHT][1080P][x264_AAC].mp4", "episode/3"},
		{"[Team] Show S2 - 03 (27) [1080p].mkv", "episode/3"},
		// SxxExx and scene names
		{"[Feibanyama] The Ramparts of Ice S02E01 [NF WebRip 1080p H265 Veryslow AAC Multi-Audio Multi-Subs].mkv", "episode/1"},
		{"[Feibanyama] FX Fighter Kurumi-chan S01E01 [CR WebRip AI2160p H265 Veryslow AAC Dual-Audio Multi-Subs].mkv", "episode/1"},
		{"Tatsuki Fujimoto 17-26 2025 S01E01-[1080p][BDRIP][x265.OPUS].mkv", "episode/1"},
		{"Aoashi S02E03 SUBFRENCH 1080p CR WEB-DL AAC2.0 x264-Tsundere-Raws.mkv", "episode/3"},
		{"Blue Box S02E03 MULTi 1080p NF WEB-DL DDP5.1 H.265-Tsundere-Raws.mkv", "episode/3"},
		{"Mission Yozakura Family S02E14 SUBFRENCH 720p DSNP WEB-DL AAC2.0 H.264-Tsundere-Raws.mkv", "episode/14"},
		{"Ranma.1-2.2024.S03E03.Happosai.Days.Are.Here.Again.1080p.NF.WEB-DL.DUAL.AAC2.0.H.264-VARYG.mkv", "episode/3"},
		{"I'm.Looking.for.a.Zombie.S01E03.Im.Looking.For.a.Photo.Shop.1080p.CR.WEB-DL.DUAL.AAC2.0.H.264.MSubs-ToonsHub.mkv", "episode/3"},
		{"Ranma 1 2 2024 S03E03 MULTi 1080p NF WEB-DL AAC2.0 x264-Tsundere-Raws.mkv", "episode/3"},
		{"Pokemon.Diamond.and.Pearl.Battle.Dimension.S11E52.FiNAL.TRUEFRENCH.1080p.WEB-DL.H265-T3KASHi.mkv", "episode/52"},
		{"[Kylian] I Want to Love You Till Your Dying Day S01E09 VOSTFR WEB 1080p H264 AAC.mkv", "episode/9"},
		{"Show.Name.S02E07.1080p.WEB-DL.mkv", "episode/7"},
		// CJK markers
		{"[桜都字幕组] 某作品 第12话 [1080P][简体].mp4", "episode/12"},
		{"[Sub] 某作品 第03話「タイトル」[1080p].mkv", "episode/3"},
		{"【10月】学生会也有洞！ 02【TV放送版】.mp4", "episode/2"},
		{"【10月】与暗黑召唤师秘密恋爱中 01【完全无修版】.mp4", "episode/1"},
		{"【2月】名侦探光之美少女！ 38.mp4", "episode/38"},
		{"[NEO·QSW]ブラッククローバー 2nd Season ページ173 黒き守護者[WEBRIP HEVC 1080P].mp4", "episode/173"},
		// bare numbers after metadata
		{"[Pokefans] Pocket Monsters (2023) 153 [RUS SUB, 1080p].mkv", "episode/153"},
		{"You're Under Arrest. TV S1 (1996) 05; DVD_480P.mp4", "episode/5"},
		{"Show/01.mkv", "episode/1"},
		{"01 - The Beginning.mkv", "episode/1"},
		// title numbers are not episodes
		{"[Sub] Mob Psycho 100 - 03 [1080p].mkv", "episode/3"},
		{"[Sub] Mob Psycho 100 III - 03 [1080p].mkv", "episode/3"},
		{"[Sub] 86 - 05 [1080p].mkv", "episode/5"},
		{"[Sub] 86 Eighty Six - 05 [1080p].mkv", "episode/5"},
		{"[Sub] Kaiju No. 8 - 03 [1080p].mkv", "episode/3"},
		{"[Sub] Kaiju No. 8 [1080p].mkv", "other/-"},
		{"[Sub] Tenipuri U-17 [1080p].mkv", "other/-"},
		{"[Sub] Steins;Gate 0 [BD].mkv", "other/-"},
		{"[Sub] Show Season 2 - 05 [1080p].mkv", "episode/5"},
		{"[Sub] Show 2nd Season [05][1080p].mkv", "episode/5"},
		{"[Sub] Show Vol.2 [BD].mkv", "other/-"},
		{"[Sub] Show Movie 2 [BD 1080p].mkv", "other/-"},
		// batches / ranges / years / dates
		{"[Sub] Show [01-12][1080p][BDRip].mkv", "other/-"},
		{"[Sub] Show [01 - 12][1080p].mkv", "other/-"},
		{"[Sub] Show (2024) [1080p].mkv", "other/-"},
		{"Future.Boy.Conan.Disc4.EP21-EP26.Remux.GBR.SDR.UHD.Blu-ray.2160P.HEVC.DTS-HD.MA.2.0-ZF.mkv", "other/-"},
		{"Film 1999-04-24; BD_1080P.mp4", "other/-"},
		{"化为风筝的母亲.Tako.ni.Natta.Okaasan.2003.TVSP.480p.DVDRip.x264.AAC-CoolFansSub.mkv", "other/-"},
		// explicit prefixes
		{"[Sub] Show EP05 [1080p].mkv", "episode/5"},
		{"[Sub] Show Episode 5 [1080p].mkv", "episode/5"},
		{"[Sub] Show #07 [1080p].mkv", "episode/7"},
		{"[TV版&无修版] 学生会也有洞！ - EP02 [简／繁] (1080p H.264 AAC SRTx2).mp4", "episode/2"},
		// ambiguous: two different explicit/dash numbers
		{"[Sub] Show - 03 - 04 [1080p].mkv", "episode/-"},
		// specials: own number, number after the keyword, single remaining number
		{"Special 1999 (Special S1 01); DVD_480P.mp4", "sp/1"},
		{"Special 1999 (Special S1 06); DVD_480P.mp4", "sp/6"},
		{"Special 2002-03-29 (Special S2) 逮捕しちゃうぞ; DVD_480P.mp4", "sp/-"},
		{"Special 1999 (Special S1). No Mercy !! ; DVD_480P.mp4", "sp/-"},
		{"Special 1999 (Special S1). ノンテロップ・オープニング; DVD_480P.mp4", "op/-"},
		{"You're Under Arrest. TV S1 (1996). ノンテロップ OP＆ED; DVD_480P.mp4", "extra/-"},
		{"You're Under Arrest. TV S2 (2001). 01~14話ダイジェスト; DVD_480P.mp4", "extra/-"},
		{"You're Under Arrest. TV S3 (2007). NCOP; DVD_480P.mp4", "op/-"},
		{"Film 1999-04-24. リマスター版CM 02; BD_480P.mp4", "cm/2"},
		{"Film 1999-04-24. 予告; BD_480P.mp4", "extra/-"},
		{"Film 1999-04-24. 特報; BD_480P.mp4", "pv/-"},
		{"OVA 1994-09-24 そしてふたりは出会った; DVD_480P.mp4", "episode/-"},
		{"[Dynamis One] Mahou no Shimai Lulutto Lilly [Special] - 01 (Baha 1920x1080 AVC AAC MP4) [4F988385].mp4", "sp/1"},
		{"[Tsukigakirei] Ao no Hako - NCED 02 [BDrip 1080P HEVC-10bit AAC][CHS&JPN].mp4", "ed/2"},
		{"[Tsukigakirei] Ao no Hako - NCOP 01 [BDrip 1080P HEVC-10bit AAC][CHS&JPN].mp4", "op/1"},
		{"[Sub] Show - SP 2 [1080p].mkv", "sp/2"},
		{"[Sub] Show [OVA02][1080p].mkv", "episode/2"},
		{"[Sub] Show OVA - 02 [1080p].mkv", "episode/2"},
		{"[Sub] Show [OVA][1080p].mkv", "episode/-"},
		{"[Sub] Show [SP01-SP03][1080p].mkv", "sp/-"}, // several specials in one file
		{"[Sub] Show - Special 03 [1080p].mkv", "sp/3"},
		{"[Sub] Show [Special 1999][1080p].mkv", "sp/-"},
		{"[Sub] Show [NCOP2][1080p].mkv", "op/2"},
		{"[Sub] Show [NCED][1080p].mkv", "ed/-"},
		{"[Sub] Show - Creditless OP 2 [1080p].mkv", "op/2"},
		{"[Sub] Show [PV][1080p].mkv", "pv/-"},
		{"[Sub] Show [PV2][1080p].mkv", "pv/2"},
		{"[Sub] Show [CM01][1080p].mkv", "cm/1"},
		{"[Sub] Show [Teaser][1080p].mkv", "pv/-"},
		{"[Sub] Show [Menu][1080p].mkv", "extra/-"},
		{"[Sub] Show [Preview 03][1080p].mkv", "extra/-"},
		{"[Sub] Show - 03 Sample.mkv", "extra/-"},
		{"[BD] 某作品 ノンクレジットOP2.mkv", "op/2"},
		{"[BD] 某作品 无字OP.mkv", "op/-"},
		{"[BD] 某作品 映像特典 01.mkv", "extra/-"},
		{"[BD] 某作品 特别篇 02.mkv", "sp/2"},
		// type words that are part of a title
		{"[Team] Special Agent Show [05][1080p].mkv", "episode/5"},
		{"[Team] takt op.Destiny - 03 [1080p].mkv", "episode/3"},
		{"[Team] SPY x FAMILY - 03 [1080p].mkv", "episode/3"},
		{"[Team] Show - 01 [NCOP ver][1080p].mkv", "episode/1"},
		{"[Team] Show - 12 [无字幕][1080p].mkv", "episode/12"},
	}
	for _, c := range cases {
		if got := keyStr(ParseEpisode(c.in)); got != c.want {
			t.Errorf("%q: got %s, want %s", c.in, got, c.want)
		}
	}
}

func TestParseTitleCases(t *testing.T) {
	cases := []struct{ in, want string }{
		{"[ANi] Tanuki and Kitsune /  小狸貓和小狐狸 - 02 [1080P][Baha][WEB-DL][AAC AVC][CHT][MP4]", "episode/2"},
		{"[绿茶字幕组] 青之箱 第二季 / Ao no Hako S2 [02][WebRip][1080p][简日内嵌]", "episode/2"},
		{"[黒ネズミたち] 用【循環利息】強制收回借出的魔力 / Revo - 02 (ABEMA 1920x1080 AVC AAC MKV)", "episode/2"},
		{"[LoliHouse] 拜托了美妙公主 / Onegai Aipri - 29 [WebRip 1080p HEVC-10bit AAC][无字幕]", "episode/29"},
		{"[雪飘工作室][おねがいアイプリ/Onegai_Aipri/拜托了美妙公主][29][简繁日外挂](检索:/美妙旋律/美妙天堂/美妙频道/美妙魔法)", "episode/29"},
		{"【豌豆字幕组】[药屋少女的呢喃（药师少女的独语）/ Kusuriya no Hitorigoto S3][03(51)][简体][1080P][MP4]", "episode/3"},
		{"[GM-Team][国漫][光阴之外][Beyond Time's Gaze][2025][41-44][GB][4K HEVC 10Bit]", "other/-"},
		{"[TSDM] Kimi ga Shinu made Koi wo Shitai [01-13][GB][1080P][HEVC 10bit]", "other/-"},
		{"[NEO·QSW]黑色五叶草 2nd Season 第173页 ブラッククローバー Black Clover 2nd Season 173[WEBRIP AVC 1080P]", "other/-"},
		{"Show 03", "other/-"},
		{"[Sub] Show - 03 [1080p]", "episode/3"},
	}
	for _, c := range cases {
		if got := keyStr(ParseTitle(c.in)); got != c.want {
			t.Errorf("%q: got %s, want %s", c.in, got, c.want)
		}
	}
}

func TestClassifyFiles(t *testing.T) {
	type c struct{ path, want string }
	run := func(name, title string, cs []c) {
		t.Helper()
		var paths []string
		for _, x := range cs {
			paths = append(paths, x.path)
		}
		got := ClassifyFiles(paths, title)
		for i, x := range cs {
			if g := keyStr(got[i]); g != x.want {
				t.Errorf("%s: %q: got %s, want %s", name, x.path, g, x.want)
			}
		}
	}
	// the 逮捕しちゃうぞ batch (excerpt): every special keeps its own number; the
	// season-relative TV numbering of a multi-season batch is not mapped
	root := "You're Under Arrest_TV+OVA+Film+Manga+CD+Other_dub jpn sub cht (2026-07-15)/"
	sp := root + "Special (2S7E)_DVDrip_480P_h264_aac_dub jpn sub cht/Special 1999~2002/"
	ex := root + "Extras (5S14E)_DVDrip_480P_h264_aac_dub jpn/"
	tv := root + "TV (3S97E)_DVDrip_480P_h264_aac_dub jpn sub cht/"
	run("taiho", "[整理搬运] 逮捕令 (逮捕しちゃうぞ) (You're Under Arrest)：TV动画 (第1~3季)+特别篇+OVA篇+剧场版+漫画+CD+其他", []c{
		{sp + "Special 1999 (Special S1 01); DVD_480P.mp4", "sp/1"},
		{sp + "Special 1999 (Special S1 02); DVD_480P.mp4", "sp/2"},
		{sp + "Special 1999 (Special S1 03); DVD_480P.mp4", "sp/3"},
		{sp + "Special 1999 (Special S1 04); DVD_480P.mp4", "sp/4"},
		{sp + "Special 1999 (Special S1 05); DVD_480P.mp4", "sp/5"},
		{sp + "Special 1999 (Special S1 06); DVD_480P.mp4", "sp/6"},
		{sp + "Special 2002-03-29 (Special S2) 逮捕しちゃうぞ; DVD_480P.mp4", "sp/7"},
		{ex + "Extras (Special 1999)/Special 1999 (Special S1). No Mercy !! ; DVD_480P.mp4", "extra/-"},
		{ex + "Extras (Special 1999)/Special 1999 (Special S1). ノンテロップ・オープニング; DVD_480P.mp4", "op/1"},
		{ex + "Extras (TV S1 1996)/You're Under Arrest. TV S1 (1996). ノンテロップ·オープニング; DVD_480P.mp4", "op/2"},
		{ex + "Extras (TV S3 2007)/You're Under Arrest. TV S3 (2007). NCOP; DVD_480P.mp4", "op/3"},
		{ex + "Extras (TV S3 2007)/You're Under Arrest. TV S3 (2007). NCED; DVD_480P.mp4", "ed/1"},
		{ex + "Extras (TV S1 1996)/You're Under Arrest. TV S1 (1996). 墨東お料理教室 出て来い凶悪犯 ! ; DVD_480P.mp4", "extra/-"},
		{ex + "Extras (Film 1994)/Film 1999-04-24. リマスター版CM 01; BD_480P.mp4", "cm/1"},
		{ex + "Extras (Film 1994)/Film 1999-04-24. リマスター版CM 02; BD_480P.mp4", "cm/2"},
		{root + "OVA (4S4E)_DVDrip_480P_h264_aac_dub jpn sub cht/OVA 1994~1995/OVA 1994-09-24 そしてふたりは出会った; DVD_480P.mp4", "episode/-"},
		{root + "OVA (4S4E)_DVDrip_480P_h264_aac_dub jpn sub cht/OVA 1994~1995/OVA 1995-01-21 東京タイフーン・ラリー; DVD_480P.mp4", "episode/-"},
		{tv + "TV S1 1996/You're Under Arrest. TV S1 (1996) 05; DVD_480P.mp4", "episode/-"},
		{tv + "TV S1 1996/You're Under Arrest. TV S1 (1996) 30; DVD_480P.mp4", "episode/-"},
		{tv + "TV S2 2001/You're Under Arrest. TV S2 (2001) 05; DVD_480P.mp4", "episode/-"},
	})
	// a torrent's only unnumbered special of a kind is number 1
	run("bd", "[Team] Show [BD]", []c{
		{"Show/[Team] Show - 01 [1080p].mkv", "episode/1"},
		{"Show/[Team] Show - 02 [1080p].mkv", "episode/2"},
		{"Show/SPs/[Team] Show - NCOP [1080p].mkv", "op/1"},
		{"Show/SPs/[Team] Show - NCED [1080p].mkv", "ed/1"},
		{"Show/SPs/[Team] Show [Menu][1080p].mkv", "extra/-"},
		{"Show/SPs/Show.mkv", "sp/1"},
		{"Show/[Team] Show - 01 [1080p].ass", "other/-"},
	})
	// unnumbered specials of a kind are numbered sequentially; numbered ones keep theirs
	run("ops", "[Team] Show [BD]", []c{
		{"Show/NCOP/Show NCOP [Ver A].mkv", "op/1"},
		{"Show/NCOP/Show NCOP [Ver B].mkv", "op/2"},
		{"Show/SPs/Show [PV01].mkv", "pv/1"},
		{"Show/SPs/Show [PV02].mkv", "pv/2"},
	})
	// language / resolution / edition variants of one episode are not a clash
	run("variants", "[Team] Show - 02", []c{
		{"[UHA-WINGS][Seitokai ni mo Ana wa Aru][02][1080p HEVC][CHS].mp4", "episode/2"},
		{"[UHA-WINGS][Seitokai ni mo Ana wa Aru][02][720p HEVC][CHT].mp4", "episode/2"},
		{"[UHA-WINGS][Seitokai ni mo Ana wa Aru][03][1080p HEVC][CHS].mp4", "episode/3"},
	})
	run("editions", "[TV版&无修版] 学生会也有洞！ - EP02 [简／繁] (1080p H.264 AAC SRTx2)", []c{
		{"TV/【10月】学生会也有洞！ 02【TV放送版】.mp4", "episode/2"},
		{"Uncut/【10月】学生会也有洞！ 02【限制解除版】.mp4", "episode/2"},
	})
	// two different contents with the same key are ambiguous
	run("clash", "[Team] Shows", []c{
		{"Season 1/[Team] Show - 01 [1080p].mkv", "episode/-"},
		{"Season 2/[Team] Show - 01 [1080p].mkv", "episode/-"},
		{"Season 2/[Team] Show - 02 [1080p].mkv", "episode/-"},
		{"SPs/[Team] Show - SP01 [1080p].mkv", "sp/1"},
	})
	// single video without a number: the torrent title
	run("single", "[Team] Show - 07 [1080p]", []c{{"[Team] Show [1080p].mkv", "episode/7"}})
	run("single-sp", "[Team] Show - OVA 2 [1080p]", []c{{"[Team] Show - OVA [1080p].mkv", "episode/2"}})
}
