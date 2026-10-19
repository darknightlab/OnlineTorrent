package clef

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/darknightlab/onlinetorrent/internal/meta"
	"github.com/darknightlab/onlinetorrent/internal/store"
)

func strp(s string) *string   { return &s }
func f64p(v float64) *float64 { return &v }

func TestBuildLLMRequest(t *testing.T) {
	tk := task(2, false)
	tk.Candidates[1].MainEpisodes, tk.Candidates[1].Season = "13–24", 2
	r := BuildLLMRequest(tk, "gpt-6-luna", []int{1})
	if r.Model != "gpt-6-luna" || r.Temperature != 0 || len(r.Messages) != 2 || !strings.Contains(r.Messages[0].Content, `"released"`) || len(r.Messages[0].Content) > 1000 {
		t.Fatalf("request %+v", r)
	}
	var in llmInput
	if err := json.Unmarshal([]byte(r.Messages[1].Content), &in); err != nil {
		t.Fatal(err)
	}
	if len(in.Files) != 1 || in.Files[0].Index != 1 || in.OtherFiles != 1 || len(in.Candidates) != 2 || in.Candidates[1].MainEpisodes != "13–24" || in.Candidates[1].Season != 2 {
		t.Errorf("input %+v", in)
	}
	b, _ := json.Marshal(r.ResponseFormat)
	for _, want := range []string{`"strict":true`, `"type":"json_schema"`, `"enum":["bgm_100","bgm_101"]`, `"additionalProperties":false`, `"suggested_title"`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("schema lacks %s: %s", want, b)
		}
	}
	// no candidates: subject_id can only be null
	tk.Candidates = nil
	b, _ = json.Marshal(BuildLLMRequest(tk, "m", []int{0}).ResponseFormat)
	if !strings.Contains(string(b), `"subject_id":{"type":"null"}`) {
		t.Errorf("empty schema %s", b)
	}
}

func TestParseChatResponse(t *testing.T) {
	ok := `{"choices":[{"message":{"content":"{\"files\":[],\"suggested_title\":null}"},"finish_reason":"stop"}],"usage":{"prompt_tokens":327,"completion_tokens":246}}`
	r, err := ParseChatResponse(200, []byte(ok))
	if err != nil || r.PromptTokens != 327 || r.CompletionTokens != 246 {
		t.Fatalf("%v %+v", err, r)
	}
	if out, err := ParseLLMOutput(r.Content); err != nil || out.SuggestedTitle != nil {
		t.Errorf("output %v %+v", err, out)
	}
	if _, err := ParseLLMOutput("not json"); err == nil {
		t.Error("garbage accepted")
	}
	_, err = ParseChatResponse(429, []byte(`{"error":{"message":"rate limited"}}`))
	var ae *APIError
	if !errors.As(err, &ae) || ae.Status != 429 || ae.Message != "rate limited" || !Retryable(err) {
		t.Errorf("429: %v", err)
	}
	if _, err := ParseChatResponse(200, []byte(`{"choices":[{"message":{"content":"{"},"finish_reason":"length"}]}`)); err == nil {
		t.Error("truncated output accepted")
	}
	if _, err := ParseChatResponse(200, []byte(`{"choices":[{"message":{"refusal":"no"}}]}`)); err == nil {
		t.Error("refusal accepted")
	}
}

// fakeChat answers from a function of the request input
type fakeChat struct {
	mu     sync.Mutex
	calls  []llmInput
	effort string
	answer func(in llmInput) LLMOutput
}

func (f *fakeChat) Chat(_ context.Context, r ChatRequest) (*ChatResult, error) {
	var in llmInput
	_ = json.Unmarshal([]byte(r.Messages[1].Content), &in)
	f.mu.Lock()
	f.calls = append(f.calls, in)
	f.effort = r.ReasoningEffort
	f.mu.Unlock()
	b, _ := json.Marshal(f.answer(in))
	return &ChatResult{Content: string(b), PromptTokens: 500, CompletionTokens: 100}, nil
}

