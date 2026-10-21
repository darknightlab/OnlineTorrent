// Package meta is the mapping service: works (anime/series) with external ids, and a
// file-level map (infohash, file_index) -> (work, episode). It owns its own SQLite file
// so it can be deployed standalone (cmd/metad) or mounted inside otd.
package meta

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

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
	kind       TEXT NOT NULL DEFAULT 'other',  -- episode|sp|op|ed|extra|other (+ pv|cm since migration 5)
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
`,
	// 2: stable numeric subject id (/subject/id/{id}); existing rows take their rowid
	`
ALTER TABLE works ADD COLUMN id INTEGER;
UPDATE works SET id = rowid;
CREATE UNIQUE INDEX works_id ON works(id);
`,
	// 3: torrents become the local catalogue (crawled from sources, no live bangumi.moe
	// proxying): listing fields, trust flag, pending state (metadata not known yet), a stable
	// integer seq for the FTS index; trigram full-text search over torrents and works.
	`
ALTER TABLE torrents ADD COLUMN seq INTEGER;
ALTER TABLE torrents ADD COLUMN size_bytes INTEGER NOT NULL DEFAULT 0;
ALTER TABLE torrents ADD COLUMN file_count INTEGER NOT NULL DEFAULT 0;
ALTER TABLE torrents ADD COLUMN team_id TEXT NOT NULL DEFAULT '';
ALTER TABLE torrents ADD COLUMN tags TEXT NOT NULL DEFAULT '[]';        -- JSON [{id,name}] (bangumi.moe tags; ids also hint matching)
ALTER TABLE torrents ADD COLUMN trusted INTEGER NOT NULL DEFAULT 0;
ALTER TABLE torrents ADD COLUMN source_kind TEXT NOT NULL DEFAULT '';  -- bangumi_moe|rss|user
ALTER TABLE torrents ADD COLUMN page_url TEXT NOT NULL DEFAULT '';
ALTER TABLE torrents ADD COLUMN status TEXT NOT NULL DEFAULT 'ready';  -- ready (files known) | pending
UPDATE torrents SET seq = rowid;
UPDATE torrents SET trusted = 1, source_kind = 'bangumi_moe' WHERE source = 'bangumi.moe';
UPDATE torrents SET source_kind = 'user' WHERE source = 'user';
UPDATE torrents SET file_count = (SELECT COUNT(*) FROM torrent_files f WHERE f.infohash = torrents.infohash),
	size_bytes = (SELECT COALESCE(SUM(size_bytes),0) FROM torrent_files f WHERE f.infohash = torrents.infohash);
CREATE UNIQUE INDEX torrents_seq ON torrents(seq);
CREATE INDEX torrents_published ON torrents(published_at);
CREATE INDEX torrents_source_id ON torrents(source_id) WHERE source_id != '';
CREATE INDEX torrent_files_work ON torrent_files(work_key, infohash);

CREATE VIRTUAL TABLE torrents_fts USING fts5(title, files, tokenize='trigram');
INSERT INTO torrents_fts(rowid, title, files)
	SELECT t.seq, t.title, COALESCE((SELECT group_concat(path, char(10)) FROM torrent_files f WHERE f.infohash = t.infohash), '') FROM torrents t;

CREATE VIRTUAL TABLE works_fts USING fts5(title, title_cn, aliases, content='works', content_rowid='id', tokenize='trigram');
INSERT INTO works_fts(works_fts) VALUES('rebuild');
CREATE TRIGGER works_fts_ai AFTER INSERT ON works BEGIN
	INSERT INTO works_fts(rowid, title, title_cn, aliases) VALUES (new.id, new.title, new.title_cn, new.aliases);
END;
CREATE TRIGGER works_fts_ad AFTER DELETE ON works BEGIN
	INSERT INTO works_fts(works_fts, rowid, title, title_cn, aliases) VALUES ('delete', old.id, old.title, old.title_cn, old.aliases);
