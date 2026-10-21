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

// The rules get the same date gate as the decision model: released more than 7 days before
// the episode aired → review (any torrent); more than 30 days after → review only for a
// single-episode torrent (one video file).
func TestRuleDateGate(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	if _, err := s.W.ExecContext(ctx, `INSERT INTO works(id,key,title,title_cn,aliases,type,begin_at,end_at,eps,updated_at) VALUES
		(1,'bgm:1','フリーレン','葬送的芙莉莲','["Sousou no Frieren"]','tv','2026-10-03T00:00:00Z','2026-12-19T00:00:00Z',12,'x'),
		(2,'bgm:2','ハルヒ','凉宫春日的忧郁','["Suzumiya Haruhi no Yuuutsu"]','tv','2006-04-02T00:00:00Z','2006-07-02T00:00:00Z',14,'x')`); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 12; i++ {
		_, _ = s.W.ExecContext(ctx, `INSERT INTO episodes(bgm_id,work_key,type,sort,airdate) VALUES(?,'bgm:1',0,?,?)`, 100+i, i, time.Date(2026, 10, 3+7*(i-1), 0, 0, 0, 0, time.UTC).Format("2006-01-02"))
	}
	for i := 1; i <= 14; i++ {
		_, _ = s.W.ExecContext(ctx, `INSERT INTO episodes(bgm_id,work_key,type,sort,airdate) VALUES(?,'bgm:2',0,?,?)`, 200+i, i, time.Date(2006, 4, 2+7*(i-1), 0, 0, 0, 0, time.UTC).Format("2006-01-02"))
	}
	uid := int64(3)
	submit := func(ih, title, at string, paths ...string) {
		t.Helper()
		var files []FileInput
		for i, p := range paths {
			files = append(files, FileInput{Index: i, Path: p, Size: 1})
		}
		if err := s.Match(ctx, TorrentInput{Infohash: ih, Title: title, Source: "user", SourceKind: "user", SubmittedAt: at, SubmittedBy: &uid, Files: files}); err != nil {
			t.Fatal(err)
		}
	}
	mapped := func(ih string, idx int) bool {
		tm, _ := s.GetTorrentMap(ctx, ih)
		return tm.Files[idx].Work != nil
	}
	review := func(ih, want string) {
		t.Helper()
		ds, _, _ := s.ListClefDecisions(ctx, "pending", ih, 0, 10)
		if len(ds) == 0 || ds[0].Source != "rule" || !strings.HasPrefix(ds[0].Reason, ReasonDateMismatch+": ") || !strings.Contains(ds[0].Reason, want) {
			t.Errorf("%s review: %+v", ih, ds)
		}
	}
	// a forged "[LoliHouse] Show - 05" submitted two weeks before episode 5 airs
	submit("f1", "[LoliHouse] Sousou no Frieren - 05 [WebRip 1080p]", "2026-10-17T00:00:00Z", "[LoliHouse] Sousou no Frieren - 05 [WebRip 1080p].mkv")
	if mapped("f1", 0) {
		t.Error("forged pre-air episode mapped")
	}
	review("f1", "早于本集播出")
	// … also inside a batch
	submit("f2", "[LoliHouse] Sousou no Frieren - 01-05 [WebRip 1080p]", "2026-10-17T00:00:00Z",
		"F/[LoliHouse] Sousou no Frieren - 01 [WebRip 1080p].mkv", "F/[LoliHouse] Sousou no Frieren - 05 [WebRip 1080p].mkv")
	if !mapped("f2", 0) || mapped("f2", 1) {
		t.Error("batch: aired episode mapped, forged one not")
	}
	// in time: applied
	submit("f3", "[LoliHouse] Sousou no Frieren - 05 [WebRip 1080p]", "2026-10-31T12:00:00Z", "[LoliHouse] Sousou no Frieren - 05 [WebRip 1080p].mkv")
	if !mapped("f3", 0) {
		t.Error("in-time episode not mapped")
	}
	// an old show submitted today: a single episode → review; a batch (2+ video files, any
	// kind) → applied
	submit("h1", "[G] Suzumiya Haruhi no Yuuutsu - 03 [BD 1080p]", "2026-10-20T00:00:00Z", "[G] Suzumiya Haruhi no Yuuutsu - 03 [BD 1080p].mkv")
	if mapped("h1", 0) {
		t.Error("old single episode mapped")
	}
	review("h1", "晚于本集播出")
	submit("h2", "[G] Suzumiya Haruhi no Yuuutsu - 03 [BD 1080p]", "2026-10-20T00:00:00Z",
		"H/[G] Suzumiya Haruhi no Yuuutsu - 03 [BD 1080p].mkv", "H/[G] Suzumiya Haruhi no Yuuutsu - Menu [BD 1080p].mkv")
	if !mapped("h2", 0) {
		t.Error("old episode + bonus video (batch) not mapped")
	}
	submit("h3", "[G] Suzumiya Haruhi no Yuuutsu [BD 1080p]", "2026-10-20T00:00:00Z",
		"H/[G] Suzumiya Haruhi no Yuuutsu - 01 [BD 1080p].mkv", "H/[G] Suzumiya Haruhi no Yuuutsu - 02 [BD 1080p].mkv")
	if !mapped("h3", 0) || !mapped("h3", 1) {
		t.Error("old batch not mapped")
	}
	// a subtitle file next to the episode doesn't make a batch
	submit("h4", "[G] Suzumiya Haruhi no Yuuutsu - 04 [BD 1080p]", "2026-10-20T00:00:00Z",
		"H/[G] Suzumiya Haruhi no Yuuutsu - 04 [BD 1080p].mkv", "H/[G] Suzumiya Haruhi no Yuuutsu - 04 [BD 1080p].ass")
	if mapped("h4", 0) {
		t.Error("episode + subtitles counted as a batch")
	}
}

