package meta

// Decision-model support (Cloudflare Workers AI "Clef", see internal/clef): files the rules
// cannot map with certainty (route.go, mapFiles queue=true) are left unmapped and their
// torrent is queued in clef_jobs. internal/clef builds one request per torrent from a
// ClefTask (candidate subjects + per-file number tokens), validates the answers
// (ValidEpisode) and applies / queues / discards them (ApplyClef, clef_decisions).

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/darknightlab/onlinetorrent/internal/store"
)

// MethodClef is torrent_files.method of mappings applied by the decision model. Automatic
// re-matching (Match, Reclassify) never overwrites such rows.
const MethodClef = "clef"

// ---- jobs ----

// ClefJob is one queued torrent.
type ClefJob struct {
	Infohash  string `json:"infohash"`
	Title     string `json:"title"`
	Status    string `json:"status"` // pending | done | failed | expired
	Attempts  int    `json:"attempts"`
	Requests  int    `json:"requests"`
	NextAt    string `json:"next_at"`
	LastError string `json:"last_error"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

func (s *Store) enqueueClef(ctx context.Context, ih string) error {
	now := store.Now()
	_, err := s.W.ExecContext(ctx, `INSERT OR IGNORE INTO clef_jobs(infohash,status,next_at,created_at,updated_at) VALUES(?, 'pending', ?, ?, ?)`, ih, now, now, now)
	return err
}

// RequeueClef (re)queues a torrent now (admin).
func (s *Store) RequeueClef(ctx context.Context, ih string) error {
	now := store.Now()
	_, err := s.W.ExecContext(ctx, `INSERT INTO clef_jobs(infohash,status,next_at,created_at,updated_at) VALUES(?, 'pending', ?, ?, ?)
		ON CONFLICT(infohash) DO UPDATE SET status='pending', attempts=0, next_at=excluded.next_at, last_error='', updated_at=excluded.updated_at`, ih, now, now, now)
	return err
}

const clefJobCols = `j.infohash, COALESCE(t.title,''), j.status, j.attempts, j.requests, j.next_at, j.last_error, j.created_at, j.updated_at`

func scanClefJob(sc interface{ Scan(...any) error }) (*ClefJob, error) {
	var j ClefJob
	if err := sc.Scan(&j.Infohash, &j.Title, &j.Status, &j.Attempts, &j.Requests, &j.NextAt, &j.LastError, &j.CreatedAt, &j.UpdatedAt); err != nil {
		return nil, err
	}
	return &j, nil
}

// NextClefJob returns the newest due pending job (nil if none), skipping the infohashes in
// busy (jobs being processed).
func (s *Store) NextClefJob(ctx context.Context, now string, busy ...string) (*ClefJob, error) {
	excl, args := "", []any{now}
	if len(busy) > 0 {
		excl = ` AND j.infohash NOT IN (` + strings.TrimRight(strings.Repeat("?,", len(busy)), ",") + `)`
		for _, b := range busy {
			args = append(args, b)
		}
	}
	j, err := scanClefJob(s.R.QueryRowContext(ctx, `SELECT `+clefJobCols+` FROM clef_jobs j LEFT JOIN torrents t ON t.infohash = j.infohash
		WHERE j.status = 'pending' AND j.next_at <= ?`+excl+` ORDER BY j.created_at DESC, j.rowid DESC LIMIT 1`, args...))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return j, err
}

// FinishClefJob records the outcome of a job run. status pending = run again at retryAt
// (attempt: count it as a failed attempt).
func (s *Store) FinishClefJob(ctx context.Context, ih, status, lastErr string, requests int, retryAt string, attempt bool) error {
	now := store.Now()
	inc := 0
	if attempt {
		inc = 1
	}
	if status != "pending" {
		retryAt = ""
	}
	_, err := s.W.ExecContext(ctx, `UPDATE clef_jobs SET status=?, last_error=?, requests=requests+?, attempts=attempts+?, next_at=CASE WHEN ?!='' THEN ? ELSE next_at END, updated_at=? WHERE infohash=?`,
		status, store.Trunc(lastErr, 500), requests, inc, retryAt, retryAt, now, ih)
	return err
}

// jobFiles: file indexes of a torrent queued to be decided again although mapped.
func (s *Store) jobFiles(ctx context.Context, ih string) map[int]bool {
	var v string
	_ = s.R.QueryRowContext(ctx, `SELECT files FROM clef_jobs WHERE infohash = ?`, ih).Scan(&v)
	out := map[int]bool{}
	for _, p := range strings.Split(v, ",") {
		if n, err := strconv.Atoi(strings.TrimSpace(p)); err == nil {
			out[n] = true
		}
	}
	return out
}

// ExpireClefJobs marks pending jobs created before `before` as expired (a backlog built
// up while the model was disabled is not worked off).
func (s *Store) ExpireClefJobs(ctx context.Context, before string) (int64, error) {
	res, err := s.W.ExecContext(ctx, `UPDATE clef_jobs SET status='expired', updated_at=? WHERE status='pending' AND created_at < ?`, store.Now(), before)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// ClefJobCounts counts jobs per status.
func (s *Store) ClefJobCounts(ctx context.Context) (map[string]int, error) {
	rows, err := s.R.QueryContext(ctx, `SELECT status, COUNT(*) FROM clef_jobs GROUP BY status`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{"pending": 0, "done": 0, "failed": 0, "expired": 0}
	for rows.Next() {
		var st string
		var n int
		if err := rows.Scan(&st, &n); err != nil {
			return nil, err
		}
		out[st] = n
	}
	return out, rows.Err()
}

// ListClefJobs lists recent jobs, unfinished first.
func (s *Store) ListClefJobs(ctx context.Context, limit int) ([]ClefJob, error) {
	rows, err := s.R.QueryContext(ctx, `SELECT `+clefJobCols+` FROM clef_jobs j LEFT JOIN torrents t ON t.infohash = j.infohash
		ORDER BY (j.status = 'pending') DESC, j.updated_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ClefJob{}
	for rows.Next() {
		j, err := scanClefJob(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *j)
	}
	return out, rows.Err()
}

// ClefCount / AddClefCount: API requests made on a day (UTC date), for the daily cap.
func (s *Store) ClefCount(ctx context.Context, day string) int {
	var v string
	_ = s.R.QueryRowContext(ctx, `SELECT value FROM meta_state WHERE key = ?`, "clef_requests:"+day).Scan(&v)
	n, _ := strconv.Atoi(v)
	return n
}

func (s *Store) AddClefCount(ctx context.Context, day string, n int) error {
	_, err := s.W.ExecContext(ctx, `INSERT INTO meta_state(key,value) VALUES(?, ?) ON CONFLICT(key) DO UPDATE SET value = CAST(CAST(value AS INTEGER) + ? AS TEXT)`,
		"clef_requests:"+day, strconv.Itoa(n), n)
	return err
}

// ---- decisions ----