func TestRunLLMChunksAndSuggestion(t *testing.T) {
	ctx := context.Background()
	tk := task(130, false)
	fc := &fakeChat{answer: func(in llmInput) LLMOutput {
		var o LLMOutput
		for _, f := range in.Files {
			o.Files = append(o.Files, LLMFileOut{Index: f.Index, SubjectID: strp("bgm_100"), Episode: f64p(float64(f.Index + 1)), Kind: "main", Confidence: 0.95})
		}
		return o
	}}
	run, err := RunLLM(ctx, fc.Chat, "m", tk, nil)
	if err != nil || run.Requests != 3 || len(run.Answers) != 130 || run.Rounds != 1 || len(fc.calls[0].Files) != LLMChunkFiles || len(fc.calls[2].Files) != 10 {
		t.Fatalf("chunks: %v %+v %d", err, run, len(fc.calls))
	}
	if len(fc.calls[1].Candidates) != 2 {
		t.Errorf("chunk candidates %d", len(fc.calls[1].Candidates))
	}

	// no fitting candidate → suggested title → search → asked once more with the new ones
	tk = task(2, false)
	fc = &fakeChat{answer: func(in llmInput) LLMOutput {
		o := LLMOutput{SuggestedTitle: strp("Other Show")}
		for _, f := range in.Files {
			a := LLMFileOut{Index: f.Index, Kind: "main", Confidence: 0.2, Episode: f64p(1)}
			for _, c := range in.Candidates {
				if c.ID == "bgm_200" {
					a.SubjectID, a.Confidence = strp("bgm_200"), 0.97
				}
			}
			o.Files = append(o.Files, a)
		}
		return o
	}}
	searched := ""
	search := func(_ context.Context, title string, have map[string]bool) []meta.ClefCandidate {
		searched = title
		if !have["bgm:100"] {
			t.Error("have lacks the existing candidates")
		}
		return []meta.ClefCandidate{{ID: "bgm_200", WorkKey: "bgm:200", Title: "Other Show", Eps: 12, Type: "tv"}}
	}
	run, err = RunLLM(ctx, fc.Chat, "m", tk, search)
	if err != nil || searched != "Other Show" || run.Rounds != 2 || run.Requests != 2 || len(tk.Candidates) != 3 || *run.Answers[0].SubjectID != "bgm_200" || run.Added[0] != "bgm_200" {
		t.Fatalf("suggestion: %v %+v", err, run)
	}
	// the search finds nothing new: one round only
	tk = task(1, false)
	run, _ = RunLLM(ctx, fc.Chat, "m", tk, func(context.Context, string, map[string]bool) []meta.ClefCandidate { return nil })
	if run.Rounds != 1 || run.Answers[0].SubjectID != nil {
		t.Errorf("no new candidates: %+v", run)
	}
}

