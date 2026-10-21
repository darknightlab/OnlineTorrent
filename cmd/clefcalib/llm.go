package main

// LLM backend calibration (OpenAI-compatible gateway). Samples as for Clef (sample /
// buildSample), but the candidates come from the production retrieval without the rules'
// anchor / hints (meta.SearchOnlyCandidates), so the model sees what it would see for an
// uncertain file:
//
//   - pos:         real retrieval; the truth is whatever the stored label says (if retrieval
//     missed it, the right answer is null)
//   - neg:         real retrieval as if the true work weren't in the catalogue yet (a new
//     show / season): only siblings, namesakes and remakes come back — realistic negatives
//   - neg-removed: the old way: the true work taken out of the rule-built candidate list
//
//	LLM_API_KEY=… clefcalib llm -meta-db /tmp/calib/meta.db -out v2-s7.jsonl -pos 40 -neg 15 -neg-removed 5 -seed 7
//	clefcalib analyze -meta-db /tmp/calib/meta.db [-llm-dump] v2-s7.jsonl

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"sort"
	"sync"
	"time"

	"github.com/darknightlab/onlinetorrent/internal/clef"
	"github.com/darknightlab/onlinetorrent/internal/meta"
)

func runLLM(args []string) error {
	fs := flag.NewFlagSet("llm", flag.ExitOnError)
	dbPath := fs.String("meta-db", "", "copy of meta.db")
	out := fs.String("out", "llm.jsonl", "output (appended)")
	base := fs.String("base", clef.DefaultLLMBaseURL, "API base URL")
	model := fs.String("model", clef.DefaultLLMModel, "model")
	effort := fs.String("effort", "medium", "reasoning_effort (default = not sent)")
	nPos := fs.Int("pos", 40, "positive torrents (real retrieval)")
	nNeg := fs.Int("neg", 15, "negatives: real retrieval without the true work")
	nRem := fs.Int("neg-removed", 5, "negatives: true work removed from the rule-built candidates")
	seed := fs.Int64("seed", 1, "sample seed")
	conc := fs.Int("concurrency", 2, "parallel torrents")
	_ = fs.Parse(args)
	key := os.Getenv("LLM_API_KEY")
	if key == "" {
		return errors.New("LLM_API_KEY not set")
	}
	ctx := context.Background()
	ms, err := meta.Open(*dbPath)
	if err != nil {
		return err
	}
	defer ms.Close()
	samples, err := sample(ctx, ms, *seed)
	if err != nil {
		return err
	}
	// sets in a fixed rotation over the shuffled samples
	want := map[string]int{"pos": *nPos, "neg": *nNeg, "neg-removed": *nRem}
	got := map[string]int{}
	order := []string{"pos", "pos", "neg", "pos", "pos", "neg", "pos", "neg-removed"}
	var recs []*record
	i := 0
	for _, sm := range samples {
		if got["pos"] >= want["pos"] && got["neg"] >= want["neg"] && got["neg-removed"] >= want["neg-removed"] {
			break
		}
		set := ""
		for j := 0; j < len(order); j++ {
			if s := order[(i+j)%len(order)]; got[s] < want[s] {
				set = s
				break
			}
		}
		i++
		if !labelsOK(ctx, ms, sm.truth) {
			continue
		}
		rec := llmRecord(ctx, ms, sm, set)
		if rec == nil {
			continue
		}
		got[set]++
		recs = append(recs, rec)
	}
	fmt.Fprintf(os.Stderr, "sample: %v\n", got)
	f, err := os.OpenFile(*out, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	var mu sync.Mutex
	enc := json.NewEncoder(f)
	cl := &clef.OpenAIClient{BaseURL: *base, APIKey: key}
	jobs := make(chan *record)
	var wg sync.WaitGroup
	done := 0
	for w := 0; w < *conc; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for rec := range jobs {
				rec.Model, rec.Mode, rec.Provider, rec.Effort = *model, "llm", "openai", *effort
				excluded := map[string]bool{}
				for _, t := range rec.Removed {
					excluded[t] = true
				}
				search := func(ctx context.Context, title string, have map[string]bool) []meta.ClefCandidate {
					h := map[string]bool{}
					for k := range have {
						h[k] = true
					}
					for k := range excluded {
						h[k] = true
					}
					return ms.CandidatesForTitle(ctx, title, h, 10)
				}
				chat := func(ctx context.Context, req clef.ChatRequest) (*clef.ChatResult, error) {
					if *effort != "default" {
						req.ReasoningEffort = *effort
					}
					var res *clef.ChatResult
					var err error
					for try := 0; try < 3; try++ {
						cctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
						res, err = cl.Chat(cctx, req)
						cancel()
						if err == nil || !clef.Retryable(err) {
							break
						}
						time.Sleep(time.Duration(5<<try) * time.Second)
					}
					return res, err
				}
				start := time.Now()
				run, err := clef.RunLLM(ctx, chat, *model, rec.Task, search)
				rec.WallMS = time.Since(start).Milliseconds()
				rec.LLM = run
				if run != nil {
					rec.Requests = run.Requests
					rec.Usage = clef.Usage{InputTokens: run.PromptTokens, OutputTokens: run.CompletionTokens}
				}
				if err != nil {
					rec.Err = err.Error()
				}
				mu.Lock()
				_ = enc.Encode(rec)
				done++
				if done%10 == 0 {
					fmt.Fprintf(os.Stderr, "llm %d/%d\n", done, len(recs))
				}
				mu.Unlock()
			}
		}()
	}
	for _, r := range recs {
		jobs <- r
	}
	close(jobs)
	wg.Wait()
	return nil
}

