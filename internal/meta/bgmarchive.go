package meta

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	wiki "github.com/bangumi/wiki-parser-go"

	"github.com/darknightlab/onlinetorrent/internal/store"
)

// bangumi Archive: the official weekly dump of bgm.tv wiki data
// (https://github.com/bangumi/Archive, updated Wednesdays ~05:00 Asia/Shanghai).
// Only subject.jsonlines (anime only), episode.jsonlines and subject-relations.jsonlines are
// read, streamed over HTTP Range requests (see bgmarchive_zip.go).

// archiveLatestURL / archiveURLPrefix are variables so tests can point at a local server.
var (
	archiveLatestURL = "https://raw.githubusercontent.com/bangumi/Archive/master/aux/latest.json"
	archiveURLPrefix = "https://github.com/bangumi/Archive/"
)

const stateArchive = "bgm_archive"

// ArchiveStatus is the persisted result of the last archive sync (meta_state).
type ArchiveStatus struct {
	Dump        string  `json:"dump"`      // e.g. dump-2026-10-13.210353Z.zip
	DumpAt      string  `json:"dump_at"`   // upload time of the dump
	Digest      string  `json:"digest"`    // sha256 from latest.json
	SyncedAt    string  `json:"synced_at"` // last successful sync
	Subjects    int     `json:"subjects"`  // anime subjects imported
	Episodes    int     `json:"episodes"`
	Relations   int     `json:"relations"`
	DurationSec float64 `json:"duration_sec"`
	CheckedAt   string  `json:"checked_at"` // last check of latest.json
	Error       string  `json:"error,omitempty"`
	ErrorAt     string  `json:"error_at,omitempty"`
	Running     bool    `json:"running"`
	Phase       string  `json:"phase,omitempty"` // subjects|episodes|relations while running
}

type archiveLatest struct {
	URL       string `json:"browser_download_url"`
	Name      string `json:"name"`
	Size      int64  `json:"size"`
	Digest    string `json:"digest"`
	CreatedAt string `json:"created_at"`
}

var (
	archiveMu      sync.Mutex // one sync at a time
	archiveRunning sync.Map   // *Store → *ArchiveStatus (live progress)
)

// ErrArchiveRunning is returned when a sync is already in progress.
var ErrArchiveRunning = errors.New("bangumi archive sync already running")

// ArchiveState returns the last sync status (with live progress while running).
func (s *Store) ArchiveState(ctx context.Context) ArchiveStatus {
	if v, ok := archiveRunning.Load(s); ok {
		return *v.(*ArchiveStatus)
	}
	var st ArchiveStatus
	var raw string
	if err := s.R.QueryRowContext(ctx, `SELECT value FROM meta_state WHERE key = ?`, stateArchive).Scan(&raw); err == nil {
		_ = json.Unmarshal([]byte(raw), &st)
	}
	return st
}

func (s *Store) saveArchiveState(ctx context.Context, st ArchiveStatus) {
	st.Running, st.Phase = false, ""
	b, _ := json.Marshal(st)
	_, _ = s.W.ExecContext(ctx, `INSERT INTO meta_state(key, value) VALUES(?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value`, stateArchive, string(b))
}

// SyncArchive imports the latest bangumi Archive dump unless its digest equals the last
// imported one (force re-imports). Returns the new status; skipped=true when unchanged.
func (s *Store) SyncArchive(ctx context.Context, hc *http.Client, ua string, force bool) (st ArchiveStatus, skipped bool, err error) {
	if !archiveMu.TryLock() {
		return s.ArchiveState(ctx), false, ErrArchiveRunning
	}
	defer archiveMu.Unlock()
	st = s.ArchiveState(ctx)
	st.CheckedAt = store.Now()
	defer func() {
		archiveRunning.Delete(s)
		if err != nil {
			st.Error, st.ErrorAt = err.Error(), store.Now()
		}
		s.saveArchiveState(context.WithoutCancel(ctx), st)
	}()

	latest, err := fetchArchiveLatest(ctx, hc, ua)
	if err != nil {
		return st, false, err
	}
	if !force && latest.Digest != "" && latest.Digest == st.Digest && st.Error == "" {
		return st, true, nil
	}
	started := time.Now()
	live := st
	live.Running = true
	archiveRunning.Store(s, &live)
	setPhase := func(p string) { live.Phase = p }

	z := &rangeZip{hc: hc, url: latest.URL, size: latest.Size, ua: ua}
	ents, err := z.entries(ctx)
	if err != nil {
		return st, false, err
	}
	byName := map[string]zipEntry{}
	for _, e := range ents {
		byName[e.Name] = e
	}
	need := func(name string) (zipEntry, error) {
		e, ok := byName[name]
		if !ok {
			return e, fmt.Errorf("archive: %s missing from %s", name, latest.Name)
		}
		return e, nil
	}
	rev := latest.Name

	setPhase("subjects")
	e, err := need("subject.jsonlines")
	if err != nil {
		return st, false, err
	}
	anime, err := s.importArchiveSubjects(ctx, z, e, rev)
	if err != nil {
		return st, false, fmt.Errorf("subjects: %w", err)
	}
	setPhase("episodes")
	if e, err = need("episode.jsonlines"); err != nil {
		return st, false, err
	}
	nEp, err := s.importArchiveEpisodes(ctx, z, e, rev, anime)
	if err != nil {
		return st, false, fmt.Errorf("episodes: %w", err)
	}
	setPhase("relations")
	if e, err = need("subject-relations.jsonlines"); err != nil {
		return st, false, err
	}
	nRel, err := s.importArchiveRelations(ctx, z, e, rev, anime)
	if err != nil {
		return st, false, fmt.Errorf("relations: %w", err)
	}
	s.resetEpisodeOffsets()
	s.resetCandidateIndex()
	st = ArchiveStatus{Dump: latest.Name, DumpAt: latest.CreatedAt, Digest: latest.Digest, SyncedAt: store.Now(), CheckedAt: st.CheckedAt,
		Subjects: len(anime), Episodes: nEp, Relations: nRel, DurationSec: time.Since(started).Round(time.Second).Seconds()}
	return st, false, nil
}

