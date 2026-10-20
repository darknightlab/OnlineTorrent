package clef

// Refine: catalogue post-checks of the decision model's answers (both backends), after the
// per-file validation (DecideLLM / Decide). In order:
//
//  1. Season continuation (SAO "S04E13–E23"): an answer rejected because the stated season
//     doesn't fit the entry is accepted when the entry is the direct sequel of a prequel
//     whose numbering it continues (its first main sort = the prequel's last + 1, the file
//     number written in that continuation), and files of the same torrent with the same
//     stated season are mapped to that prequel (stored mapping or an applied answer).
//     Release names count seasons differently than bangumi; the torrent itself settles it.
//  2. Prequel numbering (归还者 S2 "[14]"): for a stated season ≥ 2, a number the entry
//     doesn't list — or one that can't have aired yet at the release — is retried as the
//     number minus the direct prequel's main episode count (14 − 12 = 2); taken only when
//     that episode is listed and passes the date gate.
//  3. Date gate (meta.ReleaseCheck): no release time, or the release more than 7 days
//     before / 30 days after the episode's airdate → review (日期不符 / 缺少发布时间). A main
//     episode without an airdate counts as not aired when an earlier one airs later.
//  4. Batch rule: a late release (never an early one) is applied anyway for a re-release
//     batch: ≥ 3 main files of the torrent on the same work, each passing every other
//     check, whose episode numbers (with the stated season) fit no other named work among
//     the candidates and the work's family (meta.UniqueFit; after a season continuation the
//     continuation is that proof), and any year written in the title / those paths lies in
//     the work's airing years. Its specials that were held only for being late follow.

import (
	"context"
	"fmt"
	"strings"

	"github.com/darknightlab/onlinetorrent/internal/meta"
)

// Refine runs the post-checks on ds (in place). th routes answers an override revives.
func Refine(ctx context.Context, ms *meta.Store, t *meta.ClefTask, ds []FileDecision, th LLMThresholds) {
	ev := TaskEvidence(ms, t)
	continued := map[int]bool{}
	// 1. season continuation
	for i := range ds {
		d := &ds[i]
		if d.Status == StatusDiscarded && d.Reason == ReasonSeason && seasonContinues(ctx, ms, t, ds, d) {
			continued[i] = true
			d.Answers["rule"] = AnswerSummary{Choice: d.Candidate.ID + " episode " + d.Key, Note: "季数写法不同：同一种子中同季的文件属于前作，本条目接续前作编号"}
			gateLLM(ctx, d, th, ev, numberAgrees(ctx, d, ms.ValidEpisode))
		}
	}
	// 2. prequel numbering for numbers the entry doesn't list
	for i := range ds {
		if d := &ds[i]; d.Status == StatusDiscarded && d.Reason == ReasonNotOfficial {
			prequelShift(ctx, ms, t, d, th, ev)
		}
	}
	// 3. date gate
	late := map[int]bool{}
	early := map[string]bool{} // works with an early (not aired) answer in this torrent
	for i := range ds {
		d := &ds[i]
		if d.Status != StatusApplied || d.Candidate == nil {
			continue
		}
		k, ok := meta.ParseEpisodeKey(d.Key)
		if !ok {
			continue
		}
		r, isLate := ms.ReleaseCheck(ctx, t, d.Candidate.WorkKey, k)
		if r == "" {
			continue
		}
		if strings.HasPrefix(r, meta.ReasonDateMismatch) && !isLate && prequelShift(ctx, ms, t, d, th, ev) {
			continue
		}
		d.Status, d.Reason = StatusPending, r
		late[i] = isLate
		if strings.HasPrefix(r, meta.ReasonDateMismatch) && !isLate {
			early[d.Candidate.WorkKey] = true
		}
	}
	// 4. batch rule
	batchRule(ctx, ms, t, ds, late, early, continued)
}

// seasonContinues: rule 1 of Refine.
func seasonContinues(ctx context.Context, ms *meta.Store, t *meta.ClefTask, ds []FileDecision, d *FileDecision) bool {
	if d.Candidate == nil || d.Kind != "episode" || d.Episode == nil || d.File.Season <= 0 {
		return false
	}
	w := d.Candidate.WorkKey
	p, ok := ms.DirectPrequel(ctx, w)
	if !ok {
		return false
	}
	_, phi, pn := ms.MainRange(ctx, p)
	wlo, _, wn := ms.MainRange(ctx, w)
	if pn == 0 || wn == 0 || wlo != phi+1 || *d.Episode < wlo {
		return false
	}
	if ck, ok := ms.ValidEpisode(ctx, w, meta.EpisodeKey{Kind: meta.KeyMain, Number: *d.Episode}); !ok || ck.Number != *d.Episode {
		return false // numbered as written in the continuation, not shifted
	}
	// same-torrent evidence: a file with the same stated season mapped to the prequel
	for _, o := range ds {
		if o.Status == StatusApplied && o.Candidate != nil && o.Candidate.WorkKey == p && o.File.Season == d.File.Season {
			return true
		}
	}
	tm, err := ms.GetTorrentMap(ctx, t.Infohash)
	if err != nil {
		return false
	}
	ts := meta.TitleSeason(t.Title)
	for _, f := range tm.Files {
		if f.Work != nil && f.Work.Key == p && f.Status != "rejected" && f.Kind == "episode" && meta.StatedSeason(f.Path, ts) == d.File.Season {
			return true
		}
	}
	return false
}

