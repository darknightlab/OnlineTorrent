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
//  3. Date gate (meta.ReleaseGate): no release time, or more than 7 days before airing →
//     review. For a single-video torrent, more than 30 days after airing is also reviewed;
//     multi-video compilations have no late limit. An undated main after a future-airing
//     earlier episode is not aired yet. Single-file split-entry overrides additionally
//     require a dated selected chapter and unambiguous official numbering continuation.

import (
	"context"
	"fmt"
	"strings"

	"github.com/darknightlab/onlinetorrent/internal/meta"
)

// Refine runs the post-checks on ds (in place). th routes answers an override revives.
func Refine(ctx context.Context, ms *meta.Store, t *meta.ClefTask, ds []FileDecision, th LLMThresholds) {
	ev := TaskEvidence(ms, t)
	// Official split-entry continuity can prove a single-file season answer without
	// needing another file in this torrent (Chitose S01E15, cour 2).
	for i := range ds {
		d := &ds[i]
		if d.Status != StatusDiscarded || d.Reason != ReasonSeason || d.Candidate == nil || d.Kind != "episode" || d.Episode == nil {
			continue
		}
		k, valid := ms.ValidEpisode(ctx, d.Candidate.WorkKey, meta.EpisodeKey{Kind: meta.KeyMain, Number: *d.Episode})
		if valid && ms.ContinuationSeasonFits(ctx, t, d.Candidate, d.File.Season, k) {
			d.Key = k.String()
			gateLLM(ctx, d, th, ev, numberAgrees(ctx, d, ms.ValidEpisode, false))
		}
	}
	// 1. season continuation
	for i := range ds {
		d := &ds[i]
		if d.Status == StatusDiscarded && d.Reason == ReasonSeason && seasonContinues(ctx, ms, t, ds, d) {
			d.Answers["rule"] = AnswerSummary{Choice: d.Candidate.ID + " episode " + d.Key, Note: "季数写法不同：同一种子中同季的文件属于前作，本条目接续前作编号"}
			gateLLM(ctx, d, th, ev, numberAgrees(ctx, d, ms.ValidEpisode, false))
		}
	}
	// 2. prequel numbering for numbers the entry doesn't list
	for i := range ds {
		if d := &ds[i]; d.Status == StatusDiscarded && d.Reason == ReasonNotOfficial {
			prequelShift(ctx, ms, t, d, th, ev)
		}
	}
	// 3. date gate
	for i := range ds {
		d := &ds[i]
		if d.Status != StatusApplied || d.Candidate == nil {
			continue
		}
		k, ok := meta.ParseEpisodeKey(d.Key)
		if !ok {
			continue
		}
		r := ms.ReleaseGate(ctx, t, d.Candidate.WorkKey, k)
		if r == "" {
			continue
		}
		if strings.HasPrefix(r, meta.ReasonDateMismatch) && prequelShift(ctx, ms, t, d, th, ev) {
			continue
		}
		d.Status, d.Reason = StatusPending, r
	}
}

// seasonContinues: rule 1 of Refine.
func seasonContinues(ctx context.Context, ms *meta.Store, t *meta.ClefTask, ds []FileDecision, d *FileDecision) bool {
	if d.Candidate == nil || d.Candidate.SplitEntry || d.Kind != "episode" || d.Episode == nil || d.File.Season <= 0 {
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
	if r := ms.ReleaseGate(ctx, t, w, ck); r != "" {
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
