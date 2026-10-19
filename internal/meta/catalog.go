package meta

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/darknightlab/onlinetorrent/internal/store"
)

// CatalogTorrent is a torrent of the local catalogue (crawled or user-submitted).
type CatalogTorrent struct {
	Infohash    string
	Title       string
	Source      string
	SourceKind  string
	SourceID    string
	Trusted     bool
	Team        string
	TeamID      string
	PublishedAt string
	PageURL     string
	Size        int64
	FileCount   int
	Status      string // ready|pending
	Tags        []TagRef
	Files       []FileInput // filled by the *WithFiles queries
}

const catCols = `t.infohash,t.title,t.source,t.source_kind,t.source_id,t.trusted,t.team,t.team_id,t.published_at,t.page_url,t.size_bytes,t.file_count,t.status,t.tags`

func scanCatalog(sc interface{ Scan(...any) error }) (*CatalogTorrent, error) {
	var t CatalogTorrent
	var tags string
	var trusted int
	if err := sc.Scan(&t.Infohash, &t.Title, &t.Source, &t.SourceKind, &t.SourceID, &trusted, &t.Team, &t.TeamID, &t.PublishedAt, &t.PageURL, &t.Size, &t.FileCount, &t.Status, &tags); err != nil {
		return nil, err
	}
	t.Trusted = trusted != 0
	_ = json.Unmarshal([]byte(tags), &t.Tags)
	return &t, nil
}