END;
CREATE TRIGGER works_fts_au AFTER UPDATE OF id, title, title_cn, aliases ON works BEGIN
	INSERT INTO works_fts(works_fts, rowid, title, title_cn, aliases) VALUES ('delete', old.id, old.title, old.title_cn, old.aliases);
	INSERT INTO works_fts(rowid, title, title_cn, aliases) VALUES (new.id, new.title, new.title_cn, new.aliases);
END;
`,
	// every crawled torrent counts as catalogue (trusted: exempt from the push size limit)
	`
UPDATE torrents SET trusted = 1 WHERE source_kind IN ('rss', 'bangumi_moe');
`,
	// 5: specials keep their number and are mapped (kind sp|op|ed|pv|cm + episode = the
	// special's number). meta_state holds one-off jobs; the re-classification of stored
	// files runs from Go (ReclassifyOnce) because it needs the file-name parser.
	`
CREATE TABLE meta_state (key TEXT PRIMARY KEY, value TEXT NOT NULL);
INSERT INTO meta_state(key, value) VALUES ('reclassify_specials_v1', 'pending');
CREATE INDEX torrent_files_key ON torrent_files(work_key, kind, episode);
`,
	// 6: bangumi Archive (weekly bgm.tv wiki dump). Aliases are kept per source and merged
	// into works.aliases (bangumi-data and the archive no longer overwrite each other);
	// official episodes (type 0 main, 1 SP, 2 OP, 3 ED, 4 PV, 5 MAD, 6 other) and
	// subject relations (前传/续集/…, for series) of anime subjects.
	`
ALTER TABLE works ADD COLUMN bd_aliases TEXT NOT NULL DEFAULT '[]';
ALTER TABLE works ADD COLUMN bgm_aliases TEXT NOT NULL DEFAULT '[]';
ALTER TABLE works ADD COLUMN nsfw INTEGER NOT NULL DEFAULT 0;         -- bgm subject nsfw flag (stored as-is, not used by policy yet)
ALTER TABLE works ADD COLUMN platform INTEGER NOT NULL DEFAULT 0;     -- bgm platform: 1 TV, 2 OVA, 3 movie, 4 short, 5 WEB, 2006 anime comic
ALTER TABLE works ADD COLUMN tags TEXT NOT NULL DEFAULT '[]';         -- JSON []string, top bgm user tags
ALTER TABLE works ADD COLUMN meta_tags TEXT NOT NULL DEFAULT '[]';    -- JSON []string, bgm public (wiki-managed) tags
ALTER TABLE works ADD COLUMN score REAL NOT NULL DEFAULT 0;
ALTER TABLE works ADD COLUMN rank INTEGER NOT NULL DEFAULT 0;
ALTER TABLE works ADD COLUMN eps INTEGER NOT NULL DEFAULT 0;          -- infobox 话数
ALTER TABLE works ADD COLUMN bgm_synced TEXT NOT NULL DEFAULT '';     -- archive dump name that last updated this row
UPDATE works SET bd_aliases = aliases WHERE source = 'bangumi-data';

CREATE TABLE episodes (
	bgm_id    INTEGER PRIMARY KEY,
	work_key  TEXT NOT NULL,
	type      INTEGER NOT NULL,   -- 0 main, 1 SP, 2 OP, 3 ED, 4 PV, 5 MAD, 6 other
	sort      REAL NOT NULL,
	name      TEXT NOT NULL DEFAULT '',
	name_cn   TEXT NOT NULL DEFAULT '',
	airdate   TEXT NOT NULL DEFAULT '',
	duration  TEXT NOT NULL DEFAULT '',
	disc      INTEGER NOT NULL DEFAULT 0,
	rev       TEXT NOT NULL DEFAULT ''
);
CREATE INDEX episodes_work ON episodes(work_key, type, sort);

