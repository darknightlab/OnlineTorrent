package meta

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/darknightlab/onlinetorrent/internal/store"
)

// Match records a torrent with its file list and (re)computes automatic mappings for
// every file (all files are stored; only video episodes get a work + episode). Rows with
// status verified/rejected are never overwritten. With no files the torrent is stored as
// pending (see UpsertPending).
func (s *Store) Match(ctx context.Context, t TorrentInput) error {
	if t.Infohash == "" {
		return nil
	}
	if len(t.Files) == 0 {
		return s.UpsertPending(ctx, t)
	}
	now := store.Now()
	t.Title = store.Trunc(t.Title, store.MaxTitleBytes)
	t.Team = store.Trunc(t.Team, 100)
	if len(t.Files) > store.MaxFilesStored {
		t.Files = t.Files[:store.MaxFilesStored]
	}
	var total int64
	for _, f := range t.Files {
		total += f.Size
	}
	if t.Size > total {
		total = t.Size
	}
	if err := s.upsertTorrentRow(ctx, t, "ready", len(t.Files), total, now); err != nil {
		return err
	}
	// matching uses the stored title/tags when the caller only knows the files (qB metadata)
	if t.Title == "" || len(t.MoeTagIDs) == 0 {
		if st, ok := s.GetTorrentInput(ctx, t.Infohash); ok {
			if t.Title == "" {
				t.Title = st.Title
			}
			if len(t.MoeTagIDs) == 0 {
				t.MoeTagIDs = st.MoeTagIDs
			}
		}
	}

	work, method, wconf := s.resolveWork(ctx, t)

	paths := make([]string, len(t.Files))
	for i, f := range t.Files {
		paths[i] = f.Path
	}
	parsed := classifyAll(paths, t.Title, work)
	for i, f := range t.Files {
		kind, p := parsed[i].Kind, parsed[i]
		wk, ep, conf, m := fileMapping(work, method, wconf, kind, p)
		if _, err := s.W.ExecContext(ctx, `INSERT INTO torrent_files(infohash,file_index,path,size_bytes,kind,work_key,episode,confidence,status,method,updated_at) VALUES(?,?,?,?,?,?,?,?, 'auto', ?, ?)
			ON CONFLICT(infohash,file_index) DO UPDATE SET path=excluded.path, size_bytes=CASE WHEN excluded.size_bytes>0 THEN excluded.size_bytes ELSE size_bytes END,
			kind=excluded.kind, work_key=excluded.work_key, episode=excluded.episode, confidence=excluded.confidence, method=excluded.method, updated_at=excluded.updated_at
			WHERE torrent_files.status = 'auto'`,
			t.Infohash, f.Index, store.Trunc(f.Path, store.MaxPathBytes), f.Size, kind, wk, ep, conf, m, now); err != nil {
			return err
		}
	}
	return s.refreshFTS(ctx, t.Infohash)
}

// classifyAll is ClassifyFiles (all files of one torrent, cross-checked) plus the
// single-video movie rule (a movie's only video is episode 1).
func classifyAll(paths []string, title string, work *Work) []ParsedName {
	ps := ClassifyFiles(paths, title)
	videos := 0
	for _, p := range paths {
		if IsVideo(p) {
			videos++
		}
	}
	if videos == 1 && work != nil && strings.EqualFold(work.Type, "movie") {
		for i, p := range ps {
			if IsVideo(paths[i]) && (p.Kind == "other" || p.Kind == "episode") && p.Episode == nil {
				one := 1.0
				ps[i] = ParsedName{Kind: "episode", Episode: &one, Confidence: 0.7}
			}
		}
	}
	return ps
}

// fileMapping: only episodes and specials (sp/op/ed/pv/cm, see MappableKind) are mapped to
// the work; other files are just listed. wk is nil or the work key.
func fileMapping(work *Work, method string, wconf float64, kind string, p ParsedName) (wk any, ep *float64, conf float64, m string) {
	if !MappableKind(kind) {
		return nil, nil, 0, ""
	}
	ep = p.Episode
	if work == nil {
		return nil, ep, 0, ""
	}
	conf, m = wconf, method
	if p.Episode != nil {
		conf = wconf * max(p.Confidence, 0.5)
		m += "+name"
	}
	return work.Key, ep, conf, m
}

// UpsertPending records a torrent whose file list is not known yet (magnet-only feed item
// waiting for qB metadata). An existing row only gets missing fields filled in.
func (s *Store) UpsertPending(ctx context.Context, t TorrentInput) error {
	if t.Infohash == "" {
		return nil
	}
	t.Title = store.Trunc(t.Title, store.MaxTitleBytes)
	t.Team = store.Trunc(t.Team, 100)
	if err := s.upsertTorrentRow(ctx, t, "pending", 0, t.Size, store.Now()); err != nil {
		return err
	}
	return s.refreshFTS(ctx, t.Infohash)
}

