package meta

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestReleaseTimeAndRuleReview(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	if _, err := s.W.ExecContext(ctx, `INSERT INTO works(id,key,title,title_cn,aliases,type,begin_at,eps,updated_at) VALUES (1,'bgm:1','フリーレン','葬送的芙莉莲','["Sousou no Frieren"]','tv','2026-01-01T00:00:00Z',12,'x')`); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 12; i++ {
		_, _ = s.W.ExecContext(ctx, `INSERT INTO episodes(bgm_id,work_key,type,sort,airdate) VALUES(?,'bgm:1',0,?,?)`, 100+i, i, time.Date(2026, 1, 1+7*(i-1), 0, 0, 0, 0, time.UTC).Format("2006-01-02"))
	}
	files := []FileInput{{Index: 0, Path: "[LoliHouse] Sousou no Frieren - 03 [1080p].mkv", Size: 1}}
	title := "[LoliHouse] 葬送的芙莉莲 / Sousou no Frieren - 03 [1080p]"
	mapped := func(ih string) *FileMap {
		tm, err := s.GetTorrentMap(ctx, ih)
		if err != nil {
			t.Fatal(err)
		}
		return &tm.Files[0]
	}

	// crawled: published_at is the release time, the rules map as usual
	if err := s.Match(ctx, TorrentInput{Infohash: "c1", Title: title, Source: "Nyaa", SourceKind: "rss", PublishedAt: "2026-01-16T00:00:00Z", Files: files}); err != nil {
		t.Fatal(err)
	}
	if rt, by := s.ReleaseTime(ctx, "c1"); by != "published" || rt.Format("2006-01-02") != "2026-01-16" {
		t.Errorf("crawl release %v %s", rt, by)
	}
	if f := mapped("c1"); f.Work == nil || f.EpisodeKey != "3" {
		t.Errorf("crawl mapping %+v", f)
	}

	// user submission with a submission time: submitted_at counts (published_at of a user
	// row does not), submitted_by stored once
	uid := int64(7)
	if err := s.Match(ctx, TorrentInput{Infohash: "u1", Title: title, Source: "user", SourceKind: "user", PublishedAt: "2020-01-01T00:00:00Z",
		SubmittedAt: "2026-01-17T00:00:00Z", SubmittedBy: &uid, Files: files}); err != nil {
		t.Fatal(err)
	}
	if rt, by := s.ReleaseTime(ctx, "u1"); by != "submitted" || rt.Format("2006-01-02") != "2026-01-17" {
		t.Errorf("user release %v %s", rt, by)
	}
	other := int64(9)
	_ = s.Match(ctx, TorrentInput{Infohash: "u1", Source: "user", SourceKind: "user", SubmittedAt: "2026-03-01T00:00:00Z", SubmittedBy: &other, Files: files})
	if at, by := s.Submitter(ctx, "u1"); at != "2026-01-17T00:00:00Z" || by == nil || *by != 7 {
		t.Errorf("submitter overwritten: %s %v", at, by)
	}
	if f := mapped("u1"); f.Work == nil {
		t.Errorf("user mapping %+v", f)
	}

	// user submission without any release time: the certain rule mapping goes to review
	if err := s.Match(ctx, TorrentInput{Infohash: "u2", Title: title, Source: "user", SourceKind: "user", Files: files}); err != nil {
		t.Fatal(err)
	}
	if f := mapped("u2"); f.Work != nil {
		t.Errorf("mapped without release time: %+v", f)
	}
	ds, _, _ := s.ListClefDecisions(ctx, "pending", "u2", 0, 10)
	if len(ds) != 1 || ds[0].Source != "rule" || ds[0].Reason != ReasonMissingRelease || ds[0].WorkKey != "bgm:1" || ds[0].EpisodeKey != "3" {
		t.Fatalf("rule decision %+v", ds)
	}
	// re-delivered: no second decision; Reclassify leaves it alone
	_ = s.Match(ctx, TorrentInput{Infohash: "u2", Source: "user", SourceKind: "user", Files: files})
	if _, n, _ := s.ListClefDecisions(ctx, "", "u2", 0, 10); n != 1 {
		t.Errorf("decisions %d", n)
	}
	if _, err := s.Reclassify(ctx); err != nil {
		t.Fatal(err)
	}
	if f := mapped("u2"); f.Work != nil {
		t.Errorf("reclassify mapped it: %+v", f)
	}
	// accepted by an admin: mapped, verified
	if err := s.ApplyReviewed(ctx, "u2", 0, "bgm:1", "episode", ds[0].Episode, "admin"); err != nil {
		t.Fatal(err)
	}
	if f := mapped("u2"); f.Work == nil || f.Status != "verified" || f.Method != MethodReview {
		t.Errorf("accepted %+v", f)
	}
	// crawled later: the crawl source and its published_at take over
	_ = s.UpsertPending(ctx, TorrentInput{Infohash: "u2", Title: title, Source: "Nyaa", SourceKind: "rss", PublishedAt: "2026-01-15T00:00:00Z"})
	if _, by := s.ReleaseTime(ctx, "u2"); by != "published" {
		t.Errorf("crawled later: %s", by)
	}
}

