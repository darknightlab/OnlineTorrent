package clef

import (
	"encoding/json"
	"strconv"
	"strings"

	"github.com/darknightlab/onlinetorrent/internal/meta"
)

// None is the abstain option of every question.
const None = "none"

// MaxQuestions is the API limit per request.
const MaxQuestions = 64

// kind options (ids → torrent_files.kind) and their descriptions
var kindOptions = []struct{ id, fileKind, desc string }{
	{"main", "episode", "A regular episode of the series (or the main feature of a movie / an OVA part)"},
	{"sp", "sp", "A special / OVA / OAD / SP episode listed separately from the regular episodes"},
	{"op", "op", "Creditless opening (NCOP, ノンテロップOP)"},
	{"ed", "ed", "Creditless ending (NCED, ノンテロップED)"},
	{"pv", "pv", "Promotional video, trailer or teaser of the series (PV, Trailer, Teaser, 特報)"},
	{"cm", "cm", "Commercial / TV spot (CM)"},
	{"other", "", "Something else: menu, next-episode preview (Preview, 予告), bonus clip, sample, several episodes in one file, not anime"},
}

// FileKindOf maps a kind option to torrent_files.kind ("" = not mappable).
func FileKindOf(opt string) string {
	for _, k := range kindOptions {
		if k.id == opt {
			return k.fileKind
		}
	}
	return ""
}

// Question ids. File questions use the file's index in the torrent.
const QSubject = "subject"

func qSubject(i int) string { return "f" + strconv.Itoa(i) + "_subject" }
func qEp(i int) string      { return "f" + strconv.Itoa(i) + "_ep" }
func qKind(i int) string    { return "f" + strconv.Itoa(i) + "_kind" }

// SubjectQuestion is the id of the subject question that decides file f.
func SubjectQuestion(t *meta.ClefTask, f meta.ClefFile) string {
	if t.PerFile {
		return qSubject(f.Index)
	}
	return QSubject
}

// Options tunes request building.
type Options struct {
	// TokenBudget bounds the estimated input tokens of one request (0: by model, about half
	// of the context window: clef-flash 24k → 12k, clef 64k → 30k).
	TokenBudget int
	// Separate asks the subject questions and the episode/kind questions in different
	// requests (calibration comparison; default: all questions of a file in one request).
	Separate bool
	// Verify adds one yes/no question per candidate (v_{option id}: "is it exactly this
	// entry?") next to the torrent-level subject choice (not in per-file mode).
	Verify bool
}

// VerifyQuestion is the id of the yes/no question for a candidate.
func VerifyQuestion(candID string) string { return "v_" + candID }

// Built is one request with the task files it covers (indexes into Task.Files).
type Built struct {
	Request   Request
	Files     []int
	EstTokens int
}

// candidate criteria: compact JSON objects (torrent-level question) or one-line strings
// (per-file questions, repeated for every file)
type candCrit struct {
	Title   string   `json:"title"`
	CN      string   `json:"cn,omitempty"`
	Aliases []string `json:"aliases,omitempty"`
	AirDate string   `json:"air_date,omitempty"`
	Eps     int      `json:"eps,omitempty"`
	Type    string   `json:"type,omitempty"`
	Season  int      `json:"season,omitempty"`
}

func subjectCriteria(t *meta.ClefTask, compact bool) map[string]any {
	crit := map[string]any{}
	for _, c := range t.Candidates {
		if compact {
			parts := []string{c.Title}
			if c.TitleCN != "" && c.TitleCN != c.Title {
				parts = append(parts, c.TitleCN)
			}
			if len(c.Aliases) > 0 {
				parts = append(parts, c.Aliases[0])
			}
			info := []string{}
			if c.Type != "" {
				info = append(info, c.Type)
			}
			if c.Season > 0 {
				info = append(info, "season "+strconv.Itoa(c.Season))
			}
			if c.AirDate != "" {
				info = append(info, c.AirDate)
			}
			if c.Eps > 0 {
				info = append(info, strconv.Itoa(c.Eps)+" eps")
			}
			crit[c.ID] = strings.Join(parts, " / ") + " (" + strings.Join(info, ", ") + ")"
			continue
		}
		crit[c.ID] = candCrit{Title: c.Title, CN: c.TitleCN, Aliases: c.Aliases, AirDate: c.AirDate, Eps: c.Eps, Type: c.Type, Season: c.Season}
	}
	crit[None] = "None of these entries (another work, another season not listed, not anime, or cannot tell)"
	return crit
}

func kindCriteria() map[string]any {
	crit := map[string]any{}
	for _, k := range kindOptions {
		crit[k.id] = k.desc
	}
	return crit
}

func epCriteria(f meta.ClefFile) map[string]any {
	crit := map[string]any{}
	for _, n := range f.Numbers {
		crit[n.ID] = n.Context
	}
	crit[None] = "The name has no episode number (unnumbered special, movie, batch file …)"
	return crit
}

const (
	subjectInstr  = "Which bangumi entry (one TV season, movie or OVA) is the video content of this torrent? Judge from the torrent title and the file names. Every season is a separate entry: a title stating a season (S2, 2nd Season, 第二季, 第2期, II) belongs to the entry of that season, never to the first season. A remake or a new series with a similar name is a different entry (compare the air dates with the publish date). Answer none if no option fits."
	fileSubjInstr = "Which bangumi entry (one TV season, movie or OVA) does this file belong to? Every season is a separate entry: a stated season (S2, 2nd Season, 第二季, II) belongs to that season's entry, never to the first season. Answer none if no option fits."
	verifyInstr   = "Is the video content of this torrent (its episode files) from exactly this bangumi entry? Another season, a sequel, a prequel, a remake, a spin-off or a work with a similar name counts as no."
	epInstr       = "Which number in this file's name is its episode number within its entry? Not a resolution, codec, year, date, season, version, CRC or batch range. Answer none if the name has no episode number."
	kindInstr     = "What kind of video is this file?"
)

