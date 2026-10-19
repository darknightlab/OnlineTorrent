package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// Crawl sources (structure mirrors YotsubaNews store/input_sources.go): admin-managed feeds
// polled by internal/crawl. Kinds: rss (generic RSS/Atom) and bangumi_moe (JSON latest page).

var (
	ErrInvalidSource = errors.New("invalid source")
	ErrSourceExists  = errors.New("该地址的抓取源已存在")
)

// Source limits.
const (
	SourceKindRSS        = "rss"
	SourceKindBangumiMoe = "bangumi_moe"

	MinSourceIntervalMin     = 5
	DefaultSourceIntervalMin = 15
	MaxSourceIntervalMin     = 7 * 24 * 60
	MaxSourceURLBytes        = 2048
	MaxSourceNameBytes       = 100
	// a failing source waits interval × 2^fail_count, capped at this
	MaxSourceBackoff = 6 * time.Hour
)

// Source is one crawl source.
type Source struct {
	ID            int64   `json:"id"`
	Name          string  `json:"name"`
	Kind          string  `json:"kind"`
	URL           string  `json:"url"`
	IntervalMin   int     `json:"interval_min"`
	Enabled       bool    `json:"enabled"`
	ProxyDisabled bool    `json:"proxy_disabled"`
	ETag          string  `json:"-"`
	LastModified  string  `json:"-"`
	LastRunAt     *string `json:"last_run_at"`
	LastOKAt      *string `json:"last_ok_at"`
	LastItems     int     `json:"last_items"`
	LastNew       int     `json:"last_new"`
	LastError     string  `json:"last_error"`
	FailCount     int     `json:"fail_count"`
	CreatedAt     string  `json:"created_at"`
	UpdatedAt     string  `json:"updated_at"`
}

const sourceCols = `id,name,kind,url,interval_min,enabled,proxy_disabled,etag,last_modified,last_run_at,last_ok_at,last_items,last_new,last_error,fail_count,created_at,updated_at`

func scanSource(sc interface{ Scan(...any) error }) (Source, error) {
	var s Source
	var en, pd int
	var run, ok sql.NullString
	err := sc.Scan(&s.ID, &s.Name, &s.Kind, &s.URL, &s.IntervalMin, &en, &pd, &s.ETag, &s.LastModified, &run, &ok, &s.LastItems, &s.LastNew, &s.LastError, &s.FailCount, &s.CreatedAt, &s.UpdatedAt)
	s.Enabled, s.ProxyDisabled = en != 0, pd != 0
	if run.Valid {
		s.LastRunAt = &run.String
	}
	if ok.Valid {
		s.LastOKAt = &ok.String
	}
	return s, err
}

