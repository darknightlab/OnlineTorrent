package meta

import (
	"context"
	"encoding/json"
	"regexp"
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

	work, method, wconf, sure := s.resolveWork(ctx, t)

	paths := make([]string, len(t.Files))
	for i, f := range t.Files {
		paths[i] = f.Path
	}
	res := s.mapFiles(ctx, paths, t.Title, work, method, wconf, sure)
	// without any release time (no crawl publish date, no user submission time) nothing is
	// mapped automatically: certain rule mappings go to the review queue instead (a forged
	// torrent named like an official release must not get mapped). The same when the date
	// gate fails (日期不符; CheckDateFull, as for the decision model): released more than 7
	// days before the episode aired ("[LoliHouse] Show - 05" submitted before episode 5 aired
	// can't be real), or — single-episode torrents (one video file) only — more than 30
	// days after.
	rt, by := s.ReleaseTime(ctx, t.Infohash)
	noRelease := rt.IsZero()
	videos := 0
	for _, f := range t.Files {
		if IsVideo(f.Path) {
			videos++
		}
	}
	queue := false
	for i, f := range t.Files {
		kind, wk, ep, conf, m := res[i].kind, res[i].work, res[i].ep, res[i].conf, res[i].method
		if wkey, ok := wk.(string); ok && !res[i].queue {
			reason := ""
			if noRelease {
				reason = ReasonMissingRelease
			} else {
				k, _ := KeyOfFile(kind, ep)
				if k.Kind != "" {
					k, _ = s.ValidEpisode(ctx, wkey, k)
				}
				if c := s.CheckDateFull(ctx, wkey, k, rt, by, videos >= 2); !c.OK {
					reason = ReasonDateMismatch + ": " + c.Detail
				}
			}
			if reason != "" {
				if err := s.queueRuleReview(ctx, t.Infohash, f.Index, f.Path, wkey, kind, ep, conf, reason); err != nil {
					return err
				}
				wk, conf, m = nil, 0, ""
			}
		}
		// automatic rows are rewritten, except decision-model mappings (method clef) and, for
		// an uncertain result, rows that are already mapped: existing mappings stay as they
		// are (a re-delivered torrent is not re-decided)
		keep := ``
		if res[i].queue {
			keep = ` AND torrent_files.work_key IS NULL`
		}
		if _, err := s.W.ExecContext(ctx, `INSERT INTO torrent_files(infohash,file_index,path,size_bytes,kind,work_key,episode,confidence,status,method,updated_at) VALUES(?,?,?,?,?,?,?,?, 'auto', ?, ?)
			ON CONFLICT(infohash,file_index) DO UPDATE SET path=excluded.path, size_bytes=CASE WHEN excluded.size_bytes>0 THEN excluded.size_bytes ELSE size_bytes END,
			kind=excluded.kind, work_key=excluded.work_key, episode=excluded.episode, confidence=excluded.confidence, method=excluded.method, updated_at=excluded.updated_at
			WHERE torrent_files.status = 'auto' AND torrent_files.method != '`+MethodClef+`'`+keep,
			t.Infohash, f.Index, store.Trunc(f.Path, store.MaxPathBytes), f.Size, kind, wk, ep, conf, m, now); err != nil {
			return err
		}
		queue = queue || res[i].queue
	}
	if queue {
		if err := s.enqueueClef(ctx, t.Infohash); err != nil {
			return err
		}
	}
	return s.refreshFTS(ctx, t.Infohash)
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
	if t.Source != "user" && t.SourceKind != "user" {
		t.SubmittedAt, t.SubmittedBy = "", nil // only user submissions carry them
	}
	_, err := s.W.ExecContext(ctx, `INSERT INTO torrents(infohash,seq,title,source,source_id,team,team_id,published_at,size_bytes,file_count,tags,trusted,source_kind,page_url,status,updated_at,submitted_at,submitted_by)
		VALUES(?,(SELECT COALESCE(MAX(seq),0)+1 FROM torrents),?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(infohash) DO UPDATE SET title=CASE WHEN excluded.title!='' AND (title='' OR `+takeSource+`) THEN excluded.title ELSE title END,
		source=CASE WHEN `+takeSource+` THEN excluded.source ELSE source END,
		source_kind=CASE WHEN `+takeSource+` THEN excluded.source_kind ELSE source_kind END,
		source_id=CASE WHEN `+takeSource+` THEN excluded.source_id ELSE source_id END,
		team=CASE WHEN excluded.team!='' THEN excluded.team ELSE team END,
		team_id=CASE WHEN excluded.team_id!='' THEN excluded.team_id ELSE team_id END,
		published_at=CASE WHEN excluded.published_at!='' AND (published_at='' OR excluded.trusted > trusted OR (source IN ('', 'user') AND excluded.source NOT IN ('', 'user'))) THEN excluded.published_at ELSE published_at END,
		size_bytes=CASE WHEN excluded.size_bytes>0 THEN excluded.size_bytes ELSE size_bytes END,
		file_count=CASE WHEN excluded.file_count>0 THEN excluded.file_count ELSE file_count END,
		tags=CASE WHEN excluded.tags!='[]' THEN excluded.tags ELSE tags END,
		trusted=MAX(trusted, excluded.trusted),
		page_url=CASE WHEN excluded.page_url!='' AND page_url='' THEN excluded.page_url ELSE page_url END,
		status=CASE WHEN excluded.status='ready' THEN 'ready' ELSE status END,
		seq=COALESCE(seq, excluded.seq),
		updated_at=excluded.updated_at`,
		t.Infohash, t.Title, t.Source, store.Trunc(t.SourceID, 200), t.Team, store.Trunc(t.TeamID, 64), t.PublishedAt, size, files, tags,
		boolInt(t.Trusted), t.SourceKind, store.Trunc(t.PageURL, 500), status, now, t.SubmittedAt, t.SubmittedBy)
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

// resolveWork finds the torrent-level work (the anchor of the per-file routing, see
// route.go) and whether it is certain:
//
//   - a bangumi.moe bangumi tag (via bangumi-data): certain;
//   - exactly one work whose title / Chinese title / alias equals a fragment of the title
//     after normName (titleWork, years in the title must fit): certain, unless the title
//     carries a season marker that the work's own titles do not state;
//   - otherwise a guess (not certain; files are left for the decision model, see clef.go):
//     an alias substring of a recent work, or the title-fragment match with tie-breaking.
func (s *Store) resolveWork(ctx context.Context, t TorrentInput) (w *Work, method string, conf float64, certain bool) {
	for _, id := range t.MoeTagIDs {
		if w, err := s.WorkByExternal(ctx, "bangumi_moe", id); err == nil {
			return w, "moe_tag", 0.95, true
		}
	}
	title := strings.ToLower(t.Title)
	if title == "" {
		return nil, "", 0, false
	}
	tw, unique := s.titleWork(ctx, t.Title)
	if tw != nil && unique && titleSeasonFits(t.Title, tw) {
		return tw, "title", 0.6, true
	}
	if idx, err := s.aliasIndex(ctx); err == nil {
		// idx is sorted by alias length descending: the first hit is the longest match
		for _, e := range idx {
			if strings.Contains(title, e.alias) {
				return e.work, "alias", 0.7, false
			}
		}
	}
	if tw != nil {
		return tw, "title", 0.6, false
	}
	return nil, "", 0, false
}

// titleSeasonFits: a season marker in the torrent title ("Show S2", "第二季") must be the
// season the work's own titles state; "S1" also fits a work without a season marker.
func titleSeasonFits(title string, w *Work) bool {
	ts := ParseTitle(title).Season
	if ts == 0 {
		return true
	}
	ws := workSeason(w)
	return ws == ts || (ts == 1 && ws == 0)
}

// workSeason is the season number a work's own titles state (0 = none): season markers
// (第2期, Season 2, S2, Ⅱ …), else a sequel number ending a title ("Cyberpunk: Edgerunners 2",
// "エッジランナーズ２").
func workSeason(w *Work) float64 {
	names := append([]string{w.TitleCN, w.Title}, w.Aliases...)
	for _, n := range names {
		if v := nameHint(n).Season; v > 0 {
			return v
		}
	}
	// sequel numbers: the main titles only (aliases are noisy)
	for _, n := range []string{w.TitleCN, w.Title} {
		if v := sequelNumber(n); v > 0 {
			return v
		}
	}
	return 0
}

// reSequelNumber: a title ending in a lone 2–9 (ASCII or full-width) after a non-digit,
// optionally space-separated. The number must not be part of a name: "Kaiju No. 8",
// "怪兽8号" (ends in 号, no match), "#8", "Vol. 2", "Part 2" (a cour, not a season),
// "らんま1/2" (a fraction), "今日の5の2", "Lv2".
var (
	reSequelNumber = regexp.MustCompile(`([^\d０-９.#/／の])[\s\x{3000}]*([2-9２-９])$`)
	reSequelNotNum = regexp.MustCompile(`(?i)(?:\bno|\bnum|\bvol|\bpart|\bep|\bepisode|lv|パート|第|＃)[\s.:：\x{3000}]*[2-9２-９]$`)
)

// sequelNumber is the sequel number ending a work title (0 = none).
func sequelNumber(title string) float64 {
	t := strings.TrimSpace(title)
	m := reSequelNumber.FindStringSubmatch(t)
	if m == nil || reSequelNotNum.MatchString(t) {
		return 0
	}
	body := strings.TrimSpace(strings.TrimSuffix(t, m[2]))
	if utf8.RuneCountInString(body) < 2 {
		return 0
	}
	r, _ := utf8.DecodeRuneInString(m[2])
	if r >= '２' && r <= '９' {
		return float64(r - '０')
	}
	return float64(r - '0')
}

// titleFragments splits a torrent title into the parts that may be a whole title: bracket
// contents and the pieces between separators (/ | : + ;, " - ").
func titleFragments(title string) []string {
	const seps = "[]【】()「」『』{}〔〕〖〗/|:+;,、"
	var out []string
	seen := map[string]bool{}
	for _, part := range strings.FieldsFunc(normWidth(title), func(r rune) bool { return strings.ContainsRune(seps, r) }) {
		for _, f := range strings.Split(part, " - ") {
			f = strings.Trim(f, " \t._-~!?★☆​")
			n := normName(f)
			c := utf8.RuneCountInString(n)
			if seen[n] || c > 80 || c < 3 || (!hasNonASCII(n) && c < 5) {
				continue
			}
			words := false
			for _, tk := range tokenize(f) {
				if tk.cat == cWord {
					words = true
				}
			}
			if !words {
				continue
			}
			seen[n] = true
			out = append(out, f)
		}
	}
	return out
}

// titleWork finds a work (any air date) whose title, Chinese title or an alias equals a
// fragment of the torrent title (after normName). The work equal to the most fragments
// wins; ties prefer TV/WEB; a remaining tie gives nil. unique: exactly one work equals any
// fragment, or the winner (most fragments, no tie) has a fragment no other work equals —
// no tie-breaking was needed.
func (s *Store) titleWork(ctx context.Context, title string) (*Work, bool) {
	frags := titleFragments(title)
	if len(frags) == 0 || len(frags) > 20 {
		return nil, false
	}
	// years in the title ("Ranma 1/2 (2024)", "[2025][41-44]") must fit the work: begun
	// within a year of one of them, or still running since before it (no remake mix-ups).
	// Full dates ("整理时间：2026.07.15") are release dates, not title years.
	var years []int
	for _, y := range yearsIn(reFileDate.ReplaceAllString(title, " ")) {
		if y >= 1950 && y <= time.Now().Year()+1 {
			years = append(years, y)
		}
	}
	yearOK := func(w *Work) bool {
		if len(years) == 0 {
			return true
		}
		by := (&famWork{w: w}).beginYear()
		if by == 0 {
			return true
		}
		for _, y := range years {
			if d := y - by; (d >= -1 && d <= 1) || (w.End == "" && d > 0) {
				return true
			}
		}
		return false
	}
	score := map[string]int{}
	works := map[string]*Work{}
	only := map[string]bool{} // works that are the single match of some fragment
	for _, f := range frags {
		var hit []string
		nf := normName(f)
		rows, err := s.R.QueryContext(ctx, `SELECT `+workCols+` FROM works WHERE id IN (SELECT rowid FROM works_fts WHERE works_fts MATCH ? LIMIT 200)`,
			`"`+strings.ReplaceAll(f, `"`, `""`)+`"`)
		if err != nil {
			continue
		}
		for rows.Next() {
			w, err := scanWork(rows)
			if err != nil || !yearOK(w) {
				continue
			}
			for _, n := range append([]string{w.Title, w.TitleCN}, w.Aliases...) {
				if normName(n) == nf {
					score[w.Key]++
					works[w.Key] = w
					hit = append(hit, w.Key)
					break
				}
			}
		}
		rows.Close()
		if len(hit) == 1 {
			only[hit[0]] = true
		}
	}
	var best []*Work
	bestScore := 0
	for k, n := range score {
		switch {
		case n > bestScore:
			best, bestScore = []*Work{works[k]}, n
		case n == bestScore:
			best = append(best, works[k])
		}
	}
	unique := len(best) == 1 && only[best[0].Key]
	if len(best) > 1 {
		var tv []*Work
		for _, w := range best {
			if t := strings.ToLower(w.Type); t == "tv" || t == "web" {
				tv = append(tv, w)
			}
		}
		best = tv
	}
	if len(best) != 1 {
		return nil, false
	}
	w, err := s.GetWork(ctx, best[0].Key)
	if err != nil {
		return nil, false
	}
	return w, unique
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