func fetchArchiveLatest(ctx context.Context, hc *http.Client, ua string) (archiveLatest, error) {
	var l archiveLatest
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, archiveLatestURL, nil)
	if err != nil {
		return l, err
	}
	req.Header.Set("User-Agent", ua)
	resp, err := hc.Do(req)
	if err != nil {
		return l, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return l, fmt.Errorf("latest.json: HTTP %d", resp.StatusCode)
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&l); err != nil {
		return l, err
	}
	if l.URL == "" || l.Size <= 0 || !strings.HasPrefix(l.URL, archiveURLPrefix) {
		return l, fmt.Errorf("latest.json: unexpected content %q", l.URL)
	}
	return l, nil
}

// eachLine streams a zip entry line by line (lines can be long: infobox/summary).
func eachLine(ctx context.Context, z *rangeZip, e zipEntry, fn func([]byte) error) error {
	rc, err := z.open(ctx, e)
	if err != nil {
		return err
	}
	defer rc.Close()
	br := bufio.NewReaderSize(rc, 1<<20)
	for {
		line, err := br.ReadBytes('\n')
		if len(line) > 1 {
			if ferr := fn(line); ferr != nil {
				return ferr
			}
		}
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
	}
}

// batcher runs statements in transactions of n rows.
type batcher struct {
	s    *Store
	ctx  context.Context
	n, i int
	tx   *sql.Tx
}

func (b *batcher) exec(q string, args ...any) error {
	if b.tx == nil {
		tx, err := b.s.W.BeginTx(b.ctx, nil)
		if err != nil {
			return err
		}
		b.tx = tx
	}
	if _, err := b.tx.ExecContext(b.ctx, q, args...); err != nil {
		return err
	}
	if b.i++; b.i >= b.n {
		return b.flush()
	}
	return nil
}

func (b *batcher) flush() error {
	if b.tx == nil {
		return nil
	}
	err := b.tx.Commit()
	b.tx, b.i = nil, 0
	return err
}

func (b *batcher) abort() {
	if b.tx != nil {
		_ = b.tx.Rollback()
	}
}

type archiveSubject struct {
	ID       int     `json:"id"`
	Type     int     `json:"type"`
	Name     string  `json:"name"`
	NameCN   string  `json:"name_cn"`
	Infobox  string  `json:"infobox"`
	Platform int     `json:"platform"`
	Summary  string  `json:"summary"`
	NSFW     bool    `json:"nsfw"`
	Date     string  `json:"date"`
	Score    float64 `json:"score"`
	Rank     int     `json:"rank"`
	Tags     []struct {
		Name  string `json:"name"`
		Count int    `json:"count"`
	} `json:"tags"`
	MetaTags []string `json:"meta_tags"`
}

var platformType = map[int]string{1: "tv", 2: "ova", 3: "movie", 4: "movie", 5: "web"}

