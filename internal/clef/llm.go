package clef

// LLM backend: an OpenAI-compatible chat-completions endpoint with structured output
// (response_format json_schema, strict). One request per torrent (chunks of ≤ 60 files for
// very large torrents, same candidates); when the model finds no fitting candidate it may
// suggest a title, which is searched in the catalogue and asked about once more. The answers
// go through the same hard validation as Clef's (DecideLLM).

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/darknightlab/onlinetorrent/internal/meta"
)

// DefaultLLMBaseURL / DefaultLLMModel: the owner's gateway.
const (
	DefaultLLMBaseURL = "https://sub2api.projectk.org/v1"
	DefaultLLMModel   = "gpt-6-luna"
	// LLMChunkFiles: files per request; larger torrents are split (same candidates)
	LLMChunkFiles = 60
	// MaxLLMCandidates: candidates after adding the suggested-title search results
	MaxLLMCandidates = 30
)

// ChatMessage is one chat message.
type ChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// ChatRequest is the chat-completions body.
type ChatRequest struct {
	Model          string        `json:"model"`
	Messages       []ChatMessage `json:"messages"`
	Temperature    float64       `json:"temperature"`
	ResponseFormat any           `json:"response_format"`
	// ReasoningEffort: minimal | low | medium | high ("" = the model's default)
	ReasoningEffort string `json:"reasoning_effort,omitempty"`
}

// ChatResult is what we keep of a completion.
type ChatResult struct {
	Content          string
	PromptTokens     int
	CompletionTokens int
	Latency          time.Duration
}

// LLMClient runs one chat completion.
type LLMClient interface {
	Chat(ctx context.Context, req ChatRequest) (*ChatResult, error)
}

// OpenAIClient calls POST {base}/chat/completions.
type OpenAIClient struct {
	BaseURL string
	APIKey  string
	HTTP    *http.Client
}

