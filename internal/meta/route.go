package meta

// Per-file work assignment.
//
// A torrent may hold several bangumi subjects (a multi-season batch, TV + OVA + movie …),
// so after the torrent-level match (the anchor: bangumi.moe tag, alias, title) every video
// file is routed to its own work:
//
//  1. its names are read from the inside out (file stem, then the folders from the
//     innermost one); the first name that decides wins:
//     - a movie/OVA keyword (劇場版, Movie, Film, OVA, OAD) → the related work of that type
//       (several: by distinctive title, then by year written in the names, then the anchor;
//       still several → unmapped; none → the anchor);
//     - a TV season marker (S2, Season 2, 第二季, TV S3, Ⅱ …) → the N-th TV/WEB season of the
//       anchor's 前传/续集 chain (bgm subject_relations 2/3, ordered by air date; the anchor's
//       own season, from its titles or a bangumi.moe-tagged torrent title, positions it);
//     - a distinctive title of a related work (one that is no part of another related
//       work's title: "逮捕しちゃうぞ フルスロットル", not the bare "逮捕しちゃうぞ");
//     nothing decides → the anchor;
//  2. ClassifyFiles cross-checks per target work (clashes are per (work, kind, number));
//  3. files routed by a movie/OVA keyword become main episodes of that work (bgm lists OVA /
//     movie parts as type-0 episodes); a movie's only main video is episode 1; unnumbered
//     episodes are matched by official episode title or air date;
//  4. a group routed away from the anchor is checked against the official episodes (count
//     and numbers); implausible groups are left unmapped (no work, no episode).

