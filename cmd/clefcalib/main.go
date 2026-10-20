// Command clefcalib calibrates the decision-model thresholds against the real Cloudflare
// Clef API (docs/research/decision-model-calibration.md). Run it on a COPY of a mapping database:
//
//	source ~/.config/onlinetorrent/cloudflare.env; source ~/.config/onlinetorrent/clef.env
//	clefcalib run -meta-db /tmp/calib/meta.db -out /tmp/calib/flash.jsonl -model clef-flash -pos 200 -neg 100
//	clefcalib run ... -mode separate -pos 60 -neg 40 -out /tmp/calib/flash-sep.jsonl
//	clefcalib analyze -meta-db /tmp/calib/meta.db /tmp/calib/flash.jsonl
//
// Samples: positives = torrents whose stored mapping the current rules reproduce with
// certainty ("certain"), or that were mapped by the old heuristics (season / folder / type
// routing, alias substring; "heuristic", labels = stored mapping); negatives = the same kind
// of torrent with every true subject removed from the candidates (right answer: none).
// Non-episode files (NCOP/NCED/PV/CM/extras) of the sampled torrents are included.
// Requires CF_ACCOUNT_ID / CF_AI_TOKEN (never printed). -dry builds requests without
// sending them.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"math/rand"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/darknightlab/onlinetorrent/internal/clef"
	"github.com/darknightlab/onlinetorrent/internal/meta"
)

type truth struct {
	Subject string   `json:"subject"` // work key, "" = none
	Kind    string   `json:"kind"`    // torrent_files.kind (extra/other → "")
	Episode *float64 `json:"episode"`
	Key     string   `json:"key"` // canonical key
}