// upsertTorrentRow inserts or merges the torrents row. status "pending" never downgrades a
// ready row; trust only ever goes up; non-empty incoming fields win.
func (s *Store) upsertTorrentRow(ctx context.Context, t TorrentInput, status string, files int, size int64, now string) error {
	tags := "[]"
	if len(t.Tags) > 0 {
		if len(t.Tags) > 50 {
			t.Tags = t.Tags[:50]
		}
		b, _ := json.Marshal(t.Tags)
		tags = string(b)
	}
	_, err := s.W.ExecContext(ctx, `INSERT INTO torrents(infohash,seq,title,source,source_id,team,team_id,published_at,size_bytes,file_count,tags,trusted,source_kind,page_url,status,updated_at)
		VALUES(?,(SELECT COALESCE(MAX(seq),0)+1 FROM torrents),?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(infohash) DO UPDATE SET title=CASE WHEN excluded.title!='' AND (title='' OR `+takeSource+`) THEN excluded.title ELSE title END,
		source=CASE WHEN `+takeSource+` THEN excluded.source ELSE source END,
		source_kind=CASE WHEN `+takeSource+` THEN excluded.source_kind ELSE source_kind END,
		source_id=CASE WHEN `+takeSource+` THEN excluded.source_id ELSE source_id END,
		team=CASE WHEN excluded.team!='' THEN excluded.team ELSE team END,
		team_id=CASE WHEN excluded.team_id!='' THEN excluded.team_id ELSE team_id END,
		published_at=CASE WHEN excluded.published_at!='' AND (published_at='' OR excluded.trusted > trusted) THEN excluded.published_at ELSE published_at END,
		size_bytes=CASE WHEN excluded.size_bytes>0 THEN excluded.size_bytes ELSE size_bytes END,
		file_count=CASE WHEN excluded.file_count>0 THEN excluded.file_count ELSE file_count END,
		tags=CASE WHEN excluded.tags!='[]' THEN excluded.tags ELSE tags END,
		trusted=MAX(trusted, excluded.trusted),
		page_url=CASE WHEN excluded.page_url!='' AND page_url='' THEN excluded.page_url ELSE page_url END,
		status=CASE WHEN excluded.status='ready' THEN 'ready' ELSE status END,
		seq=COALESCE(seq, excluded.seq),
		updated_at=excluded.updated_at`,
		t.Infohash, t.Title, t.Source, store.Trunc(t.SourceID, 200), t.Team, store.Trunc(t.TeamID, 64), t.PublishedAt, size, files, tags,
		boolInt(t.Trusted), t.SourceKind, store.Trunc(t.PageURL, 500), status, now)
	return err
}

// takeSource: the incoming source replaces the stored one if the stored one is unknown or
// a user submission, or if the incoming one is a crawl source at least as trusted.
const takeSource = `(excluded.source != '' AND (source IN ('', 'user') OR (excluded.source != 'user' AND excluded.trusted >= trusted)))`

// refreshFTS rewrites the full-text row of a torrent (title + every file path).
func (s *Store) refreshFTS(ctx context.Context, ih string) error {
	var seq int64
	var title string
	if err := s.W.QueryRowContext(ctx, `SELECT seq, title FROM torrents WHERE infohash = ?`, ih).Scan(&seq, &title); err != nil {
		return err
	}
	var files string
	if err := s.W.QueryRowContext(ctx, `SELECT COALESCE(group_concat(path, char(10)), '') FROM torrent_files WHERE infohash = ?`, ih).Scan(&files); err != nil {
		return err
	}
	if _, err := s.W.ExecContext(ctx, `DELETE FROM torrents_fts WHERE rowid = ?`, seq); err != nil {
		return err
	}
	_, err := s.W.ExecContext(ctx, `INSERT INTO torrents_fts(rowid, title, files) VALUES(?,?,?)`, seq, title, files)
	return err
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// resolveWork finds the work: bangumi.moe bangumi tag (via bangumi-data) first,
// then a conservative alias substring match among recent works.
func (s *Store) resolveWork(ctx context.Context, t TorrentInput) (*Work, string, float64) {
	for _, id := range t.MoeTagIDs {
		if w, err := s.WorkByExternal(ctx, "bangumi_moe", id); err == nil {
			return w, "moe_tag", 0.95
		}
	}
	title := strings.ToLower(t.Title)
	if title == "" {
		return nil, "", 0
	}
	idx, err := s.aliasIndex(ctx)
	if err != nil {
		return nil, "", 0
	}
	// idx is sorted by alias length descending: the first hit is the longest match
	for _, e := range idx {
		if strings.Contains(title, e.alias) {
			return e.work, "alias", 0.7
		}
	}
	return nil, "", 0
}

type aliasEntry struct {
	alias string // lower-cased
	work  *Work
}

const aliasIndexTTL = 10 * time.Minute

// aliasIndex returns (cached for 10 min) all aliases of works begun in the last 18 months.
func (s *Store) aliasIndex(ctx context.Context) ([]aliasEntry, error) {
	s.aliasMu.Lock()
	defer s.aliasMu.Unlock()
	if s.aliases != nil && time.Since(s.aliasAt) < aliasIndexTTL {
		return s.aliases, nil
	}
	from := time.Now().AddDate(-1, -6, 0).UTC().Format(time.RFC3339)
	to := time.Now().AddDate(0, 1, 0).UTC().Format(time.RFC3339)
	works, err := s.WorksBetween(ctx, from, to)
	if err != nil {
		return nil, err
	}
	idx := []aliasEntry{}
	for _, w := range works {
		seen := map[string]bool{}
		for _, a := range append([]string{w.Title, w.TitleCN}, w.Aliases...) {
			a = strings.ToLower(strings.TrimSpace(a))
			if utf8.RuneCountInString(a) < 4 || seen[a] {
				continue
			}
			seen[a] = true
			idx = append(idx, aliasEntry{a, w})
		}
	}
	sort.SliceStable(idx, func(i, j int) bool { return len(idx[i].alias) > len(idx[j].alias) })
	s.aliases, s.aliasAt = idx, time.Now()
	return idx, nil
}
