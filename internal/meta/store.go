// Package meta is the mapping service: works (anime/series) with external ids, and a
// file-level map (infohash, file_index) -> (work, episode). It owns its own SQLite file
// so it can be deployed standalone (cmd/metad) or mounted inside otd.
package meta

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strconv"
	"strings"

	"github.com/darknightlab/onlinetorrent/internal/store"
)

var migrations = []string{`
CREATE TABLE works (
	key        TEXT PRIMARY KEY,           -- bgm:{id} or bd:{slug}
	title      TEXT NOT NULL,
	title_cn   TEXT NOT NULL DEFAULT '',
	aliases    TEXT NOT NULL DEFAULT '[]', -- JSON []string
	type       TEXT NOT NULL DEFAULT '',   -- tv|web|movie|ova
	begin_at   TEXT NOT NULL DEFAULT '',
	end_at     TEXT NOT NULL DEFAULT '',
	broadcast  TEXT NOT NULL DEFAULT '',   -- ISO8601 repeating interval R/start/P7D
	cover      TEXT NOT NULL DEFAULT '',
	summary    TEXT NOT NULL DEFAULT '',
	official   TEXT NOT NULL DEFAULT '',
	source     TEXT NOT NULL DEFAULT 'bangumi-data',
	updated_at TEXT NOT NULL
);
CREATE INDEX works_begin ON works(begin_at);

CREATE TABLE work_ids (
	provider TEXT NOT NULL,   -- bgm|bangumi_moe|mal|anidb|anilist|tmdb|imdb|tvdb|gamer|bilibili...
	ext_id   TEXT NOT NULL,
	work_key TEXT NOT NULL REFERENCES works(key) ON DELETE CASCADE,
	PRIMARY KEY (provider, ext_id)
);
CREATE INDEX work_ids_work ON work_ids(work_key);

CREATE TABLE torrents (
	infohash     TEXT PRIMARY KEY,
	title        TEXT NOT NULL DEFAULT '',
	source       TEXT NOT NULL DEFAULT '',
	source_id    TEXT NOT NULL DEFAULT '',
	team         TEXT NOT NULL DEFAULT '',
	published_at TEXT NOT NULL DEFAULT '',
	updated_at   TEXT NOT NULL
);

CREATE TABLE torrent_files (
	infohash   TEXT NOT NULL REFERENCES torrents(infohash) ON DELETE CASCADE,
	file_index INTEGER NOT NULL,
	path       TEXT NOT NULL,
	size_bytes INTEGER NOT NULL DEFAULT 0,
	kind       TEXT NOT NULL DEFAULT 'other',  -- episode|sp|op|ed|extra|other
	work_key   TEXT REFERENCES works(key) ON DELETE SET NULL,
	episode    REAL,                           -- 12.5 allowed for recap episodes
	confidence REAL NOT NULL DEFAULT 0,
	status     TEXT NOT NULL DEFAULT 'auto' CHECK (status IN ('auto','verified','rejected')),
	method     TEXT NOT NULL DEFAULT '',
	updated_at TEXT NOT NULL,
	updated_by TEXT NOT NULL DEFAULT '',
	PRIMARY KEY (infohash, file_index)
);
CREATE INDEX torrent_files_ep ON torrent_files(work_key, episode);
`}

// Store is the meta database.
type Store struct{ W, R *sql.DB }

// Open opens meta.db.
func Open(path string) (*Store, error) {
	w, r, err := store.OpenDB(path, migrations)
	if err != nil {
		return nil, err
	}
	return &Store{W: w, R: r}, nil
}

func (s *Store) Close() { s.R.Close(); s.W.Close() }

// Work is an anime/series entry.
type Work struct {
	Key       string            `json:"key"`
	Title     string            `json:"title"`
	TitleCN   string            `json:"title_cn"`
	Aliases   []string          `json:"aliases,omitempty"`
	Type      string            `json:"type"`
	Begin     string            `json:"begin"`
	End       string            `json:"end"`
	Broadcast string            `json:"broadcast"`
	Cover     string            `json:"cover"`
	Summary   string            `json:"summary"`
	Official  string            `json:"official_site,omitempty"`
	External  map[string]string `json:"external"`
}

const workCols = `key,title,title_cn,aliases,type,begin_at,end_at,broadcast,cover,summary,official`

func scanWork(sc interface{ Scan(...any) error }) (*Work, error) {
	var w Work
	var aliases string
	if err := sc.Scan(&w.Key, &w.Title, &w.TitleCN, &aliases, &w.Type, &w.Begin, &w.End, &w.Broadcast, &w.Cover, &w.Summary, &w.Official); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, store.ErrNotFound
		}
		return nil, err
	}
	_ = json.Unmarshal([]byte(aliases), &w.Aliases)
	return &w, nil
}

