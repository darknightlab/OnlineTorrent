package clef

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"

	"github.com/darknightlab/onlinetorrent/internal/meta"
	"github.com/darknightlab/onlinetorrent/internal/store"
)

func task(files int, perFile bool) *meta.ClefTask {
	t := &meta.ClefTask{Infohash: "ih", Title: "[G] Show - batch", PerFile: perFile, Candidates: []meta.ClefCandidate{
		{ID: "bgm_100", WorkKey: "bgm:100", Title: "ショー", TitleCN: "秀", Aliases: []string{"Show"}, AirDate: "2024-01-07", Eps: 12, Type: "tv"},
		{ID: "bgm_101", WorkKey: "bgm:101", Title: "ショー 第2期", Eps: 12, Type: "tv"},
	}}
	for i := 0; i < files; i++ {
		p := fmt.Sprintf("Show/[G] Show - %02d [1080p].mkv", i+1)
		t.Videos = append(t.Videos, p)
		t.Files = append(t.Files, meta.ClefFile{Index: i, Path: p, Kind: "episode", Numbers: meta.NumberOptions(p, t.Title, files == 1)})
	}
	return t
}

func TestBuildOptionsAndSplitting(t *testing.T) {
	// one file: one request, subject + ep + kind, option ids readable, "none" everywhere
	bs := Build(task(1, false), ModelFlash, Options{})
	if len(bs) != 1 || len(bs[0].Request.Questions) != 3 {
		t.Fatalf("builts: %+v", bs)
	}
	q := bs[0].Request.Questions
	subj := q[QSubject].Criteria.(map[string]any)
	if _, ok := subj["bgm_100"]; !ok || subj[None] == nil || len(subj) != 3 {
		t.Errorf("subject options %v", subj)
	}
	ep := q["f0_ep"].Criteria.(map[string]any)
	if _, ok := ep["n_01"]; !ok || ep["n_1080"] == nil || ep[None] == nil {
		t.Errorf("ep options %v", ep)
	}
	if k := q["f0_kind"].Criteria.(map[string]any); len(k) != 7 || k["main"] == nil || k["other"] == nil {
		t.Errorf("kind options %v", k)
	}
	st := bs[0].Request.State.(map[string]any)
	if st["a_torrent"] != "[G] Show - batch" || len(st["b_files"].([]string)) != 1 {
		t.Errorf("state %v", st)
	}
	b, _ := json.Marshal(bs[0].Request)
	if !strings.Contains(string(b), `"model":"clef-flash"`) || !strings.Contains(string(b), `"type":"choice"`) {
		t.Errorf("json %s", b)
	}

	// 40 files × 2 questions + the shared subject: split at 64 questions, every request
	// self-contained (repeats the subject question), every file covered once
	bs = Build(task(40, false), ModelFlash, Options{TokenBudget: 1 << 30})
	if len(bs) != 2 {
		t.Fatalf("40 files: %d requests", len(bs))
	}
	seen := map[int]bool{}
	for _, b := range bs {
		if n := len(b.Request.Questions); n > MaxQuestions {
			t.Errorf("%d questions", n)
		}
		if _, ok := b.Request.Questions[QSubject]; !ok {
			t.Error("chunk without subject question")
		}
		for _, fi := range b.Files {
			if seen[fi] {
				t.Errorf("file %d twice", fi)
			}
			seen[fi] = true
		}
	}
	if len(seen) != 40 || len(bs[0].Files) != 31 {
		t.Errorf("coverage %d, first chunk %d files", len(seen), len(bs[0].Files))
	}
	// per-file subjects: 3 questions per file → 21 files per request
	bs = Build(task(30, true), ModelFlash, Options{TokenBudget: 1 << 30})
	if len(bs) != 2 || len(bs[0].Files) != 21 || bs[0].Request.Questions["f0_subject"].Criteria == nil {
		t.Errorf("per-file: %d requests, %d files", len(bs), len(bs[0].Files))
	}
	if _, ok := bs[0].Request.Questions[QSubject]; ok {
		t.Error("per-file request has the torrent question")
	}
	// the token budget splits too
	if bs := Build(task(10, false), ModelFlash, Options{TokenBudget: 1500}); len(bs) < 2 {
		t.Errorf("budget split: %d", len(bs))
	}
	// separate mode: subject request + episode/kind request
	if bs := Build(task(2, false), ModelFlash, Options{Separate: true}); len(bs) != 2 || len(bs[0].Request.Questions) != 1 || len(bs[1].Request.Questions) != 4 {
		t.Errorf("separate: %+v", bs)
	}
	// nothing to ask
	if bs := Build(&meta.ClefTask{}, ModelFlash, Options{}); bs != nil {
		t.Errorf("empty task: %+v", bs)
	}
}