const archiveWorkSQL = `INSERT INTO works(id,key,title,title_cn,aliases,bgm_aliases,type,begin_at,end_at,summary,official,nsfw,platform,tags,meta_tags,score,rank,eps,source,bgm_synced,updated_at)
	VALUES((SELECT COALESCE(MAX(id),0)+1 FROM works),?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,'bgm',?,?)
	ON CONFLICT(key) DO UPDATE SET
		title = CASE WHEN works.source = 'bangumi-data' THEN works.title ELSE excluded.title END,
		title_cn = CASE WHEN excluded.title_cn != '' THEN excluded.title_cn ELSE works.title_cn END,
		bgm_aliases = excluded.bgm_aliases,
		aliases = (SELECT COALESCE(json_group_array(value), '[]') FROM (
			SELECT value FROM json_each(works.bd_aliases) UNION SELECT value FROM json_each(excluded.bgm_aliases))),
		type = CASE WHEN works.source = 'bangumi-data' AND works.type != '' THEN works.type ELSE excluded.type END,
		begin_at = CASE WHEN works.source = 'bangumi-data' AND works.begin_at != '' THEN works.begin_at ELSE excluded.begin_at END,
		end_at = CASE WHEN works.source = 'bangumi-data' AND works.end_at != '' THEN works.end_at ELSE excluded.end_at END,
		summary = CASE WHEN excluded.summary != '' THEN excluded.summary ELSE works.summary END,
		official = CASE WHEN works.official != '' THEN works.official ELSE excluded.official END,
		nsfw = excluded.nsfw, platform = excluded.platform, tags = excluded.tags, meta_tags = excluded.meta_tags,
		score = excluded.score, rank = excluded.rank, eps = excluded.eps, bgm_synced = excluded.bgm_synced`

func (s *Store) importArchiveSubjects(ctx context.Context, z *rangeZip, e zipEntry, rev string) (map[int]bool, error) {
	anime := map[int]bool{}
	b := &batcher{s: s, ctx: ctx, n: 2000}
	defer b.abort()
	now := store.Now()
	err := eachLine(ctx, z, e, func(line []byte) error {
		var sub archiveSubject
		if err := json.Unmarshal(line, &sub); err != nil {
			return nil // skip malformed lines
		}
		if sub.Type != 2 || sub.ID <= 0 || sub.Name == "" {
			return nil
		}
		anime[sub.ID] = true
		ib := parseArchiveInfobox(sub.Infobox)
		aliases := ib.aliases
		if aliases == nil {
			aliases = []string{}
		}
		al, _ := json.Marshal(aliases)
		tags := []string{}
		for _, t := range sub.Tags {
			if len(tags) >= 12 {
				break
			}
			if t.Name != "" {
				tags = append(tags, store.Trunc(t.Name, 60))
			}
		}
		tj, _ := json.Marshal(tags)
		mt := sub.MetaTags
		if mt == nil {
			mt = []string{}
		}
		mj, _ := json.Marshal(mt)
		begin := archiveDate(sub.Date)
		if begin == "" {
			begin = archiveDate(ib.begin)
		}
		key := "bgm:" + strconv.Itoa(sub.ID)
		if err := b.exec(archiveWorkSQL, key, sub.Name, sub.NameCN, string(al), string(al), platformType[sub.Platform], begin, archiveDate(ib.end),
			store.Trunc(sub.Summary, 8000), store.Trunc(ib.official, 500), boolInt(sub.NSFW), sub.Platform, string(tj), string(mj), sub.Score, sub.Rank, ib.eps, rev, now); err != nil {
			return err
		}
		return b.exec(`INSERT INTO work_ids(provider,ext_id,work_key) VALUES('bgm',?,?) ON CONFLICT(provider,ext_id) DO NOTHING`, strconv.Itoa(sub.ID), key)
	})
	if err != nil {
		return nil, err
	}
	return anime, b.flush()
}

type archiveInfobox struct {
	aliases    []string
	eps        int
	begin, end string
	official   string
}

func parseArchiveInfobox(s string) archiveInfobox {
	var ib archiveInfobox
	if s == "" {
		return ib
	}
	w := wiki.ParseOmitError(s)
	seen := map[string]bool{}
	add := func(v string) {
		v = strings.TrimSpace(v)
		if v != "" && !seen[v] && len(ib.aliases) < 40 {
			seen[v] = true
			ib.aliases = append(ib.aliases, store.Trunc(v, 200))
		}
	}
	for _, f := range w.Fields {
		switch strings.TrimSpace(f.Key) {
		case "别名", "中文名", "英文名", "日文名", "罗马名":
			if f.Array {
				for _, it := range f.Values {
					add(it.Value)
				}
			} else {
				add(f.Value)
			}
		case "话数":
			if n, err := strconv.Atoi(strings.TrimSpace(reDigits.FindString(f.Value))); err == nil && n > 0 && n < 5000 {
				ib.eps = n
			}
		case "放送开始", "上映年度", "上映日", "发售日", "开始":
			if ib.begin == "" {
				ib.begin = f.Value
			}
		case "播放结束", "放送结束", "结束":
			if ib.end == "" {
				ib.end = f.Value
			}
		case "官方网站":
			if strings.HasPrefix(strings.TrimSpace(f.Value), "http") {
				ib.official = strings.TrimSpace(f.Value)
			}
		}
	}
	return ib
}