func (s *Store) fillExternal(ctx context.Context, works []*Work) error {
	if len(works) == 0 {
		return nil
	}
	idx := map[string]*Work{}
	args := make([]any, 0, len(works))
	for _, w := range works {
		w.External = map[string]string{}
		idx[w.Key] = w
		args = append(args, w.Key)
	}
	ph := strings.TrimRight(strings.Repeat("?,", len(args)), ",")
	rows, err := s.R.QueryContext(ctx, `SELECT work_key, provider, ext_id FROM work_ids WHERE work_key IN (`+ph+`)`, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var k, p, id string
		if err := rows.Scan(&k, &p, &id); err != nil {
			return err
		}
		idx[k].External[p] = id
	}
	return rows.Err()
}

// GetWork loads a work with external ids.
func (s *Store) GetWork(ctx context.Context, key string) (*Work, error) {
	w, err := scanWork(s.R.QueryRowContext(ctx, `SELECT `+workCols+` FROM works WHERE key = ?`, key))
	if err != nil {
		return nil, err
	}
	return w, s.fillExternal(ctx, []*Work{w})
}

// WorkByExternal finds a work by provider id, e.g. ("bangumi_moe", tagID).
func (s *Store) WorkByExternal(ctx context.Context, provider, id string) (*Work, error) {
	var key string
	if err := s.R.QueryRowContext(ctx, `SELECT work_key FROM work_ids WHERE provider = ? AND ext_id = ?`, provider, id).Scan(&key); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, store.ErrNotFound
		}
		return nil, err
	}
	return s.GetWork(ctx, key)
}