func TestCheckDate(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	_, _ = s.W.ExecContext(ctx, `INSERT INTO works(id,key,title,type,begin_at,end_at,eps,updated_at) VALUES (1,'bgm:1','A','tv','2026-01-01T00:00:00Z','2026-03-20T00:00:00Z',12,'x'),(2,'bgm:2','B','tv','2026-01-01T00:00:00Z','',12,'x')`)
	_, _ = s.W.ExecContext(ctx, `INSERT INTO episodes(bgm_id,work_key,type,sort,airdate) VALUES(1,'bgm:1',0,3,'2026-01-15'),(2,'bgm:1',0,4,'')`)
	day := func(d string) time.Time { tm, _ := time.Parse("2006-01-02", d); return tm }
	main := func(n float64) EpisodeKey { return EpisodeKey{Kind: KeyMain, Number: n} }
	for _, c := range []struct {
		work string
		k    EpisodeKey
		rel  string
		ok   bool
		want string
	}{
		{"bgm:1", main(3), "2026-01-16", true, ""},
		{"bgm:1", main(3), "2026-02-14", true, ""},        // 30 days after
		{"bgm:1", main(3), "2026-02-17", false, "晚于本集播出"}, // > 30 days after (BD later: review)
		{"bgm:1", main(3), "2026-01-08", true, ""},        // 7 days before
		{"bgm:1", main(3), "2026-01-06", false, "早于本集播出"},
		{"bgm:1", main(4), "2026-04-30", false, "晚于条目完结"}, // no airdate: the work's window
		{"bgm:1", main(4), "2026-04-10", true, ""},
		{"bgm:2", main(4), "2030-01-01", true, ""}, // open-ended
		{"bgm:2", main(4), "2025-12-01", false, "早于条目开播"},
	} {
		ok, detail := s.CheckDate(ctx, c.work, c.k, day(c.rel), "submitted")
		if ok != c.ok || !strings.Contains(detail, c.want) || (!ok && !strings.HasPrefix(detail, "提交时间")) {
			t.Errorf("%s %v %s: %v %q", c.work, c.k, c.rel, ok, detail)
		}
	}
}

func TestRomanizeAndRetrieve(t *testing.T) {
	for in, want := range map[string]string{"タヌキとキツネ": "tanukitokitsune", "おねがいアイプリ": "onegaiaipuri", "ちょっとだけ": "chottodake", "キャット": "kyatto"} {
		if got, ok := Romanize(in); !ok || strings.ReplaceAll(got, " ", "") != want {
			t.Errorf("%s: %q", in, got)
		}
	}
	s := openTest(t)
	ctx := context.Background()
	_, _ = s.W.ExecContext(ctx, `INSERT INTO works(id,key,title,title_cn,aliases,type,begin_at,updated_at) VALUES
		(1,'bgm:1','タヌキとキツネ','小狸猫与小狐狸','[]','tv','2026-10-03T00:00:00Z','x'),
		(2,'bgm:2','タヌキとキツネ','小狸猫和小狐狸','[]','web','2018-02-09T00:00:00Z','x'),
		(3,'bgm:3','転生貴族、鑑定スキルで成り上がる','','["Tensei Kizoku, Kantei Skill de Nariagaru"]','tv','2024-04-07T00:00:00Z','x')`)
	got := s.retrieve(ctx, []string{"[ANi] Tanuki and Kitsune - 03 [1080P]"}, "2026-10-20T00:00:00Z", 5)
	if len(got) < 2 || got[0] != "bgm:1" {
		t.Errorf("romaji + recent first: %v", got)
	}
	// long-vowel / season-marker variants
	if got := s.retrieve(ctx, []string{"[G] Tensei Kizoku Kantei Sukiru de Nariagaru S3 - 28"}, "", 5); len(got) == 0 || got[0] != "bgm:3" {
		t.Errorf("token overlap: %v", got)
	}
}
