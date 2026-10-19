package clef

import (
	"context"
	"math"
	"sort"

	"github.com/darknightlab/onlinetorrent/internal/meta"
)

// Decision metrics (see docs/research/decision-model-calibration.md).
const (
	MetricProb       = "prob"       // probability of the chosen option
	MetricConfidence = "confidence" // Clef's own confidence value
)

// Thresholds route a validated decision: score ≥ Apply and margin ≥ Margin → applied;
// score ≥ Review → review queue; otherwise discarded. score / margin are the minimum over
// the questions a file's decision depends on (subject, kind, episode number).
type Thresholds struct {
	Metric string  `json:"metric"`
	Apply  float64 `json:"apply"`
	Margin float64 `json:"margin"`
	Review float64 `json:"review"`
	// Raw: do not condition the subject on the episode existing (calibration only)
	Raw bool `json:"-"`
	// UseChoice: decide the subject by the choice question even when the yes/no answers
	// are there (calibration only)
	UseChoice bool `json:"-"`
	// IgnoreAge: apply decisions for old entries too (calibration only)
	IgnoreAge bool `json:"-"`
}

// DefaultThresholds are the calibrated defaults (docs/research/decision-model-calibration.md).
var DefaultThresholds = Thresholds{Metric: MetricProb, Apply: 0.8, Margin: 0.3, Review: 0.5}

// Validator checks an episode key of a work and returns its canonical form
// (meta.Store.ValidEpisode).
type Validator func(ctx context.Context, workKey string, k meta.EpisodeKey) (meta.EpisodeKey, bool)

// OptionP is one option with its probability.
type OptionP struct {
	ID string  `json:"id"`
	P  float64 `json:"p"`
}

// AnswerSummary is what is kept of an answer: choice, confidence, top options.
type AnswerSummary struct {
	Choice     string    `json:"choice"`
	Confidence float64   `json:"confidence"`
	Top        []OptionP `json:"top"`
	// Conditioned: subject probabilities after dropping candidates without the episode
	Conditioned []OptionP `json:"conditioned,omitempty"`
	// Note: the LLM's short reason
	Note string `json:"note,omitempty"`
}

// Summarize keeps the top 4 options of an answer.
func Summarize(a Answer) AnswerSummary {
	s := AnswerSummary{Choice: a.Choice, Confidence: a.Confidence}
	for id, p := range a.Probabilities {
		s.Top = append(s.Top, OptionP{id, p})
	}
	sort.Slice(s.Top, func(i, j int) bool {
		if s.Top[i].P != s.Top[j].P {
			return s.Top[i].P > s.Top[j].P
		}
		return s.Top[i].ID < s.Top[j].ID
	})
	if len(s.Top) > 4 {
		s.Top = s.Top[:4]
	}
	return s
}

// top1/top2 of an answer; the chosen option is the most probable one (falls back to the
// reported choice when probabilities are missing)
func probs(a Answer) (choice string, p1, p2 float64) {
	choice = a.Choice
	type kv struct {
		id string
		p  float64
	}
	var l []kv
	for id, p := range a.Probabilities {
		l = append(l, kv{id, p})
	}
	sort.Slice(l, func(i, j int) bool {
		if l[i].p != l[j].p {
			return l[i].p > l[j].p
		}
		return l[i].id < l[j].id
	})
	if len(l) > 0 {
		choice, p1 = l[0].id, l[0].p
	}
	if len(l) > 1 {
		p2 = l[1].p
	}
	return
}

// use2 records a conditioned answer: metric and margin from the conditioned
// probabilities, Clef's confidence as reported, both distributions kept for the audit.
func use2(d *FileDecision, id string, raw, cond Answer, th Thresholds) (string, bool) {
	c, p1, p2 := probs(cond)
	sum := Summarize(raw)
	if !th.Raw {
		sum.Conditioned = Summarize(cond).Top
	}
	d.Answers[id] = sum
	m := p1
	if th.Metric == MetricConfidence {
		m = raw.Confidence
	}
	d.Score = math.Min(d.Score, m)
	d.Margin = math.Min(d.Margin, p1-p2)
	d.Confidence = math.Min(d.Confidence, raw.Confidence)
	return c, true
}