func (s *Store) querySources(ctx context.Context, where string, args ...any) ([]Source, error) {
	rows, err := s.R.QueryContext(ctx, `SELECT `+sourceCols+` FROM sources `+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Source{}
	for rows.Next() {
		src, err := scanSource(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, src)
	}
	return out, rows.Err()
}

// ListSources returns every source.
func (s *Store) ListSources(ctx context.Context) ([]Source, error) {
	return s.querySources(ctx, `ORDER BY id`)
}

// GetSource returns one source or ErrNotFound.
func (s *Store) GetSource(ctx context.Context, id int64) (*Source, error) {
	list, err := s.querySources(ctx, `WHERE id = ?`, id)
	if err != nil {
		return nil, err
	}
	if len(list) == 0 {
		return nil, ErrNotFound
	}
	return &list[0], nil
}

// SourceBackoff is the wait before the next poll of src.
func SourceBackoff(src Source) time.Duration {
	d := time.Duration(max(src.IntervalMin, MinSourceIntervalMin)) * time.Minute
	for i := 0; i < src.FailCount && d < MaxSourceBackoff; i++ {
		d *= 2
	}
	return min(d, MaxSourceBackoff)
}

// ListDueSources returns enabled sources whose interval (with failure backoff) has elapsed.
func (s *Store) ListDueSources(ctx context.Context, now time.Time) ([]Source, error) {
	all, err := s.querySources(ctx, `WHERE enabled = 1 ORDER BY id`)
	if err != nil {
		return nil, err
	}
	var due []Source
	for _, src := range all {
		if src.LastRunAt == nil {
			due = append(due, src)
			continue
		}
		t, err := time.Parse(time.RFC3339, *src.LastRunAt)
		if err != nil || !now.Before(t.Add(SourceBackoff(src))) {
			due = append(due, src)
		}
	}
	return due, nil
}

// TouchSourceRun records the start of a poll (before fetching, so a failing source also
// waits a full interval — same as YotsubaNews' TouchSourcePolled).
func (s *Store) TouchSourceRun(ctx context.Context, id int64) error {
	_, err := s.W.ExecContext(ctx, `UPDATE sources SET last_run_at = ? WHERE id = ?`, Now(), id)
	return err
}

// SourceResult is the outcome of one poll.
type SourceResult struct {
	NotModified  bool
	ETag         string
	LastModified string
	Items        int
	New          int
	Err          error
}

// FinishSourceRun stores a poll result.
func (s *Store) FinishSourceRun(ctx context.Context, id int64, r SourceResult) error {
	if r.Err != nil {
		_, err := s.W.ExecContext(ctx, `UPDATE sources SET last_error = ?, last_new = 0, fail_count = fail_count + 1 WHERE id = ?`, Trunc(r.Err.Error(), 500), id)
		return err
	}
	if r.NotModified {
		_, err := s.W.ExecContext(ctx, `UPDATE sources SET last_ok_at = ?, last_new = 0, last_error = '', fail_count = 0 WHERE id = ?`, Now(), id)
		return err
	}
	_, err := s.W.ExecContext(ctx, `UPDATE sources SET last_ok_at = ?, last_items = ?, last_new = ?, last_error = '', fail_count = 0, etag = ?, last_modified = ? WHERE id = ?`,
		Now(), r.Items, r.New, Trunc(r.ETag, 300), Trunc(r.LastModified, 100), id)
	return err
}

// ValidateSource normalizes and checks a source before writing.
func ValidateSource(src *Source) error {
	src.Name = strings.TrimSpace(src.Name)
	src.URL = strings.TrimSpace(src.URL)
	src.Kind = strings.TrimSpace(src.Kind)
	if src.Kind != SourceKindRSS && src.Kind != SourceKindBangumiMoe {
		return fmt.Errorf("%w: kind 必须是 rss 或 bangumi_moe", ErrInvalidSource)
	}
	if src.Name == "" {
		return fmt.Errorf("%w: 名称不能为空", ErrInvalidSource)
	}
	if len(src.Name) > MaxSourceNameBytes {
		return fmt.Errorf("%w: 名称过长", ErrInvalidSource)
	}
	if len(src.URL) > MaxSourceURLBytes {
		return fmt.Errorf("%w: 地址过长", ErrInvalidSource)
	}
	u, err := url.Parse(src.URL)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil {
		return fmt.Errorf("%w: 地址必须是 http(s):// URL", ErrInvalidSource)
	}
	if src.IntervalMin == 0 {
		src.IntervalMin = DefaultSourceIntervalMin
	}
	if src.IntervalMin < MinSourceIntervalMin || src.IntervalMin > MaxSourceIntervalMin {
		return fmt.Errorf("%w: 抓取间隔需在 %d–%d 分钟之间", ErrInvalidSource, MinSourceIntervalMin, MaxSourceIntervalMin)
	}
	return nil
}

func isUnique(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "unique constraint failed")
}