type record struct {
	Set      string                 `json:"set"` // pos | neg
	Stratum  string                 `json:"stratum"`
	Model    string                 `json:"model"`
	Mode     string                 `json:"mode"`
	Task     *meta.ClefTask         `json:"task"`
	Truth    map[int]truth          `json:"truth"`
	Answers  map[string]clef.Answer `json:"answers"`
	Usage    clef.Usage             `json:"usage"`
	Requests int                    `json:"requests"`
	EstTok   int                    `json:"est_tokens"`
	Err      string                 `json:"err,omitempty"`
	// LLM backend
	Provider string       `json:"provider,omitempty"`
	LLM      *clef.LLMRun `json:"llm,omitempty"`
	WallMS   int64        `json:"wall_ms,omitempty"`
	Removed  []string     `json:"removed,omitempty"` // neg: the true subjects taken out of the candidates
	Effort   string       `json:"effort,omitempty"`
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: clefcalib run|analyze …")
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "run":
		err = run(os.Args[2:])
	case "replay":
		err = replay(os.Args[2:])
	case "recall":
		err = recall(os.Args[2:])
	case "llm":
		err = runLLM(os.Args[2:])
	case "analyze":
		err = analyze(os.Args[2:])
	default:
		err = fmt.Errorf("unknown command %q", os.Args[1])
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

var fileToOpt = map[string]string{"episode": "main", "sp": "sp", "op": "op", "ed": "ed", "pv": "pv", "cm": "cm"}

func run(args []string) error {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	dbPath := fs.String("meta-db", "", "copy of meta.db")
	out := fs.String("out", "calib.jsonl", "output (appended)")
	model := fs.String("model", clef.ModelFlash, "clef-flash|clef")
	mode := fs.String("mode", "joint", "joint|separate")
	verify := fs.Bool("verify", false, "add the per-candidate yes/no questions")
	nPos := fs.Int("pos", 100, "positive torrents")
	nNeg := fs.Int("neg", 50, "negative torrents")
	seed := fs.Int64("seed", 1, "sample seed")
	skip := fs.Int("skip", 0, "skip the first N sampled torrents (another batch)")
	maxReq := fs.Int("max-requests", 400, "stop after this many requests")
	rate := fs.Int("rate", 200, "requests per minute")
	dry := fs.Bool("dry", false, "build only")
	_ = fs.Parse(args)
	ctx := context.Background()
	ms, err := meta.Open(*dbPath)
	if err != nil {
		return err
	}
	defer ms.Close()
	cl := &clef.HTTPClient{AccountID: os.Getenv("CF_ACCOUNT_ID"), Token: os.Getenv("CF_AI_TOKEN")}
	if !*dry && (cl.AccountID == "" || cl.Token == "") {
		return errors.New("CF_ACCOUNT_ID / CF_AI_TOKEN not set")
	}
	samples, err := sample(ctx, ms, *seed)
	if err != nil {
		return err
	}
	if *skip < len(samples) {
		samples = samples[*skip:]
	}
	f, err := os.OpenFile(*out, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	sent, pos, neg := 0, 0, 0
	gap := time.Minute / time.Duration(*rate)
	var last time.Time
	for _, sm := range samples {
		if pos >= *nPos && neg >= *nNeg {
			break
		}
		set := "pos"
		// alternate: every third torrent becomes a negative until enough
		if neg < *nNeg && (pos >= *nPos || (pos+neg)%3 == 2) {
			set = "neg"
		}
		rec := sm.record(set)
		if rec == nil {
			continue
		}
		rec.Model, rec.Mode = *model, *mode
		if *verify {
			rec.Mode += "+verify"
		}
		builts := clef.Build(rec.Task, *model, clef.Options{Separate: *mode == "separate", Verify: *verify})
		if len(builts) == 0 {
			continue
		}
		if sent+len(builts) > *maxReq {
			break
		}
		rec.Answers = map[string]clef.Answer{}
		for _, b := range builts {
			rec.EstTok += b.EstTokens
			if *dry {
				continue
			}
			if w := time.Until(last.Add(gap)); w > 0 {
				time.Sleep(w)
			}
			last = time.Now()
			var resp *clef.Response
			for try := 0; try < 3; try++ {
				resp, err = cl.Run(ctx, b.Request)
				if err == nil || !clef.Retryable(err) {
					break
				}
				time.Sleep(time.Duration(5<<try) * time.Second)
			}
			sent++
			rec.Requests++
			if err != nil {
				rec.Err = err.Error()
				break
			}
			rec.Usage.InputTokens += resp.Usage.InputTokens
			for k, v := range resp.Answers {
				rec.Answers[k] = v
			}
		}
		if set == "pos" {
			pos++
		} else {
			neg++
		}
		if err := enc.Encode(rec); err != nil {
			return err
		}
		if (pos+neg)%10 == 0 {
			fmt.Fprintf(os.Stderr, "%s pos=%d neg=%d requests=%d\n", *model, pos, neg, sent)
		}
	}
	fmt.Fprintln(os.Stderr)
	return nil
}

type sampleT struct {
	stratum string
	task    *meta.ClefTask
	truth   map[int]truth
}

// record builds the request task of a sample (neg: the true subjects removed).
func (s *sampleT) record(set string) (rec *record) {
	t := *s.task
	t.Candidates = append([]meta.ClefCandidate(nil), s.task.Candidates...)
	tr := map[int]truth{}
	for k, v := range s.truth {
		tr[k] = v
	}
	if set == "neg" {
		drop := map[string]bool{}
		for _, v := range s.truth {
			if v.Subject != "" {
				drop[v.Subject] = true
			}
		}
		var keep []meta.ClefCandidate
		for _, c := range t.Candidates {
			if !drop[c.WorkKey] {
				keep = append(keep, c)
			}
		}
		if len(keep) == 0 {
			return nil
		}
		t.Candidates = keep
		removed := []string{}
		for k := range drop {
			removed = append(removed, k)
		}
		defer func() { rec.Removed = removed }()
		for k, v := range tr {
			v.Subject, v.Key = "", ""
			tr[k] = v
		}
	}
	rec = &record{Set: set, Stratum: s.stratum, Task: &t, Truth: tr}
	return rec
}

// sample lists candidate torrents (shuffled): stored mappings + the current rules' view.
func sample(ctx context.Context, ms *meta.Store, seed int64) ([]*sampleT, error) {
	rows, err := ms.R.QueryContext(ctx, `SELECT DISTINCT infohash FROM torrent_files WHERE work_key IS NOT NULL AND status = 'auto' ORDER BY infohash`)
	if err != nil {
		return nil, err
	}
	var ihs []string
	for rows.Next() {
		var ih string
		_ = rows.Scan(&ih)
		ihs = append(ihs, ih)
	}
	rows.Close()
	rand.New(rand.NewSource(seed)).Shuffle(len(ihs), func(i, j int) { ihs[i], ihs[j] = ihs[j], ihs[i] })
	var out []*sampleT
	for _, ih := range ihs {
		sm, err := buildSample(ctx, ms, ih)
		if err != nil {
			return nil, err
		}
		if sm != nil {
			out = append(out, sm)
		}
	}
	return out, nil
}

type stored struct {
	idx    int
	path   string
	kind   string
	work   string
	ep     *float64
	method string
}

func buildSample(ctx context.Context, ms *meta.Store, ih string) (*sampleT, error) {
	rows, err := ms.R.QueryContext(ctx, `SELECT file_index, path, kind, COALESCE(work_key,''), episode, method FROM torrent_files WHERE infohash = ? ORDER BY file_index`, ih)
	if err != nil {
		return nil, err
	}
	var files []stored
	for rows.Next() {
		var f stored
		if err := rows.Scan(&f.idx, &f.path, &f.kind, &f.work, &f.ep, &f.method); err != nil {
			rows.Close()
			return nil, err
		}
		files = append(files, f)
	}
	rows.Close()
	_, rules, err := ms.RuleMapping(ctx, ih)
	if err != nil {
		return nil, nil
	}
	task, err := ms.BuildClefTask(ctx, ih, true)
	if err != nil || len(task.Candidates) == 0 {
		return nil, nil
	}
	byIdx := map[int]meta.ClefFile{}
	for _, f := range task.Files {
		byIdx[f.Index] = f
	}
	certain, heuristic := 0, 0
	tr := map[int]truth{}
	anchor := ""
	var pick []meta.ClefFile
	var extras []meta.ClefFile
	videos := len(task.Videos)
	for i, f := range files {
		if !meta.IsVideo(f.path) {
			continue
		}
		cf, ok := byIdx[f.idx]
		switch {
		case f.work != "" && f.ep != nil && ok:
			r := rules[i]
			k, _ := meta.KeyOfFile(f.kind, f.ep)
			if r.WorkKey == f.work && r.Episode != nil && *r.Episode == *f.ep {
				certain++
			} else if strings.Contains(f.method, "+season") || strings.Contains(f.method, "+folder") || strings.Contains(f.method, "+type") || strings.HasPrefix(f.method, "alias") {
				heuristic++
			} else {
				continue // stored mapping the rules now doubt for other reasons: no label
			}
			if anchor == "" {
				anchor = f.work
			}
			ck := ms.CanonicalKey(ctx, f.work, k)
			tr[f.idx] = truth{Subject: f.work, Kind: f.kind, Episode: f.ep, Key: ck.String()}
			pick = append(pick, cf)
		case f.kind == "extra" || (f.work == "" && (f.kind == "op" || f.kind == "ed" || f.kind == "pv" || f.kind == "cm")):
			// non-episode / unmapped special: the kind is the label, subject = the anchor
			extras = append(extras, meta.ClefFile{Index: f.idx, Path: f.path, Kind: f.kind, Numbers: meta.NumberOptions(f.path, task.Title, videos == 1), Group: cf.Group})
		}
	}
	if len(pick) == 0 {
		return nil, nil
	}
	stratum := "certain"
	if heuristic > 0 && certain == 0 {
		stratum = "heuristic"
	} else if heuristic > 0 {
		stratum = "mixed"
	}
	// at most 3 episode files (spread over the torrent) + 1 extra
	if len(pick) > 3 {
		pick = []meta.ClefFile{pick[0], pick[len(pick)/2], pick[len(pick)-1]}
	}
	for _, e := range extras {
		if len(pick) >= 4 {
			break
		}
		kind := e.Kind
		if kind == "extra" {
			kind = ""
		}
		tr[e.Index] = truth{Subject: anchor, Kind: kind}
		pick = append(pick, e)
		break
	}
	keep := map[int]bool{}
	for _, p := range pick {
		keep[p.Index] = true
	}
	for k := range tr {
		if !keep[k] {
			delete(tr, k)
		}
	}
	// synthetic non-episode file (real ones are rare in the DB): the first episode file's
	// path with its episode number replaced by NCOP / NCED / PV / Menu / CM01
	if t0 := tr[pick[0].Index]; t0.Episode != nil {
		for _, n := range pick[0].Numbers {
			if n.Value != *t0.Episode {
				continue
			}
			txt := strings.TrimPrefix(n.ID, "n_")
			i := strings.LastIndex(pick[0].Path, txt)
			if end := i + len(txt); i <= 0 || !strings.ContainsRune(" -_[(#", rune(pick[0].Path[i-1])) || (end < len(pick[0].Path) && strings.ContainsRune("0123456789abcdefABCDEF", rune(pick[0].Path[end]))) {
				break
			}
			syn := synthetic[(int(ih[0])+int(ih[1]))%len(synthetic)]
			p := pick[0].Path[:i] + syn.marker + pick[0].Path[i+len(txt):]
			idx := 100000 + pick[0].Index
			task.Videos = append(task.Videos, p)
			pick = append(pick, meta.ClefFile{Index: idx, Path: p, Kind: meta.ParseEpisode(p).Kind, Numbers: meta.NumberOptions(p, task.Title, false), Group: pick[0].Group})
			t := truth{Subject: t0.Subject, Kind: syn.kind}
			if syn.kind != "" {
				one := syn.num
				t.Episode = &one
				k, _ := meta.KeyOfFile(syn.kind, &one)
				t.Key = k.String()
			}
			tr[idx] = t
			break
		}
	}
	task.Files = pick
	return &sampleT{stratum: stratum, task: task, truth: tr}, nil
}

var synthetic = []struct {
	marker, kind string
	num          float64
}{{"NCOP", "op", 1}, {"NCED", "ed", 1}, {"PV", "pv", 1}, {"Menu", "", 0}, {"CM01", "cm", 1}, {"NCOP2", "op", 2}, {"Preview", "", 0}}

// ---- analysis ----

type obs struct {
	set, stratum, q  string // q: subject | ep | kind
	correct          bool
	p1, margin, conf float64
}

type fileRes struct {
	set, stratum   string
	valid, correct bool // valid: survived validation (would be applied/reviewed at threshold 0)
	reason         string
	score, margin  float64 // prob metric
	conf           float64 // min confidence
	posLabel       bool    // truth is a mapping (pos episode/special file)
	old            bool    // the chosen entry is not recent (never auto-applied)
}

func analyze(args []string) error {
	fs := flag.NewFlagSet("analyze", flag.ExitOnError)
	dbPath := fs.String("meta-db", "", "copy of meta.db (for episode validation)")
	bApply := fs.Float64("apply", 0.8, "band table: apply threshold")
	bMargin := fs.Float64("margin", 0.3, "band table: margin")
	bReview := fs.Float64("review", 0.5, "band table: review threshold")
	fs.BoolVar(dumpLLM, "llm-dump", false, "print wrong valid LLM decisions (stderr)")
	dump := fs.Float64("dump", 0, "print wrong valid decisions with score ≥ this (stderr)")
	fs.BoolVar(&ignoreAge, "ignore-age", false, "count old entries as applied too")
	refresh := fs.Bool("refresh-seasons", false, "recompute candidate / stated seasons (records made before they existed)")
	_ = fs.Parse(args)
	ctx := context.Background()
	ms, err := meta.Open(*dbPath)
	if err != nil {
		return err
	}
	defer ms.Close()
	for _, path := range fs.Args() {
		recs, err := readRecs(path)
		if err != nil {
			return err
		}
		if len(recs) > 0 && recs[0].Provider == "openai" {
			analyzeLLM(ctx, ms, path, recs)
			continue
		}
		var obsl []obs
		variants := map[string][]fileRes{}
		tokens, reqs, errs, dropped := 0, 0, 0, 0
		for _, r := range recs {
			if r.Err != "" {
				errs++
				continue
			}
			tokens += r.Usage.InputTokens
			reqs += r.Requests
			obsl = append(obsl, questionObs(r)...)
			if *refresh {
				refreshSeasons(ctx, ms, r.Task)
			}
			for _, variant := range []string{"default", "choice", "raw"} {
				raw := variant == "raw"
				zero := clef.Thresholds{Metric: clef.MetricProb, Raw: raw, UseChoice: variant != "default"}
				for _, d := range clef.Decide(ctx, r.Task, r.Answers, zero, ms.ValidEpisode) {
					t := r.Truth[d.File.Index]
					if !labelOK(ctx, ms, r.Set, t) {
						if variant == "default" {
							dropped++
						}
						continue
					}
					fr := fileRes{set: r.Set, stratum: r.Stratum, reason: d.Reason, score: d.Score, margin: d.Margin, conf: d.Confidence, posLabel: t.Subject != "" && t.Kind != ""}
					fr.valid = d.Status != clef.StatusDiscarded
					fr.old = d.Candidate != nil && !d.Candidate.Recent(r.Task.Published)
					if fr.valid {
						fr.correct = d.Candidate != nil && d.Candidate.WorkKey == t.Subject && d.Kind == t.Kind && d.Key == t.Key
					} else {
						fr.correct = !fr.posLabel // not mapping is right when there is nothing to map
					}
					variants[variant] = append(variants[variant], fr)
					if variant == "default" && *dump > 0 && fr.valid && !fr.correct && fr.score >= *dump {
						got := ""
						if d.Candidate != nil {
							got = d.Candidate.Title
						}
						fmt.Fprintf(os.Stderr, "WRONG %s %.2f/%.2f %s\n   file %s\n   got %s %s %s | want %+v\n", r.Set, fr.score, fr.margin, r.Task.Title, d.File.Path, got, d.Kind, d.Key, t)
					}
				}
			}
		}
		fmt.Printf("## %s\n\n%d torrents (%d errors), %d requests, %d input tokens (%.0f / request)\n\n", path, len(recs), errs, reqs, tokens, float64(tokens)/float64(max(reqs, 1)))
		printQuestions(obsl)
		fmt.Printf("Labels dropped (stored mapping whose episode is not in the work's official list): %d files\n\n", dropped)
		fmt.Println("#### Default: subject conditioned on the facts (episode exists, season fits); yes/no answers when asked")
		fmt.Println()
		printDecisions(variants["default"])
		printBands(variants["default"], *bApply, *bMargin, *bReview)
		fmt.Println("#### Conditioned choice probabilities (yes/no answers ignored)")
		fmt.Println()
		printDecisions(variants["choice"])
		fmt.Println("#### Raw choice probabilities (no conditioning)")
		fmt.Println()
		printDecisions(variants["raw"])
	}
	return nil
}

func readRecs(path string) ([]record, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []record
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 64<<20)
	for sc.Scan() {
		var r record
		if err := json.Unmarshal(sc.Bytes(), &r); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, sc.Err()
}

func top(a clef.Answer) (string, float64, float64) {
	type kv struct {
		k string
		p float64
	}
	var l []kv
	for k, p := range a.Probabilities {
		l = append(l, kv{k, p})
	}
	sort.Slice(l, func(i, j int) bool { return l[i].p > l[j].p || (l[i].p == l[j].p && l[i].k < l[j].k) })
	if len(l) == 0 {
		return a.Choice, 0, 0
	}
	p2 := 0.0
	if len(l) > 1 {
		p2 = l[1].p
	}
	return l[0].k, l[0].p, p2
}

func questionObs(r record) []obs {
	var out []obs
	idOf := map[string]string{}
	for _, c := range r.Task.Candidates {
		idOf[c.WorkKey] = c.ID
	}
	for _, f := range r.Task.Files {
		t := r.Truth[f.Index]
		add := func(q, qid, want string) {
			a, ok := r.Answers[qid]
			if !ok {
				return
			}
			c, p1, p2 := top(a)
			out = append(out, obs{r.Set, r.Stratum, q, c == want, p1, p1 - p2, a.Confidence})
		}
		sq := clef.SubjectQuestion(r.Task, f)
		wantS := "none"
		if t.Subject != "" {
			wantS = idOf[t.Subject]
		}
		if sq == clef.QSubject {
			if f.Index == r.Task.Files[0].Index {
				add("subject", sq, wantS)
			}
		} else {
			add("subject", sq, wantS)
		}
		wantK := "other"
		if t.Kind != "" {
			wantK = fileToOpt[t.Kind]
		}
		add("kind", fmt.Sprintf("f%d_kind", f.Index), wantK)
		if len(f.Numbers) > 0 && t.Kind != "" {
			want := "none"
			if t.Episode != nil {
				for _, n := range f.Numbers {
					if n.Value == *t.Episode {
						want = n.ID
						break
					}
				}
			}
			qid := fmt.Sprintf("f%d_ep", f.Index)
			if a, ok := r.Answers[qid]; ok && t.Kind != "episode" && t.Episode != nil && *t.Episode == 1 {
				// an unnumbered special: "none" is right too (Decide numbers the only one 1)
				if c, _, _ := top(a); c == "none" {
					want = "none"
				}
			}
			add("ep", qid, want)
		}
	}
	return out
}

var bins = []float64{0, 0.5, 0.6, 0.7, 0.8, 0.9, 0.95, 0.98, 1.0001}

func printQuestions(l []obs) {
	for _, metric := range []string{"p1", "margin", "conf"} {
		fmt.Printf("### Question accuracy by %s\n\n| question | n | acc |", metric)
		for i := 0; i+1 < len(bins); i++ {
			fmt.Printf(" %.2f–%.2f |", bins[i], min(bins[i+1], 1))
		}
		fmt.Printf("\n|---|---|---|%s\n", strings.Repeat("---|", len(bins)-1))
		for _, q := range []string{"subject", "ep", "kind"} {
			var n, ok int
			cnt := make([]int, len(bins))
			cor := make([]int, len(bins))
			for _, o := range l {
				if o.q != q {
					continue
				}
				n++
				if o.correct {
					ok++
				}
				v := map[string]float64{"p1": o.p1, "margin": o.margin, "conf": o.conf}[metric]
				for i := 0; i+1 < len(bins); i++ {
					if v >= bins[i] && v < bins[i+1] {
						cnt[i]++
						if o.correct {
							cor[i]++
						}
					}
				}
			}
			fmt.Printf("| %s | %d | %.1f%% |", q, n, pct(ok, n))
			for i := 0; i+1 < len(bins); i++ {
				if cnt[i] == 0 {
					fmt.Print(" – |")
				} else {
					fmt.Printf(" %d/%d |", cor[i], cnt[i])
				}
			}
			fmt.Println()
		}
		fmt.Println()
	}
	// subject questions split by set (positives vs negatives)
	fmt.Printf("### Subject accuracy by set\n\n| set | n | acc | acc at p1≥0.9 (n) |\n|---|---|---|---|\n")
	for _, set := range []string{"pos", "neg"} {
		var n, ok, hn, hok int
		for _, o := range l {
			if o.q != "subject" || o.set != set {
				continue
			}
			n++
			if o.correct {
				ok++
			}
			if o.p1 >= 0.9 {
				hn++
				if o.correct {
					hok++
				}
			}
		}
		fmt.Printf("| %s | %d | %.1f%% | %.1f%% (%d) |\n", set, n, pct(ok, n), pct(hok, hn), hn)
	}
	fmt.Println()
}

func pct(a, b int) float64 {
	if b == 0 {
		return 0
	}
	return 100 * float64(a) / float64(b)
}

var ignoreAge bool

func printDecisions(fs []fileRes) {
	positives := 0
	for _, f := range fs {
		if f.posLabel && f.set == "pos" {
			positives++
		}
	}
	reasons := map[string]int{}
	for _, f := range fs {
		if !f.valid {
			reasons[f.set+":"+f.reason]++
		}
	}
	fmt.Printf("### Per-file decisions\n\n%d files (%d mappable positives). Discarded by validation / answer: %v\n\n", len(fs), positives, reasons)
	fmt.Println("Applied = valid ∧ metric ≥ apply ∧ margin ≥ m. Precision = correct mappings / applied; coverage = correct applied / mappable positives.")
	fmt.Println()
	fmt.Println("| metric | apply | margin | applied | precision | coverage | wrong (pos/neg) |")
	fmt.Println("|---|---|---|---|---|---|---|")
	for _, metric := range []string{"prob", "conf"} {
		for _, a := range []float64{0.5, 0.6, 0.7, 0.8, 0.85, 0.9, 0.95} {
			for _, m := range []float64{0, 0.3, 0.5, 0.7} {
				var applied, good, wrongP, wrongN int
				for _, f := range fs {
					v := f.score
					if metric == "conf" {
						v = f.conf
					}
					if !f.valid || v < a || f.margin < m || (f.old && !ignoreAge) {
						continue
					}
					applied++
					if f.correct {
						good++
					} else if f.set == "pos" {
						wrongP++
					} else {
						wrongN++
					}
				}
				fmt.Printf("| %s | %.2f | %.1f | %d | %.1f%% | %.1f%% | %d/%d |\n", metric, a, m, applied, pct(good, applied), pct(good, positives), wrongP, wrongN)
			}
		}
	}
	fmt.Println()
}

// refreshSeasons fills the season fields of an old record's task from the database.
func refreshSeasons(ctx context.Context, ms *meta.Store, t *meta.ClefTask) {
	fresh, err := ms.BuildClefTask(ctx, t.Infohash, true)
	if err != nil {
		return
	}
	season := map[string]int{}
	for _, c := range fresh.Candidates {
		season[c.WorkKey] = c.Season
	}
	end := map[string]string{}
	for _, c := range fresh.Candidates {
		end[c.WorkKey] = c.EndDate
	}
	for i := range t.Candidates {
		if v, ok := season[t.Candidates[i].WorkKey]; ok {
			t.Candidates[i].Season = v
			t.Candidates[i].EndDate = end[t.Candidates[i].WorkKey]
		}
	}
	if t.Published == "" {
		t.Published = fresh.Published
	}
	ts := meta.TitleSeason(t.Title)
	for i := range t.Files {
		t.Files[i].Season = meta.StatedSeason(t.Files[i].Path, ts)
	}
}

// labelOK: a stored (old-rule) mapping used as truth must itself be valid — an episode the
// work's official list has; heuristic mappings sometimes are not (an alias match to season 1
// for an episode of season 2).
func labelOK(ctx context.Context, ms *meta.Store, set string, t truth) bool {
	if set != "pos" || t.Subject == "" || t.Kind == "" || t.Episode == nil {
		return true
	}
	k, ok := meta.KeyOfFile(t.Kind, t.Episode)
	if !ok {
		return false
	}
	_, ok = ms.ValidEpisode(ctx, t.Subject, k)
	return ok
}

// printBands: outcome per band at the given thresholds (prob metric; old entries → review).
func printBands(fs []fileRes, apply, margin, review float64) {
	type band struct{ n, correct, posCorrect int }
	b := map[string]*band{"apply": {}, "review": {}, "discard": {}, "invalid": {}}
	positives := 0
	for _, f := range fs {
		if f.posLabel && f.set == "pos" {
			positives++
		}
		k := "invalid"
		switch {
		case !f.valid:
		case f.score >= apply && f.margin >= margin && !f.old:
			k = "apply"
		case f.score >= review:
			k = "review"
		default:
			k = "discard"
		}
		x := b[k]
		x.n++
		if f.correct {
			x.correct++
			if f.posLabel && f.set == "pos" && f.valid {
				x.posCorrect++
			}
		}
	}
	fmt.Printf("Bands at apply ≥ %.2f ∧ margin ≥ %.2f (old entries → review), review ≥ %.2f:\n\n", apply, margin, review)
	fmt.Println("| band | files | correct | precision | share of mappable positives |")
	fmt.Println("|---|---|---|---|---|")
	for _, k := range []string{"apply", "review", "discard", "invalid"} {
		x := b[k]
		cov := "–"
		if k != "invalid" {
			cov = fmt.Sprintf("%.1f%%", pct(x.posCorrect, positives))
		}
		fmt.Printf("| %s | %d | %d | %.1f%% | %s |\n", map[string]string{"apply": "apply (直接应用)", "review": "review (审核)", "discard": "discard (低于审核阈值)", "invalid": "not mapped (none / 非正片 / 校验失败)"}[k], x.n, x.correct, pct(x.correct, x.n), cov)
	}
	fmt.Println()
}