CREATE TABLE subject_relations (
	subject_id    INTEGER NOT NULL,   -- bgm ids (both anime)
	related_id    INTEGER NOT NULL,
	relation_type INTEGER NOT NULL,   -- bangumi/common subject_relations: 2 前传, 3 续集, 4 总集篇, 5 全集, 6 番外篇…
	ord           INTEGER NOT NULL DEFAULT 0,
	rev           TEXT NOT NULL DEFAULT '',
	PRIMARY KEY (subject_id, related_id, relation_type)
);
CREATE INDEX subject_relations_related ON subject_relations(related_id);
`,
	// 7: raw .torrent files (bencoded metainfo whose v1 infohash matches), served over HTTP
	// so browsers get the metadata without a WebRTC peer (magnet xs=). Kept independently of
	// qB (a torrent deleted there stays playable via webtorrent peers + our metadata).
	`
CREATE TABLE torrent_metainfo (
	infohash   TEXT PRIMARY KEY,
	data       BLOB NOT NULL,
	source     TEXT NOT NULL DEFAULT '',   -- crawl (feed .torrent) | qb (torrents/export) | bangumi.moe
	created_at TEXT NOT NULL
);
`,
	// 8: swarm statistics per torrent (internal/swarm: tracker scrapes, Nyaa RSS initial
	// values, qB progress) and the announce list of stored .torrent files (NULL = not yet
	// extracted; filled lazily).
	`
CREATE TABLE swarm_stats (
	infohash        TEXT PRIMARY KEY,
	bt_seeders      INTEGER,               -- NULL = unknown; MAX over udp/http trackers
	bt_leechers     INTEGER,
	bt_completed    INTEGER,
	wt_seeders      INTEGER,               -- MAX over wss (WebTorrent) trackers
	wt_leechers     INTEGER,
	server_complete INTEGER NOT NULL DEFAULT 0,
	server_progress REAL NOT NULL DEFAULT 0,
	source          TEXT NOT NULL DEFAULT '',  -- scrape | rss (initial value from the feed)
	checked_at      TEXT NOT NULL DEFAULT '',  -- when the counts were observed
	scraped_at      TEXT NOT NULL DEFAULT '',  -- last scrape attempt ('' = never scraped)
	error           TEXT NOT NULL DEFAULT ''
);
CREATE INDEX swarm_stats_scraped ON swarm_stats(scraped_at);
ALTER TABLE torrent_metainfo ADD COLUMN announce TEXT;   -- JSON []string
`,
	// 9: decision model (Cloudflare Clef, clef.go + internal/clef): one job per torrent whose
	// files the rules could not map with certainty, and one decision row per file (audit,
	// review queue, revert). Mappings it applies have torrent_files.method = 'clef'.
	`