var (
	reDigits  = regexp.MustCompile(`\d+`)
	reCNDate  = regexp.MustCompile(`(\d{4})\s*[年/.-]\s*(\d{1,2})\s*[月/.-]\s*(\d{1,2})`)
	reCNMonth = regexp.MustCompile(`(\d{4})\s*[年/.-]\s*(\d{1,2})\s*月?`)
)

var jst = time.FixedZone("JST", 9*3600)

// archiveDate converts "2008-04-06" / "2008年4月6日" / "2008年4月" to RFC3339 UTC (midnight
// JST); "" if unparseable.
func archiveDate(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	y, m, d := 0, 0, 1
	if mm := reCNDate.FindStringSubmatch(s); mm != nil {
		y, _ = strconv.Atoi(mm[1])
		m, _ = strconv.Atoi(mm[2])
		d, _ = strconv.Atoi(mm[3])
	} else if mm := reCNMonth.FindStringSubmatch(s); mm != nil {
		y, _ = strconv.Atoi(mm[1])
		m, _ = strconv.Atoi(mm[2])
	}
	if y < 1900 || y > 2200 || m < 1 || m > 12 || d < 1 || d > 31 {
		return ""
	}
	return time.Date(y, time.Month(m), d, 0, 0, 0, 0, jst).UTC().Format(time.RFC3339)
}

type archiveEpisode struct {
	ID        int     `json:"id"`
	SubjectID int     `json:"subject_id"`
	Type      int     `json:"type"`
	Sort      float64 `json:"sort"`
	Name      string  `json:"name"`
	NameCN    string  `json:"name_cn"`
	Airdate   string  `json:"airdate"`
	Duration  string  `json:"duration"`
	Disc      int     `json:"disc"`
}

func (s *Store) importArchiveEpisodes(ctx context.Context, z *rangeZip, e zipEntry, rev string, anime map[int]bool) (int, error) {
	b := &batcher{s: s, ctx: ctx, n: 5000}
	defer b.abort()
	n := 0
	err := eachLine(ctx, z, e, func(line []byte) error {
		var ep archiveEpisode
		if err := json.Unmarshal(line, &ep); err != nil || ep.ID <= 0 || !anime[ep.SubjectID] {
			return nil
		}
		n++
		return b.exec(`INSERT INTO episodes(bgm_id,work_key,type,sort,name,name_cn,airdate,duration,disc,rev) VALUES(?,?,?,?,?,?,?,?,?,?)
			ON CONFLICT(bgm_id) DO UPDATE SET work_key=excluded.work_key, type=excluded.type, sort=excluded.sort, name=excluded.name, name_cn=excluded.name_cn,
			airdate=excluded.airdate, duration=excluded.duration, disc=excluded.disc, rev=excluded.rev`,
			ep.ID, "bgm:"+strconv.Itoa(ep.SubjectID), ep.Type, ep.Sort, store.Trunc(ep.Name, 300), store.Trunc(ep.NameCN, 300), store.Trunc(ep.Airdate, 20), store.Trunc(ep.Duration, 20), ep.Disc, rev)
	})
	if err == nil {
		err = b.flush()
	}
	if err != nil {
		return n, err
	}
	// rows missing from this dump were deleted upstream
	_, err = s.W.ExecContext(ctx, `DELETE FROM episodes WHERE rev != ?`, rev)
	return n, err
}

func (s *Store) importArchiveRelations(ctx context.Context, z *rangeZip, e zipEntry, rev string, anime map[int]bool) (int, error) {
	b := &batcher{s: s, ctx: ctx, n: 5000}
	defer b.abort()
	n := 0
	err := eachLine(ctx, z, e, func(line []byte) error {
		var r struct {
			SubjectID int `json:"subject_id"`
			Type      int `json:"relation_type"`
			Related   int `json:"related_subject_id"`
			Order     int `json:"order"`
		}
		if err := json.Unmarshal(line, &r); err != nil || !anime[r.SubjectID] || !anime[r.Related] {
			return nil
		}
		n++
		return b.exec(`INSERT INTO subject_relations(subject_id,related_id,relation_type,ord,rev) VALUES(?,?,?,?,?)
			ON CONFLICT(subject_id,related_id,relation_type) DO UPDATE SET ord=excluded.ord, rev=excluded.rev`, r.SubjectID, r.Related, r.Type, r.Order, rev)
	})
	if err == nil {
		err = b.flush()
	}
	if err != nil {
		return n, err
	}
	_, err = s.W.ExecContext(ctx, `DELETE FROM subject_relations WHERE rev != ?`, rev)
	return n, err
}