// ClefDecision is the decision for one file.
type ClefDecision struct {
	ID         int64           `json:"id"`
	Infohash   string          `json:"infohash"`
	FileIndex  int             `json:"file_index"`
	Path       string          `json:"path"`
	Torrent    string          `json:"torrent_title"`
	WorkKey    string          `json:"work_key,omitempty"`
	SubjectID  int64           `json:"subject_id,omitempty"`
	Title      string          `json:"title,omitempty"`
	TitleCN    string          `json:"title_cn,omitempty"`
	Kind       string          `json:"kind"`
	Episode    *float64        `json:"episode"`
	EpisodeKey string          `json:"episode_key,omitempty"`
	Score      float64         `json:"score"`
	Margin     float64         `json:"margin"`
	Confidence float64         `json:"confidence"`
	Answers    json.RawMessage `json:"answers"`
	Model      string          `json:"model"`
	Source     string          `json:"source"` // model | rule
	Status     string          `json:"status"` // applied | pending | rejected | discarded
	Reason     string          `json:"reason"`
	CreatedAt  string          `json:"created_at"`
	DecidedAt  string          `json:"decided_at,omitempty"`
	DecidedBy  string          `json:"decided_by,omitempty"`
	// the torrent's submitter (user submissions; the API adds the username)
	SubmittedAt string `json:"submitted_at,omitempty"`
	SubmittedBy *int64 `json:"submitted_by,omitempty"`
	Submitter   string `json:"submitter,omitempty"`
}

