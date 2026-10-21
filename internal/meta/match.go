package meta

import (
	"context"
	"encoding/json"
	"math"
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
	// days after. These gates are for NEW mappings only (owner decision): a file whose
	// stored row already maps it to the same work and episode keeps it (a resubmission,
	// a re-delivered feed item or re-ingested qB metadata changes the release time), and
	// rows the gate can't change anyway (manual / verified, decision-model) are left alone.
	gate := s.newRuleGate(ctx, t.Infohash, len(t.Files), func(i int) string { return t.Files[i].Path })
	// files with a conclusion are left exactly as they are (owner principle): mapped files
	// (any method), rows a human set, and files whose latest decision is a conclusion
	// (rejected; the model's none / not_episode) — no rule rewrite, no decision-model job.
	// Only uncertain files are mapped again / queued.
	concluded := s.ConcludedFiles(ctx, t.Infohash)
	pending := s.pendingDecisionFiles(ctx, t.Infohash)
	queue := false
	for i, f := range t.Files {
		if st, ok := gate.stored[f.Index]; ok && (st.status != "auto" || st.work.Valid || concluded[f.Index]) {
			continue
		}
		kind, wk, ep, conf, m := res[i].kind, res[i].work, res[i].ep, res[i].conf, res[i].method
		if wkey, ok := wk.(string); ok && !res[i].queue {
			if reason := gate.check(ctx, f.Index, wkey, kind, ep); reason != "" {
				if err := s.queueRuleReview(ctx, t.Infohash, f.Index, f.Path, wkey, kind, ep, conf, reason); err != nil {
					return err
				}
				wk, conf, m = nil, 0, ""
			}
		}
		// new and uncertain rows only (concluded ones were skipped above); the WHERE keeps
		// the old guards
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
		// a file waiting in the review queue that the rules now map (a re-run after rule /
		// catalogue changes): its review is settled — recorded as applied by the rules
		if wkey, ok := wk.(string); ok && pending[f.Index] {
			if err := s.settleByRule(ctx, t.Infohash, f.Index, f.Path, wkey, kind, ep, conf); err != nil {
				return err
			}
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

// Rematch re-runs Match over a stored ready torrent (its catalogue info and stored file
// list): what "the torrent shows up again" does for the files without a conclusion — the
// rules map what they can now (new mappings gated as new), the rest is queued for the
// decision model. Concluded files are untouched. Not ready (no file list yet): nothing.
func (s *Store) Rematch(ctx context.Context, ih string) error {
	in, ok := s.GetTorrentInput(ctx, ih)
	if !ok {
		return store.ErrNotFound
	}
	if status, _ := s.KnownTorrent(ctx, ih); status != "ready" {
		return nil
	}
	rows, err := s.R.QueryContext(ctx, `SELECT file_index, path, size_bytes FROM torrent_files WHERE infohash = ? ORDER BY file_index`, ih)
	if err != nil {
		return err
	}
	for rows.Next() {
		var f FileInput
		if err := rows.Scan(&f.Index, &f.Path, &f.Size); err != nil {
			rows.Close()
			return err
		}
		in.Files = append(in.Files, f)
	}
	rows.Close()
	if err := rows.Err(); err != nil || len(in.Files) == 0 {
		return err
	}
	return s.Match(ctx, in)
}

// ruleGate applies the release-time gates to certain rule mappings of one torrent.
type ruleGate struct {
	s        *Store
	released time.Time
	by       string
	batch    bool // 2 or more video files: no late limit
	stored   map[int]storedFile
}

func (s *Store) newRuleGate(ctx context.Context, ih string, n int, path func(int) string) *ruleGate {
	g := &ruleGate{s: s, stored: map[int]storedFile{}}
	g.released, g.by = s.ReleaseTime(ctx, ih)
	videos := 0
	for i := 0; i < n; i++ {
		if IsVideo(path(i)) {
			videos++
		}
	}
	g.batch = videos >= 2
	if rows, err := s.R.QueryContext(ctx, `SELECT file_index, path, kind, work_key, episode, status, method FROM torrent_files WHERE infohash = ?`, ih); err == nil {
		for rows.Next() {
			var f storedFile
			if rows.Scan(&f.index, &f.path, &f.kind, &f.work, &f.episode, &f.status, &f.method) == nil {
				g.stored[f.index] = f
			}
		}
		rows.Close()
	}
	return g
}

// check returns why the mapping of file idx to (wkey, kind, ep) may not be applied
// automatically ("" = it may): no release time, or the date gate (CheckDateFull). A mapping
// the stored row already has (same work, same canonical key) and rows that aren't automatic
// rule rows are not gated.
func (g *ruleGate) check(ctx context.Context, idx int, wkey, kind string, ep *float64) string {
	if f, ok := g.stored[idx]; ok {
		if f.status != "auto" || f.method == MethodClef {
			return "" // not rewritten anyway
		}
		if f.work.Valid && f.work.String == wkey && g.sameKey(ctx, wkey, kind, ep, f) {
			return ""
		}
	}
	if g.released.IsZero() {
		return ReasonMissingRelease
	}
	k, _ := KeyOfFile(kind, ep)
	if k.Kind != "" {
		k, _ = g.s.ValidEpisode(ctx, wkey, k)
	}
	if c := g.s.CheckDateFull(ctx, wkey, k, g.released, g.by, g.batch); !c.OK {
		return ReasonDateMismatch + ": " + c.Detail
	}
	return ""
}

// sameKey: the stored row's episode key equals the new one after the split-cour shift.
func (g *ruleGate) sameKey(ctx context.Context, wkey, kind string, ep *float64, f storedFile) bool {
	var old *float64
	if f.episode.Valid {
		v := f.episode.Float64
		old = &v
	}
	nk, nok := KeyOfFile(kind, ep)
	ok2, ook := KeyOfFile(f.kind, old)
	if !nok || !ook {
		return !nok && !ook && kind == f.kind
	}
	nc, _ := g.s.ValidEpisode(ctx, wkey, nk)
	oc, _ := g.s.ValidEpisode(ctx, wkey, ok2)
	return nc == oc
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
//     the longest name of any work (all eras) contained in the title, or the title-fragment
//     match with tie-breaking.
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
	if !(tw != nil && unique) || !titleSeasonFits(t.Title, tw) {
		if fw, ok := s.franchiseWork(ctx, t); fw != nil && ok {
			return fw, "title_franchise", 0.6, true
		}
	}
	if idx, err := s.aliasIndex(ctx); err == nil {
		// the longest name of any work contained in the title: a guess, never certain.
		// Several works carry it (remakes, a franchise name shared by its seasons): the
		// title-fragment guess when it is one of them, else the most recently begun one.
		if ws := idx.longest(title); len(ws) > 0 {
			pick := ws[0]
			for _, w := range ws[1:] {
				if w.begin > pick.begin {
					pick = w
				}
			}
			for _, w := range ws {
				if tw != nil && w.key == tw.Key {
					pick = w
				}
			}
			if w, err := s.GetWork(ctx, pick.key); err == nil {
				return w, "alias", 0.7, false
			}
		}
	}
	if tw != nil {
		return tw, "title", 0.6, false
	}
	return nil, "", 0, false
}

// franchiseWork resolves a title naming a franchise shared by several entries ("Toaru
// Majutsu no Index" for S1 and Ⅲ): the TV/WEB entries whose names equal a title fragment
// once season markers are dropped from both (franchiseBase). The torrent's season is the
// one its title states, else the one all its videos state; none = season 1.
//
//  1. Each entry's own titles state its season (workSeason: Ⅱ, 2nd Season, 第2期, S2, a
//     trailing numeral …; no number = season 1): exactly one entry of that season → it.
//     A title fragment that extends a name of the entries ("<name> <subtitle>") names
//     something else: not certain.
//  2. Otherwise (titles without numbers: arc subtitles, "The Final Season"; or several
//     entries read as season 1) airing order decides: the entries ordered by start date,
//     the N-th is season N — only when that position is unambiguous (no other entry starts
//     the same day) and the entry's own season, if its titles state one, is N. bgm
//     前传/续集 relations are not used: they follow story order, release names follow
//     airing order (a later-made prequel is "S2").
//
// Picked by airing order, or without a stated season, the entry must not start more than
// 7 days after the release. Episode numbers are checked per file against the entry's
// official list (mapFiles); anything else → not certain (decision model).
func (s *Store) franchiseWork(ctx context.Context, t TorrentInput) (*Work, bool) {
	cands := s.franchiseCandidates(ctx, t.Title)
	if len(cands) < 2 {
		// one entry: nothing shared to resolve — titleWork (exact name, its own season)
		// decides. ("Ace of Diamond Act II Season 2" must not become "ダイヤのA actⅡ" by
		// reading its Ⅱ as a season.)
		return nil, false
	}
	// a fragment that is a name of the franchise plus more ("JOJO的奇妙冒险 飙马野郎": the
	// Steel Ball Run part, not an entry named "JOJO的奇妙冒险") names an entry the shared
	// name doesn't cover → not certain
	for _, f := range titleFragments(t.Title) {
		fb := franchiseBase(f)
		for _, w := range cands {
			for _, n := range append([]string{w.Title, w.TitleCN}, w.Aliases...) {
				if nb := franchiseBase(n); utf8.RuneCountInString(nb) >= 3 && fb != nb && strings.Contains(fb, nb) {
					return nil, false
				}
			}
		}
	}
	paths := make([]string, len(t.Files))
	for i, f := range t.Files {
		paths[i] = f.Path
	}
	ts := torrentSeason(t.Title, paths)
	stated := ts > 0
	if ts == 0 {
		ts = 1
	}
	if ts != math.Trunc(ts) {
		return nil, false
	}
	var hit []*Work
	for _, w := range cands {
		if ws := workSeason(w); ws == ts || (ws == 0 && ts == 1) {
			hit = append(hit, w)
		}
	}
	pick, byOrder := (*Work)(nil), false
	if len(hit) == 1 {
		pick = hit[0] // the titles decide
	} else {
		pick, byOrder = airingSeason(cands, int(ts)), true
	}
	if pick == nil {
		return nil, false
	}
	if byOrder || !stated {
		rt, _ := s.ReleaseTime(ctx, t.Infohash)
		if b := parseDay(pick.Begin); !rt.IsZero() && !b.IsZero() && rt.Before(b.Add(-time.Duration(DateEarlyDays)*24*time.Hour)) {
			return pick, false // released before that entry began
		}
	}
	return pick, true
}

// torrentSeason: the season a torrent states — its title's, else the one season all its
// videos state (0 = none).
func torrentSeason(title string, paths []string) float64 {
	if ts := ParseTitle(title).Season; ts > 0 {
		return ts
	}
	seen := map[float64]bool{}
	for _, p := range paths {
		if IsVideo(p) {
			seen[StatedSeason(p, 0)] = true
		}
	}
	if len(seen) == 1 {
		for v := range seen {
			return v
		}
	}
	return 0
}

// airingSeason: the n-th of ws in airing order (start date), nil when the position is
// ambiguous (unknown / equal start dates) or the entry's titles state another season.
func airingSeason(ws []*Work, n int) *Work {
	if n < 1 || n > len(ws) {
		return nil
	}
	o := append([]*Work(nil), ws...)
	for _, w := range o {
		if parseDay(w.Begin).IsZero() {
			return nil
		}
	}
	sort.SliceStable(o, func(i, j int) bool { return o[i].Begin < o[j].Begin })
	w := o[n-1]
	day := func(x *Work) string { return parseDay(x.Begin).Format("2006-01-02") }
	if (n > 1 && day(o[n-2]) == day(w)) || (n < len(o) && day(o[n]) == day(w)) {
		return nil
	}
	if ws := workSeason(w); ws != 0 && ws != float64(n) {
		return nil
	}
	return w
}

// franchiseCandidates: the TV/WEB works (years in the title fitting) with a name equal to a
// title fragment after franchiseBase (the index only holds TV/WEB works there). Fragments
// naming different sets of works: nil.
func (s *Store) franchiseCandidates(ctx context.Context, title string) []*Work {
	frags := titleFragments(title)
	if len(frags) == 0 || len(frags) > 20 {
		return nil
	}
	years := titleYears(title)
	idx, err := s.aliasIndex(ctx)
	if err != nil {
		return nil
	}
	get := s.workGetter(ctx)
	// one name, one franchise: every fragment that names entries must name the same ones
	// ("[ANi] Keroro / KERORO 軍曹☆": "Keroro" is the 2014 series, "KERORO 軍曹☆" the 2026
	// one — two franchises, nothing to resolve)
	var out []*Work
	set := ""
	for _, f := range frags {
		bf := franchiseBase(f)
		if utf8.RuneCountInString(bf) < 3 || (!hasNonASCII(bf) && utf8.RuneCountInString(bf) < 5) {
			continue
		}
		var ws []*Work
		for _, k := range idx.base[bf] {
			if w := get(k); w != nil && yearsFitWork(years, w) {
				ws = append(ws, w)
			}
		}
		if len(ws) == 0 {
			continue
		}
		sort.Slice(ws, func(i, j int) bool { return ws[i].Key < ws[j].Key })
		keys := make([]string, len(ws))
		for i, w := range ws {
			keys[i] = w.Key
		}
		if k := strings.Join(keys, ","); set == "" {
			set, out = k, ws
		} else if k != set {
			return nil
		}
	}
	return out
}

// reFranchiseSeason: the season / part markers of reSeasonWords, without its bracket clause
// — "(2012)" / "(TV)" qualifiers tell entries apart and stay.
var reFranchiseSeason = regexp.MustCompile(strings.TrimSuffix(reSeasonWords.String(), `|[（(][^)）]{0,20}[)）]`))

// franchiseBase: a name without its season marker (reFranchiseSeason; Roman numerals Ⅱ…Ⅹ /
// II…IV; a trailing sequel numeral, sequelNumber).
func franchiseBase(n string) string {
	v := strings.TrimSpace(reFranchiseSeason.ReplaceAllString(normWidth(n), " "))
	if sequelNumber(v) > 0 {
		_, sz := utf8.DecodeLastRuneInString(v)
		v = v[:len(v)-sz]
	}
	v = strings.Map(func(r rune) rune {
		if r >= 'Ⅰ' && r <= 'Ⅻ' {
			return ' '
		}
		return r
	}, v)
	return normName(v)
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
	// Explicit season evidence in primary titles wins. A primary cour/part marker
	// prevents aliases such as "Season 2" from turning that cour into season two.
	primary := []string{w.TitleCN, w.Title}
	for _, n := range primary {
		if v := nameHint(withoutCour(n)).Season; v > 0 {
			return v
		}
	}
	if hasCourTitle(w) {
		return 0
	}
	for _, n := range w.Aliases {
		if v := nameHint(withoutCour(n)).Season; v > 0 {
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
	w, unique, _ := s.titleWorks(ctx, title)
	return w, unique
}

// titleWorks is titleWork plus every work equal to some fragment (named; years fitting).
func (s *Store) titleWorks(ctx context.Context, title string) (*Work, bool, []*Work) {
	frags := titleFragments(title)
	if len(frags) == 0 || len(frags) > 20 {
		return nil, false, nil
	}
	// years in the title ("Ranma 1/2 (2024)", "[2025][41-44]") must fit the work
	years := titleYears(title)
	yearOK := func(w *Work) bool { return yearsFitWork(years, w) }
	idx, err := s.aliasIndex(ctx)
	if err != nil {
		return nil, false, nil
	}
	get := s.workGetter(ctx)
	score := map[string]int{}
	works := map[string]*Work{}
	only := map[string]bool{} // works that are the single match of some fragment
	for _, f := range frags {
		var hit []string
		nf := normName(f)
		for _, k := range idx.norm[nf] {
			w := get(k)
			if w == nil || !yearOK(w) {
				continue
			}
			score[w.Key]++
			works[w.Key] = w
			hit = append(hit, w.Key)
		}
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
	named := make([]*Work, 0, len(works))
	for _, w := range works {
		named = append(named, w)
	}
	sort.Slice(named, func(i, j int) bool { return named[i].Key < named[j].Key })
	if len(best) != 1 {
		return nil, false, named
	}
	w, err := s.GetWork(ctx, best[0].Key)
	if err != nil {
		return nil, false, named
	}
	return w, unique, named
}

// yearsFitWork: years written in a title must fit the work — begun within a year of one of
// them, or still running since before it (no remake mix-ups). No years: true.
func yearsFitWork(years []int, w *Work) bool {
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

// titleYears: the years written in a title (full dates are release dates, not years).
func titleYears(title string) []int {
	var years []int
	for _, y := range yearsIn(reFileDate.ReplaceAllString(title, " ")) {
		if y >= 1950 && y <= time.Now().Year()+1 {
			years = append(years, y)
		}
	}
	return years
}

// aliasIndex: every title / Chinese title / alias (≥ 4 runes, lower-cased) of every work →
// the works that carry it. Only work keys and start dates are kept (no Work structs: ~31k
// works / ~120k names stay a few MB).
type aliasIndex struct {
	names  map[string][]aliasWork
	maxLen int // longest name in bytes (bounds the substrings tried per title)
	// exact-name lookups of the title-fragment match (titleWorks: normName) and of the
	// franchise match (franchiseCandidates: franchiseBase) → work keys. In memory: the
	// former works_fts trigram queries per fragment were most of Match's time.
	norm, base map[string][]string
	nativeBase map[string][]string // original-source franchise names, for contradiction exemptions
	extra      map[string][]string // attributed manami names per work (file-name checks, no DB query)
}

type aliasWork struct {
	key   string
	begin string // begin_at (RFC 3339; "" = unknown)
}

// aliasIndexMaxBytes: longer names are not indexed (no torrent title quotes a 150-byte
// alias verbatim; keeps the per-title substring scan bounded).
const (
	aliasIndexTTL      = 10 * time.Minute
	aliasIndexMaxBytes = 150
)

// aliasIndex returns the name index of all works — every era: old shows are matched as
// well as current ones. The first call builds it (≈1 s for ~31k works); afterwards an index
// older than 10 min is rebuilt in the background while the old one keeps serving (Match
// never waits for a refresh). Adding a work (catalog) drops it: the next call rebuilds.
func (s *Store) aliasIndex(ctx context.Context) (*aliasIndex, error) {
	s.aliasMu.Lock()
	defer s.aliasMu.Unlock()
	if old := s.aliases; old != nil {
		if time.Since(s.aliasAt) >= aliasIndexTTL {
			s.aliasAt = time.Now() // one refresh at a time
			go func() {
				idx, err := s.buildAliasIndex(context.Background())
				s.aliasMu.Lock()
				defer s.aliasMu.Unlock()
				if err == nil && s.aliases == old { // not dropped meanwhile
					s.aliases, s.aliasAt = idx, time.Now()
				}
			}()
		}
		return old, nil
	}
	idx, err := s.buildAliasIndex(ctx)
	if err != nil {
		return nil, err
	}
	s.aliases, s.aliasAt = idx, time.Now()
	return idx, nil
}

func (s *Store) buildAliasIndex(ctx context.Context) (*aliasIndex, error) {
	rows, err := s.R.QueryContext(ctx, `SELECT key, title, title_cn, aliases, begin_at, type, platform FROM works`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	idx := &aliasIndex{names: make(map[string][]aliasWork, 1<<17), norm: make(map[string][]string, 1<<17), base: make(map[string][]string, 1<<15), nativeBase: make(map[string][]string, 1<<15), extra: map[string][]string{}}
	addKey := func(m map[string][]string, n, key string) {
		if n == "" {
			return
		}
		l := m[n]
		for _, k := range l {
			if k == key {
				return
			}
		}
		m[n] = append(l, key)
	}
	for rows.Next() {
		var key, title, cn, aliases, begin, typ string
		var platform int
		if err := rows.Scan(&key, &title, &cn, &aliases, &begin, &typ, &platform); err != nil {
			return nil, err
		}
		var al []string
		_ = json.Unmarshal([]byte(aliases), &al)
		k := strings.ToLower(typ)
		if k == "" {
			k = platformType[platform]
		}
		series := k == "tv" || k == "web" // franchise entries (franchiseCandidates)
		seen := map[string]bool{}
		for _, a := range append([]string{title, cn}, al...) {
			addKey(idx.norm, normName(a), key)
			if series {
				addKey(idx.base, franchiseBase(a), key)
				addKey(idx.nativeBase, franchiseBase(a), key)
			}
			a = strings.ToLower(strings.TrimSpace(a))
			if utf8.RuneCountInString(a) < 4 || len(a) > aliasIndexMaxBytes || seen[a] {
				continue
			}
			seen[a] = true
			idx.names[a] = append(idx.names[a], aliasWork{key, begin})
			if len(a) > idx.maxLen {
				idx.maxLen = len(a)
			}
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()
	if UseManamiNames {
		// anime-offline-database names (manami.go), linked by shared ids
		mrows, err := s.R.QueryContext(ctx, `SELECT m.work_key, m.name, w.begin_at, w.type, w.platform FROM manami_names m JOIN works w ON w.key = m.work_key ORDER BY m.work_key`)
		if err != nil {
			return nil, err
		}
		defer mrows.Close()
		for mrows.Next() {
			var key, name, begin, typ string
			var platform int
			if err := mrows.Scan(&key, &name, &begin, &typ, &platform); err != nil {
				return nil, err
			}
			k := strings.ToLower(typ)
			if k == "" {
				k = platformType[platform]
			}
			idx.extra[key] = append(idx.extra[key], name)
			addKey(idx.norm, normName(name), key)
			if k == "tv" || k == "web" {
				addKey(idx.base, franchiseBase(name), key)
			}
			a := strings.ToLower(strings.TrimSpace(name))
			if utf8.RuneCountInString(a) < 4 || len(a) > aliasIndexMaxBytes {
				continue
			}
			if ws := idx.names[a]; len(ws) == 0 || ws[len(ws)-1].key != key {
				idx.names[a] = append(ws, aliasWork{key, begin})
			}
			if len(a) > idx.maxLen {
				idx.maxLen = len(a)
			}
		}
		if err := mrows.Err(); err != nil {
			return nil, err
		}
	}
	return idx, nil
}

// workGetter: GetWork with a per-call cache (nil: no such work).
func (s *Store) workGetter(ctx context.Context) func(string) *Work {
	cache := map[string]*Work{}
	return func(k string) *Work {
		if w, ok := cache[k]; ok {
			return w
		}
		w, err := s.GetWork(ctx, k)
		if err != nil {
			w = nil
		}
		cache[k] = w
		return w
	}
}

// longest returns the works of the longest indexed name contained in title (lower-cased),
// nil if none: map lookups of the title's substrings (at rune boundaries, ≤ maxLen bytes),
// longest first — O(len(title) × maxLen), independent of the number of works.
func (x *aliasIndex) longest(title string) []aliasWork {
	var starts []int
	for i := range title {
		starts = append(starts, i)
	}
	ends := append(starts[1:len(starts):len(starts)], len(title))
	best, bestLen := []aliasWork(nil), 0
	for si, i := range starts {
		for ei := len(ends) - 1; ei >= si; ei-- {
			j := ends[ei]
			if j-i <= bestLen {
				break // shorter than the best hit
			}
			if j-i > x.maxLen {
				continue
			}
			if ws, ok := x.names[title[i:j]]; ok {
				best, bestLen = ws, j-i
				break
			}
		}
	}
	return best
}