func TestDecideLLM(t *testing.T) {
	ctx := context.Background()
	th := LLMThresholds{Apply: 0.9, Review: 0.5, Agreement: true}
	one := func(subj *string, ep *float64, kind string, conf float64) map[int]LLMFileOut {
		return map[int]LLMFileOut{0: {Index: 0, SubjectID: subj, Episode: ep, Kind: kind, Confidence: conf, Reason: "r"}}
	}
	tk := task(1, false) // "Show/[G] Show - 01 [1080p].mkv"
	tk.Files[0].Episode = f64p(1)
	cases := []struct {
		name           string
		a              map[int]LLMFileOut
		status, reason string
		key            string
	}{
		{"applied", one(strp("bgm_100"), f64p(1), "main", 0.95), StatusApplied, "", "1"},
		{"split cour shift", one(strp("bgm_101"), f64p(1), "main", 0.95), StatusApplied, "", "13"},
		{"review band", one(strp("bgm_100"), f64p(1), "main", 0.7), StatusPending, "", "1"},
		{"low", one(strp("bgm_100"), f64p(1), "main", 0.3), StatusDiscarded, ReasonLowConfidence, "1"},
		{"null subject", one(nil, f64p(1), "main", 0.99), StatusDiscarded, ReasonNone, ""},
		{"unknown subject", one(strp("bgm_999"), f64p(1), "main", 0.99), StatusDiscarded, ReasonBadSubject, ""},
		{"not an episode", one(strp("bgm_100"), nil, "other", 0.99), StatusDiscarded, ReasonNotEpisode, ""},
		{"converted number", one(strp("bgm_101"), f64p(13), "main", 0.99), StatusDiscarded, ReasonBadNumber, ""},
		{"not official", one(strp("bgm_100"), f64p(1080), "main", 0.99), StatusDiscarded, ReasonNotOfficial, "1080"},
		{"no answer", map[int]LLMFileOut{}, StatusDiscarded, ReasonNoAnswer, ""},
	}
	for _, c := range cases {
		d := DecideLLM(ctx, tk, c.a, th, fakeValid, nil)[0]
		if d.Status != c.status || d.Reason != c.reason || d.Key != c.key {
			t.Errorf("%s: %s/%s key %q", c.name, d.Status, d.Reason, d.Key)
		}
	}
	// the parser read 1, the model says 1080 is no option; says 5 → number not in the name;
	// a different number that is in the name → review (parser disagrees)
	tk2 := task(1, false)
	tk2.Files[0].Path = "[G] Show - 03 (05) [1080p].mkv"
	tk2.Files[0].Numbers = meta.NumberOptions(tk2.Files[0].Path, "", false)
	tk2.Files[0].Episode = f64p(3)
	if d := DecideLLM(ctx, tk2, one(strp("bgm_100"), f64p(5), "main", 0.99), th, fakeValid, nil)[0]; d.Status != StatusPending || d.Reason != ReasonDisagrees {
		t.Errorf("disagrees: %s %s", d.Status, d.Reason)
	}
	// the parser says PV (keyword), the model says main → review
	tk5 := task(1, false)
	tk5.Files[0].Kind = "pv"
	if d := DecideLLM(ctx, tk5, one(strp("bgm_100"), f64p(1), "main", 0.99), th, fakeValid, nil)[0]; d.Reason != ReasonDisagrees {
		t.Errorf("kind disagrees: %s %s", d.Status, d.Reason)
	}
	th2 := th
	th2.Agreement = false
	if d := DecideLLM(ctx, tk2, one(strp("bgm_100"), f64p(5), "main", 0.99), th2, fakeValid, nil)[0]; d.Status != StatusApplied {
		t.Errorf("agreement off: %s %s", d.Status, d.Reason)
	}
	// season stated in the name must fit; old entries only to review
	tk3 := task(1, false)
	tk3.Files[0].Season = 2
	tk3.Candidates[0].Season, tk3.Candidates[1].Season = 1, 2
	if d := DecideLLM(ctx, tk3, one(strp("bgm_100"), f64p(1), "main", 0.99), th, fakeValid, nil)[0]; d.Reason != ReasonSeason {
		t.Errorf("season: %+v", d)
	}
	// no name evidence → review
	noName := func(context.Context, string) bool { return false }
	if d := DecideLLM(ctx, tk, one(strp("bgm_100"), f64p(1), "main", 0.99), th, fakeValid, noName)[0]; d.Status != StatusPending || d.Reason != ReasonNoName {
		t.Errorf("no evidence: %s %s", d.Status, d.Reason)
	}
	// the only unnumbered NCOP of its subject → 1 (applied only when the work lists OPs)
	tk4 := task(1, false)
	tk4.Candidates[0].Specials = "ncop1, nced1"
	tk4.Files[0] = meta.ClefFile{Index: 0, Path: "Show/[G] Show - NCOP.mkv", Kind: "op"}
	if d := DecideLLM(ctx, tk4, one(strp("bgm_100"), nil, "op", 0.95), th, fakeValid, nil)[0]; d.Status != StatusApplied || d.Key != "ncop1" {
		t.Errorf("lone ncop: %+v", d)
	}
	if d := DecideLLM(ctx, tk4, one(strp("bgm_100"), nil, "op", 0.95), th, fakeValid, nil)[0]; d.Answers["llm"].Note != "r" || d.Margin != -1 {
		t.Errorf("answer summary %+v", d.Answers)
	}
	tk4.Candidates[0].Specials = ""
	if d := DecideLLM(ctx, tk4, one(strp("bgm_100"), nil, "op", 0.95), th, fakeValid, nil)[0]; d.Status != StatusPending || d.Reason != ReasonUnlisted {
		t.Errorf("unlisted special: %s %s", d.Status, d.Reason)
	}
	// … but next to an applied main episode of the same work it is applied
	tk4.Files = append(tk4.Files, meta.ClefFile{Index: 1, Path: "Show/[G] Show - 01.mkv", Kind: "episode", Numbers: meta.NumberOptions("Show/[G] Show - 01.mkv", "", false)})
	both := map[int]LLMFileOut{0: {Index: 0, SubjectID: strp("bgm_100"), Kind: "op", Confidence: 0.95}, 1: {Index: 1, SubjectID: strp("bgm_100"), Episode: f64p(1), Kind: "main", Confidence: 0.95}}
	if ds := DecideLLM(ctx, tk4, both, th, fakeValid, nil); ds[0].Status != StatusApplied || ds[1].Status != StatusApplied {
		t.Errorf("special with main: %+v", ds)
	}
}