// Statuses of a decision.
const (
	StatusApplied   = "applied"
	StatusPending   = "pending"
	StatusRejected  = "rejected"
	StatusDiscarded = "discarded"
)

// FileDecision is the outcome for one file.
type FileDecision struct {
	File       meta.ClefFile            `json:"file"`
	Candidate  *meta.ClefCandidate      `json:"candidate,omitempty"`
	Kind       string                   `json:"kind"`              // torrent_files.kind
	Episode    *float64                 `json:"episode,omitempty"` // number as written
	Key        string                   `json:"episode_key,omitempty"`
	Score      float64                  `json:"score"`
	Margin     float64                  `json:"margin"`
	Confidence float64                  `json:"confidence"`
	Answers    map[string]AnswerSummary `json:"answers"`
	Status     string                   `json:"status"` // applied (= would apply) | pending | discarded
	Reason     string                   `json:"reason,omitempty"`
}

// Discard reasons.
const (
	ReasonNoAnswer      = "no_answer"
	ReasonNone          = "none"
	ReasonBadSubject    = "invalid_subject"
	ReasonNotEpisode    = "not_episode"
	ReasonNoNumber      = "no_number"
	ReasonBadNumber     = "invalid_number"
	ReasonNotOfficial   = "episode_not_official"
	ReasonSeason        = "season_mismatch"
	ReasonOldEntry      = "old_entry" // pending: the entry ended long before the publish date
	ReasonLowConfidence = "low_confidence"
)