// prequelShift: rule 2 of Refine. Reports whether d was changed to the shifted number.
func prequelShift(ctx context.Context, ms *meta.Store, t *meta.ClefTask, d *FileDecision, th LLMThresholds, ev Evidence) bool {
	if d.Candidate == nil || d.Kind != "episode" || d.Episode == nil || d.File.Season < 2 {
		return false
	}
	w := d.Candidate.WorkKey
	p, ok := ms.DirectPrequel(ctx, w)
	if !ok {
		return false
	}
	plo, _, k := ms.MainRange(ctx, p)
	n := *d.Episode - float64(k)
	if k == 0 || plo != 1 || n < 1 {
		return false
	}
	ck, ok := ms.ValidEpisode(ctx, w, meta.EpisodeKey{Kind: meta.KeyMain, Number: n})
	if !ok {
		return false
	}
	if r, _ := ms.ReleaseCheck(ctx, t, w, ck); r != "" {
		return false
	}
	nd := *d
	nd.Episode, nd.Key = &n, ck.String()
	nd.Answers = map[string]AnswerSummary{}
	for k, v := range d.Answers {
		nd.Answers[k] = v
	}
	gateLLM(ctx, &nd, th, ev, true) // the number differs from the parser's on purpose
	if nd.Status != StatusApplied {
		return false
	}
	nd.Answers["rule"] = AnswerSummary{Choice: fmt.Sprintf("%s episode %s → %s", d.Candidate.ID, fmtNum(*d.Episode), nd.Key),
		Note: fmt.Sprintf("续作按前作 %d 集顺延编号：%s − %d", k, fmtNum(*d.Episode), k)}
	*d = nd
	return true
}

// batchRule: rule 4 of Refine.
func batchRule(ctx context.Context, ms *meta.Store, t *meta.ClefTask, ds []FileDecision, late map[int]bool, early map[string]bool, continued map[int]bool) {
	type group struct {
		mains, specials []int
		lateMain        bool
		override        bool
		seasons         map[float64]bool
	}
	groups := map[string]*group{}
	for i := range ds {
		d := &ds[i]
		if d.Candidate == nil || !(d.Status == StatusApplied || (d.Status == StatusPending && late[i])) {
			continue
		}
		g := groups[d.Candidate.WorkKey]
		if g == nil {
			g = &group{seasons: map[float64]bool{}}
			groups[d.Candidate.WorkKey] = g
		}
		if d.Kind != "episode" {
			if late[i] {
				g.specials = append(g.specials, i)
			}
			continue
		}
		g.mains = append(g.mains, i)
		g.lateMain = g.lateMain || late[i]
		g.override = g.override || continued[i]
		g.seasons[d.File.Season] = true
	}
	var others []string
	for _, c := range t.Candidates {
		others = append(others, c.WorkKey)
	}
	for w, g := range groups {
		if !g.lateMain || len(g.mains) < 3 || early[w] || len(g.seasons) != 1 {
			continue
		}
		var season float64
		for s := range g.seasons {
			season = s
		}
		texts := []string{t.Title}
		var nums []float64
		for _, i := range g.mains {
			texts = append(texts, ds[i].File.Path)
			nums = append(nums, *ds[i].Episode)
		}
		if !g.override && !ms.UniqueFit(ctx, w, others, nums, season, texts) {
			continue
		}
		if !ms.YearsFit(ctx, w, texts) {
			continue
		}
		for _, i := range append(g.mains, g.specials...) {
			if d := &ds[i]; d.Status == StatusPending && late[i] {
				d.Status, d.Reason = StatusApplied, ""
				d.Answers["rule"] = AnswerSummary{Choice: d.Candidate.ID + " episode " + d.Key, Note: fmt.Sprintf("合集 / 补档：%d 个正片文件只符合这一个条目，晚发布不影响判断", len(g.mains))}
			}
		}
	}
}
