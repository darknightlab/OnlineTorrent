package clef

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/darknightlab/onlinetorrent/internal/meta"
	"github.com/darknightlab/onlinetorrent/internal/store"
)

// Providers.
const (
	ProviderOpenAI = "openai" // OpenAI-compatible LLM with structured output (default)
	ProviderClef   = "clef"   // Cloudflare Workers AI Clef
)

// LLMConfig is the LLM backend configuration.
type LLMConfig struct {
	BaseURL     string
	APIKey      string
	Model       string
	Timeout     time.Duration
	Concurrency int
	Effort      string // reasoning_effort
	Thresholds  LLMThresholds
}

// Config is the decision-model configuration (settings group 决策模型).
type Config struct {
	Enabled  bool
	Provider string
	// Clef
	AccountID  string
	Token      string
	Model      string // Clef model
	BaseURL    string // Clef API base
	Thresholds Thresholds
	// LLM
	LLM LLMConfig
	// of the active provider
	RatePerMin int
	DailyCap   int
}

// Configured reports whether requests can be made with the active provider.
func (c Config) Configured() bool {
	if c.Provider == ProviderClef {
		return c.AccountID != "" && c.Token != ""
	}
	return c.LLM.APIKey != "" && c.LLM.BaseURL != "" && c.LLM.Model != ""
}

// ModelName is the model of the active provider (stored with each decision).
func (c Config) ModelName() string {
	if c.Provider == ProviderClef {
		return c.Model
	}
	return c.LLM.Model
}

func (c Config) concurrency() int {
	if c.Provider == ProviderClef {
		return 1
	}
	return c.LLM.Concurrency
}

// Errors.
var (
	ErrNotConfigured = errors.New("决策模型未配置（Clef：Account ID / API Token；LLM：API 地址 / Key / 模型）")
	ErrDailyCap      = errors.New("已达到每日请求上限")
)

// Service is the queue worker plus the admin operations.
type Service struct {
	St   *store.Store
	Meta *meta.Store
	Log  *slog.Logger
	// NewClient / NewLLM build the API clients (nil: HTTPClient / OpenAIClient). Tests use fakes.
	NewClient func(Config) Client
	NewLLM    func(Config) LLMClient

	wake chan struct{}
	once sync.Once

	mu       sync.Mutex
	lastReq  time.Time
	lastErr  string
	lastErrT time.Time
	lastOK   time.Time
	current  map[string]bool
	tokens   int // input tokens since start
	requests int // requests since start
}

func (s *Service) init() {
	s.once.Do(func() {
		s.wake = make(chan struct{}, 1)
		s.mu.Lock()
		if s.current == nil {
			s.current = map[string]bool{}
		}
		s.mu.Unlock()
	})
}

