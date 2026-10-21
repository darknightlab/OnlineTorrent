package clef

import (
	"context"
	"testing"
)

func TestLLMOfficialSingleton(t *testing.T) {
	ms := refineStore(t)
	ctx := context.Background()
	w := addWork(t, ms, 99796, "ゆるゆり なちゅやちゅみ！", `["Yuru Yuri Nachuyachumi!"]`, "2014-11-29", "2014-11-29", 1, 1, "2014-11-29")
	_, _ = ms.W.Exec(`UPDATE works SET type='ova' WHERE key=?`, w)
	path := "[VCB-Studio] Yuru Yuri/[VCB-Studio] Yuru Yuri Nachuyachumi! [Ma10p_1080p][x265_flac].mkv"
	for _, kind := range []string{"movie", "ova", "web", "tv"} {
		_, _ = ms.W.Exec(`UPDATE works SET type=? WHERE key=?`, kind, w)
		for _, stated := range []*float64{nil, f64p(1)} {
			tk := refineTask(t, ms, "Yuru Yuri collection", "2014-12-01T00:00:00Z", []string{path}, w)
			// Hundreds of other videos in the collection are irrelevant to chapter proof.
			tk.Videos = append(tk.Videos, "other work/03.mkv")
			tk.Batch = true
			a := answerAll(tk, "bgm_99796")
			x := a[0]
			x.Episode = stated
			a[0] = x
			ds := decideRefine(ms, tk, a)
			if d := ds[0]; d.Status != StatusApplied || d.Key != "1" {
				t.Errorf("%s episode %v: %+v", kind, stated, d)
			}
		}
	}
	// Official singleton's sort is not necessarily 1.
	_, _ = ms.W.Exec(`UPDATE episodes SET sort=13 WHERE work_key=?`, w)
	for _, stated := range []*float64{nil, f64p(13)} {
		tk := refineTask(t, ms, "Yuru Yuri collection", "2014-12-01T00:00:00Z", []string{path}, w)
		a := answerAll(tk, "bgm_99796")
		x := a[0]
		x.Episode = stated
		a[0] = x
		if d := decideRefine(ms, tk, a)[0]; d.Status != StatusApplied || d.Key != "13" {
			t.Fatalf("sort13: %+v", d)
		}
	}
	_, _ = ms.W.Exec(`UPDATE episodes SET sort=1 WHERE work_key=?`, w)
	for _, bad := range []string{
		"Yuru Yuri Nachuyachumi! - 02.mkv", "Yuru Yuri Nachuyachumi! E02E03.mkv",
		"Yuru Yuri Nachuyachumi! Part 2.mkv", "Yuru Yuri Nachuyachumi! IV.mkv",
		"Yuru Yuri Nachuyachumi! NCOP.mkv", "Yuru Yuri Nachuyachumi! Interview.mkv",
		"Extras/Yuru Yuri Nachuyachumi!.mkv", "Yuru Yuri Nachuyachumi! SP.mkv",
	} {
		tk := refineTask(t, ms, "Yuru Yuri collection", "2014-12-01T00:00:00Z", []string{bad}, w)
		a := answerAll(tk, "bgm_99796", 1)
		d := DecideLLM(ctx, tk, a, DefaultLLMThresholds, ms.ValidEpisode, TaskEvidence(ms, tk))[0]
		if d.Status == StatusApplied {
			t.Errorf("false singleton %s: %+v", bad, d)
		}
	}
	// Two unnumbered main answers cannot both claim the same single official chapter.
	tk := refineTask(t, ms, "Yuru Yuri collection", "2014-12-01T00:00:00Z", []string{path, path}, w)
	for _, d := range decideRefine(ms, tk, answerAll(tk, "bgm_99796", 1, 1)) {
		if d.Status == StatusApplied {
			t.Fatal("duplicate unnumbered mains")
		}
	}
	// The implicit number never bypasses confidence / evidence / release gates.
	tk = refineTask(t, ms, "Yuru Yuri collection", "2014-12-01T00:00:00Z", []string{path}, w)
	a := answerAll(tk, "bgm_99796", 1)
	low := a[0]
	low.Confidence = 0.6
	a[0] = low
	if d := decideRefine(ms, tk, a)[0]; d.Status != StatusPending {
		t.Fatal("confidence gate bypassed")
	}
	a = answerAll(tk, "bgm_99796", 1)
	if d := DecideLLM(ctx, tk, a, DefaultLLMThresholds, ms.ValidEpisode, func(context.Context, string) bool { return false })[0]; d.Reason != ReasonNoName {
		t.Fatal("name gate bypassed")
	}
	tk.Released = "2014-01-01T00:00:00Z"
	if d := decideRefine(ms, tk, a)[0]; d.Status != StatusPending {
		t.Fatal("early gate bypassed")
	}
	// No official chapter list is not evidence, even when works.eps is 1.
	_, _ = ms.W.Exec(`DELETE FROM episodes WHERE work_key=?`, w)
	tk = refineTask(t, ms, "Yuru Yuri collection", "2014-12-01T00:00:00Z", []string{path}, w)
	if d := decideRefine(ms, tk, answerAll(tk, "bgm_99796", 1))[0]; d.Status == StatusApplied {
		t.Fatal("planned singleton accepted")
	}
	// Multiple official chapters: no implicit number.
	_, _ = ms.W.Exec(`INSERT INTO episodes(bgm_id,work_key,type,sort) VALUES(1,?,0,1),(2,?,0,2)`, w, w)
	tk = refineTask(t, ms, "Yuru Yuri collection", "2014-12-01T00:00:00Z", []string{path}, w)
	if d := decideRefine(ms, tk, answerAll(tk, "bgm_99796", 1))[0]; d.Status == StatusApplied {
		t.Fatal("multi chapter accepted")
	}
}