// Decide turns the answers of a task's requests into one decision per file: subject must be
// a candidate, the number one of the file's number tokens, the key (canonical, split-cour
// shift applied) an episode of the subject (valid); then thresholds route it.
func Decide(ctx context.Context, t *meta.ClefTask, answers map[string]Answer, th Thresholds, valid Validator) []FileDecision {
	cands := map[string]*meta.ClefCandidate{}
	for i := range t.Candidates {
		cands[t.Candidates[i].ID] = &t.Candidates[i]
	}
	// an unnumbered special (or a movie's main video) is number 1 only when it is the only
	// file of its kind for its subject in the torrent (same rule as ClassifyFiles)
	type sk struct{ subj, kind string }
	count := map[sk]int{}
	for _, f := range t.Files {
		s, _, _ := probs(answers[SubjectQuestion(t, f)])
		k, _, _ := probs(answers[qKind(f.Index)])
		count[sk{s, k}]++
	}
	out := make([]FileDecision, 0, len(t.Files))
	for _, f := range t.Files {
		d := FileDecision{File: f, Answers: map[string]AnswerSummary{}, Score: 1, Margin: 1, Confidence: 1}
		use := func(id string) (string, bool) {
			a, ok := answers[id]
			if !ok || len(a.Probabilities) == 0 {
				return "", false
			}
			c, p1, p2 := probs(a)
			d.Answers[id] = Summarize(a)
			m := p1
			if th.Metric == MetricConfidence {
				m = a.Confidence
			}
			d.Score = math.Min(d.Score, m)
			d.Margin = math.Min(d.Margin, p1-p2)
			d.Confidence = math.Min(d.Confidence, a.Confidence)
			return c, true
		}
		discard := func(r string) {
			d.Status, d.Reason = StatusDiscarded, r
			out = append(out, d)
		}
		kind, ok := use(qKind(f.Index))
		sa, okS := answers[SubjectQuestion(t, f)]
		if !ok || !okS || len(sa.Probabilities) == 0 {
			d.Score, d.Margin, d.Confidence = 0, 0, 0
			discard(ReasonNoAnswer)
			continue
		}
		d.Kind = FileKindOf(kind)
		if d.Kind == "" {
			use(SubjectQuestion(t, f))
			discard(ReasonNotEpisode)
			continue
		}
		// the episode number: a number token of the name, or 1 for the only unnumbered
		// file of its kind (specials; main only for a movie)
		var ep *float64
		defaultOne := false
		if len(f.Numbers) > 0 {
			num, ok := use(qEp(f.Index))
			if !ok {
				discard(ReasonNoAnswer)
				continue
			}
			if num != None {
				for _, n := range f.Numbers {
					if n.ID == num {
						v := n.Value
						ep = &v
					}
				}
				if ep == nil {
					use(SubjectQuestion(t, f))
					discard(ReasonBadNumber)
					continue
				}
			}
		}
		if ep == nil {
			rawSubj, _, _ := probs(sa)
			if count[sk{rawSubj, kind}] != 1 {
				use(SubjectQuestion(t, f))
				discard(ReasonNoNumber)
				continue
			}
			one := 1.0
			ep, defaultOne = &one, true
		}
		d.Episode = ep
		k, ok := meta.KeyOfFile(d.Kind, ep)
		if !ok {
			use(SubjectQuestion(t, f))
			discard(ReasonBadNumber)
			continue
		}
		// the subject, conditioned on the facts: candidates whose official episode list lacks
		// the key, or that are another TV season than the one the name states, get
		// probability 0; the rest (and none) are renormalised
		// (Thresholds.Raw: the model's probabilities as they are)
		cond := Answer{Type: sa.Type, Choice: sa.Choice, Confidence: sa.Confidence, Probabilities: map[string]float64{}}
		total := 0.0
		fits := map[string]bool{}
		for id, p := range sa.Probabilities {
			if c := cands[id]; c != nil {
				_, ok := valid(ctx, c.WorkKey, k)
				if defaultOne && d.Kind == "episode" && c.Type != "movie" {
					ok = false
				}
				ok = ok && c.SeasonFits(f.Season)
				fits[id] = ok
				if !ok && !th.Raw {
					p = 0
				}
			}
			cond.Probabilities[id] = p
			total += p
		}
		if total > 0 {
			for id, p := range cond.Probabilities {
				cond.Probabilities[id] = p / total
			}
		}
		// with the per-candidate yes/no answers (Options.Verify) the subject is the fitting
		// candidate the model confirms most; its yes-probability is the subject's score and
		// the gap to the next fitting candidate its margin (calibration: far better separated
		// than the choice probabilities). Without them: the conditioned choice.
		var subj string
		verify := map[string]float64{}
		for _, c := range t.Candidates {
			if v, ok := answers[VerifyQuestion(c.ID)]; ok && v.Noul != nil && (fits[c.ID] || th.Raw) {
				verify[c.ID] = *v.Noul
			}
		}
		if len(verify) > 0 && !th.UseChoice {
			vs := Answer{Type: "noul", Probabilities: verify}
			best, p1, p2 := probs(vs)
			sum := Summarize(sa)
			sum.Conditioned = Summarize(cond).Top
			d.Answers[SubjectQuestion(t, f)] = sum
			vsum := Summarize(vs)
			vsum.Choice, vsum.Confidence = best, p1
			d.Answers["verify"] = vsum
			d.Score = math.Min(d.Score, p1)
			d.Margin = math.Min(d.Margin, p1-p2)
			d.Confidence = math.Min(d.Confidence, sa.Confidence)
			subj = best
			if p1 < 0.5 {
				subj = None
			}
		} else {
			subj, _ = use2(&d, SubjectQuestion(t, f), sa, cond, th)
		}
		if subj == None {
			discard(ReasonNone)
			continue
		}
		c := cands[subj]
		if c == nil {
			discard(ReasonBadSubject)
			continue
		}
		d.Candidate = c
		ck, ok := valid(ctx, c.WorkKey, k)
		if defaultOne && d.Kind == "episode" && c.Type != "movie" {
			ok = false
		}
		if !c.SeasonFits(f.Season) {
			d.Key = ck.String()
			discard(ReasonSeason)
			continue
		}
		if !ok {
			d.Key = ck.String()
			discard(ReasonNotOfficial)
			continue
		}
		d.Key = ck.String()
		switch {
		case d.Score >= th.Apply && d.Margin >= th.Margin && !c.Recent(t.Published) && !th.IgnoreAge:
			// an entry that ended long before the torrent was published: a new work with the
			// same name may be missing from the catalogue (remakes, re-edits) — never applied
			// automatically
			d.Status, d.Reason = StatusPending, ReasonOldEntry
		case d.Score >= th.Apply && d.Margin >= th.Margin:
			d.Status = StatusApplied
		case d.Score >= th.Review:
			d.Status = StatusPending
		default:
			d.Status, d.Reason = StatusDiscarded, ReasonLowConfidence
		}
		out = append(out, d)
	}
	return out
}