// Wake makes the worker look at the queue now.
func (s *Service) Wake() {
	s.init()
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// Config reads the settings (numbers clamped to sane ranges).
func (s *Service) Config() Config {
	c := Config{
		Enabled: s.St.Bool(store.KeyClefEnabled), Provider: s.St.Str(store.KeyClefProvider),
		AccountID: strings.TrimSpace(s.St.Str(store.KeyClefAccountID)),
		Token:     strings.TrimSpace(s.St.Str(store.KeyClefToken)), Model: s.St.Str(store.KeyClefModel), BaseURL: strings.TrimSpace(s.St.Str(store.KeyClefBaseURL)),
		Thresholds: Thresholds{Metric: s.St.Str(store.KeyClefMetric), Apply: s.St.Num(store.KeyClefApply), Margin: s.St.Num(store.KeyClefMargin), Review: s.St.Num(store.KeyClefReview)},
		LLM: LLMConfig{BaseURL: strings.TrimSpace(s.St.Str(store.KeyLLMBaseURL)), APIKey: strings.TrimSpace(s.St.Str(store.KeyLLMAPIKey)), Model: strings.TrimSpace(s.St.Str(store.KeyLLMModel)),
			Timeout: time.Duration(s.St.Num(store.KeyLLMTimeoutSec) * float64(time.Second)), Concurrency: int(s.St.Num(store.KeyLLMConcurrency)),
			Effort:     strings.TrimSpace(s.St.Str(store.KeyLLMEffort)),
			Thresholds: LLMThresholds{Apply: s.St.Num(store.KeyLLMApply), Review: s.St.Num(store.KeyLLMReview), Agreement: s.St.Bool(store.KeyLLMAgreement)}},
	}
	clamp := func(v, lo, hi float64) float64 { return math.Max(lo, math.Min(hi, v)) }
	if c.Provider != ProviderClef {
		c.Provider = ProviderOpenAI
		c.RatePerMin, c.DailyCap = int(s.St.Num(store.KeyLLMRate)), int(s.St.Num(store.KeyLLMDailyCap))
	} else {
		c.RatePerMin, c.DailyCap = int(s.St.Num(store.KeyClefRate)), int(s.St.Num(store.KeyClefDailyCap))
	}
	if c.Model != ModelFull {
		c.Model = ModelFlash
	}
	if c.BaseURL == "" {
		c.BaseURL = DefaultBaseURL
	}
	if c.LLM.BaseURL == "" {
		c.LLM.BaseURL = DefaultLLMBaseURL
	}
	if c.LLM.Model == "" {
		c.LLM.Model = DefaultLLMModel
	}
	if c.LLM.Timeout < 10*time.Second || c.LLM.Timeout > 10*time.Minute {
		c.LLM.Timeout = 2 * time.Minute
	}
	c.LLM.Concurrency = int(clamp(float64(c.LLM.Concurrency), 1, 8))
	c.LLM.Thresholds.Apply = clamp(c.LLM.Thresholds.Apply, 0, 1)
	c.LLM.Thresholds.Review = clamp(c.LLM.Thresholds.Review, 0, c.LLM.Thresholds.Apply)
	if c.Thresholds.Metric != MetricConfidence {
		c.Thresholds.Metric = MetricProb
	}
	c.Thresholds.Apply = clamp(c.Thresholds.Apply, 0, 1)
	c.Thresholds.Margin = clamp(c.Thresholds.Margin, 0, 1)
	c.Thresholds.Review = clamp(c.Thresholds.Review, 0, c.Thresholds.Apply)
	c.RatePerMin = int(clamp(float64(c.RatePerMin), 1, 600))
	c.DailyCap = int(clamp(float64(c.DailyCap), 0, 1e6))
	return c
}

func (s *Service) client(c Config) Client {
	if s.NewClient != nil {
		return s.NewClient(c)
	}
	return &HTTPClient{BaseURL: c.BaseURL, AccountID: c.AccountID, Token: c.Token}
}

func (s *Service) llm(c Config) LLMClient {
	if s.NewLLM != nil {
		return s.NewLLM(c)
	}
	return &OpenAIClient{BaseURL: c.LLM.BaseURL, APIKey: c.LLM.APIKey}
}

func today() string { return time.Now().UTC().Format("2006-01-02") }

// admit applies the daily cap and the per-minute pacing before one request, and counts it.
func (s *Service) admit(ctx context.Context, c Config) error {
	day := today()
	if s.Meta.ClefCount(ctx, day) >= c.DailyCap {
		return ErrDailyCap
	}
	gap := time.Minute / time.Duration(c.RatePerMin)
	for {
		s.mu.Lock()
		wait := time.Until(s.lastReq.Add(gap))
		if wait <= 0 {
			s.lastReq = time.Now()
			s.mu.Unlock()
			break
		}
		s.mu.Unlock()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(wait):
		}
	}
	return s.Meta.AddClefCount(ctx, day, 1)
}

func (s *Service) record(err error, tokens int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.requests++
	if err != nil {
		s.lastErr, s.lastErrT = err.Error(), time.Now()
	} else {
		s.lastOK = time.Now()
		s.tokens += tokens
	}
}

// call sends one Clef request.
func (s *Service) call(ctx context.Context, c Config, cl Client, req Request) (*Response, error) {
	if err := s.admit(ctx, c); err != nil {
		return nil, err
	}
	rctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	resp, err := cl.Run(rctx, req)
	tok := 0
	if resp != nil {
		tok = resp.Usage.InputTokens
	}
	s.record(err, tok)
	return resp, err
}