func labelsOK(ctx context.Context, ms *meta.Store, tr map[int]truth) bool {
	for _, t := range tr {
		if !labelOK(ctx, ms, "pos", t) {
			return false
		}
	}
	return true
}

// llmRecord builds the task of a sample for a set (see the file comment).
func llmRecord(ctx context.Context, ms *meta.Store, sm *sampleT, set string) *record {
	if set == "neg-removed" {
		r := sm.record("neg")
		if r != nil {
			r.Set = "neg-removed"
		}
		return r
	}
	t := *sm.task
	tr := map[int]truth{}
	truthKeys := map[string]bool{}
	for k, v := range sm.truth {
		tr[k] = v
		if v.Subject != "" {
			truthKeys[v.Subject] = true
		}
	}
	paths := make([]string, len(t.Files))
	for i, f := range t.Files {
		paths[i] = f.Path
	}
	var removed []string
	if set == "neg" {
		t.Candidates = ms.SearchOnlyCandidates(ctx, t.Title, paths, t.Published, truthKeys)
		for k := range truthKeys {
			removed = append(removed, k)
		}
		sort.Strings(removed)
		for k, v := range tr {
			v.Subject, v.Key = "", ""
			tr[k] = v
		}
	} else {
		t.Candidates = ms.SearchOnlyCandidates(ctx, t.Title, paths, t.Published)
		in := map[string]bool{}
		for _, c := range t.Candidates {
			in[c.WorkKey] = true
		}
		for k, v := range tr {
			if v.Subject != "" && !in[v.Subject] {
				v.Subject, v.Key = "", "" // retrieval missed it: null is right
				tr[k] = v
			}
		}
	}
	if len(t.Candidates) == 0 {
		return nil
	}
	return &record{Set: set, Stratum: sm.stratum, Task: &t, Truth: tr, Removed: removed}
}

// ---- analysis ----

type llmRes struct {
	set      string
	posLabel bool    // a mappable truth (subject + kind)
	conf     float64 // self-reported
	raw      string  // model alone: applied (valid answer) | none
	rawOK    bool    // the model-alone outcome is right
	final    string  // with all gates: applied | pending | discarded
	finalOK  bool    // applied & right, or not applied
	reason   string  // review / discard reason with gates
	dateHit  bool    // the date gate sent it to review
}