import (
	"context"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// bgm subject relation types followed when routing
var (
	chainRelations  = map[int]bool{2: true, 3: true}                                      // 前传, 续集
	familyRelations = map[int]bool{2: true, 3: true, 4: true, 5: true, 6: true, 12: true} // + 总集篇, 全集, 番外篇, 主线故事
)

// famWork is a work related to the anchor, with what routing needs.
type famWork struct {
	w       *Work
	bgm     int64
	norms   []string // normName of title, Chinese title and aliases (≥ 4 runes)
	names   []string // the distinctive ones (no part of another related work's name)
	mainN   int      // official main episodes
	mainMax float64
	spN     int
	spMax   float64
	year    int // begin year (JST), 0 = unknown
}

// beginYear is the work's begin year in JST (0 = unknown).
func (f *famWork) beginYear() int {
	if t, err := time.Parse(time.RFC3339, f.w.Begin); err == nil {
		return t.In(jst).Year()
	}
	if len(f.w.Begin) >= 4 {
		y, _ := strconv.Atoi(f.w.Begin[:4])
		return y
	}
	return 0
}

func (f *famWork) kind() string {
	if t := strings.ToLower(f.w.Type); t != "" {
		return t
	}
	return platformType[f.w.Platform]
}

type family struct {
	anchor       *famWork
	works        []*famWork // anchor first
	chain        []*famWork // TV/WEB seasons in air order
	anchorIdx    int        // anchor position in chain, -1 if not in it
	anchorSeason float64    // season number of the anchor from its own titles, 0 = unknown
}

func bgmIDOf(w *Work) int64 {
	if id, err := strconv.ParseInt(w.External["bgm"], 10, 64); err == nil {
		return id
	}
	if k, ok := strings.CutPrefix(w.Key, "bgm:"); ok {
		if id, err := strconv.ParseInt(k, 10, 64); err == nil {
			return id
		}
	}
	return 0
}

// relatedBgm walks subject_relations (both directions) from id over the given types.
func (s *Store) relatedBgm(ctx context.Context, id int64, types map[int]bool, maxDepth, maxNodes int) []int64 {
	seen := map[int64]bool{id: true}
	var out []int64
	frontier := []int64{id}
	for depth := 0; depth < maxDepth && len(frontier) > 0 && len(out) < maxNodes; depth++ {
		var next []int64
		for _, cur := range frontier {
			rows, err := s.R.QueryContext(ctx, `SELECT related_id, relation_type FROM subject_relations WHERE subject_id = ?
				UNION ALL SELECT subject_id, relation_type FROM subject_relations WHERE related_id = ?`, cur, cur)
			if err != nil {
				return out
			}
			for rows.Next() {
				var rid int64
				var typ int
				if rows.Scan(&rid, &typ) != nil || !types[typ] || seen[rid] {
					continue
				}
				seen[rid] = true
				if len(out) < maxNodes {
					out = append(out, rid)
					next = append(next, rid)
				}
			}
			rows.Close()
		}
		frontier = next
	}
	return out
}

func (s *Store) famWorkOf(ctx context.Context, w *Work) *famWork {
	f := &famWork{w: w, bgm: bgmIDOf(w)}
	seen := map[string]bool{}
	for _, n := range append([]string{w.Title, w.TitleCN}, w.Aliases...) {
		if n = normName(n); utf8.RuneCountInString(n) >= 4 && !seen[n] {
			seen[n] = true
			f.norms = append(f.norms, n)
		}
	}
	f.year = f.beginYear()
	rows, err := s.R.QueryContext(ctx, `SELECT type, COUNT(*), MAX(sort) FROM episodes WHERE work_key = ? AND type IN (0,1) GROUP BY type`, w.Key)
	if err != nil {
		return f
	}
	defer rows.Close()
	for rows.Next() {
		var typ, n int
		var mx float64
		if rows.Scan(&typ, &n, &mx) != nil {
			continue
		}
		if typ == 0 {
			f.mainN, f.mainMax = n, mx
		} else {
			f.spN, f.spMax = n, mx
		}
	}
	return f
}

// loadFamily collects the works related to the anchor (bgm relations, unless relations is
// false) and its season chain.
func (s *Store) loadFamily(ctx context.Context, anchor *Work, relations bool) *family {
	a := s.famWorkOf(ctx, anchor)
	fam := &family{anchor: a, works: []*famWork{a}, anchorIdx: -1}
	byID := map[int64]*famWork{}
	if a.bgm > 0 && relations {
		byID[a.bgm] = a
		chainIDs := s.relatedBgm(ctx, a.bgm, chainRelations, 8, 30)
		famIDs := s.relatedBgm(ctx, a.bgm, familyRelations, 3, 60)
		get := func(id int64) *famWork {
			if f, ok := byID[id]; ok {
				return f
			}
			w, err := s.WorkByExternal(ctx, "bgm", strconv.FormatInt(id, 10))
			if err != nil {
				byID[id] = nil
				return nil
			}
			f := s.famWorkOf(ctx, w)
			byID[id] = f
			fam.works = append(fam.works, f)
			return f
		}
		for _, id := range famIDs {
			get(id)
		}
		isSeason := func(f *famWork) bool {
			if k := f.kind(); k != "tv" && k != "web" {
				return false
			}
			// a one/two-episode TV special inside the chain is no season
			n := f.mainN
			if n == 0 {
				n = f.w.Eps
			}
			return f == a || n == 0 || n >= 3
		}
		for _, id := range append([]int64{a.bgm}, chainIDs...) {
			if f := get(id); f != nil && isSeason(f) {
				fam.chain = append(fam.chain, f)
			}
		}
		sort.SliceStable(fam.chain, func(i, j int) bool {
			bi, bj := fam.chain[i].w.Begin, fam.chain[j].w.Begin
			if (bi == "") != (bj == "") {
				return bj == ""
			}
			if bi != bj {
				return bi < bj
			}
			return fam.chain[i].bgm < fam.chain[j].bgm
		})
		for i, f := range fam.chain {
			if f == a {
				fam.anchorIdx = i
			}
		}
	}
	// distinctive names: not contained in a name of another related work
	for _, f := range fam.works {
		for _, n := range f.norms {
			ok := true
			for _, o := range fam.works {
				if o == f {
					continue
				}
				for _, on := range o.norms {
					if strings.Contains(on, n) {
						ok = false
						break
					}
				}
				if !ok {
					break
				}
			}
			if ok {
				f.names = append(f.names, n)
			}
		}
	}
	for _, n := range append([]string{anchor.TitleCN, anchor.Title}, anchor.Aliases...) {
		if v := nameHint(n).Season; v > 0 {
			fam.anchorSeason = v
			break
		}
	}
	return fam
}

// seasonWork is the chain entry of TV season n (nil if unknown).
func (f *family) seasonWork(n, anchorSeason float64) *famWork {
	if n != math.Trunc(n) || n < 1 {
		return nil
	}
	idx := int(n) - 1
	if anchorSeason > 0 && f.anchorIdx >= 0 {
		idx += f.anchorIdx - int(anchorSeason) + 1
	}
	if idx < 0 || idx >= len(f.chain) {
		return nil
	}
	return f.chain[idx]
}

// matchName: the related work whose distinctive name (longest) occurs in norm.
func (f *family) matchName(norm string) *famWork {
	var best *famWork
	bestLen, tie := 0, false
	for _, w := range f.works {
		for _, n := range w.names {
			if len(n) < bestLen || !strings.Contains(norm, n) {
				continue
			}
			switch {
			case len(n) > bestLen:
				best, bestLen, tie = w, len(n), false
			case w != best:
				tie = true
			}
		}
	}
	if tie {
		return nil
	}
	return best
}

// pickType chooses among the related works of a type (movie|ova) for a file with hints hs.
// ok=false: several candidates and nothing tells them apart.
func (f *family) pickType(typ string, hs []routeHint) (w *famWork, ok bool) {
	var cands []*famWork
	for _, x := range f.works {
		if x.kind() == typ {
			cands = append(cands, x)
		}
	}
	switch len(cands) {
	case 0:
		return f.anchor, true // no such related work: keep the anchor
	case 1:
		return cands[0], true
	}
	for _, h := range hs {
		var hit []*famWork
		for _, c := range cands {
			for _, n := range c.names {
				if strings.Contains(h.Norm, n) {
					hit = append(hit, c)
					break
				}
			}
		}
		if len(hit) == 1 {
			return hit[0], true
		}
	}
	for _, h := range hs {
		var hit []*famWork
		for _, c := range cands {
			for _, y := range h.Years {
				if c.year == y {
					hit = append(hit, c)
					break
				}
			}
		}
		if len(hit) == 1 {
			return hit[0], true
		}
	}
	for _, c := range cands {
		if c == f.anchor {
			return c, true
		}
	}
	return nil, false
}

// fileRoute is where one file goes.
type fileRoute struct {
	w   *famWork // nil: unmapped
	how string   // "" (anchor by default) | type | season | folder
}

func baseMethod(m string) string {
	b, _, _ := strings.Cut(m, "+")
	return b
}

// isRoutedMethod: the file was assigned to a work other than by the torrent-level match.
func isRoutedMethod(m string) bool {
	return strings.Contains(m, "+season") || strings.Contains(m, "+folder") || strings.Contains(m, "+type")
}

// route assigns every file of a torrent to a work of the family (see the package comment
// at the top of this file). title is the torrent title, method the anchor's match method.
func (f *family) route(paths []string, title, method string) []fileRoute {
	out := make([]fileRoute, len(paths))
	anchorSeason := f.anchorSeason
	if ts := ParseTitle(title).Season; anchorSeason == 0 && ts > 0 && method == "moe_tag" {
		anchorSeason = ts // a bangumi.moe-tagged "Show S2" torrent: the tag is season 2
	}
	seasons := make([]float64, len(paths))
	for i, p := range paths {
		out[i] = fileRoute{w: f.anchor}
		if !IsVideo(p) {
			continue
		}
		hs := pathHints(p)
		for _, h := range hs {
			if h.Type == "movie" || h.Type == "ova" {
				w, ok := f.pickType(h.Type, hs)
				if !ok {
					out[i] = fileRoute{how: "type"}
				} else {
					out[i] = fileRoute{w: w, how: "type"}
				}
				break
			}
			if h.Season > 0 {
				seasons[i] = h.Season
				out[i] = fileRoute{w: f.seasonWork(h.Season, anchorSeason), how: "season"}
				break
			}
			if w := f.matchName(h.Norm); w != nil {
				out[i] = fileRoute{w: w, how: "folder"}
				break
			}
		}
	}
	// seasons without a usable chain, or a single season the torrent-level match already
	// identifies (bangumi.moe tag; a season missing from the chain): the anchor
	distinct := map[float64]bool{}
	for _, s := range seasons {
		if s > 0 {
			distinct[s] = true
		}
	}
	// a folder/file name that moves every video away from the anchor onto one other work is
	// the series' base title ("Show - 14" of a tagged second cour), not a second work
	var moved *famWork
	single, onAnchor := true, false
	for i, r := range out {
		if !IsVideo(paths[i]) || r.w == nil {
			continue
		}
		switch {
		case r.w == f.anchor:
			onAnchor = true
		case r.how == "folder":
			if moved != nil && moved != r.w {
				single = false
			}
			moved = r.w
		}
	}
	if moved != nil && single && !onAnchor {
		for i, r := range out {
			if r.how == "folder" {
				out[i] = fileRoute{w: f.anchor}
			}
		}
	}
	for s := range distinct {
		keep := len(f.chain) == 0
		if len(distinct) == 1 && ((method == "moe_tag" && anchorSeason == 0) || f.seasonWork(s, anchorSeason) == nil) {
			keep = true
		}
		if !keep {
			continue
		}
		for i := range out {
			if seasons[i] == s {
				out[i] = fileRoute{w: f.anchor}
			}
		}
	}
	return out
}

// fileResult is the mapping of one file.
type fileResult struct {
	kind   string
	work   any // nil or work key
	ep     *float64
	conf   float64
	method string
}

// mapFiles classifies the files of a torrent and assigns each one its work and episode;
// anchor is the torrent-level match (nil: none, files are classified only).
func (s *Store) mapFiles(ctx context.Context, paths []string, title string, anchor *Work, method string, wconf float64) []fileResult {
	out := make([]fileResult, len(paths))
	if anchor == nil {
		for i, p := range ClassifyFiles(paths, title) {
			wk, ep, conf, m := fileMapping(nil, method, wconf, p.Kind, p)
			out[i] = fileResult{p.Kind, wk, ep, conf, m}
		}
		return out
	}
	// relations only matter for several videos, or one naming a season / movie / OVA
	videos, hinted := 0, false
	for _, p := range paths {
		if !IsVideo(p) {
			continue
		}
		videos++
		if !hinted && videos == 1 {
			for _, h := range pathHints(p) {
				if h.Season > 0 || h.Type == "movie" || h.Type == "ova" {
					hinted = true
				}
			}
		}
	}
	fam := s.loadFamily(ctx, anchor, videos > 1 || hinted)
	routes := fam.route(paths, title, method)
	groups := make([]string, len(paths))
	for i, r := range routes {
		if r.w != nil {
			groups[i] = r.w.w.Key
		} else {
			groups[i] = "\x00unmapped"
		}
	}
	ps := classifyFilesIn(paths, title, groups)
	s.refineRouted(ctx, fam, paths, routes, ps)
	for i, p := range ps {
		r := routes[i]
		if r.w == nil {
			out[i] = fileResult{kind: p.Kind}
			continue
		}
		m, conf := method, wconf
		if r.how != "" && (r.w != fam.anchor || r.how == "type") {
			m += "+" + r.how
			if r.w != fam.anchor {
				conf *= 0.9
			}
		}
		wk, ep, c, mm := fileMapping(r.w.w, m, conf, p.Kind, p)
		out[i] = fileResult{p.Kind, wk, ep, c, mm}
	}
	return out
}

var reFileDate = regexp.MustCompile(`((?:19|20)\d\d)[-./](0?[1-9]|1[0-2])[-./](0?[1-9]|[12]\d|3[01])`)

// refineRouted applies the per-work rules after classification (see the top of this file):
// movie/OVA keyword files become main episodes, a movie's only video is episode 1,
// unnumbered episodes are matched by official title / air date, and groups routed away
// from the anchor that do not fit the official episodes are unmapped (routes[i].w = nil).
func (s *Store) refineRouted(ctx context.Context, fam *family, paths []string, routes []fileRoute, ps []ParsedName) {
	groups := map[*famWork][]int{}
	for i, r := range routes {
		if r.w != nil && IsVideo(paths[i]) {
			groups[r.w] = append(groups[r.w], i)
		}
	}
	for w, idx := range groups {
		k := w.kind()
		if k == "movie" || k == "ova" {
			for _, i := range idx {
				if routes[i].how == "type" && (ps[i].Kind == "sp" || ps[i].Kind == "other") {
					if ps[i].Kind == "other" {
						ps[i].Episode = nil
					}
					ps[i].Kind = "episode"
					ps[i].Confidence = max(ps[i].Confidence, 0.5)
				}
			}
		}
		if k == "movie" {
			var mains []int
			for _, i := range idx {
				if ps[i].Kind == "episode" || ps[i].Kind == "other" {
					mains = append(mains, i)
				}
			}
			if len(mains) == 1 && ps[mains[0]].Episode == nil {
				ps[mains[0]] = ParsedName{Kind: "episode", Episode: ptrF(1), Confidence: 0.7, Season: ps[mains[0]].Season}
			}
		}
		s.matchEpisodeTitles(ctx, w, paths, idx, ps)
		if w != fam.anchor && !plausible(w, idx, ps) {
			for _, i := range idx {
				routes[i].w = nil
			}
		}
	}
}

// matchEpisodeTitles numbers unnumbered main episodes by the official episode title written
// in the file name ("OVA 1994-09-24 そしてふたりは出会った") or by the air date.
func (s *Store) matchEpisodeTitles(ctx context.Context, w *famWork, paths []string, idx []int, ps []ParsedName) {
	var todo []int
	for _, i := range idx {
		if ps[i].Kind == "episode" && ps[i].Episode == nil {
			todo = append(todo, i)
		}
	}
	if len(todo) == 0 || w.mainN == 0 || w.mainN > 500 {
		return
	}
	eps, err := s.OfficialEpisodes(ctx, w.w.Key)
	if err != nil {
		return
	}
	for _, i := range todo {
		name := stem(paths[i])
		norm := normName(name)
		var best *OfficialEpisode
		bestLen, tie := 0, false
		for j := range eps {
			e := &eps[j]
			if e.Key.Kind != KeyMain {
				continue
			}
			for _, t := range []string{e.Name, e.NameCN} {
				n := normName(t)
				if c := utf8.RuneCountInString(n); c < 4 || (!hasNonASCII(n) && c < 6) || len(n) < bestLen || !strings.Contains(norm, n) {
					continue
				}
				switch {
				case len(n) > bestLen:
					best, bestLen, tie = e, len(n), false
				case best != e:
					tie = true
				}
			}
		}
		if best == nil || tie {
			best, tie = nil, false
			if m := reFileDate.FindStringSubmatch(name); m != nil {
				y, _ := strconv.Atoi(m[1])
				mo, _ := strconv.Atoi(m[2])
				d, _ := strconv.Atoi(m[3])
				date := time.Date(y, time.Month(mo), d, 0, 0, 0, 0, time.UTC).Format("2006-01-02")
				for j := range eps {
					if e := &eps[j]; e.Key.Kind == KeyMain && e.Airdate == date {
						if best != nil {
							tie = true
						}
						best = e
					}
				}
			}
		}
		if best != nil && !tie {
			ps[i].Episode, ps[i].Confidence = ptrF(best.Key.Number), 0.6
		}
	}
}

// plausible checks the main episodes of a group routed away from the anchor against the
// work's official episode list (or its planned count): no more distinct numbers than the
// official count plus a little slack, and every number within 0..count or the official
// sorts (a last episode listed as SP counts: "24" of 23 main + SP 24).
func plausible(w *famWork, idx []int, ps []ParsedName) bool {
	c := w.mainN
	if c == 0 {
		c = w.w.Eps
	}
	if c == 0 {
		return true // nothing to check against
	}
	slack := 1 + c/10
	limit := math.Max(float64(c), math.Max(w.mainMax, w.spMax))
	nums := map[float64]bool{}
	for _, i := range idx {
		if ps[i].Kind != "episode" || ps[i].Episode == nil {
			continue
		}
		v := *ps[i].Episode
		if v < 0 || v > limit {
			return false
		}
		nums[v] = true
	}
	return len(nums) <= c+slack
}