// AddSource inserts a source.
func (s *Store) AddSource(ctx context.Context, src Source) (int64, error) {
	if err := ValidateSource(&src); err != nil {
		return 0, err
	}
	now := Now()
	res, err := s.W.ExecContext(ctx, `INSERT INTO sources(name,kind,url,interval_min,enabled,proxy_disabled,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?)`,
		src.Name, src.Kind, src.URL, src.IntervalMin, boolInt(src.Enabled), boolInt(src.ProxyDisabled), now, now)
	if isUnique(err) {
		return 0, ErrSourceExists
	}
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// UpdateSource writes all editable fields; a changed URL/kind/proxy resets the
// conditional-GET validators and makes the source due immediately.
func (s *Store) UpdateSource(ctx context.Context, src Source) error {
	if err := ValidateSource(&src); err != nil {
		return err
	}
	res, err := s.W.ExecContext(ctx, `UPDATE sources SET
		etag = CASE WHEN url <> ? OR kind <> ? OR proxy_disabled <> ? THEN '' ELSE etag END,
		last_modified = CASE WHEN url <> ? OR kind <> ? OR proxy_disabled <> ? THEN '' ELSE last_modified END,
		last_run_at = CASE WHEN url <> ? OR kind <> ? OR proxy_disabled <> ? OR (enabled = 0 AND ? = 1) THEN NULL ELSE last_run_at END,
		fail_count = CASE WHEN url <> ? OR kind <> ? THEN 0 ELSE fail_count END,
		name = ?, kind = ?, url = ?, interval_min = ?, enabled = ?, proxy_disabled = ?, updated_at = ?
		WHERE id = ?`,
		src.URL, src.Kind, boolInt(src.ProxyDisabled),
		src.URL, src.Kind, boolInt(src.ProxyDisabled),
		src.URL, src.Kind, boolInt(src.ProxyDisabled), boolInt(src.Enabled),
		src.URL, src.Kind,
		src.Name, src.Kind, src.URL, src.IntervalMin, boolInt(src.Enabled), boolInt(src.ProxyDisabled), Now(), src.ID)
	if isUnique(err) {
		return ErrSourceExists
	}
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteSource removes a source (its item memory cascades).
func (s *Store) DeleteSource(ctx context.Context, id int64) error {
	res, err := s.W.ExecContext(ctx, `DELETE FROM sources WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// ---- per-item memory ----

// SeenItems returns which of guids were already handled for a source.
func (s *Store) SeenItems(ctx context.Context, sourceID int64, guids []string) (map[string]bool, error) {
	out := map[string]bool{}
	for start := 0; start < len(guids); start += 200 {
		chunk := guids[start:min(start+200, len(guids))]
		args := []any{sourceID}
		for _, g := range chunk {
			args = append(args, g)
		}
		ph := strings.TrimRight(strings.Repeat("?,", len(chunk)), ",")
		rows, err := s.R.QueryContext(ctx, `SELECT guid FROM source_items WHERE source_id = ? AND guid IN (`+ph+`)`, args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var g string
			if err := rows.Scan(&g); err != nil {
				rows.Close()
				return nil, err
			}
			out[g] = true
		}
		rows.Close()
	}
	return out, nil
}

// MarkItemSeen remembers a handled feed item.
func (s *Store) MarkItemSeen(ctx context.Context, sourceID int64, guid, ih string) error {
	_, err := s.W.ExecContext(ctx, `INSERT INTO source_items(source_id,guid,infohash,seen_at) VALUES(?,?,?,?)
		ON CONFLICT(source_id,guid) DO UPDATE SET infohash = excluded.infohash, seen_at = excluded.seen_at`, sourceID, Trunc(guid, 500), ih, Now())
	return err
}

// PruneSourceItems forgets items older than 30 days (feeds only show page 1).
func (s *Store) PruneSourceItems(ctx context.Context) (int64, error) {
	res, err := s.W.ExecContext(ctx, `DELETE FROM source_items WHERE seen_at < ?`, time.Now().UTC().AddDate(0, 0, -30).Format(time.RFC3339))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// ---- qB metadata queue ----

// MetadataJob is a magnet-only torrent waiting for qB to fetch its metadata.
type MetadataJob struct {
	Infohash  string   `json:"infohash"`
	SourceID  *int64   `json:"source_id"`
	Status    string   `json:"status"` // pending|running|done|failed
	Attempts  int      `json:"attempts"`
	NextAt    string   `json:"next_at"`
	ServerID  *int64   `json:"server_id"`
	Added     bool     `json:"added"`
	StartedAt *string  `json:"started_at"`
	LastError string   `json:"last_error"`
	Trackers  []string `json:"-"`
	CreatedAt string   `json:"created_at"`
	UpdatedAt string   `json:"updated_at"`
	Priority  int      `json:"priority"` // 1 = interactive (a user is waiting)
}

const jobCols = `infohash,source_id,status,attempts,next_at,server_id,added,started_at,last_error,trackers,created_at,updated_at,priority`

func scanJob(sc interface{ Scan(...any) error }) (MetadataJob, error) {
	var j MetadataJob
	var src, srv sql.NullInt64
	var started sql.NullString
	var added int
	var trackers string
	err := sc.Scan(&j.Infohash, &src, &j.Status, &j.Attempts, &j.NextAt, &srv, &added, &started, &j.LastError, &trackers, &j.CreatedAt, &j.UpdatedAt, &j.Priority)
	_ = json.Unmarshal([]byte(trackers), &j.Trackers)
	if src.Valid {
		j.SourceID = &src.Int64
	}
	if srv.Valid {
		j.ServerID = &srv.Int64
	}
	if started.Valid {
		j.StartedAt = &started.String
	}
	j.Added = added != 0
	return j, err
}

func (s *Store) queryJobs(ctx context.Context, where string, args ...any) ([]MetadataJob, error) {
	rows, err := s.R.QueryContext(ctx, `SELECT `+jobCols+` FROM metadata_jobs `+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []MetadataJob{}
	for rows.Next() {
		j, err := scanJob(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, j)
	}
	return out, rows.Err()
}

// EnqueueMetadataJob adds a pending job unless one exists for ih.
func (s *Store) EnqueueMetadataJob(ctx context.Context, ih string, sourceID *int64, trackers []string) (bool, error) {
	now := Now()
	if len(trackers) > 10 {
		trackers = trackers[:10]
	}
	tr, _ := json.Marshal(trackers)
	if trackers == nil {
		tr = []byte("[]")
	}
	res, err := s.W.ExecContext(ctx, `INSERT INTO metadata_jobs(infohash,source_id,status,next_at,trackers,created_at,updated_at) VALUES(?,?, 'pending', ?, ?, ?, ?)
		ON CONFLICT(infohash) DO NOTHING`, ih, sourceID, now, string(tr), now, now)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// GetMetadataJob returns the job for ih, or nil.
func (s *Store) GetMetadataJob(ctx context.Context, ih string) (*MetadataJob, error) {
	list, err := s.queryJobs(ctx, `WHERE infohash = ?`, ih)
	if err != nil || len(list) == 0 {
		return nil, err
	}
	return &list[0], nil
}

// DueMetadataJobs returns pending jobs whose next_at has passed: interactive ones
// (priority 1) first, then oldest first.
func (s *Store) DueMetadataJobs(ctx context.Context, limit int) ([]MetadataJob, error) {
	return s.queryJobs(ctx, `WHERE status = 'pending' AND next_at <= ? ORDER BY priority DESC, next_at LIMIT ?`, Now(), limit)
}

// RequestMetadataJob queues ih for an interactive metadata fetch (a user opened a hash we
// have no metadata for): a new job, or an existing one made due now with priority 1 (a
// failed one gets its attempts back). A running job only gets the priority.
func (s *Store) RequestMetadataJob(ctx context.Context, ih string) error {
	now := Now()
	_, err := s.W.ExecContext(ctx, `INSERT INTO metadata_jobs(infohash,status,next_at,trackers,priority,created_at,updated_at) VALUES(?, 'pending', ?, '[]', 1, ?, ?)
		ON CONFLICT(infohash) DO UPDATE SET priority = 1, updated_at = excluded.updated_at,
			attempts = CASE WHEN metadata_jobs.status IN ('failed','done') THEN 0 ELSE metadata_jobs.attempts END,
			next_at = CASE WHEN metadata_jobs.status = 'running' THEN metadata_jobs.next_at ELSE excluded.next_at END,
			status = CASE WHEN metadata_jobs.status = 'running' THEN 'running' ELSE 'pending' END`, ih, now, now, now)
	return err
}

// ListMetadataJobs lists jobs for the admin view (unfinished first, then newest).
func (s *Store) ListMetadataJobs(ctx context.Context, limit int) ([]MetadataJob, error) {
	return s.queryJobs(ctx, `ORDER BY CASE status WHEN 'running' THEN 0 WHEN 'pending' THEN 1 WHEN 'failed' THEN 2 ELSE 3 END, updated_at DESC LIMIT ?`, limit)
}

// ResetRunningJobs puts jobs interrupted by a restart back into the queue (they keep
// server_id/added so the torrent already in qB is reused and cleaned up).
func (s *Store) ResetRunningJobs(ctx context.Context) error {
	_, err := s.W.ExecContext(ctx, `UPDATE metadata_jobs SET status = 'pending', next_at = ?, updated_at = ? WHERE status = 'running'`, Now(), Now())
	return err
}

// StartMetadataJob marks a job running.
func (s *Store) StartMetadataJob(ctx context.Context, ih string) error {
	_, err := s.W.ExecContext(ctx, `UPDATE metadata_jobs SET status = 'running', started_at = ?, updated_at = ? WHERE infohash = ?`, Now(), Now(), ih)
	return err
}

// SetMetadataJobServer records that we added the torrent to a qB server.
func (s *Store) SetMetadataJobServer(ctx context.Context, ih string, serverID int64, added bool) error {
	_, err := s.W.ExecContext(ctx, `UPDATE metadata_jobs SET server_id = ?, added = ?, updated_at = ? WHERE infohash = ?`, serverID, boolInt(added), Now(), ih)
	return err
}

// FinishMetadataJob marks a job done, or failed/retried (attempts+1, retry after `retry`
// unless attempts reached maxAttempts).
func (s *Store) FinishMetadataJob(ctx context.Context, ih string, jobErr error, retry time.Duration, maxAttempts int) error {
	now := Now()
	if jobErr == nil {
		_, err := s.W.ExecContext(ctx, `UPDATE metadata_jobs SET status = 'done', attempts = attempts + 1, last_error = '', updated_at = ? WHERE infohash = ?`, now, ih)
		return err
	}
	_, err := s.W.ExecContext(ctx, `UPDATE metadata_jobs SET attempts = attempts + 1,
		status = CASE WHEN attempts + 1 >= ? THEN 'failed' ELSE 'pending' END,
		next_at = ?, last_error = ?, updated_at = ? WHERE infohash = ?`,
		maxAttempts, time.Now().UTC().Add(retry).Format(time.RFC3339), Trunc(jobErr.Error(), 500), now, ih)
	return err
}

// DeferMetadataJob postpones a job without spending an attempt (e.g. no qB online).
func (s *Store) DeferMetadataJob(ctx context.Context, ih string, d time.Duration, reason string) error {
	_, err := s.W.ExecContext(ctx, `UPDATE metadata_jobs SET status = 'pending', next_at = ?, last_error = ?, updated_at = ? WHERE infohash = ?`,
		time.Now().UTC().Add(d).Format(time.RFC3339), Trunc(reason, 500), Now(), ih)
	return err
}

// MetadataJobAdded reports whether we added ih to qB server serverID for a metadata job
// (used to decide whether a leftover torrent in the meta category is ours to delete).
func (s *Store) MetadataJobAdded(ctx context.Context, ih string, serverID int64) bool {
	var n int
	_ = s.R.QueryRowContext(ctx, `SELECT COUNT(*) FROM metadata_jobs WHERE infohash = ? AND server_id = ? AND added = 1 AND status IN ('done','failed')`, ih, serverID).Scan(&n)
	return n > 0
}

// PruneMetadataJobs drops finished jobs older than 30 days.
func (s *Store) PruneMetadataJobs(ctx context.Context) (int64, error) {
	res, err := s.W.ExecContext(ctx, `DELETE FROM metadata_jobs WHERE status IN ('done','failed') AND updated_at < ?`, time.Now().UTC().AddDate(0, 0, -30).Format(time.RFC3339))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