// The release-time gates only stop NEW mappings: an existing mapping that the rules give
// again is kept (also by Reclassify), manual / decision-model rows are never touched, and a
// new file of the same torrent is gated.
func TestRuleGateOnlyNewMappings(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	if _, err := s.W.ExecContext(ctx, `INSERT INTO works(id,key,title,title_cn,aliases,type,begin_at,end_at,eps,updated_at) VALUES
		(2,'bgm:2','ハルヒ','凉宫春日的忧郁','["Suzumiya Haruhi no Yuuutsu"]','tv','2006-04-02T00:00:00Z','2006-07-02T00:00:00Z',14,'x')`); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 14; i++ {
		_, _ = s.W.ExecContext(ctx, `INSERT INTO episodes(bgm_id,work_key,type,sort,airdate) VALUES(?,'bgm:2',0,?,?)`, 200+i, i, time.Date(2006, 4, 2+7*(i-1), 0, 0, 0, 0, time.UTC).Format("2006-01-02"))
	}
	ep3 := FileInput{Index: 0, Path: "[G] Suzumiya Haruhi no Yuuutsu - 03 [1080p].mkv", Size: 1}
	in := TorrentInput{Infohash: "aa", Title: "[G] Suzumiya Haruhi no Yuuutsu - 03 [1080p]", Source: "Nyaa", SourceKind: "rss", PublishedAt: "2006-04-17T00:00:00Z", Files: []FileInput{ep3}}
	if err := s.Match(ctx, in); err != nil {
		t.Fatal(err)
	}
	row := func(idx int) FileMap {
		tm, _ := s.GetTorrentMap(ctx, "aa")
		for _, f := range tm.Files {
			if f.Index == idx {
				return f
			}
		}
		return FileMap{}
	}
	pending := func() int {
		_, n, _ := s.ListClefDecisions(ctx, "pending", "aa", 0, 10)
		return n
	}
	if f := row(0); f.Work == nil || f.EpisodeKey != "3" {
		t.Fatalf("in time: %+v", f)
	}
	// the release time changes to 20 years later (the gate would now fail for a single
	// episode): re-matched and reclassified, the mapping stays, nothing is queued
	_, _ = s.W.ExecContext(ctx, `UPDATE torrents SET published_at = '2026-10-20T00:00:00Z' WHERE infohash = 'aa'`)
	if err := s.Match(ctx, in); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Reclassify(ctx); err != nil {
		t.Fatal(err)
	}
	if f := row(0); f.Work == nil || f.EpisodeKey != "3" || pending() != 0 {
		t.Fatalf("existing mapping after re-match: %+v pending %d", f, pending())
	}
	// the same without any release time
	_, _ = s.W.ExecContext(ctx, `UPDATE torrents SET published_at = '', source = 'user', source_kind = 'user' WHERE infohash = 'aa'`)
	in2 := in
	in2.Source, in2.SourceKind, in2.PublishedAt = "user", "user", ""
	if err := s.Match(ctx, in2); err != nil {
		t.Fatal(err)
	}
	if f := row(0); f.Work == nil || pending() != 0 {
		t.Fatalf("no release time, existing mapping: %+v pending %d", f, pending())
	}
	_, _ = s.W.ExecContext(ctx, `UPDATE torrents SET published_at = '2006-04-17T00:00:00Z', source = 'Nyaa', source_kind = 'rss' WHERE infohash = 'aa'`)

	// a new file in the same torrent is gated: episode 14 "released" 2006-04-17, months
	// before it aired → review; episode 3 stays
	ep14 := FileInput{Index: 1, Path: "[G] Suzumiya Haruhi no Yuuutsu - 14 [1080p].mkv", Size: 1}
	in3 := in
	in3.Files = []FileInput{ep3, ep14}
	if err := s.Match(ctx, in3); err != nil {
		t.Fatal(err)
	}
	if f := row(1); f.Work != nil {
		t.Errorf("new pre-air file mapped: %+v", f)
	}
	if ds, _, _ := s.ListClefDecisions(ctx, "pending", "aa", 0, 10); len(ds) != 1 || ds[0].FileIndex != 1 || !strings.HasPrefix(ds[0].Reason, ReasonDateMismatch) {
		t.Errorf("new file review: %+v", ds)
	}
	if f := row(0); f.Work == nil || f.EpisodeKey != "3" {
		t.Errorf("episode 3 after the new file: %+v", f)
	}

	// manual and decision-model rows: untouched, no review queued, even when the gate fails
	_, _ = s.W.ExecContext(ctx, `UPDATE torrent_files SET status = 'verified', method = 'manual', work_key = NULL, episode = NULL WHERE infohash = 'aa' AND file_index = 0`)
	_, _ = s.W.ExecContext(ctx, `UPDATE torrent_files SET method = ?, work_key = 'bgm:2', episode = 1 WHERE infohash = 'aa' AND file_index = 1`, MethodClef)
	_, _ = s.W.ExecContext(ctx, `DELETE FROM clef_decisions`)
	_, _ = s.W.ExecContext(ctx, `UPDATE torrents SET published_at = '2026-10-20T00:00:00Z' WHERE infohash = 'aa'`)
	_, _ = s.W.ExecContext(ctx, `UPDATE torrents SET published_at = '2006-04-17T00:00:00Z' WHERE infohash = 'aa'`) // ep 14 still early
	if err := s.Match(ctx, in3); err != nil {
		t.Fatal(err)
	}
	if n := pending(); n != 0 {
		t.Errorf("reviews queued for untouched rows (ep 14 early): %d", n)
	}
	_, _ = s.W.ExecContext(ctx, `UPDATE torrents SET published_at = '2026-10-20T00:00:00Z' WHERE infohash = 'aa'`)
	in4 := in3
	in4.Files = []FileInput{ep3}
	if err := s.Match(ctx, in4); err != nil {
		t.Fatal(err)
	}
	if f := row(0); f.Work != nil || f.Status != "verified" || f.Method != "manual" {
		t.Errorf("manual row: %+v", f)
	}
	if f := row(1); f.Work == nil || f.Method != MethodClef || f.EpisodeKey != "1" {
		t.Errorf("decision-model row: %+v", f)
	}
	if n := pending(); n != 0 {
		t.Errorf("reviews queued for untouched rows: %d", n)
	}
}