func (s *Store) queryCatalog(ctx context.Context, q string, args ...any) ([]*CatalogTorrent, error) {
	rows, err := s.R.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*CatalogTorrent{}
	for rows.Next() {
		t, err := scanCatalog(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// fillFiles loads file lists (paths + sizes) for the given torrents.
func (s *Store) fillFiles(ctx context.Context, ts []*CatalogTorrent) error {
	if len(ts) == 0 {
		return nil
	}
	idx := map[string]*CatalogTorrent{}
	args := make([]any, 0, len(ts))
	for _, t := range ts {
		idx[t.Infohash] = t
		args = append(args, t.Infohash)
	}
	ph := strings.TrimRight(strings.Repeat("?,", len(args)), ",")
	rows, err := s.R.QueryContext(ctx, `SELECT infohash, file_index, path, size_bytes FROM torrent_files WHERE infohash IN (`+ph+`) ORDER BY infohash, file_index`, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var ih string
		var f FileInput
		if err := rows.Scan(&ih, &f.Index, &f.Path, &f.Size); err != nil {
			return err
		}
		if t := idx[ih]; t != nil {
			t.Files = append(t.Files, f)
		}
	}
	return rows.Err()
}

// CatalogByHash returns one catalogue torrent with files, or ErrNotFound.
func (s *Store) CatalogByHash(ctx context.Context, ih string) (*CatalogTorrent, error) {
	list, err := s.queryCatalog(ctx, `SELECT `+catCols+` FROM torrents t WHERE t.infohash = ?`, ih)
	if err != nil {
		return nil, err
	}
	if len(list) == 0 {
		return nil, store.ErrNotFound
	}
	return list[0], s.fillFiles(ctx, list)
}

// CatalogBySourceID finds a torrent by its id at the source (e.g. bangumi.moe _id).
func (s *Store) CatalogBySourceID(ctx context.Context, kind, id string) (*CatalogTorrent, error) {
	list, err := s.queryCatalog(ctx, `SELECT `+catCols+` FROM torrents t WHERE t.source_id = ? AND t.source_kind = ? LIMIT 1`, id, kind)
	if err != nil {
		return nil, err
	}
	if len(list) == 0 {
		return nil, store.ErrNotFound
	}
	return list[0], s.fillFiles(ctx, list)
}

// TorrentsByWork lists torrents with at least one file mapped to the work, newest first.
func (s *Store) TorrentsByWork(ctx context.Context, workKey string, limit int, withFiles bool) ([]*CatalogTorrent, error) {
	list, err := s.queryCatalog(ctx, `SELECT `+catCols+` FROM torrents t
		WHERE t.infohash IN (SELECT infohash FROM torrent_files WHERE work_key = ? AND status != 'rejected')
		ORDER BY t.published_at DESC LIMIT ?`, workKey, limit)
	if err != nil || !withFiles {
		return list, err
	}
	return list, s.fillFiles(ctx, list)
}

// KnownTorrent reports whether ih is in the catalogue and its status.
func (s *Store) KnownTorrent(ctx context.Context, ih string) (status string, ok bool) {
	err := s.R.QueryRowContext(ctx, `SELECT status FROM torrents WHERE infohash = ?`, ih).Scan(&status)
	return status, err == nil
}

// GetTorrentInput returns the stored catalogue info of ih as a TorrentInput (no files),
// e.g. to match a pending torrent once qB delivers its file list.
func (s *Store) GetTorrentInput(ctx context.Context, ih string) (TorrentInput, bool) {
	list, err := s.queryCatalog(ctx, `SELECT `+catCols+` FROM torrents t WHERE t.infohash = ?`, ih)
	if err != nil || len(list) == 0 {
		return TorrentInput{}, false
	}
	t := list[0]
	in := TorrentInput{Infohash: ih, Title: t.Title, Source: t.Source, SourceKind: t.SourceKind, SourceID: t.SourceID, Trusted: t.Trusted,
		Team: t.Team, TeamID: t.TeamID, PublishedAt: t.PublishedAt, PageURL: t.PageURL, Size: t.Size, Tags: t.Tags}
	for _, g := range t.Tags {
		in.MoeTagIDs = append(in.MoeTagIDs, g.ID)
	}
	return in, true
}

// ---- full-text search (FTS5 trigram; queries shorter than 3 characters use LIKE) ----

// ftsQuery turns user input into an FTS5 MATCH expression: every whitespace-separated
// term quoted (AND). ok is false when a term is shorter than 3 characters (trigram can't
// match it) — the caller then falls back to LIKE.
func ftsQuery(q string) (expr string, terms []string, ok bool) {
	terms = strings.Fields(q)
	if len(terms) == 0 {
		return "", nil, false
	}
	ok = true
	parts := make([]string, len(terms))
	for i, t := range terms {
		if utf8.RuneCountInString(t) < 3 {
			ok = false
		}
		parts[i] = `"` + strings.ReplaceAll(t, `"`, `""`) + `"`
	}
	return strings.Join(parts, " "), terms, ok
}

func likeEscape(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return "%" + r.Replace(s) + "%"
}

// SearchTorrents searches torrent titles and file names; newest first. Torrents with a file
// mapped to a subject outside scope are left out (nil scope: unrestricted).
func (s *Store) SearchTorrents(ctx context.Context, q string, offset, limit int, scope *store.ScopePlan) ([]*CatalogTorrent, int, error) {
	expr, terms, ok := ftsQuery(q)
	if len(terms) == 0 {
		return []*CatalogTorrent{}, 0, nil
	}
	var where string
	var args []any
	if ok {
		where = `t.seq IN (SELECT rowid FROM torrents_fts WHERE torrents_fts MATCH ?)`
		args = []any{expr}
	} else {
		var conds []string
		for _, t := range terms {
			conds = append(conds, `t.title LIKE ? ESCAPE '\'`)
			args = append(args, likeEscape(t))
		}
		where = strings.Join(conds, " AND ")
	}
	if scope.Restricted() {
		pred, pargs := store.TorrentScopePredicate("t", scope)
		where = "(" + where + ") AND " + pred
		args = append(args, pargs...)
	}
	var total int
	if err := s.R.QueryRowContext(ctx, `SELECT COUNT(*) FROM torrents t WHERE `+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	list, err := s.queryCatalog(ctx, `SELECT `+catCols+` FROM torrents t WHERE `+where+` ORDER BY t.published_at DESC, t.seq DESC LIMIT ? OFFSET ?`,
		append(args, limit, offset)...)
	if err != nil {
		return nil, 0, err
	}
	return list, total, s.fillFiles(ctx, list)
}

// SearchWorksFTS searches works by title / Chinese title / aliases (newest first) within
// the content scope (nil: unrestricted).
func (s *Store) SearchWorksFTS(ctx context.Context, q string, limit int, scope *store.ScopePlan) ([]*Work, error) {
	expr, terms, ok := ftsQuery(q)
	if len(terms) == 0 {
		return []*Work{}, nil
	}
	pred, pargs := store.WorkScopePredicate("works", scope)
	var rows *sql.Rows
	var err error
	if ok {
		args := append([]any{expr}, pargs...)
		rows, err = s.R.QueryContext(ctx, `SELECT `+workCols+` FROM works WHERE id IN (SELECT rowid FROM works_fts WHERE works_fts MATCH ?) AND `+pred+` ORDER BY begin_at DESC LIMIT ?`, append(args, limit)...)
	} else {
		var conds []string
		var args []any
		for _, t := range terms {
			l := likeEscape(t)
			conds = append(conds, `(title LIKE ? ESCAPE '\' OR title_cn LIKE ? ESCAPE '\' OR aliases LIKE ? ESCAPE '\')`)
			args = append(args, l, l, l)
		}
		args = append(args, pargs...)
		rows, err = s.R.QueryContext(ctx, `SELECT `+workCols+` FROM works WHERE `+strings.Join(conds, " AND ")+` AND `+pred+` ORDER BY begin_at DESC LIMIT ?`, append(args, limit)...)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Work{}
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

// ExternalWork is a subject found on bgm.tv / TMDB, upserted into works on demand.
type ExternalWork struct {
	Provider string // bgm|tmdb
	ExtID    string // bgm subject id, or "tv/123" / "movie/123" for TMDB
	Title    string
	TitleCN  string
	Aliases  []string
	Type     string // tv|web|movie|ova
	Begin    string // RFC3339 or ""
	Cover    string
	Summary  string
}

// UpsertExternalWork stores a work found via an external search. Existing works (e.g. from
// bangumi-data, matched by the external id) only get an empty cover/summary filled in.
func (s *Store) UpsertExternalWork(ctx context.Context, e ExternalWork) (*Work, error) {
	if e.Provider == "" || e.ExtID == "" || e.Title == "" {
		return nil, errors.New("incomplete external work")
	}
	if w, err := s.WorkByExternal(ctx, e.Provider, e.ExtID); err == nil {
		_ = s.SetDetail(ctx, w.Key, e.Summary, e.Cover)
		return s.GetWork(ctx, w.Key)
	}
	key := e.Provider + ":" + e.ExtID
	al, _ := json.Marshal(e.Aliases)
	if e.Aliases == nil {
		al = []byte("[]")
	}
	tx, err := s.W.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `INSERT INTO works(id,key,title,title_cn,aliases,type,begin_at,cover,summary,source,updated_at) VALUES((SELECT COALESCE(MAX(id),0)+1 FROM works),?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(key) DO NOTHING`,
		key, store.Trunc(e.Title, 300), store.Trunc(e.TitleCN, 300), string(al), e.Type, e.Begin, e.Cover, store.Trunc(e.Summary, 4000), e.Provider, store.Now()); err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO work_ids(provider,ext_id,work_key) VALUES(?,?,?) ON CONFLICT(provider,ext_id) DO NOTHING`, e.Provider, e.ExtID, key); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	s.aliasMu.Lock()
	s.aliases = nil // new work: rebuild the alias index on next match
	s.aliasMu.Unlock()
	s.resetCandidateIndex()
	return s.GetWork(ctx, key)
}

// SubjectTag is a subject tag with the number of works carrying it (tags ∪ meta_tags).
type SubjectTag struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
}

// SubjectTags autocompletes subject tags (admin content scope editor): tags containing q
// (prefix matches first), by number of works; empty q lists the most common tags.
func (s *Store) SubjectTags(ctx context.Context, q string, limit int) ([]SubjectTag, error) {
	esc := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(q)
	rows, err := s.R.QueryContext(ctx, `SELECT value, COUNT(*) AS c FROM (
			SELECT w.id, j.value FROM works w, json_each(CASE WHEN json_valid(w.tags) THEN w.tags ELSE '[]' END) j
			UNION SELECT w.id, j.value FROM works w, json_each(CASE WHEN json_valid(w.meta_tags) THEN w.meta_tags ELSE '[]' END) j)
		WHERE typeof(value) = 'text' AND value LIKE ? ESCAPE '\'
		GROUP BY value ORDER BY (value LIKE ? ESCAPE '\') DESC, c DESC, value LIMIT ?`, "%"+esc+"%", esc+"%", limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []SubjectTag{}
	for rows.Next() {
		var t SubjectTag
		if err := rows.Scan(&t.Name, &t.Count); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// HiddenByScope reports which of the torrents have a (non-rejected, i.e. applied) file
// mapped to a subject outside the scope — those are not playable for the scope's role.
// Unmapped files never hide a torrent.
func (s *Store) HiddenByScope(ctx context.Context, ihs []string, scope *store.ScopePlan) (map[string]bool, error) {
	out := map[string]bool{}
	if !scope.Restricted() || len(ihs) == 0 {
		return out, nil
	}
	pred, pargs := store.TorrentScopePredicate("t", scope)
	for start := 0; start < len(ihs); start += 400 {
		part := ihs[start:min(len(ihs), start+400)]
		args := make([]any, 0, len(part)+len(pargs))
		for _, ih := range part {
			args = append(args, ih)
		}
		args = append(args, pargs...)
		rows, err := s.R.QueryContext(ctx, `SELECT t.infohash FROM torrents t WHERE t.infohash IN (`+placeholders(len(part))+`) AND NOT (`+pred+`)`, args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var ih string
			if rows.Scan(&ih) == nil {
				out[ih] = true
			}
		}
		rows.Close()
	}
	return out, nil
}