// WorksBetween returns works whose begin is in [from, to) (RFC3339 strings).
func (s *Store) WorksBetween(ctx context.Context, from, to string) ([]*Work, error) {
	rows, err := s.R.QueryContext(ctx, `SELECT `+workCols+` FROM works WHERE begin_at >= ? AND begin_at < ? ORDER BY begin_at`, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Work
	for rows.Next() {
		w, err := scanWork(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, s.fillExternal(ctx, out)
}

// SearchWorks matches title/aliases.
func (s *Store) SearchWorks(ctx context.Context, q string, limit int) ([]*Work, error) {
	like := "%" + q + "%"
	rows, err := s.R.QueryContext(ctx, `SELECT `+workCols+` FROM works WHERE title LIKE ? OR title_cn LIKE ? OR aliases LIKE ? ORDER BY begin_at DESC LIMIT ?`, like, like, like, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Work
	for rows.Next() {
		w, err := scanWork(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, s.fillExternal(ctx, out)
}

// SetCover sets a work cover if the source has one.
func (s *Store) SetCover(ctx context.Context, key, cover string) error {
	_, err := s.W.ExecContext(ctx, `UPDATE works SET cover = ? WHERE key = ? AND cover != ?`, cover, key, cover)
	return err
}

// SetSummary sets a work summary/cover from bgm.tv.
func (s *Store) SetDetail(ctx context.Context, key, summary, cover string) error {
	_, err := s.W.ExecContext(ctx, `UPDATE works SET summary = CASE WHEN ? != '' THEN ? ELSE summary END, cover = CASE WHEN cover = '' THEN ? ELSE cover END WHERE key = ?`, summary, summary, cover, key)
	return err
}

// ---- file map ----

// FileInput is a torrent file to (re)match.
type FileInput struct {
	Index int
	Path  string
	Size  int64
}

// TorrentInput describes a torrent for matching.
type TorrentInput struct {
	Infohash    string
	Title       string
	Source      string
	SourceID    string
	Team        string
	PublishedAt string
	MoeTagIDs   []string // bangumi.moe tag ids (any type; bangumi ones resolve to works)
	Files       []FileInput
}

// FileMap is one mapped file.
type FileMap struct {
	Index      int      `json:"index"`
	Path       string   `json:"path"`
	Size       int64    `json:"size_bytes"`
	Kind       string   `json:"kind"`
	Work       *WorkRef `json:"work"`
	Episode    *float64 `json:"episode"`
	Confidence float64  `json:"confidence"`
	Status     string   `json:"status"`
	Method     string   `json:"method"`
}

// WorkRef is a compact work reference.
type WorkRef struct {
	Key      string            `json:"key"`
	Title    string            `json:"title"`
	TitleCN  string            `json:"title_cn"`
	External map[string]string `json:"external,omitempty"`
}

// TorrentMap is the API view of a torrent's files.
type TorrentMap struct {
	Infohash string    `json:"infohash"`
	Title    string    `json:"title"`
	Files    []FileMap `json:"files"`
}

// GetTorrentMap returns file mappings for a torrent.
func (s *Store) GetTorrentMap(ctx context.Context, ih string) (*TorrentMap, error) {
	tm := &TorrentMap{Infohash: ih, Files: []FileMap{}}
	if err := s.R.QueryRowContext(ctx, `SELECT title FROM torrents WHERE infohash = ?`, ih).Scan(&tm.Title); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, store.ErrNotFound
		}
		return nil, err
	}
	rows, err := s.R.QueryContext(ctx, `SELECT f.file_index,f.path,f.size_bytes,f.kind,f.work_key,COALESCE(w.title,''),COALESCE(w.title_cn,''),f.episode,f.confidence,f.status,f.method
		FROM torrent_files f LEFT JOIN works w ON w.key = f.work_key WHERE f.infohash = ? ORDER BY f.file_index`, ih)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var works []*Work
	for rows.Next() {
		var f FileMap
		var wk sql.NullString
		var wt, wcn string
		var ep sql.NullFloat64
		if err := rows.Scan(&f.Index, &f.Path, &f.Size, &f.Kind, &wk, &wt, &wcn, &ep, &f.Confidence, &f.Status, &f.Method); err != nil {
			return nil, err
		}
		if wk.Valid {
			f.Work = &WorkRef{Key: wk.String, Title: wt, TitleCN: wcn}
			works = append(works, &Work{Key: wk.String})
		}
		if ep.Valid {
			v := ep.Float64
			f.Episode = &v
		}
		tm.Files = append(tm.Files, f)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	uniq := dedupWorks(works)
	if err := s.fillExternal(ctx, uniq); err != nil {
		return nil, err
	}
	ext := map[string]map[string]string{}
	for _, w := range uniq {
		ext[w.Key] = w.External
	}
	for i := range tm.Files {
		if tm.Files[i].Work != nil {
			tm.Files[i].Work.External = ext[tm.Files[i].Work.Key]
		}
	}
	return tm, nil
}

func dedupWorks(ws []*Work) []*Work {
	seen := map[string]bool{}
	var out []*Work
	for _, w := range ws {
		if seen[w.Key] {
			continue
		}
		seen[w.Key] = true
		out = append(out, w)
	}
	return out
}

// EpisodeFile is a file known to contain an episode.
type EpisodeFile struct {
	Infohash    string  `json:"infohash"`
	Index       int     `json:"file_index"`
	Path        string  `json:"path"`
	Size        int64   `json:"size_bytes"`
	Title       string  `json:"torrent_title"`
	Team        string  `json:"team"`
	PublishedAt string  `json:"published_at"`
	SourceID    string  `json:"source_id"`
	Confidence  float64 `json:"confidence"`
}

// EpisodeFiles lists files mapped to work+episode.
func (s *Store) EpisodeFiles(ctx context.Context, workKey string, ep float64) ([]EpisodeFile, error) {
	rows, err := s.R.QueryContext(ctx, `SELECT f.infohash,f.file_index,f.path,f.size_bytes,t.title,t.team,t.published_at,t.source_id,f.confidence
		FROM torrent_files f JOIN torrents t ON t.infohash = f.infohash
		WHERE f.work_key = ? AND f.episode = ? AND f.status != 'rejected' ORDER BY t.published_at DESC LIMIT 200`, workKey, ep)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []EpisodeFile{}
	for rows.Next() {
		var e EpisodeFile
		if err := rows.Scan(&e.Infohash, &e.Index, &e.Path, &e.Size, &e.Title, &e.Team, &e.PublishedAt, &e.SourceID, &e.Confidence); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// LatestEpisodes returns max mapped episode per work (for timeline "已有资源").
func (s *Store) LatestEpisodes(ctx context.Context) (map[string]float64, error) {
	rows, err := s.R.QueryContext(ctx, `SELECT work_key, MAX(episode) FROM torrent_files WHERE work_key IS NOT NULL AND episode IS NOT NULL AND kind = 'episode' AND status != 'rejected' GROUP BY work_key`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]float64{}
	for rows.Next() {
		var k string
		var e float64
		if err := rows.Scan(&k, &e); err != nil {
			return nil, err
		}
		out[k] = e
	}
	return out, rows.Err()
}

// SetFile is a manual correction.
type SetFile struct {
	WorkKey *string  `json:"work_key"`
	Episode *float64 `json:"episode"`
	Kind    string   `json:"kind"`
	Status  string   `json:"status"`
}

func (s *Store) CorrectFile(ctx context.Context, ih string, idx int, p SetFile, by string) error {
	if p.Status == "" {
		p.Status = "verified"
	}
	switch p.Status {
	case "auto", "verified", "rejected":
	default:
		return errors.New("invalid status")
	}
	if p.Kind == "" {
		p.Kind = "episode"
	}
	var wk any
	if p.WorkKey != nil && *p.WorkKey != "" {
		wk = *p.WorkKey
	}
	res, err := s.W.ExecContext(ctx, `UPDATE torrent_files SET work_key=?, episode=?, kind=?, status=?, confidence=1, method='manual', updated_at=?, updated_by=? WHERE infohash=? AND file_index=?`,
		wk, p.Episode, p.Kind, p.Status, store.Now(), by, ih, idx)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return store.ErrNotFound
	}
	return nil
}

func parseExternal(q string) (provider, id string, ok bool) {
	p, v, found := strings.Cut(q, ":")
	if !found || p == "" || v == "" {
		return "", "", false
	}
	return p, v, true
}

func atof(s string) (float64, bool) {
	v, err := strconv.ParseFloat(s, 64)
	return v, err == nil
}