// chat sends one LLM request.
func (s *Service) chat(c Config, cl LLMClient) func(context.Context, ChatRequest) (*ChatResult, error) {
	return func(ctx context.Context, req ChatRequest) (*ChatResult, error) {
		if err := s.admit(ctx, c); err != nil {
			return nil, err
		}
		if req.ReasoningEffort == "" && c.LLM.Effort != "default" {
			req.ReasoningEffort = c.LLM.Effort
		}
		rctx, cancel := context.WithTimeout(ctx, c.LLM.Timeout)
		defer cancel()
		res, err := cl.Chat(rctx, req)
		tok := 0
		if res != nil {
			tok = res.PromptTokens
		}
		s.record(err, tok)
		return res, err
	}
}

// GateRelease sends decisions that would be applied to human review when the torrent has no
// release time (缺少发布时间) or its release doesn't fit the episode's airdate (日期不符: more
// than 30 days after / 7 days before; without an airdate the work's airing window) —
// meta.ReleaseGate. Both backends.
func GateRelease(ctx context.Context, ms *meta.Store, t *meta.ClefTask, ds []FileDecision) {
	for i := range ds {
		d := &ds[i]
		if d.Status != StatusApplied || d.Candidate == nil {
			continue
		}
		k, ok := meta.ParseEpisodeKey(d.Key)
		if !ok {
			continue
		}
		if r := ms.ReleaseGate(ctx, t, d.Candidate.WorkKey, k); r != "" {
			d.Status, d.Reason = StatusPending, r
		}
	}
}

// TaskEvidence checks the task's title and file paths for a name of the work.
func TaskEvidence(ms *meta.Store, t *meta.ClefTask) Evidence {
	texts := []string{t.Title}
	for _, f := range t.Files {
		texts = append(texts, f.Path)
	}
	return func(ctx context.Context, workKey string) bool { return ms.NameEvidence(ctx, workKey, texts) }
}

// BuildOptions is how production Clef requests are built (calibrated, see
// docs/research/decision-model-calibration.md): the subject request (choice + one yes/no
// question per candidate) separate from the episode / kind request.
var BuildOptions = Options{Separate: true, Verify: true}

// Outcome is one decision run (job or dry run).
type Outcome struct {
	Provider  string            `json:"provider"`
	Task      *meta.ClefTask    `json:"task"`
	Model     string            `json:"model"`
	Requests  []Built           `json:"requests,omitempty"` // Clef
	Answers   map[string]Answer `json:"answers,omitempty"`  // Clef
	LLM       *LLMRun           `json:"llm,omitempty"`      // LLM
	Decisions []FileDecision    `json:"decisions"`
	Usage     Usage             `json:"usage"`
	Sent      int               `json:"sent"` // requests made
}

// Decide runs the model for a torrent (all: every video, for the dry run) and returns the
// decisions without writing anything.
func (s *Service) Decide(ctx context.Context, c Config, ih string, all bool) (*Outcome, error) {
	if !c.Configured() {
		return nil, ErrNotConfigured
	}
	t, err := s.Meta.BuildClefTask(ctx, ih, all)
	if err != nil {
		return nil, err
	}
	out := &Outcome{Provider: c.Provider, Task: t, Model: c.ModelName(), Decisions: []FileDecision{}}
	if c.Provider != ProviderClef {
		if len(t.Files) == 0 {
			return out, nil
		}
		search := func(ctx context.Context, title string, have map[string]bool) []meta.ClefCandidate {
			return s.Meta.CandidatesForTitle(ctx, title, have, 10)
		}
		run, err := RunLLM(ctx, s.chat(c, s.llm(c)), c.LLM.Model, t, search)
		out.LLM, out.Sent = run, run.Requests
		out.Usage = Usage{InputTokens: run.PromptTokens, OutputTokens: run.CompletionTokens}
		if err != nil {
			return out, err
		}
		out.Decisions = DecideLLM(ctx, t, run.Answers, c.LLM.Thresholds, s.Meta.ValidEpisode, TaskEvidence(s.Meta, t))
		GateRelease(ctx, s.Meta, t, out.Decisions)
		return out, nil
	}
	out.Answers = map[string]Answer{}
	out.Requests = Build(t, c.Model, BuildOptions)
	cl := s.client(c)
	for _, b := range out.Requests {
		resp, err := s.call(ctx, c, cl, b.Request)
		if err != nil {
			return out, err
		}
		out.Sent++
		out.Usage.InputTokens += resp.Usage.InputTokens
		out.Usage.OutputTokens += resp.Usage.OutputTokens
		for id, a := range resp.Answers {
			if _, ok := b.Request.Questions[id]; ok {
				out.Answers[id] = a
			}
		}
	}
	// the subject questions are asked once (separate request); episode/kind requests may be
	// split, but every file's questions are in one of them: decide over all answers
	out.Decisions = Decide(ctx, t, out.Answers, c.Thresholds, s.Meta.ValidEpisode)
	GateRelease(ctx, s.Meta, t, out.Decisions)
	return out, nil
}

// ProcessNow runs the pending job of ih right away (an interactive play request is waiting
// for it) and returns when it is done. If the worker is already on it, it waits for that
// run instead. No-op when the backend is disabled / not configured or nothing is pending.
func (s *Service) ProcessNow(ctx context.Context, ih string) error {
	s.init()
	c := s.Config()
	if !c.Enabled || !c.Configured() {
		return nil
	}
	s.mu.Lock()
	busy := s.current[ih]
	if !busy {
		s.current[ih] = true // reserved: the worker skips it
	}
	s.mu.Unlock()
	if busy {
		for {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(200 * time.Millisecond):
			}
			s.mu.Lock()
			busy = s.current[ih]
			s.mu.Unlock()
			if !busy {
				return nil
			}
		}
	}
	job, err := s.Meta.GetClefJob(ctx, ih)
	if err != nil || job == nil || job.Status != "pending" {
		s.mu.Lock()
		delete(s.current, ih)
		s.mu.Unlock()
		return err
	}
	return s.Process(ctx, c, job)
}

// Process runs one queued job: decide, store every decision, apply the confident ones.
func (s *Service) Process(ctx context.Context, c Config, job *meta.ClefJob) error {
	s.init()
	s.mu.Lock()
	s.current[job.Infohash] = true
	s.mu.Unlock()
	defer func() { s.mu.Lock(); delete(s.current, job.Infohash); s.mu.Unlock() }()
	out, err := s.Decide(ctx, c, job.Infohash, false)
	sent := 0
	if out != nil {
		sent = out.Sent
	}
	switch {
	case errors.Is(err, store.ErrNotFound):
		return s.Meta.FinishClefJob(ctx, job.Infohash, "failed", "torrent not in the catalogue", sent, "", false)
	case errors.Is(err, ErrDailyCap), QuotaExhausted(err):
		// partial runs are redone tomorrow (decisions are only written after all chunks)
		tomorrow := time.Now().UTC().Truncate(24 * time.Hour).Add(24*time.Hour + time.Minute).Format(time.RFC3339)
		return s.Meta.FinishClefJob(ctx, job.Infohash, "pending", err.Error(), sent, tomorrow, false)
	case err != nil:
		if ctx.Err() != nil {
			return ctx.Err()
		}
		status := "pending"
		if job.Attempts+1 >= 5 || !Retryable(err) {
			status = "failed"
		}
		retry := time.Now().Add(time.Duration(5<<job.Attempts) * time.Minute).UTC().Format(time.RFC3339)
		s.Log.Warn("decision model: request failed", "ih", job.Infohash, "attempt", job.Attempts+1, "err", err)
		return s.Meta.FinishClefJob(ctx, job.Infohash, status, err.Error(), sent, retry, true)
	}
	applied := 0
	for _, d := range out.Decisions {
		row := decisionRow(job.Infohash, out.Model, d)
		if d.Status == StatusApplied {
			if err := s.Meta.ApplyClef(ctx, job.Infohash, d.File.Index, d.Candidate.WorkKey, d.Kind, d.Episode, d.Score); err != nil {
				row.Status, row.Reason = StatusDiscarded, "conflict"
				if !errors.Is(err, meta.ErrClefConflict) {
					return err
				}
			} else {
				applied++
				row.DecidedAt, row.DecidedBy = store.Now(), "clef"
			}
		}
		if err := s.Meta.AddClefDecision(ctx, row); err != nil {
			return err
		}
	}
	s.Log.Info("decision model: decided", "ih", job.Infohash, "provider", c.Provider, "files", len(out.Decisions), "applied", applied, "requests", sent, "tokens", out.Usage.InputTokens)
	return s.Meta.FinishClefJob(ctx, job.Infohash, "done", "", sent, "", false)
}