func analyzeLLM(ctx context.Context, ms *meta.Store, path string, recs []record) {
	var rs []llmRes
	var prompt, compl, reqs, rounds2, errs, n int64
	var walls []int64
	sets := map[string]int{}
	for _, r := range recs {
		if r.Err != "" || r.LLM == nil {
			errs++
			continue
		}
		n++
		sets[r.Set]++
		walls = append(walls, r.WallMS)
		prompt += int64(r.LLM.PromptTokens)
		compl += int64(r.LLM.CompletionTokens)
		reqs += int64(r.LLM.Requests)
		if r.LLM.Rounds > 1 {
			rounds2++
		}
		for i := range r.Task.Files {
			if r.Task.Files[i].Kind == "" {
				r.Task.Files[i].Kind = meta.ParseEpisode(r.Task.Files[i].Path).Kind
			}
		}
		// release time as production (crawl published_at / submitted_at)
		if rt, by := ms.ReleaseTime(ctx, r.Task.Infohash); !rt.IsZero() {
			r.Task.Released, r.Task.ReleasedBy = rt.UTC().Format(time.RFC3339), by
		}
		r.Task.Batch = ms.VideoFiles(ctx, r.Task.Infohash) >= 2 // the real torrent, not the sample
		for i := range r.Task.Candidates {
			r.Task.Candidates[i].OfficialSingleton = ms.OfficialSingleton(ctx, r.Task.Candidates[i].WorkKey)
		}
		// model alone: every validated answer is applied (no threshold, no gates)
		alone := clef.DecideLLM(ctx, r.Task, r.LLM.Answers, clef.LLMThresholds{}, ms.ValidEpisode, nil)
		gated := clef.DecideLLM(ctx, r.Task, r.LLM.Answers, *llmTh, ms.ValidEpisode, clef.TaskEvidence(ms, r.Task))
		preDate := make([]string, len(gated))
		for i, d := range gated {
			preDate[i] = d.Status
		}
		clef.Refine(ctx, ms, r.Task, gated, *llmTh)
		for i, d := range alone {
			t := r.Truth[d.File.Index]
			x := llmRes{set: r.Set, conf: d.Confidence, posLabel: t.Subject != "" && t.Kind != ""}
			right := func(fd clef.FileDecision) bool {
				return fd.Candidate != nil && fd.Candidate.WorkKey == t.Subject && fd.Kind == t.Kind && fd.Key == t.Key
			}
			if d.Status == clef.StatusApplied {
				x.raw, x.rawOK = "applied", right(d)
			} else {
				x.raw, x.rawOK = "none", !x.posLabel
			}
			g := gated[i]
			x.final, x.reason = g.Status, g.Reason
			x.dateHit = preDate[i] == clef.StatusApplied && g.Status == clef.StatusPending
			if g.Status == clef.StatusApplied {
				x.finalOK = right(g)
			} else {
				x.finalOK = !x.posLabel
			}
			if *dumpLLM && ((x.raw == "applied" && !x.rawOK) || (g.Status == clef.StatusApplied && !x.finalOK)) {
				got, want := "", t.Subject
				if d.Candidate != nil {
					got = d.Candidate.Title + " (" + d.Candidate.AirDate + ")"
				}
				for _, c := range r.Task.Candidates {
					if c.WorkKey == t.Subject {
						want = c.Title + " (" + c.AirDate + ")"
					}
				}
				fmt.Fprintf(os.Stderr, "WRONG %s conf %.2f gated=%s/%s %s\n   file %s\n   got %s %s %s | want %s %s %s | %s\n", r.Set, x.conf, g.Status, g.Reason, r.Task.Title, d.File.Path, got, d.Kind, d.Key, want, t.Kind, t.Key, d.Answers["llm"].Note)
			}
			rs = append(rs, x)
		}
	}
	sort.Slice(walls, func(i, j int) bool { return walls[i] < walls[j] })
	q := func(p float64) float64 {
		if len(walls) == 0 {
			return 0
		}
		return float64(walls[int(p*float64(len(walls)-1))]) / 1000
	}
	fmt.Printf("## %s\n\n%d torrents %v (%d errors), %d requests, second round in %d torrents.\n", path, n, sets, errs, reqs, rounds2)
	fmt.Printf("Per torrent: %.0f prompt + %.0f completion tokens; latency median %.1f s, p90 %.1f s, max %.1f s.\n\n",
		float64(prompt)/float64(max(n, 1)), float64(compl)/float64(max(n, 1)), q(0.5), q(0.9), q(1))

	type agg struct{ files, mappable, applied, good, wrongPos, wrongNeg, cover int }
	tally := func(set string, gatedMode bool) agg {
		var a agg
		for _, x := range rs {
			if set != "all" && x.set != set {
				continue
			}
			a.files++
			if x.posLabel {
				a.mappable++
			}
			applied, ok := x.raw == "applied", x.rawOK
			if gatedMode {
				applied, ok = x.final == clef.StatusApplied, x.finalOK
			}
			if !applied {
				continue
			}
			a.applied++
			if ok {
				a.good++
				if x.posLabel {
					a.cover++
				}
			} else if x.set == "pos" {
				a.wrongPos++
			} else {
				a.wrongNeg++
			}
		}
		return a
	}
	fmt.Println("| set | mode | files | mappable | applied | precision | coverage | wrong |")
	fmt.Println("|---|---|---|---|---|---|---|---|")
	for _, set := range []string{"pos", "neg", "neg-removed", "all"} {
		for _, m := range []bool{false, true} {
			a := tally(set, m)
			if a.files == 0 {
				continue
			}
			mode := "model alone"
			if m {
				mode = "with gates"
			}
			fmt.Printf("| %s | %s | %d | %d | %d | %.1f%% | %.1f%% | %d |\n", set, mode, a.files, a.mappable, a.applied, pct(a.good, a.applied), pct(a.cover, a.mappable), a.wrongPos+a.wrongNeg)
		}
	}
	// what the gates did
	reasons := map[string][2]int{} // reason → [right to review, caught a wrong one]
	dateRight, dateWrong := 0, 0
	for _, x := range rs {
		if x.raw != "applied" || x.final == clef.StatusApplied {
			continue
		}
		r := x.reason
		if i := len(r); i > 0 {
			for j := 0; j < len(r); j++ {
				if r[j] == ':' {
					r = r[:j]
					break
				}
			}
		}
		v := reasons[r]
		if x.rawOK {
			v[0]++
		} else {
			v[1]++
		}
		reasons[r] = v
		if x.dateHit {
			if x.rawOK {
				dateRight++
			} else {
				dateWrong++
			}
		}
	}
	fmt.Println("\nGates (answers the model alone would apply, held back by a gate): reason → right answer held back / wrong answer caught")
	fmt.Println()
	keys := make([]string, 0, len(reasons))
	for k := range reasons {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Printf("- %s: %d / %d\n", k, reasons[k][0], reasons[k][1])
	}
	fmt.Printf("\nDate gate alone: %d right answers sent to review, %d wrong answers caught.\n\n", dateRight, dateWrong)

	bands := []float64{0, 0.5, 0.9, 0.98, 1.0001}
	fmt.Println("| confidence | answers applied by the model alone | right |")
	fmt.Println("|---|---|---|")
	for i := 0; i+1 < len(bands); i++ {
		c, ok := 0, 0
		for _, x := range rs {
			if x.raw == "applied" && x.conf >= bands[i] && x.conf < bands[i+1] {
				c++
				if x.rawOK {
					ok++
				}
			}
		}
		fmt.Printf("| %.2f–%.2f | %d | %d (%.1f%%) |\n", bands[i], min(bands[i+1], 1), c, ok, pct(ok, c))
	}
	fmt.Println()
}

var (
	dumpLLM = new(bool)
	llmTh   = &clef.DefaultLLMThresholds
)