func fileInstr(q, path string) map[string]string {
	return map[string]string{"question": q, "file": path}
}

// state: the torrent title and its video files (alphabetical keys: Clef sorts JSON keys and
// truncates the state at the end, so the title comes first and long file lists lose their
// tail)
func state(t *meta.ClefTask) map[string]any {
	var files []string
	size := 0
	for _, v := range t.Videos {
		if len(v) > 200 {
			v = v[:200]
		}
		if len(files) >= 40 || size+len(v) > 6000 {
			break
		}
		size += len(v)
		files = append(files, v)
	}
	st := map[string]any{"a_torrent": t.Title, "b_files": files}
	if t.Published != "" {
		st["c_published"] = t.Published
	}
	return st
}

// questionsFor returns the questions of task file fi: subject (per-file mode), episode
// number (when the name has numbers; choice needs ≥ 2 options) and kind.
func questionsFor(t *meta.ClefTask, fi int, subjects, episodes bool) map[string]Question {
	f := t.Files[fi]
	qs := map[string]Question{}
	if subjects && t.PerFile {
		qs[qSubject(f.Index)] = Question{Type: "choice", Instructions: fileInstr(fileSubjInstr, f.Path), Criteria: subjectCriteria(t, true)}
	}
	if episodes {
		if len(f.Numbers) > 0 {
			qs[qEp(f.Index)] = Question{Type: "choice", Instructions: fileInstr(epInstr, f.Path), Criteria: epCriteria(f)}
		}
		qs[qKind(f.Index)] = Question{Type: "choice", Instructions: fileInstr(kindInstr, f.Path), Criteria: kindCriteria()}
	}
	return qs
}

// EstimateTokens roughly estimates the input tokens of a request: JSON bytes × 0.4
// (measured against the usage Clef reports for our requests: ~1.3× bytes/3.2).
func EstimateTokens(r Request) int {
	b, _ := json.Marshal(r)
	return len(b)*13/32 + 20
}

func budgetFor(model string, o Options) int {
	if o.TokenBudget > 0 {
		return o.TokenBudget
	}
	if model == ModelFull {
		return 30000
	}
	return 12000
}

// Build turns a task into requests: one per torrent, split only when the questions exceed
// 64 or the estimated tokens exceed the budget. Every request repeats the torrent-level
// subject question (unless per-file) so each one is self-contained. A task without
// files or candidates gives no request.
func Build(t *meta.ClefTask, model string, o Options) []Built {
	if len(t.Files) == 0 || len(t.Candidates) == 0 {
		return nil
	}
	budget := budgetFor(model, o)
	st := state(t)
	var out []Built
	type pass struct{ subjects, episodes bool }
	passes := []pass{{true, true}}
	if o.Separate {
		passes = []pass{{true, false}, {false, true}}
	}
	for _, ps := range passes {
		start := func() Built {
			b := Built{Request: Request{Model: model, State: st, Questions: map[string]Question{}}}
			if ps.subjects && !t.PerFile {
				b.Request.Questions[QSubject] = Question{Type: "choice", Instructions: subjectInstr, Criteria: subjectCriteria(t, false)}
				if o.Verify {
					for _, c := range t.Candidates {
						crit := candCrit{Title: c.Title, CN: c.TitleCN, Aliases: c.Aliases, AirDate: c.AirDate, Eps: c.Eps, Type: c.Type, Season: c.Season}
						b.Request.Questions[VerifyQuestion(c.ID)] = Question{Type: "noul", Instructions: map[string]any{"question": verifyInstr, "entry": crit}}
					}
				}
			}
			return b
		}
		cur := start()
		base := len(cur.Request.Questions)
		for fi := range t.Files {
			qs := questionsFor(t, fi, ps.subjects, ps.episodes)
			if len(qs) == 0 {
				continue
			}
			fits := len(cur.Request.Questions)+len(qs) <= MaxQuestions
			if fits && len(cur.Files) > 0 {
				trial := Request{Model: model, State: st, Questions: map[string]Question{}}
				for k, v := range cur.Request.Questions {
					trial.Questions[k] = v
				}
				for k, v := range qs {
					trial.Questions[k] = v
				}
				fits = EstimateTokens(trial) <= budget
			}
			if !fits {
				out = append(out, finish(cur))
				cur = start()
			}
			for k, v := range qs {
				cur.Request.Questions[k] = v
			}
			cur.Files = append(cur.Files, fi)
		}
		if len(cur.Request.Questions) > base || (len(cur.Files) > 0 && len(cur.Request.Questions) > 0) {
			out = append(out, finish(cur))
		} else if ps.subjects && !ps.episodes && !t.PerFile {
			// subject-only pass: the torrent question alone
			cur.Files = allFiles(t)
			out = append(out, finish(cur))
		}
	}
	return out
}

func allFiles(t *meta.ClefTask) []int {
	out := make([]int, len(t.Files))
	for i := range out {
		out[i] = i
	}
	return out
}

func finish(b Built) Built {
	b.EstTokens = EstimateTokens(b.Request)
	return b
}
