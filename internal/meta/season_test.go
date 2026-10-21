package meta

import (
	"context"
	"testing"
)

func TestOfficialContinuationProof(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*Store, string, string)
		want   bool
	}{
		{"clean", nil, true},
		{"undated overlap", func(s *Store, p, w string) {
			s.W.Exec(`INSERT INTO episodes(bgm_id,work_key,type,sort,airdate) VALUES(9999,?,0,3,'')`, w)
		}, true},
		{"dated overlap", func(s *Store, p, w string) {
			s.W.Exec(`INSERT INTO episodes(bgm_id,work_key,type,sort,airdate) VALUES(9999,?,0,3,'2026-04-01')`, w)
		}, false},
		{"gap", func(s *Store, p, w string) { s.W.Exec(`DELETE FROM episodes WHERE work_key=? AND sort=4`, w) }, false},
		{"no boundary airdate", func(s *Store, p, w string) { s.W.Exec(`UPDATE episodes SET airdate='' WHERE work_key=? AND sort=4`, w) }, false},
		{"boundary before prequel", func(s *Store, p, w string) {
			s.W.Exec(`UPDATE episodes SET airdate='2025-01-01' WHERE work_key=? AND sort=4`, w)
		}, false},
		{"multiple prequels", func(s *Store, p, w string) {
			seedWork(t, s, 103, "別作品", "", `[]`, "tv", "2025-01-01", "", seq(1, 3), weekly("2025-01-01", 3))
			relate(t, s, 102, 103, 2)
		}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := openTest(t)
			ctx := context.Background()
			p := seedWork(t, s, 101, "Show", "", `[]`, "tv", "2025-01-01", "2025-01-15", seq(1, 3), weekly("2025-01-01", 3))
			// Deliberately NO cour/part in the target's title. Numbering, not a Part2 string,
			// is sufficient evidence for a corresponding split entry.
			w := seedWork(t, s, 102, "Show Arc", "", `["Show Arc"]`, "tv", "2026-04-01", "", seq(4, 6), weekly("2026-04-01", 3))
			relate(t, s, 102, 101, 2)
			if tc.mutate != nil {
				tc.mutate(s, p, w)
			}
			_, first, n, ok := s.officialContinuation(ctx, w)
			if ok != tc.want {
				t.Fatalf("proof %v first%v count%d", ok, first, n)
			}
			c, _ := s.CandidateFor(ctx, w)
			task := &ClefTask{Released: "2026-04-09T00:00:00Z", ReleasedBy: "published"}
			if got := s.ContinuationSeasonFits(ctx, task, &c, 1, EpisodeKey{Kind: KeyMain, Number: 5}); got != tc.want {
				t.Fatalf("season proof %v want %v", got, tc.want)
			}
		})
	}
}

func TestStatedSeasonNotCour(t *testing.T) {
	for text, want := range map[string]float64{"Show 第2部分": 0, "Show 第2クール": 0, "Show Part 2": 0, "Show S1 Part 2": 1, "Show 第二季 第2部分": 2} {
		if got := TitleSeason(text); got != want {
			t.Errorf("%s season %v want %v", text, got, want)
		}
	}
}

func TestWorkSeasonNotCour(t *testing.T) {
	for _, tc := range []struct {
		title, cn, alias string
		want             float64
	}{
		{"Show 第2クール", "Show 第2部分", `["Show Season 2"]`, 0},
		{"Show 2nd Season Part 2", "Show 第二季 第2部分", `[]`, 2},
		{"Show Part 2", "", `["Show Season 2"]`, 0},
		{"Show 第2期", "", `[]`, 2},
		{"Show 第弐幕", "", `[]`, 2},
	} {
		w := &Work{Title: tc.title, TitleCN: tc.cn}
		if tc.alias == `["Show Season 2"]` {
			w.Aliases = []string{"Show Season 2"}
		}
		if got := workSeason(w); got != tc.want {
			t.Errorf("%s %v want %v", tc.title, got, tc.want)
		}
	}
}