func TestParseResponse(t *testing.T) {
	ok := `{"result":{"model":"clef-flash","answers":{"subject":{"type":"choice","choice":"bgm_1","probabilities":{"bgm_1":0.8,"none":0.2},"confidence":0.6}},"usage":{"input_tokens":626,"output_tokens":0}},"success":true,"errors":[],"messages":[]}`
	r, err := ParseResponse(200, []byte(ok))
	if err != nil || r.Answers["subject"].Choice != "bgm_1" || r.Usage.InputTokens != 626 || r.Answers["subject"].Probabilities["none"] != 0.2 {
		t.Fatalf("%v %+v", err, r)
	}
	if _, err := ParseResponse(200, []byte(`{"answers":{"a":{"type":"choice","choice":"x","probabilities":{"x":1},"confidence":1}}}`)); err != nil {
		t.Errorf("bare result: %v", err)
	}
	_, err = ParseResponse(403, []byte(`{"result":null,"success":false,"errors":[{"code":10000,"message":"Authentication error"}]}`))
	var ae *APIError
	if !errors.As(err, &ae) || ae.Status != 403 || !strings.Contains(ae.Message, "Authentication error") || Retryable(err) {
		t.Errorf("auth error: %v", err)
	}
	if _, err := ParseResponse(429, []byte(`rate limited`)); !Retryable(err) {
		t.Errorf("429 not retryable: %v", err)
	}
	_, err = ParseResponse(429, []byte(`{"success":false,"errors":[{"code":4006,"message":"AiError: you have used up your daily free allocation of 10,000 neurons"}]}`))
	if !QuotaExhausted(err) {
		t.Errorf("quota: %v", err)
	}
	if _, err := ParseResponse(200, []byte(`{"success":false,"errors":[{"code":5006,"message":"bad input"}]}`)); err == nil {
		t.Error("success=false accepted")
	}
}

func choice(probs map[string]float64) Answer {
	best, bp := "", -1.0
	for k, p := range probs {
		if p > bp {
			best, bp = k, p
		}
	}
	return Answer{Type: "choice", Choice: best, Probabilities: probs, Confidence: bp * 0.7}
}

// valid: bgm:100 has main episodes 1..12; bgm:101 is split-cour 13..24 (file 01 → 13)
func fakeValid(_ context.Context, wk string, k meta.EpisodeKey) (meta.EpisodeKey, bool) {
	switch {
	case k.Kind != meta.KeyMain:
		return k, true
	case wk == "bgm:100":
		return k, k.Number >= 1 && k.Number <= 12
	case wk == "bgm:101":
		if k.Number < 13 {
			k.Number += 12
		}
		return k, k.Number >= 13 && k.Number <= 24
	}
	return k, false
}