func TestServiceLLMProvider(t *testing.T) {
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
		(1,'bgm:100','ショー','秀','["Some Show"]','tv','2024-01-07T15:00:00Z',12,'x'),
		(2,'bgm:101','ショー 第2期','秀 第二季','["Some Show 2nd Season"]','tv','2025-01-07T15:00:00Z',12,'x')`); err != nil {
		t.Fatal(err)
	}
	_, _ = ms.W.Exec(`INSERT INTO work_ids VALUES('bgm','100','bgm:100'),('bgm','101','bgm:101')`)
	for i := 1; i <= 12; i++ {
		_, _ = ms.W.Exec(`INSERT INTO episodes(bgm_id,work_key,type,sort) VALUES(?,'bgm:100',0,?),(?,'bgm:101',0,?)`, 1000+i, i, 2000+i, 12+i)
	}
	if err := ms.Match(ctx, meta.TorrentInput{Infohash: "aa", Title: "[G] Some Show S2 - 04 [1080p]", PublishedAt: "2025-02-01T00:00:00Z",
		Files: []meta.FileInput{{Index: 0, Path: "[G] Some Show S2 - 04 [1080p].mkv", Size: 1}}}); err != nil {
		t.Fatal(err)
	}
	_ = st.UpdateSettings(ctx, map[string]json.RawMessage{store.KeyLLMAPIKey: json.RawMessage(`"k"`), store.KeyClefEnabled: json.RawMessage(`true`), store.KeyLLMRate: json.RawMessage(`600`)})
	fc := &fakeChat{answer: func(in llmInput) LLMOutput {
		return LLMOutput{Files: []LLMFileOut{{Index: 0, SubjectID: strp("bgm_101"), Episode: f64p(4), Kind: "main", Confidence: 0.97, Reason: "S2 ep 4"}}}
	}}
	svc := &Service{St: st, Meta: ms, Log: discardLog(), NewLLM: func(Config) LLMClient { return fc }}
	c := svc.Config()
	if c.Provider != ProviderOpenAI || !c.Configured() || c.LLM.Model != DefaultLLMModel || c.LLM.Concurrency != 2 || c.LLM.Thresholds != DefaultLLMThresholds {
		t.Fatalf("config %+v", c)
	}
	job, _ := ms.NextClefJob(ctx, "9999")
	if err := svc.Process(ctx, c, job); err != nil {
		t.Fatal(err)
	}
	tm, _ := ms.GetTorrentMap(ctx, "aa")
	if f := tm.Files[0]; f.Work == nil || f.Work.Key != "bgm:101" || f.EpisodeKey != "16" || f.Method != meta.MethodClef {
		ds, _, _ := ms.ListClefDecisions(ctx, "", "", 0, 10)
		t.Errorf("applied: %+v %+v", f, ds)
	}
	ds, _, _ := ms.ListClefDecisions(ctx, "", "", 0, 10)
	if len(ds) != 1 || ds[0].Model != DefaultLLMModel || !strings.Contains(string(ds[0].Answers), "S2 ep 4") {
		t.Errorf("decision %+v", ds)
	}
	if len(fc.calls) != 1 || len(fc.calls[0].Candidates) < 2 || fc.calls[0].Candidates[0].MainEpisodes == "" || fc.calls[0].Released != "2025-02-01" || fc.effort != "medium" {
		t.Errorf("calls %+v effort %q", fc.calls, fc.effort)
	}

	// date gate: episode 4 of S2 aired 2025-01-28; released 2025-06-01 → review (日期不符)
	_, _ = ms.W.Exec(`UPDATE episodes SET airdate='2025-01-28' WHERE work_key='bgm:101' AND sort=16`)
	if err := ms.Match(ctx, meta.TorrentInput{Infohash: "dd", Title: "[G] Some Show S2 - 04 [BD 1080p]", PublishedAt: "2025-06-01T00:00:00Z", Source: "Nyaa", SourceKind: "rss",
		Files: []meta.FileInput{{Index: 0, Path: "[G] Some Show S2 - 04 [BD 1080p].mkv", Size: 1}}}); err != nil {
		t.Fatal(err)
	}
	if out, err := svc.Decide(ctx, c, "dd", false); err != nil || out.Decisions[0].Status != StatusPending || !strings.HasPrefix(out.Decisions[0].Reason, meta.ReasonDateMismatch+": 发布时间 2025-06-01 晚于本集播出 2025-01-28") {
		t.Errorf("date gate: %v %+v", err, out.Decisions)
	}
	// no release time (user submission without a submission time) → review (缺少发布时间)
	_ = ms.Match(ctx, meta.TorrentInput{Infohash: "ee", Title: "[G] Some Show S2 - 04 [1080p]", Source: "user", SourceKind: "user",
		Files: []meta.FileInput{{Index: 0, Path: "[G] Some Show S2 - 04 [1080p] v2.mkv", Size: 1}}})
	if out, err := svc.Decide(ctx, c, "ee", true); err != nil || len(out.Decisions) == 0 || out.Decisions[0].Status != StatusPending || out.Decisions[0].Reason != meta.ReasonMissingRelease {
		t.Errorf("missing release: %v %+v", err, out)
	}

	// anomalous mapping: episode 1080 of bgm:100 (1–12) is listed, queued with its file, and
	// a decision replaces the wrong mapping
	if err := ms.Match(ctx, meta.TorrentInput{Infohash: "bb", Title: "[G] Some Show - 05 [1080p]", PublishedAt: "2024-02-01T00:00:00Z", Files: []meta.FileInput{{Index: 0, Path: "[G] Some Show - 05 [1080p].mkv", Size: 1}}}); err != nil {
		t.Fatal(err)
	}
	_, _ = ms.W.Exec(`UPDATE torrent_files SET work_key='bgm:100', episode=1080, method='title+name' WHERE infohash='bb'`)
	an, err := ms.AnomalousMappings(ctx)
	if err != nil || len(an) != 1 || an[0].Infohash != "bb" || an[0].Files[0].Episode != 1080 || an[0].Files[0].Range != "1–12" {
		t.Fatalf("anomalies %v %+v", err, an)
	}
	if n, err := ms.QueueAnomalies(ctx, an); err != nil || n != 1 {
		t.Fatal(err)
	}
	fc.answer = func(in llmInput) LLMOutput {
		if len(in.Files) != 1 || in.Files[0].Index != 0 {
			t.Errorf("anomaly files %+v", in.Files)
		}
		return LLMOutput{Files: []LLMFileOut{{Index: 0, SubjectID: strp("bgm_100"), Episode: f64p(5), Kind: "main", Confidence: 0.96}}}
	}
	job, _ = ms.NextClefJob(ctx, "9999")
	if job == nil || job.Infohash != "bb" {
		t.Fatalf("job %+v", job)
	}
	if err := svc.Process(ctx, c, job); err != nil {
		t.Fatal(err)
	}
	if tm, _ := ms.GetTorrentMap(ctx, "bb"); tm.Files[0].EpisodeKey != "5" || tm.Files[0].Method != meta.MethodClef {
		t.Errorf("anomaly fixed: %+v", tm.Files[0])
	}
	if an, _ := ms.AnomalousMappings(ctx); len(an) != 0 {
		t.Errorf("anomalies left %+v", an)
	}

	// concurrency: the worker runs two jobs at once
	for i := 0; i < 4; i++ {
		ih := fmt.Sprintf("c%d", i)
		_ = ms.Match(ctx, meta.TorrentInput{Infohash: ih, Title: "[G] Show S2 - 0" + fmt.Sprint(i+1), PublishedAt: "2025-02-01T00:00:00Z",
			Files: []meta.FileInput{{Index: 0, Path: "[G] Show S2 - 0" + fmt.Sprint(i+1) + ".mkv", Size: 1}}})
	}
	gate := make(chan struct{})
	var mu sync.Mutex
	inflight, peak := 0, 0
	svc.NewLLM = func(Config) LLMClient {
		return llmFunc(func(ctx context.Context, r ChatRequest) (*ChatResult, error) {
			mu.Lock()
			inflight++
			peak = max(peak, inflight)
			mu.Unlock()
			<-gate
			mu.Lock()
			inflight--
			mu.Unlock()
			return &ChatResult{Content: `{"files":[],"suggested_title":null}`}, nil
		})
	}
	rctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { svc.Run(rctx); close(done) }()
	for i := 0; i < 100; i++ {
		mu.Lock()
		p := peak
		mu.Unlock()
		if p >= 2 {
			break
		}
		sleepMS(20)
	}
	close(gate)
	for i := 0; i < 200; i++ {
		if j, _ := ms.NextClefJob(ctx, "9999"); j == nil {
			break
		}
		sleepMS(20)
	}
	cancel()
	<-done
	if peak != 2 {
		t.Errorf("peak concurrency %d", peak)
	}
	if j, _ := ms.NextClefJob(ctx, "9999"); j != nil {
		t.Errorf("job left %+v", j)
	}
}

type llmFunc func(context.Context, ChatRequest) (*ChatResult, error)

func (f llmFunc) Chat(ctx context.Context, r ChatRequest) (*ChatResult, error) { return f(ctx, r) }

func sleepMS(n int) { time.Sleep(time.Duration(n) * time.Millisecond) }
