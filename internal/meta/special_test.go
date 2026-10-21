package meta

import (
	"context"
	"fmt"
	"testing"
)

func TestSpecialsAndOVAUnified(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)

	// 1. Work with SP1–SP4 on bgm, torrent has SP1–SP36: all 36 are valid and map by rule.
	if _, err := s.W.ExecContext(ctx, `INSERT INTO works(id,key,title,title_cn,aliases,type,begin_at,eps,updated_at) VALUES
		(100,'bgm:272510','まちカドまぞく','街角魔族','["The Demon Girl Next Door","Machikado Mazoku"]','tv','2019-07-11T00:00:00Z',12,'x')`); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 4; i++ {
		if _, err := s.W.ExecContext(ctx, `INSERT INTO episodes(bgm_id,work_key,type,sort,name,name_cn) VALUES(?,'bgm:272510',1,?,?,?)`,
			2725100+i, i, fmt.Sprintf("みに %d", i), fmt.Sprintf("小剧场 %d", i)); err != nil {
			t.Fatal(err)
		}
	}
	var spFiles []FileInput
	for i := 1; i <= 36; i++ {
		spFiles = append(spFiles, FileInput{
			Index: i - 1,
			Path:  fmt.Sprintf("Machikado Mazoku/SPs/[Group] Machikado Mazoku - SP%02d [1080p].mkv", i),
			Size:  10,
		})
	}
	if err := s.Match(ctx, TorrentInput{
		Infohash:    "sp36ih",
		Title:       "[Group] Machikado Mazoku [BDRip]",
		Source:      "Nyaa",
		SourceKind:  "rss",
		PublishedAt: "2020-01-15T00:00:00Z",
		Files:       spFiles,
	}); err != nil {
		t.Fatal(err)
	}
	keys, err := s.EpisodeKeys(ctx, "bgm:272510")
	if err != nil || len(keys) != 36 {
		t.Fatalf("expected 36 SP keys, got %d (err=%v)", len(keys), err)
	}
	if keys[4].Key.String() != "sp5" || keys[4].Title != "SP05" {
		t.Errorf("sp5 key/title: %+v", keys[4])
	}

	// 2. Compound CM/PV tokens and sequential numbering of multiple unnumbered specials.
	paths := []string{
		"[Airota&VCB-Studio] Show [Ma10p_1080p]/SPs/[Airota&VCB-Studio] Show [Original PV][Ma10p_1080p][x265_flac].mkv",
		"[Airota&VCB-Studio] Show [Ma10p_1080p]/SPs/[Airota&VCB-Studio] Show [PV02][Ma10p_1080p][x265_flac].mkv",
		"[Airota&VCB-Studio] Show [Ma10p_1080p]/SPs/[Airota&VCB-Studio] Show [Teaser PV][Ma10p_1080p][x265_flac].mkv",
		"[Nekomoe kissaten] Show [BDRip]/SPs/[Nekomoe kissaten] Show [BDCMs][BDRip 1080p HEVC-10bit FLAC].mkv",
		"[Nekomoe kissaten] Show [BDRip]/SPs/[Nekomoe kissaten] Show [TVCMs][BDRip 1080p HEVC-10bit FLAC].mkv",
		"[Hakugetsu&VCB-Studio] Show [1080p]/SPs/[Hakugetsu&VCB-Studio] Show [NCOP][Hi10p_1080p][x264_flac].mkv",
		"[Hakugetsu&VCB-Studio] Show [1080p]/SPs/[Hakugetsu&VCB-Studio] Show [NCOP_Live_ver][DVDRip][Hi10p_480p][x264_flac].mkv",
	}
	ps := ClassifyFiles(paths, "[VCB-Studio] Show [1080p]")
	wantKeys := []string{"pv1", "pv2", "pv3", "cm1", "cm2", "ncop1", "ncop2"}
	for i, w := range wantKeys {
		k, ok := KeyOfFile(ps[i].Kind, ps[i].Episode)
		if !ok || k.String() != w || !ps[i].Sure {
			t.Errorf("file %d: got %v (sure=%v), want %s", i, k.String(), ps[i].Sure, w)
		}
	}

	// 3. DeriveSpecialTitle checks.
	wNames := []string{"ご注文はうさぎですか？BLOOM", "Gochuumon wa Usagi Desuka? BLOOM", "Munou na Nana", "No Game No Life", "The Demon Girl Next Door", "Lucky Star"}
	titleCases := []struct {
		path string
		want string
	}{
		{"[Airota&VCB-Studio] Gochuumon wa Usagi Desuka？BLOOM [Original PV][Ma10p_1080p][x265_flac].mkv", "Original PV"},
		{"[Airota&VCB-Studio] Gochuumon wa Usagi Desuka？BLOOM [Teaser PV][Ma10p_1080p][x265_flac].mkv", "Teaser PV"},
		{"[Nekomoe kissaten] Munou na Nana [BDCMs][BDRip 1080p HEVC-10bit FLAC].mkv", "BDCMs"},
		{"[Hakugetsu&VCB-Studio] No Game No Life [NCOP_Live_ver][DVDRip][Hi10p_480p][x264_flac].mkv", "NCOP Live ver"},
		{"The.Demon.Girl.Next.Door.S00E05.Machikado.Mazoku.Mini.05.1080p.BluRay.REMUX.AVC.FLAC.2.0-StaFer.mkv", "Machikado Mazoku Mini 05"},
		{"[I.G&CASO][Lucky_Star][SP][Lucky_Channel_in_New_Tokyo_Circuit][BDRIP][1920x1080][x264_FLAC][E1DEA6ED].mkv", "Lucky Channel in New Tokyo Circuit"},
	}
	for _, tc := range titleCases {
		if got := DeriveSpecialTitle(tc.path, wNames); got != tc.want {
			t.Errorf("DeriveSpecialTitle(%q) = %q, want %q", tc.path, got, tc.want)
		}
	}

	// 4. OVA / OAD routing: never attached as sp to TV anchor; queued when TV has no unambiguous OVA target;
	// mapped as main episode when anchor is the standalone OVA work.
	if _, err := s.W.ExecContext(ctx, `INSERT INTO works(id,key,title,title_cn,aliases,type,begin_at,eps,updated_at) VALUES
		(200,'bgm:200','らき☆すた','幸运星','["Lucky Star"]','tv','2007-04-08T00:00:00Z',24,'x'),
		(201,'bgm:13557','らき☆すた OVA','幸运星 OVA','["Lucky Star OVA"]','ova','2008-09-26T00:00:00Z',1,'x')`); err != nil {
		t.Fatal(err)
	}
	_, _ = s.W.ExecContext(ctx, `INSERT INTO episodes(bgm_id,work_key,type,sort) VALUES (135570,'bgm:13557',0,1)`)
	s.aliasMu.Lock()
	s.aliases = nil
	s.aliasMu.Unlock()

	// Anchor is TV bgm:200 (no relation to bgm:13557 yet): [OVA] file must NOT attach to bgm:200 as sp1 or main 1; it is queued.
	if err := s.Match(ctx, TorrentInput{
		Infohash:    "ovanoattach",
		Title:       "[CASO&I.G] Lucky Star [BDRip]",
		Source:      "Nyaa",
		SourceKind:  "rss",
		PublishedAt: "2009-01-01T00:00:00Z",
		Files: []FileInput{
			{Index: 0, Path: "[CASO&I.G][Lucky_Star]/[I.G&CASO][Lucky_Star][01][BDRIP][1920x1080][x264_FLAC].mkv", Size: 100},
			{Index: 1, Path: "[CASO&I.G][Lucky_Star]/[I.G&CASO][Lucky_Star][OVA][BDRIP][1920x1080][x264_FLAC][891A3A4B].mkv", Size: 100},
		},
	}); err != nil {
		t.Fatal(err)
	}
	tm, _ := s.GetTorrentMap(ctx, "ovanoattach")
	if tm.Files[0].Work == nil || tm.Files[0].Work.Key != "bgm:200" || tm.Files[0].EpisodeKey != "1" {
		t.Errorf("TV episode 1: %+v", tm.Files[0])
	}
	if tm.Files[1].Work != nil || tm.Files[1].Kind != "episode" {
		t.Errorf("OVA file attached to TV work instead of queued: %+v", tm.Files[1])
	}

	// Standalone OVA anchor: [OVA] file maps as main episode 1 of bgm:13557.
	if err := s.Match(ctx, TorrentInput{
		Infohash:    "ovaanchor",
		Title:       "[CASO&I.G] Lucky Star OVA [BDRip]",
		Source:      "Nyaa",
		SourceKind:  "rss",
		PublishedAt: "2009-01-01T00:00:00Z",
		Files: []FileInput{
			{Index: 0, Path: "[I.G&CASO][Lucky_Star][OVA][BDRIP][1920x1080][x264_FLAC][891A3A4B].mkv", Size: 100},
		},
	}); err != nil {
		t.Fatal(err)
	}
	tmOVA, _ := s.GetTorrentMap(ctx, "ovaanchor")
	if tmOVA.Files[0].Work == nil || tmOVA.Files[0].Work.Key != "bgm:13557" || tmOVA.Files[0].EpisodeKey != "1" || tmOVA.Files[0].Kind != "episode" {
		t.Errorf("standalone OVA anchor: %+v", tmOVA.Files[0])
	}

	// UnnumberedMainVideo allows [OVA] / [OAD] / [OVA_v2].
	for _, p := range []string{
		"[CASO&I.G][Lucky_Star]/[I.G&CASO][Lucky_Star][OVA][BDRIP][1920x1080][x264_FLAC][891A3A4B].mkv",
		"[VCB-Studio] GIRLS und PANZER/[Kamigami&Mabors&VCB-Studio] GIRLS und PANZER der FILM [Ma10p_1080p]/[Kamigami&Mabors&VCB-Studio] GIRLS und PANZER der FILM [OVA_v2][Ma10p_1080p][x265_flac].mkv",
		"[VCB-Studio] Ansatsu Kyoushitsu [OAD][Ma10p_1080p][x265_flac].mkv",
	} {
		if !UnnumberedMainVideo(p) {
			t.Errorf("UnnumberedMainVideo(%q) = false, want true", p)
		}
	}
}