CREATE TABLE clef_jobs (
	infohash   TEXT PRIMARY KEY,
	status     TEXT NOT NULL DEFAULT 'pending',  -- pending | done | failed | expired
	attempts   INTEGER NOT NULL DEFAULT 0,
	requests   INTEGER NOT NULL DEFAULT 0,       -- API requests made for it
	next_at    TEXT NOT NULL DEFAULT '',
	last_error TEXT NOT NULL DEFAULT '',
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL
);
CREATE INDEX clef_jobs_next ON clef_jobs(status, next_at);
CREATE TABLE clef_decisions (
	id          INTEGER PRIMARY KEY,
	infohash    TEXT NOT NULL,
	file_index  INTEGER NOT NULL,
	path        TEXT NOT NULL DEFAULT '',
	work_key    TEXT,                        -- chosen subject (NULL: none)
	kind        TEXT NOT NULL DEFAULT '',    -- torrent_files.kind
	episode     REAL,                        -- number as written in the file
	episode_key TEXT NOT NULL DEFAULT '',    -- canonical key (split-cour shift applied)
	score       REAL NOT NULL DEFAULT 0,     -- decision metric (min over the file's questions)
	margin      REAL NOT NULL DEFAULT 0,     -- min top1 − top2 probability
	confidence  REAL NOT NULL DEFAULT 0,     -- min Clef confidence
	answers     TEXT NOT NULL DEFAULT '{}',  -- JSON: per question choice + top probabilities
	model       TEXT NOT NULL DEFAULT '',
	status      TEXT NOT NULL,               -- applied | pending | rejected | discarded
	reason      TEXT NOT NULL DEFAULT '',
	created_at  TEXT NOT NULL,
	decided_at  TEXT NOT NULL DEFAULT '',
	decided_by  TEXT NOT NULL DEFAULT ''
);
CREATE INDEX clef_decisions_status ON clef_decisions(status, id);
CREATE INDEX clef_decisions_ih ON clef_decisions(infohash, file_index);
`,
	// 10: files of a job to decide again although mapped (anomalous mappings: the episode is
	// not in the work's official list), comma-separated file indexes
	`
ALTER TABLE clef_jobs ADD COLUMN files TEXT NOT NULL DEFAULT '';
`,
	// 11: release time of user submissions (submitted_at / submitted_by: set once when a
	// user first submitted the torrent; never for crawled ones; submitted_by = app user id,
	// no FK — users live in otd.db) and review decisions from the rules (source rule: a
	// certain rule mapping of a torrent without any release time goes to human review)
	`
ALTER TABLE torrents ADD COLUMN submitted_at TEXT NOT NULL DEFAULT '';
ALTER TABLE torrents ADD COLUMN submitted_by INTEGER;
ALTER TABLE clef_decisions ADD COLUMN source TEXT NOT NULL DEFAULT 'model';
`,
	// 12: a forced decision-model job (admin 重新判断): every video file is decided again and
	// may replace automatic mappings; automatic jobs only take the uncertain files
	`
ALTER TABLE clef_jobs ADD COLUMN force INTEGER NOT NULL DEFAULT 0;
`,
	// 13: danmaku (danmaku.go): the streaming-site ids of every bangumi-data item (all years,
	// unlike the works import) used to map an episode to a platform episode, and the stored
	// comments per (work, episode, platform). A stored set is never replaced.
	`
CREATE TABLE bd_sites (
	bgm_id INTEGER NOT NULL,   -- bangumi subject id
	site   TEXT NOT NULL,      -- bangumi-data site: bilibili | bilibili_hk_mo_tw | bilibili_hk_mo | bilibili_tw | gamer | gamer_hk
	ext_id TEXT NOT NULL,      -- bilibili media id / Bahamut acgDetail id
	PRIMARY KEY (bgm_id, site, ext_id)
);
CREATE INDEX bd_sites_ext ON bd_sites(ext_id);
CREATE TABLE danmaku (
	work_key   TEXT NOT NULL,
	episode    TEXT NOT NULL,   -- episode key (official numbering, e.g. "29")
	platform   TEXT NOT NULL,   -- bilibili | bahamut
	ref        TEXT NOT NULL,   -- platform episode: bilibili ep id / Bahamut video sn
	count      INTEGER NOT NULL,
	data       BLOB NOT NULL,   -- gzip JSON [[time seconds, mode, colour, text], ...] (mode: 1 scroll, 4 bottom, 5 top)
	fetched_at TEXT NOT NULL,
	PRIMARY KEY (work_key, episode, platform)
);
`,
	// 14: danmaku sent by users (danmaku.go), per episode like the collected ones; merged into
	// GET …/danmaku. user_id = app user id (otd.db, no FK).
	`
CREATE TABLE user_danmaku (
	id         INTEGER PRIMARY KEY,
	work_key   TEXT NOT NULL,
	episode    TEXT NOT NULL,   -- episode key (canonical)
	user_id    INTEGER NOT NULL,
	time       REAL NOT NULL,   -- seconds from the start of the episode
	mode       INTEGER NOT NULL, -- 1 scroll, 4 bottom, 5 top
	color      INTEGER NOT NULL, -- 0xRRGGBB
	text       TEXT NOT NULL,
	created_at TEXT NOT NULL
);
CREATE INDEX user_danmaku_episode ON user_danmaku(work_key, episode);
CREATE INDEX user_danmaku_user ON user_danmaku(user_id, created_at);
`,
	// 15: names from manami-project's anime-offline-database (manami.go; ODbL), linked to
	// works by shared external ids only; used for matching, kept apart from works.aliases
	`
CREATE TABLE manami_names (
	work_key TEXT NOT NULL,
	name     TEXT NOT NULL,
	entry    TEXT NOT NULL,   -- the database entry (its first source URL)
	PRIMARY KEY (work_key, name)
);
`}

// Store is the meta database.
type Store struct {
	W, R *sql.DB

	aliasMu sync.Mutex
	aliases *aliasIndex
	aliasAt time.Time

	candMu sync.Mutex // decision-model candidate retrieval index (retrieve.go)
	cand   *candIndex
}

// Open opens meta.db.
func Open(path string) (*Store, error) {
	w, r, err := store.OpenDB(path, migrations)
	if err != nil {
		return nil, err
	}
	st := &Store{W: w, R: r}
	st.resetEpisodeOffsets() // the split-cour cache is per process; another store (tests) may have filled it
	return st, nil
}

func (s *Store) Close() { s.R.Close(); s.W.Close() }

// Work is an anime/series entry.
type Work struct {
	ID        int64             `json:"id"` // stable numeric subject id
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
	NSFW      bool              `json:"nsfw"`
	Platform  int               `json:"platform,omitempty"` // bgm platform code
	Tags      []string          `json:"tags"`
	MetaTags  []string          `json:"meta_tags,omitempty"`
	Score     float64           `json:"score,omitempty"`
	Rank      int               `json:"rank,omitempty"`
	Eps       int               `json:"eps,omitempty"` // planned episode count (bgm infobox 话数)
}

// ScopeFacts implements store.ScopeTarget (content scope: tags ∪ meta_tags).
func (w *Work) ScopeFacts() (tags, metaTags []string) { return w.Tags, w.MetaTags }

const workCols = `COALESCE(id,0),key,title,title_cn,aliases,type,begin_at,end_at,broadcast,cover,summary,official,nsfw,platform,tags,meta_tags,score,rank,eps`

func scanWork(sc interface{ Scan(...any) error }) (*Work, error) {
	var w Work
	var aliases, tags, metaTags string
	var nsfw int
	if err := sc.Scan(&w.ID, &w.Key, &w.Title, &w.TitleCN, &aliases, &w.Type, &w.Begin, &w.End, &w.Broadcast, &w.Cover, &w.Summary, &w.Official,
		&nsfw, &w.Platform, &tags, &metaTags, &w.Score, &w.Rank, &w.Eps); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, store.ErrNotFound
		}
		return nil, err
	}
	_ = json.Unmarshal([]byte(aliases), &w.Aliases)
	_ = json.Unmarshal([]byte(tags), &w.Tags)
	_ = json.Unmarshal([]byte(metaTags), &w.MetaTags)
	if w.Tags == nil {
		w.Tags = []string{}
	}
	w.NSFW = nsfw != 0
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

// GetWorkByID loads a work by its numeric subject id.
func (s *Store) GetWorkByID(ctx context.Context, id int64) (*Work, error) {
	w, err := scanWork(s.R.QueryRowContext(ctx, `SELECT `+workCols+` FROM works WHERE id = ?`, id))
	if err != nil {
		return nil, err
	}
	return w, s.fillExternal(ctx, []*Work{w})
}

// ResolveSubject finds a work by provider and id: provider "id" is our numeric id,
// otherwise an external provider (bgm, mal, anidb, anilist, tmdb, bangumi_moe, …).
func (s *Store) ResolveSubject(ctx context.Context, provider, id string) (*Work, error) {
	if provider == "id" {
		n, err := strconv.ParseInt(id, 10, 64)
		if err != nil {
			return nil, store.ErrNotFound
		}
		return s.GetWorkByID(ctx, n)
	}
	return s.WorkByExternal(ctx, provider, id)
}

// TorrentSource returns where a mapped torrent came from (e.g. "bangumi.moe", moe _id).
func (s *Store) TorrentSource(ctx context.Context, ih string) (source, sourceID string, ok bool) {
	err := s.R.QueryRowContext(ctx, `SELECT source, source_id FROM torrents WHERE infohash = ?`, ih).Scan(&source, &sourceID)
	return source, sourceID, err == nil
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

// WorksForSchedule returns candidate works for the airing timeline at now: begun within
// the last 200 days (up to 21 days ahead), plus older ones (≤ 2 years) that have no end
// date or ended within the last 10 days (long-running / multi-cour shows).
func (s *Store) WorksForSchedule(ctx context.Context, now time.Time) ([]*Work, error) {
	f := func(t time.Time) string { return t.UTC().Format(time.RFC3339) }
	from := f(now.AddDate(0, 0, -200))
	to := f(now.AddDate(0, 0, 21))
	oldest := f(now.AddDate(-2, 0, 0))
	ended := f(now.AddDate(0, 0, -10))
	// only bangumi-data works: it is the broadcast calendar (weekday/time); archive-only
	// subjects have dates but no broadcast slot and would flood the timeline
	rows, err := s.R.QueryContext(ctx, `SELECT `+workCols+` FROM works WHERE source = 'bangumi-data' AND begin_at < ? AND (begin_at >= ? OR (begin_at >= ? AND (end_at = '' OR end_at >= ?))) ORDER BY begin_at`,
		to, from, oldest, ended)
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

// TorrentInput describes a torrent for matching / the local catalogue.
type TorrentInput struct {
	Infohash    string
	Title       string
	Source      string // crawl source name ("bangumi.moe", "Nyaa", …) or "user"
	SourceKind  string // bangumi_moe|rss|user
	SourceID    string // id at the source (bangumi.moe _id, feed guid)
	Trusted     bool   // from a crawl source (exempt from the push size limit)
	Team        string
	TeamID      string
	PublishedAt string
	// SubmittedAt / SubmittedBy: first user submission (user torrents only; stored when the
	// row is created, never overwritten)
	SubmittedAt string
	SubmittedBy *int64
	PageURL     string
	Size        int64
	Tags        []TagRef // display tags (bangumi.moe)
	MoeTagIDs   []string // bangumi.moe tag ids (any type; bangumi ones resolve to works)
	Files       []FileInput
}

// TagRef is a display tag.
type TagRef struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// FileMap is one mapped file.
type FileMap struct {
	Index      int      `json:"index"`
	Path       string   `json:"path"`
	Size       int64    `json:"size_bytes"`
	Kind       string   `json:"kind"`
	Work       *WorkRef `json:"work"`
	Episode    *float64 `json:"episode"`               // main episode number, or the special's number (kind sp|op|ed|pv|cm)
	EpisodeKey string   `json:"episode_key,omitempty"` // "12", "12.5", "sp1", "ncop1"… (only for mappable kinds with a number)
	Confidence float64  `json:"confidence"`
	Status     string   `json:"status"`
	Method     string   `json:"method"`
}

// WorkRef is a compact work reference.
type WorkRef struct {
	ID       int64             `json:"id"`
	Key      string            `json:"key"`
	Title    string            `json:"title"`
	TitleCN  string            `json:"title_cn"`
	External map[string]string `json:"external,omitempty"`

	Tags     []string `json:"-"` // content scope only
	MetaTags []string `json:"-"`
}

// ScopeFacts implements store.ScopeTarget.
func (w *WorkRef) ScopeFacts() (tags, metaTags []string) { return w.Tags, w.MetaTags }

// TorrentMap is the API view of a torrent's files.
type TorrentMap struct {
	Infohash string    `json:"infohash"`
	Title    string    `json:"title"`
	Status   string    `json:"status"` // ready | pending (file list not known yet)
	Files    []FileMap `json:"files"`
}

// GetTorrentMap returns file mappings for a torrent.
func (s *Store) GetTorrentMap(ctx context.Context, ih string) (*TorrentMap, error) {
	tm := &TorrentMap{Infohash: ih, Files: []FileMap{}}
	if err := s.R.QueryRowContext(ctx, `SELECT title, status FROM torrents WHERE infohash = ?`, ih).Scan(&tm.Title, &tm.Status); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, store.ErrNotFound
		}
		return nil, err
	}
	rows, err := s.R.QueryContext(ctx, `SELECT f.file_index,f.path,f.size_bytes,f.kind,f.work_key,COALESCE(w.id,0),COALESCE(w.title,''),COALESCE(w.title_cn,''),COALESCE(w.tags,'[]'),COALESCE(w.meta_tags,'[]'),f.episode,f.confidence,f.status,f.method
		FROM torrent_files f LEFT JOIN works w ON w.key = f.work_key WHERE f.infohash = ? ORDER BY f.file_index`, ih)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var works []*Work
	for rows.Next() {
		var f FileMap
		var wk sql.NullString
		var wt, wcn, wtags, wmeta string
		var wid int64
		var ep sql.NullFloat64
		if err := rows.Scan(&f.Index, &f.Path, &f.Size, &f.Kind, &wk, &wid, &wt, &wcn, &wtags, &wmeta, &ep, &f.Confidence, &f.Status, &f.Method); err != nil {
			return nil, err
		}
		if wk.Valid {
			f.Work = &WorkRef{ID: wid, Key: wk.String, Title: wt, TitleCN: wcn}
			_ = json.Unmarshal([]byte(wtags), &f.Work.Tags)
			_ = json.Unmarshal([]byte(wmeta), &f.Work.MetaTags)
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
	rows.Close() // CanonicalKey may query (offset cache miss): don't hold two connections
	for i := range tm.Files {
		f := &tm.Files[i]
		if k, ok := KeyOfFile(f.Kind, f.Episode); ok {
			if f.Work != nil {
				k = s.CanonicalKey(ctx, f.Work.Key, k)
			}
			f.EpisodeKey = k.String()
		}
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

// HideWorks drops the subject and episode of every file mapped to a subject outside the
// content scope (the file itself stays listed, like an unmapped file).
func (tm *TorrentMap) HideWorks(scope *store.ScopePlan) {
	if !scope.Restricted() {
		return
	}
	for i := range tm.Files {
		f := &tm.Files[i]
		if f.Work != nil && !scope.Visible(f.Work) {
			f.Work, f.Episode, f.EpisodeKey = nil, nil, ""
			f.Confidence, f.Method = 0, ""
		}
	}
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
	Source      string  `json:"source"`
	Confidence  float64 `json:"confidence"`
	Trusted     bool    `json:"-"`
}

// EpisodeFiles lists files mapped to work + episode key (main episode or special).
func (s *Store) EpisodeFiles(ctx context.Context, workKey string, key EpisodeKey) ([]EpisodeFile, error) {
	nums := s.fileNumbers(ctx, workKey, key) // split-cour: also the unshifted file number
	rows, err := s.R.QueryContext(ctx, `SELECT f.infohash,f.file_index,f.path,f.size_bytes,t.title,t.team,t.published_at,t.source_id,t.source,f.confidence,t.trusted
		FROM torrent_files f JOIN torrents t ON t.infohash = f.infohash
		WHERE f.work_key = ? AND f.kind = ? AND f.episode IN (?, ?) AND f.status != 'rejected' ORDER BY t.published_at DESC LIMIT 200`, workKey, key.FileKind(), nums[0], nums[len(nums)-1])
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []EpisodeFile{}
	for rows.Next() {
		var e EpisodeFile
		if err := rows.Scan(&e.Infohash, &e.Index, &e.Path, &e.Size, &e.Title, &e.Team, &e.PublishedAt, &e.SourceID, &e.Source, &e.Confidence, &e.Trusted); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// EpisodeCount is one episode key of a work with its number of mapped files.
type EpisodeCount struct {
	Key   EpisodeKey
	Files int
	Title string // fallback title derived from a mapped special file path
}

// EpisodeKeys lists every episode key (main episodes and specials) that has mapped files
// for the work, with the number of files, sorted by LessKey.
func (s *Store) EpisodeKeys(ctx context.Context, workKey string) ([]EpisodeCount, error) {
	return s.EpisodeKeysWhere(ctx, workKey, nil)
}

// EpisodeKeysWhere is EpisodeKeys counting only the files keep accepts (nil keep: all,
// same as EpisodeKeys). The file carries the fields policy decisions need (infohash, path,
// torrent title, team, trusted).
func (s *Store) EpisodeKeysWhere(ctx context.Context, workKey string, keep func(EpisodeFile) bool) ([]EpisodeCount, error) {
	s.episodeOffset(ctx, workKey) // warm the cache before holding a rows cursor
	rows, err := s.R.QueryContext(ctx, `SELECT f.kind, f.episode, f.infohash, f.file_index, f.path, f.size_bytes, t.title, t.team, t.trusted
		FROM torrent_files f JOIN torrents t ON t.infohash = f.infohash
		WHERE f.work_key = ? AND f.episode IS NOT NULL AND f.kind IN ('episode','sp','op','ed','pv','cm') AND f.status != 'rejected'`, workKey)
	if err != nil {
		return nil, err
	}
	type fileKey struct {
		kind string
		ep   float64
	}
	counts := map[fileKey]int{}
	paths := map[fileKey]string{}
	var order []fileKey
	for rows.Next() {
		var k fileKey
		var f EpisodeFile
		if err := rows.Scan(&k.kind, &k.ep, &f.Infohash, &f.Index, &f.Path, &f.Size, &f.Title, &f.Team, &f.Trusted); err != nil {
			rows.Close()
			return nil, err
		}
		if keep != nil && !keep(f) {
			continue
		}
		if _, ok := counts[k]; !ok {
			order = append(order, k)
			paths[k] = f.Path
		}
		counts[k]++
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	var workNames []string
	out := []EpisodeCount{}
	idx := map[EpisodeKey]int{}
	for _, fk := range order {
		ep := fk.ep
		k, ok := KeyOfFile(fk.kind, &ep)
		if !ok {
			continue
		}
		k = s.CanonicalKey(ctx, workKey, k)
		if i, dup := idx[k]; dup {
			out[i].Files += counts[fk]
			if out[i].Title == "" && IsSpecialKind(k.Kind) {
				if workNames == nil {
					workNames = s.WorkNames(ctx, workKey)
				}
				out[i].Title = DeriveSpecialTitle(paths[fk], workNames)
			}
			continue
		}
		title := ""
		if IsSpecialKind(k.Kind) {
			if workNames == nil {
				workNames = s.WorkNames(ctx, workKey)
			}
			title = DeriveSpecialTitle(paths[fk], workNames)
		}
		idx[k] = len(out)
		out = append(out, EpisodeCount{Key: k, Files: counts[fk], Title: title})
	}
	sort.Slice(out, func(i, j int) bool { return LessKey(out[i].Key, out[j].Key) })
	return out, nil
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
	SubjectID *int64   `json:"subject_id"` // preferred; resolved to WorkKey
	WorkKey   *string  `json:"work_key"`
	Episode   *float64 `json:"episode"`
	Kind      string   `json:"kind"`        // episode|sp|op|ed|pv|cm|extra|other (default episode)
	Key       string   `json:"episode_key"` // alternative to kind+episode: "12.5", "sp1", "ncop1"…
	Status    string   `json:"status"`
}

var fileKinds = map[string]bool{"episode": true, "sp": true, "op": true, "ed": true, "pv": true, "cm": true, "extra": true, "other": true}

func (s *Store) CorrectFile(ctx context.Context, ih string, idx int, p SetFile, by string) error {
	if p.Status == "" {
		p.Status = "verified"
	}
	switch p.Status {
	case "auto", "verified", "rejected":
	default:
		return errors.New("invalid status")
	}
	if p.Key != "" {
		k, ok := ParseEpisodeKey(p.Key)
		if !ok {
			return errors.New("invalid episode_key")
		}
		p.Kind, p.Episode = k.FileKind(), &k.Number
	}
	if p.Kind == "" {
		p.Kind = "episode"
	}
	if !fileKinds[p.Kind] {
		return errors.New("invalid kind")
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