func TestDecideValidationAndThresholds(t *testing.T) {
	ctx := context.Background()
	tk := task(1, false)
	th := Thresholds{Metric: MetricProb, Apply: 0.9, Margin: 0.3, Review: 0.6}
	ans := func(subj string, ps float64, ep string, pe float64, kind string, pk float64) map[string]Answer {
		return map[string]Answer{
			QSubject: choice(func() map[string]float64 {
				m := map[string]float64{"bgm_100": 0, "bgm_101": 0, None: 0}
				m[subj] = ps
				for k := range m {
					if k != subj {
						m[k] = (1 - ps) / float64(len(m)-1)
					}
				}
				return m
			}()),
			"f0_ep":   choice(map[string]float64{ep: pe, "n_1080": 1 - pe}),
			"f0_kind": choice(map[string]float64{kind: pk, "sp": 1 - pk}),
		}
	}
	cases := []struct {
		name   string
		a      map[string]Answer
		status string
		reason string
		key    string
	}{
		{"confident", ans("bgm_100", 0.97, "n_01", 0.95, "main", 0.93), StatusApplied, "", "1"},
		{"review band", ans("bgm_100", 0.75, "n_01", 0.95, "main", 0.93), StatusPending, "", "1"},
		{"low", ans("bgm_100", 0.55, "n_01", 0.95, "main", 0.93), StatusDiscarded, ReasonLowConfidence, "1"},
		{"small margin", map[string]Answer{
			QSubject: choice(map[string]float64{"bgm_100": 0.91, "bgm_101": 0.09}),
			"f0_ep":  choice(map[string]float64{"n_01": 0.62, "n_1080": 0.38}), "f0_kind": choice(map[string]float64{"main": 0.99, "sp": 0.01})}, StatusPending, "", "1"},
		{"none", ans(None, 0.97, "n_01", 0.95, "main", 0.93), StatusDiscarded, ReasonNone, ""},
		{"unknown subject", ans("bgm_999", 0.97, "n_01", 0.95, "main", 0.93), StatusDiscarded, ReasonBadSubject, ""},
		{"not an episode", ans("bgm_100", 0.97, "n_01", 0.95, "other", 0.93), StatusDiscarded, ReasonNotEpisode, ""},
		{"number not in the name", ans("bgm_100", 0.97, "n_77", 0.95, "main", 0.93), StatusDiscarded, ReasonBadNumber, ""},
		// episode 1080 exists in no candidate: conditioning moves the subject mass to none
		{"not official", ans("bgm_100", 0.97, "n_1080", 0.95, "main", 0.93), StatusDiscarded, ReasonNone, ""},
		{"split cour", ans("bgm_101", 0.97, "n_01", 0.95, "main", 0.93), StatusApplied, "", "13"},
		{"missing answer", map[string]Answer{QSubject: choice(map[string]float64{"bgm_100": 1})}, StatusDiscarded, ReasonNoAnswer, ""},
	}
	for _, c := range cases {
		ds := Decide(ctx, tk, c.a, th, fakeValid)
		if len(ds) != 1 {
			t.Fatalf("%s: %d decisions", c.name, len(ds))
		}
		d := ds[0]
		if d.Status != c.status || d.Reason != c.reason || d.Key != c.key {
			t.Errorf("%s: %s/%s key %q (score %.2f margin %.2f)", c.name, d.Status, d.Reason, d.Key, d.Score, d.Margin)
		}
	}
	// conditioning: bgm:100 (1–12) lacks episode 20, bgm:101 (13–24) has it: 0.38 of 0.40
	tk20 := task(1, false)
	tk20.Files[0].Path = "[G] Show - 20 [1080p].mkv"
	tk20.Files[0].Numbers = meta.NumberOptions(tk20.Files[0].Path, "", false)
	c20 := map[string]Answer{
		QSubject: choice(map[string]float64{"bgm_100": 0.6, "bgm_101": 0.38, None: 0.02}),
		"f0_ep":  choice(map[string]float64{"n_20": 0.97, "n_1080": 0.03}), "f0_kind": choice(map[string]float64{"main": 0.99, "sp": 0.01})}
	if d := Decide(ctx, tk20, c20, th, fakeValid)[0]; d.Status != StatusApplied || d.Candidate.WorkKey != "bgm:101" || d.Key != "20" || d.Answers[QSubject].Conditioned[0].ID != "bgm_101" {
		t.Errorf("conditioned: %+v", d)
	}
	// an entry that ended years before the torrent was published: review, never applied
	old := task(1, false)
	old.Published = "2026-10-01"
	old.Candidates[0].EndDate = "2018-03-24"
	if d := Decide(ctx, old, cases[0].a, th, fakeValid)[0]; d.Status != StatusPending || d.Reason != ReasonOldEntry {
		t.Errorf("old entry: %s %s", d.Status, d.Reason)
	}
	old.Candidates[0].EndDate = "2026-03-24"
	if d := Decide(ctx, old, cases[0].a, th, fakeValid)[0]; d.Status != StatusApplied {
		t.Errorf("recent entry: %s %s", d.Status, d.Reason)
	}
	// yes/no answers decide the subject: the confirmed fitting candidate, margin = gap
	yes := func(p float64) Answer { return Answer{Type: "noul", Noul: &p} }
	v := map[string]Answer{
		QSubject:                  choice(map[string]float64{"bgm_100": 0.5, "bgm_101": 0.45, None: 0.05}),
		VerifyQuestion("bgm_100"): yes(0.12), VerifyQuestion("bgm_101"): yes(0.93),
		"f0_ep": choice(map[string]float64{"n_01": 0.96, "n_1080": 0.04}), "f0_kind": choice(map[string]float64{"main": 0.95, "sp": 0.05}),
	}
	if d := Decide(ctx, tk, v, th, fakeValid)[0]; d.Candidate == nil || d.Candidate.ID != "bgm_101" || d.Key != "13" || d.Score != 0.93 || d.Margin < 0.8 || d.Status != StatusApplied {
		t.Errorf("verify: %+v", d)
	}
	v[VerifyQuestion("bgm_101")] = yes(0.3)
	if d := Decide(ctx, tk, v, th, fakeValid)[0]; d.Reason != ReasonNone {
		t.Errorf("nothing confirmed: %+v", d)
	}
	// raw probabilities (no conditioning): the chosen subject fails validation instead
	raw := th
	raw.Raw = true
	if d := Decide(ctx, tk, cases[8].a, raw, fakeValid)[0]; d.Reason != ReasonNotOfficial || d.Key != "1080" {
		t.Errorf("raw not official: %+v", d)
	}
	// metric = Clef's confidence (0.7 × prob in this fake): the confident case drops to review
	thc := th
	thc.Metric = MetricConfidence
	if d := Decide(ctx, tk, cases[0].a, thc, fakeValid)[0]; d.Status != StatusPending || d.Score > 0.7 {
		t.Errorf("confidence metric: %s %.2f", d.Status, d.Score)
	}
	// an unnumbered NCOP: none → 1 when it is the only one of its kind for the subject
	tk2 := task(1, false)
	tk2.Files[0] = meta.ClefFile{Index: 0, Path: "Show/[G] Show - NCOP.mkv", Kind: "op"}
	a := map[string]Answer{QSubject: choice(map[string]float64{"bgm_100": 0.99, None: 0.01}), "f0_kind": choice(map[string]float64{"op": 0.98, "ed": 0.02})}
	if d := Decide(ctx, tk2, a, th, fakeValid)[0]; d.Status != StatusApplied || d.Key != "ncop1" || d.Kind != "op" {
		t.Errorf("lone ncop: %+v", d)
	}
	// two unnumbered NCOPs: no number
	tk2.Files = append(tk2.Files, meta.ClefFile{Index: 1, Path: "Show/[G] Show - NCOP v2.mkv", Kind: "op"})
	a["f1_kind"] = a["f0_kind"]
	if d := Decide(ctx, tk2, a, th, fakeValid)[0]; d.Status != StatusDiscarded || d.Reason != ReasonNoNumber {
		t.Errorf("two ncops: %+v", d)
	}
}