// AddClefDecision stores a decision (ID and CreatedAt are set).
func (s *Store) AddClefDecision(ctx context.Context, d *ClefDecision) error {
	if d.CreatedAt == "" {
		d.CreatedAt = store.Now()
	}
	if len(d.Answers) == 0 {
		d.Answers = json.RawMessage("{}")
	}
	if d.Source == "" {
		d.Source = "model"
	}
	var wk any
	if d.WorkKey != "" {
		wk = d.WorkKey
	}
	res, err := s.W.ExecContext(ctx, `INSERT INTO clef_decisions(infohash,file_index,path,work_key,kind,episode,episode_key,score,margin,confidence,answers,model,source,status,reason,created_at,decided_at,decided_by)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		d.Infohash, d.FileIndex, store.Trunc(d.Path, store.MaxPathBytes), wk, d.Kind, d.Episode, d.EpisodeKey, d.Score, d.Margin, d.Confidence, string(d.Answers), d.Model, d.Source, d.Status, store.Trunc(d.Reason, 300), d.CreatedAt, d.DecidedAt, d.DecidedBy)
	if err != nil {
		return err
	}
	d.ID, _ = res.LastInsertId()
	return nil
}

const clefDecCols = `d.id,d.infohash,d.file_index,d.path,COALESCE(t.title,''),COALESCE(d.work_key,''),COALESCE(w.id,0),COALESCE(w.title,''),COALESCE(w.title_cn,''),
	d.kind,d.episode,d.episode_key,d.score,d.margin,d.confidence,d.answers,d.model,d.source,d.status,d.reason,d.created_at,d.decided_at,d.decided_by,
	COALESCE(t.submitted_at,''),t.submitted_by`

const clefDecFrom = ` FROM clef_decisions d LEFT JOIN torrents t ON t.infohash = d.infohash LEFT JOIN works w ON w.key = d.work_key`

func scanClefDecision(sc interface{ Scan(...any) error }) (*ClefDecision, error) {
	var d ClefDecision
	var ep sql.NullFloat64
	var answers string
	var by sql.NullInt64
	if err := sc.Scan(&d.ID, &d.Infohash, &d.FileIndex, &d.Path, &d.Torrent, &d.WorkKey, &d.SubjectID, &d.Title, &d.TitleCN,
		&d.Kind, &ep, &d.EpisodeKey, &d.Score, &d.Margin, &d.Confidence, &answers, &d.Model, &d.Source, &d.Status, &d.Reason, &d.CreatedAt, &d.DecidedAt, &d.DecidedBy,
		&d.SubmittedAt, &by); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, store.ErrNotFound
		}
		return nil, err
	}
	if ep.Valid {
		d.Episode = &ep.Float64
	}
	if by.Valid {
		v := by.Int64
		d.SubmittedBy = &v
	}
	d.Answers = json.RawMessage(answers)
	return &d, nil
}

// ListClefDecisions lists decisions (newest first), optionally by status / infohash.
//
// Every run of a torrent adds new decisions; an older decision of the same file is
// superseded. Lists by status show only the latest decision per file (a file mapped on a
// re-run no longer shows up as 未映射); a list for one torrent (ih) shows the whole history.
func (s *Store) ListClefDecisions(ctx context.Context, status, ih string, offset, limit int) ([]ClefDecision, int, error) {
	where, args := "1=1", []any{}
	if status != "" {
		where += " AND d.status = ?"
		args = append(args, status)
	}
	if ih != "" {
		where += " AND d.infohash = ?"
		args = append(args, ih)
	} else {
		where += " AND " + latestClefDecision
	}
	var total int
	if err := s.R.QueryRowContext(ctx, `SELECT COUNT(*) FROM clef_decisions d WHERE `+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := s.R.QueryContext(ctx, `SELECT `+clefDecCols+clefDecFrom+` WHERE `+where+` ORDER BY d.id DESC LIMIT ? OFFSET ?`, append(args, limit, offset)...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []ClefDecision{}
	for rows.Next() {
		d, err := scanClefDecision(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, *d)
	}
	return out, total, rows.Err()
}

// latestClefDecision: d is the newest decision of its file (older ones are superseded).
const latestClefDecision = `d.id = (SELECT MAX(x.id) FROM clef_decisions x WHERE x.infohash = d.infohash AND x.file_index = d.file_index)`

// ClefDecisionCounts counts the latest decision of each file per status.
func (s *Store) ClefDecisionCounts(ctx context.Context) (map[string]int, error) {
	rows, err := s.R.QueryContext(ctx, `SELECT d.status, COUNT(*) FROM clef_decisions d WHERE `+latestClefDecision+` GROUP BY d.status`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{"applied": 0, "pending": 0, "rejected": 0, "discarded": 0}
	for rows.Next() {
		var st string
		var n int
		if err := rows.Scan(&st, &n); err != nil {
			return nil, err
		}
		out[st] = n
	}
	return out, rows.Err()
}

// GetClefDecision loads one decision.
func (s *Store) GetClefDecision(ctx context.Context, id int64) (*ClefDecision, error) {
	return scanClefDecision(s.R.QueryRowContext(ctx, `SELECT `+clefDecCols+clefDecFrom+` WHERE d.id = ?`, id))
}

// SetClefDecisionStatus records an admin decision.
func (s *Store) SetClefDecisionStatus(ctx context.Context, id int64, status, by string) error {
	_, err := s.W.ExecContext(ctx, `UPDATE clef_decisions SET status=?, decided_at=?, decided_by=? WHERE id=?`, status, store.Now(), by, id)
	return err
}

// ErrClefConflict: the file is no longer an unmapped automatic row (mapped meanwhile, or
// corrected by hand), or no longer holds the decision-model mapping.
var ErrClefConflict = errors.New("file changed since the decision")

// ApplyClef maps a file as decided by the model (method clef). Only an automatic row
// without a work is changed, or one whose (anomalous) mapping was queued again
// (clef_jobs.files, QueueAnomalies).
func (s *Store) ApplyClef(ctx context.Context, ih string, idx int, workKey, kind string, ep *float64, conf float64) error {
	return s.applyDecision(ctx, ih, idx, workKey, kind, ep, conf, MethodClef, "auto", "clef")
}

// MethodReview: a rule mapping an admin accepted from the review queue (status verified,
// automatic matching leaves it alone).
const MethodReview = "review"

// ApplyReviewed applies an accepted rule decision.
func (s *Store) ApplyReviewed(ctx context.Context, ih string, idx int, workKey, kind string, ep *float64, by string) error {
	return s.applyDecision(ctx, ih, idx, workKey, kind, ep, 1, MethodReview, "verified", by)
}

// ApplyOverride applies a decision an admin accepts against the gates (discarded / rejected):
// the file must be unmapped (an existing mapping is never overwritten), whatever its status —
// a file the admin earlier confirmed as unmapped can be mapped now.
func (s *Store) ApplyOverride(ctx context.Context, ih string, idx int, workKey, kind string, ep *float64, by string) error {
	res, err := s.W.ExecContext(ctx, `UPDATE torrent_files SET work_key=?, kind=?, episode=?, confidence=1, method=?, status='verified', updated_at=?, updated_by=?
		WHERE infohash=? AND file_index=? AND work_key IS NULL`, workKey, kind, ep, MethodReview, store.Now(), by, ih, idx)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrClefConflict
	}
	return nil
}

func (s *Store) applyDecision(ctx context.Context, ih string, idx int, workKey, kind string, ep *float64, conf float64, method, status, by string) error {
	res, err := s.W.ExecContext(ctx, `UPDATE torrent_files SET work_key=?, kind=?, episode=?, confidence=?, method=?, status=?, updated_at=?, updated_by=?
		WHERE infohash=? AND file_index=? AND status='auto' AND (work_key IS NULL OR (method != ? AND instr(','||(SELECT files FROM clef_jobs WHERE infohash=?)||',', ','||file_index||',') > 0))`,
		workKey, kind, ep, conf, method, status, store.Now(), by, ih, idx, MethodClef, ih)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrClefConflict
	}
	return nil
}

// RevertClef undoes a decision-model mapping: the file becomes unmapped, marked verified
// (by the admin) so no automatic run maps it again.
func (s *Store) RevertClef(ctx context.Context, ih string, idx int, by string) error {
	res, err := s.W.ExecContext(ctx, `UPDATE torrent_files SET work_key=NULL, status='verified', method='manual', confidence=1, updated_at=?, updated_by=?
		WHERE infohash=? AND file_index=? AND method=?`, store.Now(), by, ih, idx, MethodClef)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrClefConflict
	}
	return nil
}

// ---- task ----

// ClefCandidate is one candidate subject (a choice option).
type ClefCandidate struct {
	ID        string   `json:"id"` // option id: bgm_{bgm id} or w_{subject id}
	WorkKey   string   `json:"work_key"`
	SubjectID int64    `json:"subject_id"`
	Title     string   `json:"title"`
	TitleCN   string   `json:"cn,omitempty"`
	Aliases   []string `json:"aliases,omitempty"`
	AirDate   string   `json:"air_date,omitempty"`
	EndDate   string   `json:"end_date,omitempty"` // YYYY-MM-DD, "" = running / unknown
	// official episode list, compact: "13–24", "sp1–2, ncop1, nced1" (LLM input)
	MainEpisodes string `json:"main_episodes,omitempty"`
	Romaji       string `json:"romaji,omitempty"` // offline kana transliteration (no romanised alias)
	Specials     string `json:"specials,omitempty"`
	Eps          int    `json:"eps,omitempty"`
	Type         string `json:"type,omitempty"`
	// Season: the TV season this entry is — stated by its titles (第2期, Season 2, Ⅱ …), else
	// 1 for an entry without a TV/WEB prequel; 0 = a later season of unknown number
	Season int    `json:"season,omitempty"`
	Why    string `json:"why"` // anchor | hint | search | related
}

// Recent: the entry was airing around the publish date (YYYY-MM-DD): it ended at most a
// year before, or (no end date) began at most two years before. Unknown dates: true.
func (c ClefCandidate) Recent(published string) bool {
	pub, err := time.Parse("2006-01-02", published)
	if err != nil {
		return true
	}
	if end, err := time.Parse("2006-01-02", c.EndDate); err == nil {
		return pub.Sub(end) <= 365*24*time.Hour
	}
	begin, err := time.Parse("2006-01-02", c.AirDate)
	if err != nil {
		return true
	}
	return pub.Sub(begin) <= 730*24*time.Hour
}

// StartsAfter: the entry begins more than days after the publish date (YYYY-MM-DD; unknown
// dates: false) — an episode file can hardly exist yet.
func (c ClefCandidate) StartsAfter(published string, days int) bool {
	pub, err1 := time.Parse("2006-01-02", published)
	begin, err2 := time.Parse("2006-01-02", c.AirDate)
	return err1 == nil && err2 == nil && begin.Sub(pub) > time.Duration(days)*24*time.Hour
}

// SeasonFits: an entry fits a season stated in a file / torrent name (0 = none stated):
// the entry's own season, or any for a later entry without its own marker (Season 0).
// Movies and OVAs carry no season.
func (c ClefCandidate) SeasonFits(stated float64) bool {
	if stated <= 0 || c.Type == "movie" || c.Type == "ova" {
		return true
	}
	if c.Season == 0 {
		// a later entry of a franchise without its own season marker (a new Precure
		// series): "S01" is often written for any new series, so 1 fits too
		return stated >= 1
	}
	return float64(c.Season) == stated
}

// ClefNumber is a number token of a file name (an option of the episode question).
type ClefNumber struct {
	ID      string  `json:"id"` // n_03, n_1080, n_12.5
	Value   float64 `json:"value"`
	Context string  `json:"context"`
}

// ClefFile is a video file to decide.
type ClefFile struct {
	Index   int          `json:"index"`
	Path    string       `json:"path"`
	Kind    string       `json:"kind"`              // the parser's kind
	Episode *float64     `json:"episode,omitempty"` // the parser's number (not shown to the model)
	Numbers []ClefNumber `json:"numbers"`
	Hint    string       `json:"hint,omitempty"` // the rules' guess (work key)
	Group   string       `json:"group"`          // routing group: files of different groups may be different works
	// Season stated by the file name / its folders, else by the torrent title (0 = none)
	Season float64 `json:"season,omitempty"`
}

// ClefTask is everything one decision run needs.
type ClefTask struct {
	Infohash  string `json:"infohash"`
	Title     string `json:"title"`
	Published string `json:"published,omitempty"` // YYYY-MM-DD (of Released)
	// Released: the release time (crawl published_at, or a user submission's submitted_at —
	// ReleasedBy "published" | "submitted"); empty = unknown (never applied automatically)
	Released   string `json:"released,omitempty"`
	ReleasedBy string `json:"released_by,omitempty"`
	// Batch: the torrent has 2 or more video files (no late limit in the date gate)
	Batch      bool            `json:"batch,omitempty"`
	Videos     []string        `json:"videos"` // every video path (the state)
	Files      []ClefFile      `json:"files"`
	Candidates []ClefCandidate `json:"candidates"`
	PerFile    bool            `json:"per_file"` // per-file subject questions (files of different works)
}

// MaxClefCandidates / MaxClefFiles bound a task.
const (
	MaxClefCandidates = 20
	MaxClefFiles      = 256
)

// BuildClefTask prepares the decision for a torrent: its unmapped automatic video files
// (all=true: every video except extras, for the admin dry run), the number tokens of each
// file name and up to 20 candidate subjects. A task without files needs no request.
func (s *Store) BuildClefTask(ctx context.Context, ih string, all bool) (*ClefTask, error) {
	in, ok := s.GetTorrentInput(ctx, ih)
	if !ok {
		return nil, store.ErrNotFound
	}
	redo := s.jobFiles(ctx, ih) // mapped files queued again (anomalous mappings)
	rows, err := s.R.QueryContext(ctx, `SELECT file_index, path, COALESCE(work_key,''), status, method FROM torrent_files WHERE infohash = ? ORDER BY file_index`, ih)
	if err != nil {
		return nil, err
	}
	type row struct {
		idx      int
		path     string
		work     string
		unmapped bool
		status   string
		method   string
	}
	var files []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.idx, &r.path, &r.work, &r.status, &r.method); err != nil {
			rows.Close()
			return nil, err
		}
		r.unmapped = r.work == ""
		files = append(files, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	paths := make([]string, len(files))
	for i, f := range files {
		paths[i] = f.path
	}
	anchor, method, wconf, sure := s.resolveWork(ctx, in)
	res := s.mapFiles(ctx, paths, in.Title, anchor, method, wconf, sure)

	t := &ClefTask{Infohash: ih, Title: in.Title, Videos: []string{}, Files: []ClefFile{}, Candidates: []ClefCandidate{}}
	if rt, by := s.ReleaseTime(ctx, ih); !rt.IsZero() {
		t.Released, t.ReleasedBy = rt.UTC().Format(time.RFC3339), by
		t.Published = rt.UTC().Format("2006-01-02")
	}
	t.Batch = s.VideoFiles(ctx, ih) >= 2
	titleSeason := ParseTitle(in.Title).Season
	videos := 0
	for _, p := range paths {
		if IsVideo(p) {
			videos++
		}
	}
	for i, f := range files {
		if !IsVideo(f.path) {
			continue
		}
		t.Videos = append(t.Videos, f.path)
		if res[i].kind == "extra" || len(t.Files) >= MaxClefFiles {
			continue
		}
		again := redo[f.idx] && f.status == "auto" && f.method != MethodClef
		if !all && !(f.unmapped && f.status == "auto") && !again {
			continue
		}
		hint := res[i].hint
		if hint == "" && !f.unmapped {
			hint = f.work // the current (doubtful) mapping is a candidate too
		}
		cf := ClefFile{Index: f.idx, Path: f.path, Kind: res[i].kind, Episode: res[i].ep, Hint: hint,
			Numbers: NumberOptions(f.path, in.Title, videos == 1), Group: routeGroup(f.path, res[i].hint), Season: StatedSeason(f.path, titleSeason)}
		t.Files = append(t.Files, cf)
	}
	if len(t.Files) == 0 {
		return t, nil
	}
	groups := map[string]bool{}
	for _, f := range t.Files {
		groups[f.Group] = true
	}
	t.PerFile = len(groups) > 1
	t.Candidates = s.clefCandidates(ctx, in.Title, t.Files, anchor, t.Published)
	return t, nil
}

// StatedSeason is the TV season written in a file path (stem, then folders), else
// titleSeason (ParseTitle of the torrent title).
func StatedSeason(p string, titleSeason float64) float64 {
	for _, h := range pathHints(p) {
		if h.Season > 0 {
			return h.Season
		}
	}
	return titleSeason
}

// TitleSeason is the TV season written in a torrent title (0 = none).
func TitleSeason(title string) float64 { return ParseTitle(title).Season }

// routeGroup: files with another rule hint, season or movie/OVA keyword may be other works.
func routeGroup(p, hint string) string {
	season, typ := 0.0, ""
	for _, h := range pathHints(p) {
		if season == 0 && h.Season > 0 {
			season = h.Season
		}
		if typ == "" && (h.Type == "movie" || h.Type == "ova") {
			typ = h.Type
		}
	}
	return hint + "|" + fmtNum(season) + "|" + typ
}

// numberSpans finds the numbers of s: digit runs, with a one-digit decimal part when the
// dot is followed by exactly one digit that ends the token ("12.5", not "01.480p", "5.1ch").
func numberSpans(s string) [][2]int {
	var out [][2]int
	isDigit := func(i int) bool { return i < len(s) && s[i] >= '0' && s[i] <= '9' }
	isAlnum := func(i int) bool {
		return i < len(s) && (isDigit(i) || (s[i]|0x20 >= 'a' && s[i]|0x20 <= 'z'))
	}
	for i := 0; i < len(s); {
		if !isDigit(i) {
			i++
			continue
		}
		j := i
		for isDigit(j) {
			j++
		}
		if j+1 < len(s) && s[j] == '.' && isDigit(j+1) && !isAlnum(j+2) {
			j += 2
		}
		out = append(out, [2]int{i, j})
		i = j
	}
	return out
}

// NumberOptions lists the distinct numbers of a file name (stem, then the two innermost
// folders; plus the torrent title for a single-video torrent) with a short context.
func NumberOptions(p, title string, single bool) []ClefNumber {
	parts := strings.Split(strings.ReplaceAll(p, "\\", "/"), "/")
	type src struct{ text, where string }
	srcs := []src{{stem(parts[len(parts)-1]), "file name"}}
	for i, n := len(parts)-2, 0; i >= 0 && n < 2; i, n = i-1, n+1 {
		srcs = append(srcs, src{parts[i], "folder"})
	}
	if single && title != "" {
		srcs = append(srcs, src{title, "torrent title"})
	}
	var out []ClefNumber
	seen := map[float64]bool{}
	for _, sr := range srcs {
		for _, loc := range numberSpans(sr.text) {
			txt := sr.text[loc[0]:loc[1]]
			v, err := strconv.ParseFloat(txt, 64)
			if err != nil || v > MaxMainEpisode || seen[v] || len(out) >= 24 {
				continue
			}
			seen[v] = true
			out = append(out, ClefNumber{ID: "n_" + txt, Value: v, Context: sr.where + ": " + snippet(sr.text, loc[0], loc[1])})
		}
	}
	return out
}

// snippet marks text[a:b] with ⟦⟧ inside ~16 runes of context on each side.
func snippet(text string, a, b int) string {
	pre, post := text[:a], text[b:]
	if utf8.RuneCountInString(pre) > 16 {
		r := []rune(pre)
		pre = "…" + string(r[len(r)-16:])
	}
	if utf8.RuneCountInString(post) > 16 {
		post = string([]rune(post)[:16]) + "…"
	}
	return pre + "⟦" + text[a:b] + "⟧" + post
}

// clefCandidates: the rules' anchor and hints, then works found by full-text search over
// titles / aliases (torrent title + first file name), then the 前传/续集/related works of
// the top hits; at most MaxClefCandidates.
func (s *Store) clefCandidates(ctx context.Context, title string, files []ClefFile, anchor *Work, published string, exclude ...map[string]bool) []ClefCandidate {
	var out []ClefCandidate
	seen := map[string]bool{}
	for _, ex := range exclude {
		for k := range ex {
			seen[k] = true // calibration: works treated as missing from the catalogue
		}
	}
	add := func(w *Work, why string) {
		if w == nil || seen[w.Key] || len(out) >= MaxClefCandidates {
			return
		}
		seen[w.Key] = true
		out = append(out, s.candidateOf(ctx, w, why))
	}
	add(anchor, "anchor")
	for _, f := range files {
		if f.Hint != "" {
			if w, err := s.GetWork(ctx, f.Hint); err == nil {
				add(w, "hint")
			}
		}
	}
	texts := []string{title}
	for _, f := range files[:min(3, len(files))] {
		texts = append(texts, f.Path)
	}
	var hits []*Work
	for _, k := range s.retrieve(ctx, texts, published, 12) {
		if w, err := s.GetWork(ctx, k); err == nil {
			hits = append(hits, w)
			add(w, "search")
		}
	}
	// related works (seasons, movies, OVAs) of the anchor and the top hits
	var tops []*Work
	if anchor != nil {
		tops = append(tops, anchor)
	}
	tops = append(tops, hits[:min(3, len(hits))]...)
	for _, w := range tops {
		id := bgmIDOf(w)
		if id == 0 {
			continue
		}
		for _, rid := range s.relatedBgm(ctx, id, familyRelations, 2, 12) {
			if len(out) >= MaxClefCandidates {
				break
			}
			if rw, err := s.WorkByExternal(ctx, "bgm", strconv.FormatInt(rid, 10)); err == nil {
				add(rw, "related")
			}
		}
	}
	return out
}

func (s *Store) candidateOf(ctx context.Context, w *Work, why string) ClefCandidate {
	c := ClefCandidate{WorkKey: w.Key, SubjectID: w.ID, Title: w.Title, TitleCN: w.TitleCN, Eps: w.Eps, Type: strings.ToLower(w.Type), Why: why}
	if c.Type == "" {
		c.Type = platformType[w.Platform]
	}
	if id := bgmIDOf(w); id > 0 {
		c.ID = "bgm_" + strconv.FormatInt(id, 10)
	} else {
		c.ID = "w_" + strconv.FormatInt(w.ID, 10)
	}
	if len(w.Begin) >= 10 {
		if t, err := time.Parse(time.RFC3339, w.Begin); err == nil {
			c.AirDate = t.In(jst).Format("2006-01-02")
		} else {
			c.AirDate = w.Begin[:10]
		}
	}
	if len(w.End) >= 10 {
		if t, err := time.Parse(time.RFC3339, w.End); err == nil {
			c.EndDate = t.In(jst).Format("2006-01-02")
		} else {
			c.EndDate = w.End[:10]
		}
	}
	if n := s.officialSet(ctx, w.Key).kinds[KeyMain]; n > 0 {
		c.Eps = n
	}
	c.MainEpisodes, c.Specials = s.episodeSummary(ctx, w.Key)
	if v := workSeason(w); v > 0 && v == float64(int(v)) {
		c.Season = int(v)
	} else if c.Type == "tv" || c.Type == "web" {
		c.Season = 1
		if id := bgmIDOf(w); id > 0 && s.hasSeasonPrequel(ctx, id) {
			c.Season = 0
		}
	}
	// a few aliases: distinct from the titles, Latin ones first (fansub names are mostly
	// romanised), short ones first
	seen := map[string]bool{normName(w.Title): true, normName(w.TitleCN): true}
	var al []string
	for _, a := range w.Aliases {
		if n := normName(a); n != "" && !seen[n] && utf8.RuneCountInString(a) <= 60 {
			seen[n] = true
			al = append(al, a)
		}
	}
	sort.SliceStable(al, func(i, j int) bool {
		if hi, hj := hasNonASCII(al[i]), hasNonASCII(al[j]); hi != hj {
			return !hi
		}
		return len(al[i]) < len(al[j])
	})
	c.Aliases = al[:min(8, len(al))]
	if len(al) == 0 || hasNonASCII(al[0]) {
		// no romanised alias: an offline transliteration of the kana in the title
		if ro, ok := Romanize(w.Title); ok && len(ro) >= 4 {
			c.Romaji = ro
		}
	}
	return c
}

// RuleFile is what the rules make of one stored file (RuleMapping).
type RuleFile struct {
	Index   int      `json:"index"`
	Path    string   `json:"path"`
	Kind    string   `json:"kind"`
	WorkKey string   `json:"work_key,omitempty"` // set when the rules map the file (certain)
	Episode *float64 `json:"episode,omitempty"`
	Queue   bool     `json:"queue"`          // uncertain video: for the decision model
	Hint    string   `json:"hint,omitempty"` // the rules' guess for queued files
}

// RuleMapping re-runs the (current, tightened) rules over a stored torrent without writing
// anything: admin dry run and calibration.
func (s *Store) RuleMapping(ctx context.Context, ih string) (string, []RuleFile, error) {
	in, ok := s.GetTorrentInput(ctx, ih)
	if !ok {
		return "", nil, store.ErrNotFound
	}
	rows, err := s.R.QueryContext(ctx, `SELECT file_index, path FROM torrent_files WHERE infohash = ? ORDER BY file_index`, ih)
	if err != nil {
		return "", nil, err
	}
	var out []RuleFile
	for rows.Next() {
		var f RuleFile
		if err := rows.Scan(&f.Index, &f.Path); err != nil {
			rows.Close()
			return "", nil, err
		}
		out = append(out, f)
	}
	rows.Close()
	paths := make([]string, len(out))
	for i, f := range out {
		paths[i] = f.Path
	}
	w, method, wconf, sure := s.resolveWork(ctx, in)
	for i, r := range s.mapFiles(ctx, paths, in.Title, w, method, wconf, sure) {
		f := &out[i]
		f.Kind, f.Episode, f.Queue, f.Hint = r.kind, r.ep, r.queue, r.hint
		if k, ok := r.work.(string); ok {
			f.WorkKey = k
		}
	}
	return in.Title, out, rows.Err()
}

// hasSeasonPrequel: the subject has a TV/WEB 前传 (bgm relation 2).
func (s *Store) hasSeasonPrequel(ctx context.Context, id int64) bool {
	var n int
	_ = s.R.QueryRowContext(ctx, `SELECT COUNT(*) FROM subject_relations r JOIN work_ids i ON i.provider = 'bgm' AND i.ext_id = CAST(r.related_id AS TEXT)
		JOIN works w ON w.key = i.work_key WHERE r.subject_id = ? AND r.relation_type = 2 AND (lower(w.type) IN ('tv','web') OR (w.type = '' AND w.platform IN (1,5)))`, id).Scan(&n)
	return n > 0
}

// episodeSummary describes the official episode list compactly: main sorts as runs
// ("1–12, 12.5"), specials as kind + runs ("sp1–2, ncop1, nced1–2"), at most ~12 runs each.
func (s *Store) episodeSummary(ctx context.Context, workKey string) (main, specials string) {
	eps, err := s.OfficialEpisodes(ctx, workKey)
	if err != nil || len(eps) == 0 {
		return "", ""
	}
	byKind := map[string][]float64{}
	for _, e := range eps {
		byKind[e.Key.Kind] = append(byKind[e.Key.Kind], e.Key.Number)
	}
	runs := func(prefix string, nums []float64) []string {
		sort.Float64s(nums)
		var out []string
		for i := 0; i < len(nums); {
			j := i
			for j+1 < len(nums) && nums[j+1] == nums[j]+1 {
				j++
			}
			r := prefix + fmtNum(nums[i])
			if j > i {
				r += "–" + fmtNum(nums[j])
			}
			out = append(out, r)
			i = j + 1
		}
		if len(out) > 12 {
			out = append(out[:12], "…")
		}
		return out
	}
	main = strings.Join(runs("", byKind[KeyMain]), ", ")
	var sp []string
	for _, k := range KeyKinds[1:] {
		if len(byKind[k]) > 0 {
			sp = append(sp, runs(k, byKind[k])...)
		}
	}
	if len(sp) > 16 {
		sp = append(sp[:16], "…")
	}
	return main, strings.Join(sp, ", ")
}

// CandidatesForTitle finds more candidates for a title the model suggested (full-text
// search + related works of the top hits), skipping the work keys in have; at most limit.
func (s *Store) CandidatesForTitle(ctx context.Context, title string, have map[string]bool, limit int) []ClefCandidate {
	var out []ClefCandidate
	seen := map[string]bool{}
	for k := range have {
		seen[k] = true
	}
	add := func(w *Work) {
		if w == nil || seen[w.Key] || len(out) >= limit {
			return
		}
		seen[w.Key] = true
		out = append(out, s.candidateOf(ctx, w, "suggested"))
	}
	var hits []*Work
	for _, k := range s.retrieve(ctx, []string{title}, "", 8) {
		if w, err := s.GetWork(ctx, k); err == nil && !seen[k] {
			hits = append(hits, w)
			add(w)
		}
	}
	for _, w := range hits[:min(2, len(hits))] {
		if id := bgmIDOf(w); id > 0 {
			for _, rid := range s.relatedBgm(ctx, id, familyRelations, 1, 8) {
				if rw, err := s.WorkByExternal(ctx, "bgm", strconv.FormatInt(rid, 10)); err == nil {
					add(rw)
				}
			}
		}
	}
	return out
}

// AnomalousFile is a mapped main episode that is not in its work's official list.
type AnomalousFile struct {
	Index   int     `json:"index"`
	Path    string  `json:"path"`
	WorkKey string  `json:"work_key"`
	Title   string  `json:"title"`
	Episode float64 `json:"episode"`
	Range   string  `json:"official"` // the work's official main episodes
}

// AnomalousTorrent groups the anomalous files of a torrent.
type AnomalousTorrent struct {
	Infohash string          `json:"infohash"`
	Title    string          `json:"title"`
	Files    []AnomalousFile `json:"files"`
}

// AnomalousMappings lists automatic main-episode mappings (not by the decision model) whose
// episode — after the split-cour shift — is not in the work's official episode list. Only
// works that have an official list are checked (no list: nothing to compare against), and
// only whole numbers (recaps like 12.5 are rarely listed).
func (s *Store) AnomalousMappings(ctx context.Context) ([]AnomalousTorrent, error) {
	rows, err := s.R.QueryContext(ctx, `SELECT f.infohash, COALESCE(t.title,''), f.file_index, f.path, f.work_key, COALESCE(NULLIF(w.title_cn,''), w.title), f.episode
		FROM torrent_files f JOIN works w ON w.key = f.work_key LEFT JOIN torrents t ON t.infohash = f.infohash
		WHERE f.kind = 'episode' AND f.episode IS NOT NULL AND f.episode = CAST(f.episode AS INTEGER) AND f.status = 'auto' AND f.method != ?
		  AND EXISTS (SELECT 1 FROM episodes e WHERE e.work_key = f.work_key AND e.type = 0)
		ORDER BY f.infohash, f.file_index`, MethodClef)
	if err != nil {
		return nil, err
	}
	type rec struct {
		ih, title string
		f         AnomalousFile
	}
	var all []rec
	for rows.Next() {
		var r rec
		if err := rows.Scan(&r.ih, &r.title, &r.f.Index, &r.f.Path, &r.f.WorkKey, &r.f.Title, &r.f.Episode); err != nil {
			rows.Close()
			return nil, err
		}
		all = append(all, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sets := map[string]*officialSet{}
	ranges := map[string]string{}
	var out []AnomalousTorrent
	for _, r := range all {
		set, ok := sets[r.f.WorkKey]
		if !ok {
			set = s.officialSet(ctx, r.f.WorkKey)
			sets[r.f.WorkKey] = set
			ranges[r.f.WorkKey], _ = s.episodeSummary(ctx, r.f.WorkKey)
		}
		k := s.CanonicalKey(ctx, r.f.WorkKey, EpisodeKey{Kind: KeyMain, Number: r.f.Episode})
		if set.keys[k] {
			continue
		}
		r.f.Range = ranges[r.f.WorkKey]
		if n := len(out); n == 0 || out[n-1].Infohash != r.ih {
			out = append(out, AnomalousTorrent{Infohash: r.ih, Title: r.title})
		}
		out[len(out)-1].Files = append(out[len(out)-1].Files, r.f)
	}
	return out, nil
}

// QueueAnomalies queues the given torrents for the decision model with their anomalous
// files to be decided again (clef_jobs.files); the current mappings stay until a decision
// replaces them. Returns the number of torrents queued.
func (s *Store) QueueAnomalies(ctx context.Context, list []AnomalousTorrent) (int, error) {
	now := store.Now()
	n := 0
	for _, t := range list {
		idx := make([]string, len(t.Files))
		for i, f := range t.Files {
			idx[i] = strconv.Itoa(f.Index)
		}
		if _, err := s.W.ExecContext(ctx, `INSERT INTO clef_jobs(infohash,status,next_at,files,created_at,updated_at) VALUES(?, 'pending', ?, ?, ?, ?)
			ON CONFLICT(infohash) DO UPDATE SET status='pending', attempts=0, next_at=excluded.next_at, files=excluded.files, last_error='', created_at=excluded.created_at, updated_at=excluded.updated_at`,
			t.Infohash, now, strings.Join(idx, ","), now, now); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

// reSeasonWords: season / part markers removed before comparing names (the season itself is
// checked separately, SeasonFits).
var reSeasonWords = regexp.MustCompile(`(?i)\b(?:S\d{1,2}|season\s*\d{1,2}|\d{1,2}(?:st|nd|rd|th)\s*season|part\s*\d|cour\s*\d|II|III|IV)\b|第\s*[0-9一二三四五六七八九十壱弐参]+\s*(?:季|期|部|クール|幕)|(?:^|[^a-z])dai[\s_.~-]*(?:ichi|ni|san|yon|shi|go)[\s_.~-]*maku|[（(][^)）]{0,20}[)）]`)

func seasonless(s string) string { return normName(reSeasonWords.ReplaceAllString(normWidth(s), " ")) }

// NameEvidence reports whether the torrent title / file paths literally carry a name of the
// work: one of its names occurs in the text, or a title fragment of the text occurs in one
// of its names — compared after normName and without season / part markers (≥ 3 runes
// with CJK, ≥ 5 otherwise). A decision without such evidence (the model inferred the work
// from something else) is not applied automatically. The work's names are its titles and
// aliases plus (WorkNames):
//
//   - the names of its direct prequels / sequels (a franchise name: 夜桜 第2期第2クール has
//     no romanised alias of its own, its prequel "Yozakura-san Chi no Daisakusen Season 2"
//     does). Which entry of the franchise it is stays the job of the episode list, season
//     and date checks;
//   - an English / romanised name derived from related works titled "<its title> <subtitle>"
//     whose aliases read "<name>: <subtitle>" (名探偵コナン → "Detective Conan", from the
//     movies), when at least two such works agree.
//
// A name of only two CJK characters (遮天) counts when a whole bracketed part of the text
// equals it ("[遮天]").
func (s *Store) NameEvidence(ctx context.Context, workKey string, texts []string) bool {
	names := s.WorkNames(ctx, workKey)
	if len(names) == 0 {
		return false
	}
	long := func(n string) bool {
		c := utf8.RuneCountInString(n)
		return c >= 5 || (c >= 3 && hasNonASCII(n))
	}
	short := func(n string) bool { return utf8.RuneCountInString(n) == 2 && cjkOnly(n) }
	var keep, shorts []string
	seen := map[string]bool{}
	for _, n := range names {
		for _, v := range []string{normName(n), seasonless(n)} {
			if seen[v] {
				continue
			}
			seen[v] = true
			if long(v) {
				keep = append(keep, v)
			} else if short(v) {
				shorts = append(shorts, v)
			}
		}
	}
	var all strings.Builder
	var frags []string
	brackets := map[string]bool{}
	for _, t := range texts {
		all.WriteString(normName(t))
		all.WriteByte('|')
		all.WriteString(seasonless(t))
		all.WriteByte('|')
		// fragments of the last path part, and of the whole text: a torrent title
		// "[G] 牧神记 / Tales of Herding Gods - 105" is not a path ("/" separates titles)
		for _, src := range []string{stem(t), t} {
			for _, f := range titleFragments(src) {
				for _, v := range []string{normName(f), seasonless(f)} {
					if long(v) {
						frags = append(frags, v)
					}
				}
			}
		}
		for _, m := range reBracketPart.FindAllStringSubmatch(normWidth(t), -1) {
			brackets[normName(m[1])] = true
		}
	}
	text := all.String()
	for _, n := range keep {
		if strings.Contains(text, n) {
			return true
		}
		for _, f := range frags {
			if strings.Contains(n, f) {
				return true
			}
		}
	}
	for _, n := range shorts {
		if brackets[n] {
			return true
		}
	}
	return false
}

// reBracketPart: the content of one bracket pair.
var reBracketPart = regexp.MustCompile(`[\[【〔「『(（]([^\]】〕」』)）\[【]{1,40})[\]】〕」』)）]`)

func cjkOnly(s string) bool {
	for _, r := range s {
		if !unicode.Is(unicode.Han, r) && !unicode.Is(unicode.Hiragana, r) && !unicode.Is(unicode.Katakana, r) {
			return false
		}
	}
	return s != ""
}

// directFamily: bgm relation types of a direct prequel (前传 2) / sequel (续集 3).
var directFamily = map[int]bool{2: true, 3: true}

// WorkNames are the names that count as naming the work (NameEvidence): its titles and
// aliases, those of its direct prequels / sequels, and English / romanised names derived
// from related works (see NameEvidence).
func (s *Store) WorkNames(ctx context.Context, workKey string) []string {
	w, err := s.GetWork(ctx, workKey)
	if err != nil {
		return nil
	}
	names := append([]string{w.Title, w.TitleCN}, w.Aliases...)
	id := bgmIDOf(w)
	if id == 0 {
		return names
	}
	for _, rid := range s.relatedBgm(ctx, id, directFamily, 1, 12) {
		if rw, err := s.WorkByExternal(ctx, "bgm", strconv.FormatInt(rid, 10)); err == nil {
			names = append(names, rw.Title, rw.TitleCN)
			names = append(names, rw.Aliases...)
		}
	}
	return append(names, s.derivedNames(ctx, w, id)...)
}

// derivedNames: related works (any family relation, one step) whose Japanese title is the
// work's title followed by a subtitle and that have a Latin alias "<name>: <subtitle>" or
// "<name> - <subtitle>" give <name>; a name needs two such works.
func (s *Store) derivedNames(ctx context.Context, w *Work, id int64) []string {
	base := normName(w.Title)
	if utf8.RuneCountInString(base) < 2 {
		return nil
	}
	votes := map[string]int{}
	shown := map[string]string{}
	for _, rid := range s.relatedBgm(ctx, id, familyRelations, 1, 200) {
		rw, err := s.WorkByExternal(ctx, "bgm", strconv.FormatInt(rid, 10))
		if err != nil || !strings.HasPrefix(normName(rw.Title), base) || normName(rw.Title) == base {
			continue
		}
		mine := map[string]bool{}
		for _, a := range rw.Aliases {
			if hasNonASCII(a) {
				continue
			}
			i := strings.IndexAny(a, ":：")
			if j := strings.Index(a, " - "); i < 0 || (j > 0 && j < i) {
				i = j
			}
			if i <= 0 {
				continue
			}
			p := strings.TrimSpace(a[:i])
			if n := normName(p); len(n) >= 5 && !mine[n] {
				mine[n] = true
				votes[n]++
				shown[n] = p
			}
		}
	}
	var out []string
	for n, v := range votes {
		if v >= 2 {
			out = append(out, shown[n])
		}
	}
	sort.Strings(out)
	return out
}

// SearchOnlyCandidates is candidate retrieval without the rules' anchor and hints (search +
// relations only), optionally as if the excluded works weren't in the catalogue:
// calibration.
func (s *Store) SearchOnlyCandidates(ctx context.Context, title string, paths []string, published string, exclude ...map[string]bool) []ClefCandidate {
	files := make([]ClefFile, len(paths))
	for i, p := range paths {
		files[i] = ClefFile{Index: i, Path: p}
	}
	return s.clefCandidates(ctx, title, files, nil, published, exclude...)
}

// bgm episode type of a key kind
var keyKindType = map[string]int{KeyMain: 0, KeySP: 1, KeyNCOP: 2, KeyNCED: 3, KeyPV: 4}

// EpisodeAirdate is the official airdate (YYYY-MM-DD) of a canonical key ("" if unknown).
func (s *Store) EpisodeAirdate(ctx context.Context, workKey string, k EpisodeKey) string {
	typ, ok := keyKindType[k.Kind]
	if !ok {
		return ""
	}
	var d string
	_ = s.R.QueryRowContext(ctx, `SELECT airdate FROM episodes WHERE work_key = ? AND type = ? AND sort = ? AND airdate != '' LIMIT 1`, workKey, typ, k.Number).Scan(&d)
	return d
}

// Date gate tolerances (owner decisions): a release may be at most 7 days before the
// episode's airdate (or, without one, the work's start) — for every torrent. A single-
// episode torrent (exactly one video file) may also be at most 30 days after the airdate
// (without one: after the work's end); a batch (2 or more video files of any kind: season
// packs, BDs, an episode plus bonus material) may come any time after airing.
const (
	DateEarlyDays = 7
	DateLateDays  = 30
)

// DateCheck is the outcome of the date gate (OK=false: review).
type DateCheck struct {
	OK     bool
	Detail string // why (Chinese, for the admin)
}

// CheckDate is CheckDateFull for a single-episode torrent.
func (s *Store) CheckDate(ctx context.Context, workKey string, k EpisodeKey, released time.Time, releasedBy string) (bool, string) {
	c := s.CheckDateFull(ctx, workKey, k, released, releasedBy, false)
	return c.OK, c.Detail
}

// CheckDateFull compares a release time with the airdate of the (canonical) episode, else
// with the work's airing window: more than 7 days before → not OK; for a single-episode
// torrent (batch=false) also more than 30 days after. A main episode without an airdate
// counts as not aired yet when an earlier main episode of the work airs more than 7 days
// after the release (bangumi lists future episodes before their dates are known: "S2 [14]"
// released after S2's 2nd broadcast is not episode 14). Unknown dates pass.
func (s *Store) CheckDateFull(ctx context.Context, workKey string, k EpisodeKey, released time.Time, releasedBy string, batch bool) DateCheck {
	if released.IsZero() {
		return DateCheck{OK: true}
	}
	by := "发布时间"
	if releasedBy == "submitted" {
		by = "提交时间"
	}
	rel := released.In(jst).Format("2006-01-02")
	early, late := time.Duration(DateEarlyDays)*24*time.Hour, time.Duration(DateLateDays)*24*time.Hour
	if d := s.EpisodeAirdate(ctx, workKey, k); d != "" {
		if air, err := time.ParseInLocation("2006-01-02", d, jst); err == nil {
			switch {
			case released.Before(air.Add(-early)):
				return DateCheck{Detail: fmt.Sprintf("%s %s 早于本集播出 %s 超过 %d 天", by, rel, d, DateEarlyDays)}
			case !batch && released.After(air.Add(24*time.Hour+late)):
				return DateCheck{Detail: fmt.Sprintf("%s %s 晚于本集播出 %s 超过 %d 天（单集种子）", by, rel, d, DateLateDays)}
			}
			return DateCheck{OK: true}
		}
	}
	if k.Kind == KeyMain {
		var sort sql.NullFloat64
		var d sql.NullString
		_ = s.R.QueryRowContext(ctx, `SELECT sort, airdate FROM episodes WHERE work_key = ? AND type = 0 AND sort < ? AND airdate != ''
			ORDER BY airdate DESC LIMIT 1`, workKey, k.Number).Scan(&sort, &d)
		if d.Valid {
			if air, err := time.ParseInLocation("2006-01-02", d.String, jst); err == nil && released.Before(air.Add(-early)) {
				return DateCheck{Detail: fmt.Sprintf("%s %s 时本集尚未播出（本集无播出日期，前面的第 %s 集 %s 才播出）", by, rel, strconv.FormatFloat(sort.Float64, 'f', -1, 64), d.String)}
			}
		}
	}
	w, err := s.GetWork(ctx, workKey)
	if err != nil {
		return DateCheck{OK: true}
	}
	begin, end := parseDay(w.Begin), parseDay(w.End)
	if !begin.IsZero() && released.Before(begin.Add(-early)) {
		return DateCheck{Detail: fmt.Sprintf("%s %s 早于条目开播 %s 超过 %d 天（本集无播出日期）", by, rel, begin.In(jst).Format("2006-01-02"), DateEarlyDays)}
	}
	if !batch && !end.IsZero() && released.After(end.Add(24*time.Hour+late)) {
		return DateCheck{Detail: fmt.Sprintf("%s %s 晚于条目完结 %s 超过 %d 天（本集无播出日期，单集种子）", by, rel, end.In(jst).Format("2006-01-02"), DateLateDays)}
	}
	return DateCheck{OK: true}
}

// VideoFiles counts the torrent's video files (any kind: episodes, specials, NCOP, PV,
// extras); 2 or more make it a batch for the date gate.
func (s *Store) VideoFiles(ctx context.Context, ih string) int {
	rows, err := s.R.QueryContext(ctx, `SELECT path FROM torrent_files WHERE infohash = ?`, ih)
	if err != nil {
		return 0
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		var p string
		if rows.Scan(&p) == nil && IsVideo(p) {
			n++
		}
	}
	return n
}

// Review reasons shared by the decision model and the rules (the decision goes to human
// review instead of being applied).
const (
	ReasonMissingRelease = "missing_release_time" // 缺少发布时间
	ReasonDateMismatch   = "date_mismatch"        // 日期不符 (": detail" appended)
)

// ReleaseGate says whether a decision for (work, canonical key) may be applied
// automatically given the task's release time: never without one; otherwise the
// per-episode date gate (CheckDateFull; t.Batch: no late limit). "" when it may, else the
// review reason.
func (s *Store) ReleaseGate(ctx context.Context, t *ClefTask, workKey string, k EpisodeKey) string {
	rt, err := time.Parse(time.RFC3339, t.Released)
	if t.Released == "" || err != nil {
		return ReasonMissingRelease
	}
	if c := s.CheckDateFull(ctx, workKey, k, rt, t.ReleasedBy, t.Batch); !c.OK {
		return ReasonDateMismatch + ": " + c.Detail
	}
	return ""
}

// hasRuleDecision: a rule decision (any status) exists for the file and mapping, so a
// re-delivered torrent doesn't queue it again (also not after a rejection).
func (s *Store) hasRuleDecision(ctx context.Context, ih string, idx int, workKey, key string) bool {
	var n int
	_ = s.R.QueryRowContext(ctx, `SELECT COUNT(*) FROM clef_decisions WHERE infohash = ? AND file_index = ? AND source = 'rule' AND work_key = ? AND episode_key = ?`, ih, idx, workKey, key).Scan(&n)
	return n > 0
}

// queueRuleReview records a certain rule mapping that may not be applied automatically (no
// release time, or released before the episode aired) as a pending review decision
// (source rule).
func (s *Store) queueRuleReview(ctx context.Context, ih string, idx int, path, workKey, kind string, ep *float64, conf float64, reason string) error {
	key := ""
	if k, ok := KeyOfFile(kind, ep); ok {
		ck, _ := s.ValidEpisode(ctx, workKey, k)
		key = ck.String()
	}
	if s.hasRuleDecision(ctx, ih, idx, workKey, key) {
		return nil
	}
	return s.AddClefDecision(ctx, &ClefDecision{Infohash: ih, FileIndex: idx, Path: path, WorkKey: workKey, Kind: kind, Episode: ep, EpisodeKey: key,
		Score: conf, Margin: -1, Confidence: conf, Model: "rules", Source: "rule", Status: "pending", Reason: reason})
}

// GetClefJob returns the decision-model job of ih (nil if none).
func (s *Store) GetClefJob(ctx context.Context, ih string) (*ClefJob, error) {
	j, err := scanClefJob(s.R.QueryRowContext(ctx, `SELECT `+clefJobCols+` FROM clef_jobs j LEFT JOIN torrents t ON t.infohash = j.infohash WHERE j.infohash = ?`, ih))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return j, err
}