func (c *OpenAIClient) Chat(ctx context.Context, req ChatRequest) (*ChatResult, error) {
	if c.APIKey == "" {
		return nil, &APIError{Status: 400, Message: "api key not set"}
	}
	base := strings.TrimRight(c.BaseURL, "/")
	if base == "" {
		base = DefaultLLMBaseURL
	}
	body, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	hr, err := http.NewRequestWithContext(ctx, "POST", base+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	hr.Header.Set("Authorization", "Bearer "+c.APIKey)
	hr.Header.Set("Content-Type", "application/json")
	hr.Header.Set("User-Agent", "onlinetorrent/2 (decision model)") // the gateway's WAF rejects default UAs
	hc := c.HTTP
	if hc == nil {
		hc = &http.Client{}
	}
	start := time.Now()
	resp, err := hc.Do(hr)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	res, err := ParseChatResponse(resp.StatusCode, raw)
	if res != nil {
		res.Latency = time.Since(start)
	}
	return res, err
}

// ParseChatResponse decodes a chat-completions response (errors as APIError).
func ParseChatResponse(status int, raw []byte) (*ChatResult, error) {
	var r struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
				Refusal string `json:"refusal"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Usage struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
		} `json:"usage"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	_ = json.Unmarshal(raw, &r)
	if status < 200 || status > 299 || r.Error != nil {
		msg := strings.TrimSpace(string(raw))
		if r.Error != nil && r.Error.Message != "" {
			msg = r.Error.Message
		}
		if len(msg) > 300 {
			msg = msg[:300]
		}
		if status >= 200 && status <= 299 {
			status = 502
		}
		return nil, &APIError{Status: status, Message: msg}
	}
	if len(r.Choices) == 0 {
		return nil, &APIError{Status: 502, Message: "response without choices"}
	}
	c := r.Choices[0]
	if c.Message.Refusal != "" {
		return nil, &APIError{Status: 422, Message: "refusal: " + c.Message.Refusal}
	}
	if c.FinishReason == "length" {
		return nil, &APIError{Status: 502, Message: "output truncated (finish_reason length)"}
	}
	return &ChatResult{Content: c.Message.Content, PromptTokens: r.Usage.PromptTokens, CompletionTokens: r.Usage.CompletionTokens}, nil
}

// ---- request ----

// llmSystem is short on purpose (owner decision): the model gets the information (titles in
// every language, types, air dates, the release date, official episode numbers) rather than
// rules.
const llmSystem = `Map every video file of an anime torrent to one of the candidate bangumi entries (one entry = one TV season, movie or OVA; sequels, split cours, remakes and re-edits are separate entries). Use the titles, any season stated in the name and the dates: a file is normally released shortly after its episode aired, and "released" is the torrent's release date.

Per file: subject_id = a candidate id, or null if none fits; episode = the number exactly as written in the file name (a special's own number: NCOP2 → 2), null if none; kind = main | sp | op | ed | pv | cm | other (menu, preview, bonus, a file holding several episodes); confidence 0–1; reason = a few words.
suggested_title: the work's title if no candidate fits, else null.`

type llmCandidate struct {
	ID           string   `json:"id"`
	Title        string   `json:"title"`
	TitleCN      string   `json:"title_cn,omitempty"`
	Aliases      []string `json:"aliases,omitempty"`
	Romaji       string   `json:"romaji,omitempty"`
	Type         string   `json:"type,omitempty"`
	Season       int      `json:"season,omitempty"`
	AirDate      string   `json:"air_date,omitempty"`
	EndDate      string   `json:"end_date,omitempty"`
	Eps          int      `json:"eps,omitempty"`
	MainEpisodes string   `json:"main_episodes,omitempty"`
	Specials     string   `json:"specials,omitempty"`
}

type llmFile struct {
	Index int    `json:"index"`
	Path  string `json:"path"`
}

type llmInput struct {
	TorrentTitle string         `json:"torrent_title"`
	Released     string         `json:"released,omitempty"`
	Files        []llmFile      `json:"files"`
	OtherFiles   int            `json:"other_files_in_torrent,omitempty"`
	Candidates   []llmCandidate `json:"candidates"`
}

// LLMFileOut is the model's answer for one file.
type LLMFileOut struct {
	Index      int      `json:"index"`
	SubjectID  *string  `json:"subject_id"`
	Episode    *float64 `json:"episode"`
	Kind       string   `json:"kind"`
	Confidence float64  `json:"confidence"`
	Reason     string   `json:"reason"`
}

// LLMOutput is the structured output.
type LLMOutput struct {
	Files          []LLMFileOut `json:"files"`
	SuggestedTitle *string      `json:"suggested_title"`
}

func llmSchema(ids []string) map[string]any {
	subject := any(map[string]any{"type": "null"})
	if len(ids) > 0 {
		subject = map[string]any{"anyOf": []any{map[string]any{"type": "string", "enum": ids}, map[string]any{"type": "null"}}}
	}
	kinds := make([]string, len(kindOptions))
	for i, k := range kindOptions {
		kinds[i] = k.id
	}
	file := map[string]any{
		"type": "object", "additionalProperties": false,
		"required": []string{"index", "subject_id", "episode", "kind", "confidence", "reason"},
		"properties": map[string]any{
			"index":      map[string]any{"type": "integer"},
			"subject_id": subject,
			"episode":    map[string]any{"type": []string{"number", "null"}},
			"kind":       map[string]any{"type": "string", "enum": kinds},
			"confidence": map[string]any{"type": "number"},
			"reason":     map[string]any{"type": "string"},
		},
	}
	return map[string]any{
		"type": "object", "additionalProperties": false,
		"required": []string{"files", "suggested_title"},
		"properties": map[string]any{
			"files":           map[string]any{"type": "array", "items": file},
			"suggested_title": map[string]any{"type": []string{"string", "null"}},
		},
	}
}

// BuildLLMRequest builds the request for the given files (indexes into t.Files).
func BuildLLMRequest(t *meta.ClefTask, model string, files []int) ChatRequest {
	in := llmInput{TorrentTitle: t.Title, Released: t.Published, Files: []llmFile{}, Candidates: []llmCandidate{}}
	for _, fi := range files {
		in.Files = append(in.Files, llmFile{Index: t.Files[fi].Index, Path: t.Files[fi].Path})
	}
	if n := len(t.Videos) - len(files); n > 0 {
		in.OtherFiles = n
	}
	ids := make([]string, 0, len(t.Candidates))
	for _, c := range t.Candidates {
		ids = append(ids, c.ID)
		in.Candidates = append(in.Candidates, llmCandidate{ID: c.ID, Title: c.Title, TitleCN: c.TitleCN, Aliases: c.Aliases, Romaji: c.Romaji, Type: c.Type, Season: c.Season,
			AirDate: c.AirDate, EndDate: c.EndDate, Eps: c.Eps, MainEpisodes: c.MainEpisodes, Specials: c.Specials})
	}
	user, _ := json.Marshal(in)
	return ChatRequest{Model: model, Temperature: 0,
		Messages:       []ChatMessage{{Role: "system", Content: llmSystem}, {Role: "user", Content: string(user)}},
		ResponseFormat: map[string]any{"type": "json_schema", "json_schema": map[string]any{"name": "file_mapping", "strict": true, "schema": llmSchema(ids)}}}
}

// ParseLLMOutput decodes the structured output.
func ParseLLMOutput(content string) (*LLMOutput, error) {
	var out LLMOutput
	if err := json.Unmarshal([]byte(content), &out); err != nil {
		return nil, &APIError{Status: 502, Message: "invalid structured output: " + err.Error()}
	}
	return &out, nil
}

// ---- run ----

// SearchFunc finds more candidates for a suggested title (skipping work keys in have).
type SearchFunc func(ctx context.Context, title string, have map[string]bool) []meta.ClefCandidate

// LLMRun records one decision run.
type LLMRun struct {
	Model            string             `json:"model"`
	Rounds           int                `json:"rounds"`
	Requests         int                `json:"requests"`
	PromptTokens     int                `json:"prompt_tokens"`
	CompletionTokens int                `json:"completion_tokens"`
	LatencyMS        int64              `json:"latency_ms"` // sum over requests
	Suggested        string             `json:"suggested_title,omitempty"`
	Added            []string           `json:"added_candidates,omitempty"` // ids found for the suggestion
	Answers          map[int]LLMFileOut `json:"answers"`
}

// RunLLM asks the model about every file of the task (chunks of LLMChunkFiles), then — if
// some file got no subject and the model suggested a title that the catalogue search turns
// into new candidates — once more for those files with the extra candidates (added to
// t.Candidates). chat sends one request (pacing / caps are the caller's).
func RunLLM(ctx context.Context, chat func(context.Context, ChatRequest) (*ChatResult, error), model string, t *meta.ClefTask, search SearchFunc) (*LLMRun, error) {
	run := &LLMRun{Model: model, Answers: map[int]LLMFileOut{}}
	ask := func(files []int) (string, error) {
		suggested := ""
		run.Rounds++
		for start := 0; start < len(files); start += LLMChunkFiles {
			chunk := files[start:min(len(files), start+LLMChunkFiles)]
			res, err := chat(ctx, BuildLLMRequest(t, model, chunk))
			if err != nil {
				return "", err
			}
			run.Requests++
			run.PromptTokens += res.PromptTokens
			run.CompletionTokens += res.CompletionTokens
			run.LatencyMS += res.Latency.Milliseconds()
			out, err := ParseLLMOutput(res.Content)
			if err != nil {
				return "", err
			}
			want := map[int]bool{}
			for _, fi := range chunk {
				want[t.Files[fi].Index] = true
			}
			for _, a := range out.Files {
				if want[a.Index] {
					run.Answers[a.Index] = a
				}
			}
			if out.SuggestedTitle != nil && suggested == "" {
				suggested = strings.TrimSpace(*out.SuggestedTitle)
			}
		}
		return suggested, nil
	}
	all := make([]int, len(t.Files))
	for i := range all {
		all[i] = i
	}
	if len(all) == 0 || len(t.Candidates) == 0 && search == nil {
		return run, nil
	}
	suggested, err := ask(all)
	if err != nil {
		return run, err
	}
	var missing []int
	for i, f := range t.Files {
		if a, ok := run.Answers[f.Index]; !ok || a.SubjectID == nil {
			missing = append(missing, i)
		}
	}
	if len(missing) == 0 || suggested == "" || search == nil {
		return run, nil
	}
	run.Suggested = suggested
	have := map[string]bool{}
	for _, c := range t.Candidates {
		have[c.WorkKey] = true
	}
	extra := search(ctx, suggested, have)
	if room := MaxLLMCandidates - len(t.Candidates); len(extra) > room {
		extra = extra[:max(0, room)]
	}
	if len(extra) == 0 {
		return run, nil
	}
	for _, c := range extra {
		run.Added = append(run.Added, c.ID)
	}
	t.Candidates = append(t.Candidates, extra...)
	_, err = ask(missing)
	return run, err
}

// ---- decide ----

// LLMThresholds route validated LLM answers: confidence ≥ Apply (and, with Agreement, the
// same number the file-name parser found, when it found one) → applied; ≥ Review → review;
// otherwise not mapped.
type LLMThresholds struct {
	Apply     float64 `json:"apply"`
	Review    float64 `json:"review"`
	Agreement bool    `json:"require_agreement"`
}

// DefaultLLMThresholds are the calibrated defaults (docs/research/decision-model-calibration.md).
var DefaultLLMThresholds = LLMThresholds{Apply: 0.9, Review: 0.5, Agreement: true}

// Extra reasons of the LLM backend (all pending: review instead of apply).
const (
	ReasonDisagrees = "parser_disagrees" // the parser read another number
	ReasonNoName    = "no_name_evidence" // the title / paths don't carry a name of the work
	ReasonUnlisted  = "unlisted_special" // an unlisted special without an applied main episode of the work
)

// parser kinds that come from an explicit keyword (an "episode" may still be an OVA part)
var strictKinds = map[string]bool{"op": true, "ed": true, "pv": true, "cm": true, "extra": true}

// Evidence reports whether the torrent's title / paths name the work (meta.NameEvidence).
type Evidence func(ctx context.Context, workKey string) bool

// DecideLLM validates the model's answers (same hard checks as Clef: candidate id, a number
// written in the file name, the key in the official list after the split-cour shift, season
// consistency) and routes them by confidence.
//
// Only these are applied automatically (calibration: self-reported confidence is ~0.99 for
// nearly everything, right or wrong): confidence ≥ Apply, the torrent title / paths name the
// chosen work (evidence; nil = not checked), a special kind the work's official list has (or
// next to an applied main episode), the parser's number when it found one. Otherwise review.
// The catalogue post-checks (Refine: date gate, batch rule, prequel numbering, season
// continuation) run afterwards, for both backends.
func DecideLLM(ctx context.Context, t *meta.ClefTask, answers map[int]LLMFileOut, th LLMThresholds, valid Validator, evidence Evidence) []FileDecision {
	cands := map[string]*meta.ClefCandidate{}
	for i := range t.Candidates {
		cands[t.Candidates[i].ID] = &t.Candidates[i]
	}
	type sk struct{ subj, kind string }
	count := map[sk]int{}
	for _, f := range t.Files {
		if a, ok := answers[f.Index]; ok && a.SubjectID != nil {
			count[sk{*a.SubjectID, a.Kind}]++
		}
	}
	out := make([]FileDecision, 0, len(t.Files))
	for _, f := range t.Files {
		d := FileDecision{File: f, Answers: map[string]AnswerSummary{}, Margin: -1}
		discard := func(r string) {
			d.Status, d.Reason = StatusDiscarded, r
			out = append(out, d)
		}
		a, ok := answers[f.Index]
		if !ok {
			discard(ReasonNoAnswer)
			continue
		}
		conf := math.Max(0, math.Min(1, a.Confidence))
		d.Score, d.Confidence = conf, conf
		subj := None
		if a.SubjectID != nil {
			subj = *a.SubjectID
		}
		ep := ""
		if a.Episode != nil {
			ep = fmtNum(*a.Episode)
		}
		d.Answers["llm"] = AnswerSummary{Choice: strings.TrimSpace(subj + " " + a.Kind + " " + ep), Confidence: conf, Note: a.Reason}
		if a.SubjectID == nil {
			discard(ReasonNone)
			continue
		}
		c := cands[subj]
		if c == nil {
			discard(ReasonBadSubject)
			continue
		}
		d.Candidate = c
		d.Kind = FileKindOf(a.Kind)
		if d.Kind == "" {
			discard(ReasonNotEpisode)
			continue
		}
		var num *float64
		// A work's official singleton proves the number even when it is not written.
		// Exactly one answer may claim that main chapter: don't guess several parts as
		// duplicate mains. File kind and raw parser checks exclude specials/extras/IV.
		implicit := d.Kind == "episode" && count[sk{subj, a.Kind}] == 1 &&
			c.OfficialSingleton != nil && (a.Episode == nil || *a.Episode == *c.OfficialSingleton) &&
			(f.Kind == "other" || f.Kind == "episode" || f.Kind == "") &&
			f.Episode == nil && meta.UnnumberedMainVideo(f.Path)
		if implicit {
			v := *c.OfficialSingleton
			num = &v
		} else if a.Episode != nil {
			// the number must be written in the file name (or, single video, the title)
			for _, n := range f.Numbers {
				if n.Value == *a.Episode {
					v := n.Value
					num = &v
				}
			}
			if num == nil {
				discard(ReasonBadNumber)
				continue
			}
		} else if count[sk{subj, a.Kind}] == 1 && d.Kind != "episode" {
			one := 1.0
			num = &one
		} else {
			discard(ReasonNoNumber)
			continue
		}
		d.Episode = num
		k, ok := meta.KeyOfFile(d.Kind, num)
		if !ok {
			discard(ReasonBadNumber)
			continue
		}
		ck, ok := valid(ctx, c.WorkKey, k)
		d.Key = ck.String()
		if !c.SeasonFits(f.Season) {
			discard(ReasonSeason)
			continue
		}
		if !ok {
			discard(ReasonNotOfficial)
			continue
		}
		gateLLM(ctx, &d, th, evidence, numberAgrees(ctx, &d, valid))
		out = append(out, d)
	}
	// a special the work's official list doesn't have (OP/ED/PV are rarely listed) is only
	// applied while the work has an applied main episode: in this run, or a main-episode file
	// of any torrent already mapped to it (MainMapped; rules, model or admin). Otherwise
	// which season's NCOP it is can't be checked.
	mains := map[string]bool{}
	for _, d := range out {
		if d.Status == StatusApplied && d.Kind == "episode" {
			mains[d.Candidate.WorkKey] = true
		}
	}
	for i := range out {
		d := &out[i]
		if d.Status != StatusApplied || d.Kind == "episode" {
			continue
		}
		if k, ok := meta.ParseEpisodeKey(d.Key); ok && !strings.Contains(d.Candidate.Specials, k.Kind) && !mains[d.Candidate.WorkKey] && !d.Candidate.MainMapped {
			d.Status, d.Reason = StatusPending, ReasonUnlisted
		}
	}
	return out
}

// numberAgrees: the parser found no number, or the same one — compared after the split-cour
// shift, so "Slime 4th Season - 24(96)" read as 24 by the parser and as 96 by the model is
// the same official episode (96).
func numberAgrees(ctx context.Context, d *FileDecision, valid Validator) bool {
	f := d.File
	if f.Episode == nil || d.Episode == nil || *f.Episode == *d.Episode {
		return true
	}
	if d.Kind != "episode" || d.Candidate == nil {
		return false
	}
	pk, ok := meta.KeyOfFile("episode", f.Episode)
	if !ok {
		return false
	}
	pc, _ := valid(ctx, d.Candidate.WorkKey, pk)
	return pc.String() == d.Key
}

// gateLLM routes a validated answer: confidence ≥ Apply, the parser's number (numberAgrees)
// and a clear special kind of the parser (NCOP/NCED/PV/CM keyword, or Menu/Preview/特典 =
// extra) not contradicted, and the work named by the torrent (evidence) → applied; else
// review / not mapped.
func gateLLM(ctx context.Context, d *FileDecision, th LLMThresholds, evidence Evidence, numberOK bool) {
	f, conf := d.File, d.Confidence
	disagrees := th.Agreement && (!numberOK || (strictKinds[f.Kind] && f.Kind != d.Kind))
	switch {
	case conf >= th.Apply && disagrees:
		d.Status, d.Reason = StatusPending, ReasonDisagrees
	case conf >= th.Apply && evidence != nil && !evidence(ctx, d.Candidate.WorkKey):
		d.Status, d.Reason = StatusPending, ReasonNoName
	case conf >= th.Apply:
		d.Status, d.Reason = StatusApplied, ""
	case conf >= th.Review:
		d.Status, d.Reason = StatusPending, ""
	default:
		d.Status, d.Reason = StatusDiscarded, ReasonLowConfidence
	}
}

func fmtNum(v float64) string {
	return strings.TrimSuffix(strings.TrimRight(fmt.Sprintf("%.3f", v), "0"), ".")
}

// ErrNoCandidates: nothing to choose from (no request is made).
var ErrNoCandidates = errors.New("no candidates")