// fake API: answers every question with a fixed choice per question suffix
type fakeClient struct {
	calls  int
	reqs   []Request
	answer func(id string, q Question) map[string]float64
	err    error
}

func (f *fakeClient) Run(_ context.Context, r Request) (*Response, error) {
	f.calls++
	f.reqs = append(f.reqs, r)
	if f.err != nil {
		return nil, f.err
	}
	out := &Response{Model: r.Model, Answers: map[string]Answer{}, Usage: Usage{InputTokens: 600}}
	for id, q := range r.Questions {
		probs := f.answer(id, q)
		if q.Type == "noul" {
			p := probs["yes"]
			out.Answers[id] = Answer{Type: "noul", Noul: &p}
			continue
		}
		out.Answers[id] = choice(probs)
	}
	return out, nil
}

func TestServiceQueueApplyReviewRevert(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "otd.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ms, err := meta.Open(filepath.Join(dir, "meta.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer ms.Close()
	if _, err := ms.W.Exec(`INSERT INTO works(id,key,title,title_cn,aliases,type,begin_at,eps,updated_at) VALUES
		(1,'bgm:100','ショー','秀','["Show"]','tv','2024-01-07T15:00:00Z',12,'x'),
		(2,'bgm:101','ショー 第2期','秀 第二季','["Show 2nd Season"]','tv','2025-01-07T15:00:00Z',12,'x')`); err != nil {
		t.Fatal(err)
	}
	_, _ = ms.W.Exec(`INSERT INTO work_ids VALUES('bgm','100','bgm:100'),('bgm','101','bgm:101')`)
	// "Show S2" with an exact title match to season 1 only: season ambiguity → queued
	if err := ms.Match(ctx, meta.TorrentInput{Infohash: "aa", PublishedAt: "2025-02-01T00:00:00Z", Title: "[G] Show S2 - 03 [1080p]", Source: "Nyaa", SourceKind: "rss", Trusted: true,
		Files: []meta.FileInput{{Index: 0, Path: "[G] Show S2 - 03 [1080p].mkv", Size: 1}, {Index: 1, Path: "[G] Show S2 - 03 [1080p].ass", Size: 1}}}); err != nil {
		t.Fatal(err)
	}
	if err := ms.Match(ctx, meta.TorrentInput{Infohash: "bb", PublishedAt: "2025-02-01T00:00:00Z", Title: "[G] Show S2 - 04 [1080p]", Source: "Nyaa", SourceKind: "rss", Trusted: true,
		Files: []meta.FileInput{{Index: 0, Path: "[G] Show S2 - 04 [1080p].mkv", Size: 1}}}); err != nil {
		t.Fatal(err)
	}
	if tm, _ := ms.GetTorrentMap(ctx, "aa"); tm.Files[0].Work != nil {
		t.Fatalf("rules mapped an uncertain file: %+v", tm.Files[0])
	}
	_ = st.UpdateSettings(ctx, map[string]json.RawMessage{store.KeyClefAccountID: json.RawMessage(`"acc"`), store.KeyClefToken: json.RawMessage(`"tok"`), store.KeyClefEnabled: json.RawMessage(`true`), store.KeyClefRate: json.RawMessage(`300`), store.KeyClefProvider: json.RawMessage(`"clef"`)})
	subjP := 0.97
	fc := &fakeClient{answer: func(id string, q Question) map[string]float64 {
		switch {
		case id == QSubject:
			return map[string]float64{"bgm_101": subjP, "bgm_100": (1 - subjP) / 2, None: (1 - subjP) / 2}
		case id == VerifyQuestion("bgm_101"):
			return map[string]float64{"yes": subjP}
		case strings.HasPrefix(id, "v_"):
			return map[string]float64{"yes": 0.05}
		case strings.HasSuffix(id, "_kind"):
			return map[string]float64{"main": 0.97, "other": 0.03}
		default: // the episode number: n_03 / n_04
			for k := range q.Criteria.(map[string]any) {
				if k == "n_03" || k == "n_04" {
					return map[string]float64{k: 0.96, None: 0.04}
				}
			}
		}
		return map[string]float64{None: 1}
	}}
	svc := &Service{St: st, Meta: ms, Log: discardLog(), NewClient: func(Config) Client { return fc }}
	c := svc.Config()
	if !c.Enabled || !c.Configured() || c.Model != ModelFlash || c.Thresholds != (Thresholds{Metric: MetricProb, Apply: 0.8, Margin: 0.3, Review: 0.5}) {
		t.Fatalf("config %+v", c)
	}
	// newest job first: bb, then aa (lower subject probability → review)
	job, _ := ms.NextClefJob(ctx, "9999")
	if job == nil || job.Infohash != "bb" {
		t.Fatalf("next job %+v", job)
	}
	if err := svc.Process(ctx, c, job); err != nil {
		t.Fatal(err)
	}
	tm, _ := ms.GetTorrentMap(ctx, "bb")
	if f := tm.Files[0]; f.Work == nil || f.Work.Key != "bgm:101" || f.EpisodeKey != "4" || f.Method != meta.MethodClef {
		t.Errorf("applied: %+v", f)
	}
	subjP = 0.75
	job, _ = ms.NextClefJob(ctx, "9999")
	if err := svc.Process(ctx, c, job); err != nil {
		t.Fatal(err)
	}
	if tm, _ := ms.GetTorrentMap(ctx, "aa"); tm.Files[0].Work != nil {
		t.Errorf("review decision applied: %+v", tm.Files[0])
	}
	if j, _ := ms.NextClefJob(ctx, "9999"); j != nil {
		t.Errorf("job left: %+v", j)
	}
	pend, n, _ := ms.ListClefDecisions(ctx, StatusPending, "", 0, 10)
	if n != 1 || pend[0].Infohash != "aa" || pend[0].WorkKey != "bgm:101" || pend[0].EpisodeKey != "3" {
		t.Fatalf("pending: %+v", pend)
	}
	var sum map[string]AnswerSummary
	if json.Unmarshal(pend[0].Answers, &sum) != nil || sum[QSubject].Top[0].ID != "bgm_101" {
		t.Errorf("answers %s", pend[0].Answers)
	}
	// accept → applied; reject the applied one → reverted (unmapped, verified)
	if d, err := svc.Accept(ctx, pend[0].ID, "admin"); err != nil || d.Status != StatusApplied {
		t.Fatalf("accept: %v %+v", err, d)
	}
	if tm, _ := ms.GetTorrentMap(ctx, "aa"); tm.Files[0].Work == nil || tm.Files[0].Work.Key != "bgm:101" || tm.Files[0].Method != meta.MethodClef {
		t.Errorf("accepted: %+v", tm.Files[0])
	}
	if _, err := svc.Accept(ctx, pend[0].ID, "admin"); !errors.Is(err, ErrState) {
		t.Errorf("double accept: %v", err)
	}
	if d, err := svc.Reject(ctx, pend[0].ID, "admin"); err != nil || d.Status != StatusRejected {
		t.Fatalf("revert: %v", err)
	}
	if tm, _ := ms.GetTorrentMap(ctx, "aa"); tm.Files[0].Work != nil || tm.Files[0].Status != "verified" {
		t.Errorf("reverted: %+v", tm.Files[0])
	}
	// an admin can still accept the rejected decision (override): mapped, human-verified
	if d, err := svc.Accept(ctx, pend[0].ID, "admin"); err != nil || d.Status != StatusApplied {
		t.Fatalf("override accept: %v %+v", err, d)
	}
	if tm, _ := ms.GetTorrentMap(ctx, "aa"); tm.Files[0].Work == nil || tm.Files[0].Work.Key != "bgm:101" || tm.Files[0].Method != meta.MethodReview || tm.Files[0].Status != "verified" {
		t.Errorf("override: %+v", tm.Files[0])
	}
	// … and revert it again (the next steps expect an unmapped file)
	_, _ = ms.W.Exec(`UPDATE torrent_files SET work_key=NULL, status='verified', method='manual' WHERE infohash='aa'`)
	// a clef mapping survives a re-delivery of the torrent (Match does not overwrite it)
	if err := ms.Match(ctx, meta.TorrentInput{Infohash: "bb", PublishedAt: "2025-02-01T00:00:00Z", Title: "[G] Show S2 - 04 [1080p]", Files: []meta.FileInput{{Index: 0, Path: "[G] Show S2 - 04 [1080p].mkv", Size: 1}}}); err != nil {
		t.Fatal(err)
	}
	if tm, _ := ms.GetTorrentMap(ctx, "bb"); tm.Files[0].Work == nil || tm.Files[0].Method != meta.MethodClef {
		t.Errorf("clef row overwritten: %+v", tm.Files[0])
	}
	// errors: retried with backoff, daily cap defers without counting an attempt
	_ = ms.RequeueClef(ctx, "aa")
	_, _ = ms.W.Exec(`UPDATE torrent_files SET status='auto', method='' WHERE infohash='aa'`)
	fc.err = &APIError{Status: 503, Message: "busy"}
	job, _ = ms.NextClefJob(ctx, "9999")
	_ = svc.Process(ctx, c, job)
	if jobs, _ := ms.ListClefJobs(ctx, 10); jobs[0].Infohash != "aa" || jobs[0].Status != "pending" || jobs[0].Attempts != 1 || jobs[0].LastError == "" {
		t.Errorf("retry: %+v", jobs[0])
	}
	fc.err = nil
	c.DailyCap = ms.ClefCount(ctx, today())
	job, _ = ms.NextClefJob(ctx, "9999")
	_ = svc.Process(ctx, c, job)
	if jobs, _ := ms.ListClefJobs(ctx, 10); jobs[0].Status != "pending" || jobs[0].Attempts != 1 || !strings.Contains(jobs[0].LastError, "上限") {
		t.Errorf("cap: %+v", jobs[0])
	}
	// dry run decides without writing
	c.DailyCap = 1000
	out, err := svc.Decide(ctx, c, "bb", true)
	if err != nil || len(out.Decisions) != 1 || out.Sent != 2 || len(out.Task.Candidates) < 2 {
		t.Fatalf("dry run: %v %+v", err, out)
	}
	// the rejected decision was accepted again as an override above: 2 applied, 0 rejected
	sv, err := svc.Status(ctx)
	if err != nil || sv.Today < 3 || sv.Decisions[StatusApplied] != 2 || sv.Decisions[StatusRejected] != 0 {
		t.Errorf("status: %v %+v", err, sv)
	}
}

func discardLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }
