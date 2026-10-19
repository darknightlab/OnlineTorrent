package clef

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"strings"
	"sync"
	"time"

	"github.com/darknightlab/onlinetorrent/internal/meta"
	"github.com/darknightlab/onlinetorrent/internal/store"
)

// Config is the decision-model configuration (settings group 决策模型).
type Config struct {
	Enabled    bool
	AccountID  string
	Token      string
	Model      string
	BaseURL    string
	Thresholds Thresholds
	RatePerMin int
	DailyCap   int
}

// Configured reports whether requests can be made.
func (c Config) Configured() bool { return c.AccountID != "" && c.Token != "" }

// Errors.
var (
	ErrNotConfigured = errors.New("决策模型未配置（Account ID / API Token）")
	ErrDailyCap      = errors.New("已达到每日请求上限")
)

// Service is the queue worker plus the admin operations.
type Service struct {
	St   *store.Store
	Meta *meta.Store
	Log  *slog.Logger
	// NewClient builds the API client (nil: HTTPClient). Tests use a fake.
	NewClient func(Config) Client

	wake chan struct{}
	once sync.Once

	mu       sync.Mutex
	lastReq  time.Time
	lastErr  string
	lastErrT time.Time
	lastOK   time.Time
	current  string
	tokens   int // input tokens since start
	requests int // requests since start
}

func (s *Service) init() { s.once.Do(func() { s.wake = make(chan struct{}, 1) }) }

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
		Enabled: s.St.Bool(store.KeyClefEnabled), AccountID: strings.TrimSpace(s.St.Str(store.KeyClefAccountID)),
		Token: strings.TrimSpace(s.St.Str(store.KeyClefToken)), Model: s.St.Str(store.KeyClefModel), BaseURL: strings.TrimSpace(s.St.Str(store.KeyClefBaseURL)),
		Thresholds: Thresholds{Metric: s.St.Str(store.KeyClefMetric), Apply: s.St.Num(store.KeyClefApply), Margin: s.St.Num(store.KeyClefMargin), Review: s.St.Num(store.KeyClefReview)},
		RatePerMin: int(s.St.Num(store.KeyClefRate)), DailyCap: int(s.St.Num(store.KeyClefDailyCap)),
	}
	if c.Model != ModelFull {
		c.Model = ModelFlash
	}
	if c.BaseURL == "" {
		c.BaseURL = DefaultBaseURL
	}
	if c.Thresholds.Metric != MetricConfidence {
		c.Thresholds.Metric = MetricProb
	}
	clamp := func(v, lo, hi float64) float64 { return math.Max(lo, math.Min(hi, v)) }
	c.Thresholds.Apply = clamp(c.Thresholds.Apply, 0, 1)
	c.Thresholds.Margin = clamp(c.Thresholds.Margin, 0, 1)
	c.Thresholds.Review = clamp(c.Thresholds.Review, 0, c.Thresholds.Apply)
	c.RatePerMin = int(clamp(float64(c.RatePerMin), 1, 300))
	c.DailyCap = int(clamp(float64(c.DailyCap), 0, 1e6))
	return c
}

func (s *Service) client(c Config) Client {
	if s.NewClient != nil {
		return s.NewClient(c)
	}
	return &HTTPClient{BaseURL: c.BaseURL, AccountID: c.AccountID, Token: c.Token}
}

func today() string { return time.Now().UTC().Format("2006-01-02") }

// call sends one request: daily cap, then the per-minute pacing, then the API.
func (s *Service) call(ctx context.Context, c Config, cl Client, req Request) (*Response, error) {
	day := today()
	if s.Meta.ClefCount(ctx, day) >= c.DailyCap {
		return nil, ErrDailyCap
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
			return nil, ctx.Err()
		case <-time.After(wait):
		}
	}
	_ = s.Meta.AddClefCount(ctx, day, 1)
	rctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	resp, err := cl.Run(rctx, req)
	s.mu.Lock()
	s.requests++
	if err != nil {
		s.lastErr, s.lastErrT = err.Error(), time.Now()
	} else {
		s.lastOK = time.Now()
		s.tokens += resp.Usage.InputTokens
	}
	s.mu.Unlock()
	return resp, err
}

// BuildOptions is how production requests are built (calibrated, see
// docs/research/clef-calibration.md): the subject request (choice + one yes/no question per
// candidate) separate from the episode / kind request.
var BuildOptions = Options{Separate: true, Verify: true}

// Outcome is one decision run (job or dry run).
type Outcome struct {
	Task      *meta.ClefTask    `json:"task"`
	Model     string            `json:"model"`
	Requests  []Built           `json:"requests"`
	Answers   map[string]Answer `json:"answers"`
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
	out := &Outcome{Task: t, Model: c.Model, Answers: map[string]Answer{}, Decisions: []FileDecision{}}
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
	return out, nil
}

// Process runs one queued job: decide, store every decision, apply the confident ones.
func (s *Service) Process(ctx context.Context, c Config, job *meta.ClefJob) error {
	s.mu.Lock()
	s.current = job.Infohash
	s.mu.Unlock()
	defer func() { s.mu.Lock(); s.current = ""; s.mu.Unlock() }()
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
		s.Log.Warn("clef: request failed", "ih", job.Infohash, "attempt", job.Attempts+1, "err", err)
		return s.Meta.FinishClefJob(ctx, job.Infohash, status, err.Error(), sent, retry, true)
	}
	applied := 0
	for _, d := range out.Decisions {
		row := decisionRow(job.Infohash, c.Model, d)
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
	s.Log.Info("clef: decided", "ih", job.Infohash, "files", len(out.Decisions), "applied", applied, "requests", sent, "tokens", out.Usage.InputTokens)
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

// Run is the queue worker: newest due job first, one at a time; pending jobs older than
// 3 days are expired (no backlog from a disabled period).
func (s *Service) Run(ctx context.Context) {
	s.init()
	for {
		wait := 20 * time.Second
		c := s.Config()
		if c.Enabled && c.Configured() {
			_, _ = s.Meta.ExpireClefJobs(ctx, time.Now().Add(-72*time.Hour).UTC().Format(time.RFC3339))
			job, err := s.Meta.NextClefJob(ctx, store.Now())
			if err != nil {
				s.Log.Warn("clef: queue", "err", err)
			} else if job != nil {
				if err := s.Process(ctx, c, job); err != nil && ctx.Err() == nil {
					s.Log.Warn("clef: job", "ih", job.Infohash, "err", err)
				}
				wait = 0
			}
		}
		if wait == 0 {
			if ctx.Err() != nil {
				return
			}
			continue
		}
		select {
		case <-ctx.Done():
			return
		case <-s.wake:
		case <-time.After(wait):
		}
	}
}

// ---- admin ----

// StatusView is GET /admin/clef.
type StatusView struct {
	Enabled    bool           `json:"enabled"`
	Configured bool           `json:"configured"`
	Model      string         `json:"model"`
	BaseURL    string         `json:"base_url"`
	Thresholds Thresholds     `json:"thresholds"`
	RatePerMin int            `json:"rate_per_min"`
	DailyCap   int            `json:"daily_cap"`
	Today      int            `json:"today"` // requests today (UTC)
	Jobs       map[string]int `json:"jobs"`
	Decisions  map[string]int `json:"decisions"`
	Recent     []meta.ClefJob `json:"recent_jobs"`
	Current    string         `json:"current,omitempty"`
	LastError  string         `json:"last_error,omitempty"`
	LastErrAt  string         `json:"last_error_at,omitempty"`
	LastOKAt   string         `json:"last_ok_at,omitempty"`
	Session    map[string]int `json:"session"` // since start: requests, input_tokens
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
	v := &StatusView{Enabled: c.Enabled, Configured: c.Configured(), Model: c.Model, BaseURL: c.BaseURL, Thresholds: c.Thresholds,
		RatePerMin: c.RatePerMin, DailyCap: c.DailyCap, Today: s.Meta.ClefCount(ctx, today())}
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
	v.Current, v.LastError, v.LastErrAt, v.LastOKAt = s.current, s.lastErr, ts(s.lastErrT), ts(s.lastOK)
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
	if d.Status != StatusPending || d.WorkKey == "" {
		return nil, fmt.Errorf("%w: %s", ErrState, d.Status)
	}
	k, ok := meta.KeyOfFile(d.Kind, d.Episode)
	if !ok {
		return nil, fmt.Errorf("%w: no episode", ErrState)
	}
	if _, ok := s.Meta.ValidEpisode(ctx, d.WorkKey, k); !ok {
		return nil, fmt.Errorf("%w: episode not official", ErrState)
	}
	if err := s.Meta.ApplyClef(ctx, d.Infohash, d.FileIndex, d.WorkKey, d.Kind, d.Episode, d.Score); err != nil {
		return nil, err
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
