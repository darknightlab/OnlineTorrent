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
	"sort"
	"strconv"
	"strings"
	"time"
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

// NextClefJob returns the newest due pending job (nil if none).
func (s *Store) NextClefJob(ctx context.Context, now string) (*ClefJob, error) {
	j, err := scanClefJob(s.R.QueryRowContext(ctx, `SELECT `+clefJobCols+` FROM clef_jobs j LEFT JOIN torrents t ON t.infohash = j.infohash
		WHERE j.status = 'pending' AND j.next_at <= ? ORDER BY j.created_at DESC, j.rowid DESC LIMIT 1`, now))
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
	Status     string          `json:"status"` // applied | pending | rejected | discarded
	Reason     string          `json:"reason"`
	CreatedAt  string          `json:"created_at"`
	DecidedAt  string          `json:"decided_at,omitempty"`
	DecidedBy  string          `json:"decided_by,omitempty"`
}

// AddClefDecision stores a decision (ID and CreatedAt are set).
func (s *Store) AddClefDecision(ctx context.Context, d *ClefDecision) error {
	if d.CreatedAt == "" {
		d.CreatedAt = store.Now()
	}
	if len(d.Answers) == 0 {
		d.Answers = json.RawMessage("{}")
	}
	var wk any
	if d.WorkKey != "" {
		wk = d.WorkKey
	}
	res, err := s.W.ExecContext(ctx, `INSERT INTO clef_decisions(infohash,file_index,path,work_key,kind,episode,episode_key,score,margin,confidence,answers,model,status,reason,created_at,decided_at,decided_by)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		d.Infohash, d.FileIndex, store.Trunc(d.Path, store.MaxPathBytes), wk, d.Kind, d.Episode, d.EpisodeKey, d.Score, d.Margin, d.Confidence, string(d.Answers), d.Model, d.Status, d.Reason, d.CreatedAt, d.DecidedAt, d.DecidedBy)
	if err != nil {
		return err
	}
	d.ID, _ = res.LastInsertId()
	return nil
}

const clefDecCols = `d.id,d.infohash,d.file_index,d.path,COALESCE(t.title,''),COALESCE(d.work_key,''),COALESCE(w.id,0),COALESCE(w.title,''),COALESCE(w.title_cn,''),
	d.kind,d.episode,d.episode_key,d.score,d.margin,d.confidence,d.answers,d.model,d.status,d.reason,d.created_at,d.decided_at,d.decided_by`

const clefDecFrom = ` FROM clef_decisions d LEFT JOIN torrents t ON t.infohash = d.infohash LEFT JOIN works w ON w.key = d.work_key`

func scanClefDecision(sc interface{ Scan(...any) error }) (*ClefDecision, error) {
	var d ClefDecision
	var ep sql.NullFloat64
	var answers string
	if err := sc.Scan(&d.ID, &d.Infohash, &d.FileIndex, &d.Path, &d.Torrent, &d.WorkKey, &d.SubjectID, &d.Title, &d.TitleCN,
		&d.Kind, &ep, &d.EpisodeKey, &d.Score, &d.Margin, &d.Confidence, &answers, &d.Model, &d.Status, &d.Reason, &d.CreatedAt, &d.DecidedAt, &d.DecidedBy); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, store.ErrNotFound
		}
		return nil, err
	}
	if ep.Valid {
		d.Episode = &ep.Float64
	}
	d.Answers = json.RawMessage(answers)
	return &d, nil
}

// ListClefDecisions lists decisions (newest first), optionally by status / infohash.
func (s *Store) ListClefDecisions(ctx context.Context, status, ih string, offset, limit int) ([]ClefDecision, int, error) {
	where, args := "1=1", []any{}
	if status != "" {
		where += " AND d.status = ?"
		args = append(args, status)
	}
	if ih != "" {
		where += " AND d.infohash = ?"
		args = append(args, ih)
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

// ClefDecisionCounts counts decisions per status.
func (s *Store) ClefDecisionCounts(ctx context.Context) (map[string]int, error) {
	rows, err := s.R.QueryContext(ctx, `SELECT status, COUNT(*) FROM clef_decisions GROUP BY status`)
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
// without a work is changed.
func (s *Store) ApplyClef(ctx context.Context, ih string, idx int, workKey, kind string, ep *float64, conf float64) error {
	res, err := s.W.ExecContext(ctx, `UPDATE torrent_files SET work_key=?, kind=?, episode=?, confidence=?, method=?, updated_at=?, updated_by='clef'
		WHERE infohash=? AND file_index=? AND status='auto' AND work_key IS NULL`, workKey, kind, ep, conf, MethodClef, store.Now(), ih, idx)
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
	Eps       int      `json:"eps,omitempty"`
	Type      string   `json:"type,omitempty"`
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

// SeasonFits: an entry fits a season stated in a file / torrent name (0 = none stated).
// Movies and OVAs carry no season.
func (c ClefCandidate) SeasonFits(stated float64) bool {
	if stated <= 0 || c.Type == "movie" || c.Type == "ova" {
		return true
	}
	if c.Season == 0 {
		return stated >= 2
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
	Infohash   string          `json:"infohash"`
	Title      string          `json:"title"`
	Published  string          `json:"published,omitempty"` // YYYY-MM-DD
	Videos     []string        `json:"videos"`              // every video path (the state)
	Files      []ClefFile      `json:"files"`
	Candidates []ClefCandidate `json:"candidates"`
	PerFile    bool            `json:"per_file"` // per-file subject questions (files of different works)
}

// MaxClefCandidates / MaxClefFiles bound a task.
const (
	MaxClefCandidates = 20
	MaxClefFiles      = 128
)

// BuildClefTask prepares the decision for a torrent: its unmapped automatic video files
// (all=true: every video except extras, for the admin dry run), the number tokens of each
// file name and up to 20 candidate subjects. A task without files needs no request.
func (s *Store) BuildClefTask(ctx context.Context, ih string, all bool) (*ClefTask, error) {
	in, ok := s.GetTorrentInput(ctx, ih)
	if !ok {
		return nil, store.ErrNotFound
	}
	rows, err := s.R.QueryContext(ctx, `SELECT file_index, path, work_key IS NULL, status FROM torrent_files WHERE infohash = ? ORDER BY file_index`, ih)
	if err != nil {
		return nil, err
	}
	type row struct {
		idx      int
		path     string
		unmapped bool
		status   string
	}
	var files []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.idx, &r.path, &r.unmapped, &r.status); err != nil {
			rows.Close()
			return nil, err
		}
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
	if len(in.PublishedAt) >= 10 {
		t.Published = in.PublishedAt[:10]
	}
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
		if !all && !(f.unmapped && f.status == "auto") {
			continue
		}
		cf := ClefFile{Index: f.idx, Path: f.path, Kind: res[i].kind, Episode: res[i].ep, Hint: res[i].hint,
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
	t.Candidates = s.clefCandidates(ctx, in.Title, t.Files, anchor)
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
func (s *Store) clefCandidates(ctx context.Context, title string, files []ClefFile, anchor *Work) []ClefCandidate {
	var out []ClefCandidate
	seen := map[string]bool{}
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
	if len(files) > 0 {
		texts = append(texts, stem(files[0].Path))
	}
	hits := s.searchCandidates(ctx, texts, 12)
	for _, w := range hits {
		add(w, "search")
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
	c.Aliases = al[:min(4, len(al))]
	return c
}

// searchCandidates finds works by full-text search (trigram) over title / Chinese title /
// aliases for the title fragments and words of texts, scored by how much of the text their
// names contain (an exact fragment match counts most).
func (s *Store) searchCandidates(ctx context.Context, texts []string, limit int) []*Work {
	var terms []string // raw FTS terms
	frags := map[string]bool{}
	seenT := map[string]bool{}
	addTerm := func(t string) {
		t = strings.TrimSpace(t)
		if utf8.RuneCountInString(t) < 3 || seenT[strings.ToLower(t)] || len(terms) >= 16 {
			return
		}
		seenT[strings.ToLower(t)] = true
		terms = append(terms, t)
	}
	for _, text := range texts {
		for _, f := range titleFragments(text) {
			frags[normName(f)] = true
			addTerm(f)
			for _, tk := range tokenize(f) {
				if tk.cat != cWord {
					continue
				}
				r := []rune(tk.text)
				switch {
				case hasNonASCII(tk.text) && len(r) >= 3:
					addTerm(string(r[:3]))
					addTerm(string(r[len(r)-3:]))
				case !hasNonASCII(tk.text) && len(r) >= 4:
					addTerm(tk.text)
				}
			}
		}
	}
	if len(terms) == 0 {
		return nil
	}
	q := make([]string, len(terms))
	for i, t := range terms {
		q[i] = `"` + strings.ReplaceAll(t, `"`, `""`) + `"`
	}
	rows, err := s.R.QueryContext(ctx, `SELECT `+workCols+` FROM works WHERE id IN (SELECT rowid FROM works_fts WHERE works_fts MATCH ? LIMIT 400)`, strings.Join(q, " OR "))
	if err != nil {
		return nil
	}
	type scored struct {
		w     *Work
		score int
	}
	var list []scored
	for rows.Next() {
		w, err := scanWork(rows)
		if err != nil {
			continue
		}
		var names []string
		for _, n := range append([]string{w.Title, w.TitleCN}, w.Aliases...) {
			if n = normName(n); n != "" {
				names = append(names, n)
			}
		}
		score := 0
		for _, n := range names {
			if frags[n] {
				score += 1000
				break
			}
		}
		for _, t := range terms {
			nt := normName(t)
			if nt == "" {
				continue
			}
			for _, n := range names {
				if strings.Contains(n, nt) {
					score += utf8.RuneCountInString(nt)
					break
				}
			}
		}
		list = append(list, scored{w, score})
	}
	rows.Close()
	sort.SliceStable(list, func(i, j int) bool {
		if list[i].score != list[j].score {
			return list[i].score > list[j].score
		}
		return list[i].w.Begin > list[j].w.Begin
	})
	var out []*Work
	for _, x := range list[:min(limit, len(list))] {
		if w, err := s.GetWork(ctx, x.w.Key); err == nil {
			out = append(out, w)
		}
	}
	return out
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