func decisionRow(ih, model string, d FileDecision) *meta.ClefDecision {
	row := &meta.ClefDecision{Infohash: ih, FileIndex: d.File.Index, Path: d.File.Path, Kind: d.Kind, Episode: d.Episode, EpisodeKey: d.Key,
		Score: d.Score, Margin: d.Margin, Confidence: d.Confidence, Model: model, Status: d.Status, Reason: d.Reason}
	if d.Candidate != nil {
		row.WorkKey = d.Candidate.WorkKey
	}
	row.Answers, _ = json.Marshal(d.Answers)
	return row
}

// Run is the queue worker: newest due jobs first, up to the provider's concurrency at once
// (Clef: 1, LLM: setting); pending jobs older than 3 days are expired (no backlog from a
// disabled period).
func (s *Service) Run(ctx context.Context) {
	s.init()
	var wg sync.WaitGroup
	defer wg.Wait()
	done := make(chan struct{}, 16)
	running := 0
	for {
		wait := 20 * time.Second
		c := s.Config()
		if c.Enabled && c.Configured() && running < c.concurrency() {
			_, _ = s.Meta.ExpireClefJobs(ctx, time.Now().Add(-72*time.Hour).UTC().Format(time.RFC3339))
			s.mu.Lock()
			busy := make([]string, 0, len(s.current))
			for ih := range s.current {
				busy = append(busy, ih)
			}
			s.mu.Unlock()
			job, err := s.Meta.NextClefJob(ctx, store.Now(), busy...)
			if err != nil {
				s.Log.Warn("decision model: queue", "err", err)
			} else if job != nil {
				running++
				s.mu.Lock()
				s.current[job.Infohash] = true // reserved before the goroutine starts
				s.mu.Unlock()
				wg.Add(1)
				go func() {
					defer wg.Done()
					if err := s.Process(ctx, c, job); err != nil && ctx.Err() == nil {
						s.Log.Warn("decision model: job", "ih", job.Infohash, "err", err)
					}
					done <- struct{}{}
				}()
				wait = 0
			}
		}
		if wait == 0 && running < c.concurrency() {
			if ctx.Err() != nil {
				return
			}
			continue
		}
		select {
		case <-ctx.Done():
			return
		case <-s.wake:
		case <-done:
			running--
		case <-time.After(wait):
		}
	}
}

// ---- admin ----

// StatusView is GET /admin/clef.
type StatusView struct {
	Enabled       bool           `json:"enabled"`
	Configured    bool           `json:"configured"`
	Provider      string         `json:"provider"`
	Model         string         `json:"model"`
	BaseURL       string         `json:"base_url"`
	Thresholds    Thresholds     `json:"thresholds"`     // Clef
	LLMThresholds LLMThresholds  `json:"llm_thresholds"` // LLM
	Concurrency   int            `json:"concurrency"`
	RatePerMin    int            `json:"rate_per_min"`
	DailyCap      int            `json:"daily_cap"`
	Today         int            `json:"today"` // requests today (UTC)
	Jobs          map[string]int `json:"jobs"`
	Decisions     map[string]int `json:"decisions"`
	Recent        []meta.ClefJob `json:"recent_jobs"`
	Current       string         `json:"current,omitempty"`
	LastError     string         `json:"last_error,omitempty"`
	LastErrAt     string         `json:"last_error_at,omitempty"`
	LastOKAt      string         `json:"last_ok_at,omitempty"`
	Session       map[string]int `json:"session"` // since start: requests, input_tokens
}

func ts(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

// Status returns the admin view.
func (s *Service) Status(ctx context.Context) (*StatusView, error) {
	c := s.Config()
	v := &StatusView{Enabled: c.Enabled, Configured: c.Configured(), Provider: c.Provider, Model: c.ModelName(), BaseURL: c.BaseURL, Thresholds: c.Thresholds,
		LLMThresholds: c.LLM.Thresholds, Concurrency: c.concurrency(), RatePerMin: c.RatePerMin, DailyCap: c.DailyCap, Today: s.Meta.ClefCount(ctx, today())}
	if c.Provider != ProviderClef {
		v.BaseURL = c.LLM.BaseURL
	}
	var err error
	if v.Jobs, err = s.Meta.ClefJobCounts(ctx); err != nil {
		return nil, err
	}
	if v.Decisions, err = s.Meta.ClefDecisionCounts(ctx); err != nil {
		return nil, err
	}
	if v.Recent, err = s.Meta.ListClefJobs(ctx, 20); err != nil {
		return nil, err
	}
	s.mu.Lock()
	cur := make([]string, 0, len(s.current))
	for ih := range s.current {
		cur = append(cur, ih)
	}
	sort.Strings(cur)
	v.Current = strings.Join(cur, ",")
	v.LastError, v.LastErrAt, v.LastOKAt = s.lastErr, ts(s.lastErrT), ts(s.lastOK)
	v.Session = map[string]int{"requests": s.requests, "input_tokens": s.tokens}
	s.mu.Unlock()
	return v, nil
}

// ErrState: the decision is not in a state that allows the action.
var ErrState = errors.New("decision state does not allow this")

// Accept applies a pending decision (admin).
func (s *Service) Accept(ctx context.Context, id int64, by string) (*meta.ClefDecision, error) {
	d, err := s.Meta.GetClefDecision(ctx, id)
	if err != nil {
		return nil, err
	}
	// pending: the review queue; discarded / rejected: an admin overrides the gates (e.g. a
	// right answer dropped by the season check) — applied as a human-verified mapping
	manual := d.Status == StatusDiscarded || d.Status == StatusRejected
	if (d.Status != StatusPending && !manual) || d.WorkKey == "" {
		return nil, fmt.Errorf("%w: %s", ErrState, d.Status)
	}
	k, ok := meta.KeyOfFile(d.Kind, d.Episode)
	if !ok {
		return nil, fmt.Errorf("%w: no episode", ErrState)
	}
	if _, ok := s.Meta.ValidEpisode(ctx, d.WorkKey, k); !ok {
		return nil, fmt.Errorf("%w: episode not official", ErrState)
	}
	var apply error
	if manual {
		apply = s.Meta.ApplyOverride(ctx, d.Infohash, d.FileIndex, d.WorkKey, d.Kind, d.Episode, by)
	} else if d.Source == "rule" {
		apply = s.Meta.ApplyReviewed(ctx, d.Infohash, d.FileIndex, d.WorkKey, d.Kind, d.Episode, by)
	} else {
		apply = s.Meta.ApplyClef(ctx, d.Infohash, d.FileIndex, d.WorkKey, d.Kind, d.Episode, d.Score)
	}
	if apply != nil {
		return nil, apply
	}
	if err := s.Meta.SetClefDecisionStatus(ctx, id, StatusApplied, by); err != nil {
		return nil, err
	}
	return s.Meta.GetClefDecision(ctx, id)
}

// Reject rejects a pending decision, or reverts an applied one (the file becomes unmapped,
// verified by the admin).
func (s *Service) Reject(ctx context.Context, id int64, by string) (*meta.ClefDecision, error) {
	d, err := s.Meta.GetClefDecision(ctx, id)
	if err != nil {
		return nil, err
	}
	switch d.Status {
	case StatusPending:
	case StatusApplied:
		if err := s.Meta.RevertClef(ctx, d.Infohash, d.FileIndex, by); err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("%w: %s", ErrState, d.Status)
	}
	if err := s.Meta.SetClefDecisionStatus(ctx, id, StatusRejected, by); err != nil {
		return nil, err
	}
	return s.Meta.GetClefDecision(ctx, id)
}
