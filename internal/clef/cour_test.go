package clef

import (
	"strings"
	"testing"
)

func TestOfficialCourContinuation(t *testing.T) {
	ms := refineStore(t)
	p := addWork(t, ms, 507634, "千歳くんはラムネ瓶のなか", `["Chitose Is in the Ramune Bottle"]`, "2025-10-07", "2026-03-31", 1, 13, "2025-10-07")
	_, _ = ms.W.Exec(`UPDATE episodes SET airdate='2026-03-31' WHERE work_key=? AND sort>=11`, p)
	w := addWork(t, ms, 584847, "千歳くんはラムネ瓶のなか(第2クール)", `["Chitose Is in the Ramune Bottle Part 2","Chitose Is in the Ramune Bottle Season 2"]`, "2026-10-13", "", 14, 25, "2026-10-13")
	_, _ = ms.W.Exec(`UPDATE works SET title_cn='弹珠汽水瓶里的千岁同学 第2部分' WHERE key=?`, w)
	_, _ = ms.W.Exec(`INSERT INTO episodes(bgm_id,work_key,type,sort,airdate) VALUES(999901,?,0,13,''),(999902,?,0,13.5,'')`, w, w)
	prequelOf(t, ms, 584847, 507634)
	path := "Chitose.Is.in.the.Ramune.Bottle.S01E15.1080p.WEB-DL.mkv"
	run := func(path string, num float64, release string) FileDecision {
		tk := refineTask(t, ms, path, release, []string{path}, w, p)
		return decideRefine(ms, tk, answerAll(tk, "bgm_584847", num))[0]
	}
	// Absolute episode 15 with actual airdate; overlapping undated 13/13.5 are NOT
	// the beginning of the second part's broadcast spine. No mapped files in this torrent.
	if d := run(path, 15, "2026-10-20T12:00:00Z"); d.Status != StatusApplied || d.Key != "15" {
		t.Fatalf("Chitose: %+v", d)
	}
	// Local episode 2 canonicalises to 15 (first REAL episode 14).
	local := strings.Replace(path, "S01E15", "S01E02", 1)
	if d := run(local, 2, "2026-10-20T12:00:00Z"); d.Status != StatusApplied || d.Key != "15" {
		t.Fatalf("local numbering: %+v", d)
	}
	// A local parser number and absolute model number agree after canonicalisation.
	if d := run(local, 15, "2026-10-20T12:00:00Z"); d.Status == StatusApplied {
		t.Fatal("model number not present must remain strict")
	}
	// An episode overlapping the first part / undated placeholder is not enough proof.
	if d := run(strings.Replace(path, "E15", "E13", 1), 13, "2026-10-20T12:00:00Z"); d.Status == StatusApplied {
		t.Fatal("undated overlapping placeholder accepted")
	}
	if d := run(path, 15, "2026-09-01T00:00:00Z"); d.Status == StatusApplied {
		t.Fatal("pre-air accepted")
	}
	// Missing release time or a selected chapter without its actual airdate cannot prove
	// this override. A dated overlap at the boundary is also ambiguous, never guessed.
	if d := run(path, 15, ""); d.Status == StatusApplied {
		t.Fatal("missing release accepted")
	}
	_, _ = ms.W.Exec(`UPDATE episodes SET airdate='' WHERE work_key=? AND sort=15`, w)
	if d := run(path, 15, "2026-10-20T12:00:00Z"); d.Status == StatusApplied {
		t.Fatal("undated selected chapter accepted")
	}
	_, _ = ms.W.Exec(`UPDATE episodes SET airdate='2026-10-20' WHERE work_key=? AND sort=15`, w)
	_, _ = ms.W.Exec(`UPDATE episodes SET airdate='2026-10-01' WHERE work_key=? AND sort=13`, w)
	if d := run(path, 15, "2026-10-20T12:00:00Z"); d.Status == StatusApplied {
		t.Fatal("dated overlapping chapter accepted")
	}
	_, _ = ms.W.Exec(`UPDATE episodes SET airdate='' WHERE work_key=? AND sort=13`, w)
	// A genuinely explicit second season is NOT overridden by merely containing episode 15.
	_, _ = ms.W.Exec(`UPDATE works SET title='千歳くんはラムネ瓶のなか 第2期',title_cn='弹珠汽水瓶里的千岁同学 第二季' WHERE key=?`, w)
	if d := run(path, 15, "2026-10-20T12:00:00Z"); d.Status != StatusDiscarded || d.Reason != ReasonSeason {
		t.Fatalf("real season2: %+v", d)
	}
}
