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
//     movie parts as type-0 episodes); a movie's only main video is episode 1.
//
// Certainty (since the decision model, clef.go): a file is mapped by rule only when the
// anchor is certain (resolveWork), the file stays on the anchor without a contrary hint
// (its season is the anchor's own, a movie/OVA keyword matches the anchor's type or no
// related work of that type exists, no related work's distinctive title), the number is
// sure (ParsedName.Sure) and the key is in the anchor's official episode list as written.
// Routing to another work (season chain, related title, movie/OVA keyword) only produces a
// hint; such files stay unmapped and are queued for the decision model. Removed rules:
// the "all files moved onto one other work → anchor" and "season not in the chain →
// anchor" fallbacks, unnumbered episodes matched by official title / air date, and the
// plausibility check of routed groups.

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

// fileRoute is where one file goes. sure=false: w is only a guess (a hint for the decision
// model), the file is not mapped by rule.
type fileRoute struct {
	w    *famWork // nil: no idea
	how  string   // "" (anchor by default) | type | season | folder
	sure bool
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
// at the top of this file). title is the torrent title, method the anchor's match method,
// anchorSure whether the anchor itself is certain. Only files that stay on the anchor
// without any contrary hint are sure; routing to a related work (season chain, related
// title, movie/OVA keyword) is a guess.
func (f *family) route(paths []string, title, method string, anchorSure bool) []fileRoute {
	out := make([]fileRoute, len(paths))
	anchorSeason := f.anchorSeason
	if ts := ParseTitle(title).Season; anchorSeason == 0 && ts > 0 && method == "moe_tag" {
		anchorSeason = ts // a bangumi.moe-tagged "Show S2" torrent: the tag is season 2
	}
	if anchorSeason == 0 && method == "title_franchise" {
		// the franchise entry of the stated season (franchiseWork, possibly by airing
		// order): that season is the anchor's own
		anchorSeason = torrentSeason(title, paths)
	}
	// the season a file names is the anchor's own: stated by its titles (or the tagged
	// title), or "S1" for an anchor without a season marker that starts its chain
	onAnchorSeason := func(s float64) bool {
		return s == anchorSeason || (s == 1 && anchorSeason == 0 && (f.anchorIdx == 0 || len(f.chain) == 0))
	}
	for i, p := range paths {
		out[i] = fileRoute{w: f.anchor, sure: anchorSure}
		if !IsVideo(p) {
			continue
		}
		hs := pathHints(p)
		for _, h := range hs {
			if h.Type == "movie" || h.Type == "ova" {
				if f.anchor.kind() == h.Type {
					out[i] = fileRoute{w: f.anchor, how: "type", sure: anchorSure}
					break
				}
				w, ok := f.pickType(h.Type, hs)
				if h.Type == "ova" && w != nil && w.kind() == "ova" {
					out[i] = fileRoute{w: w, how: "type", sure: anchorSure}
					break
				}
				if h.Type == "ova" {
					out[i] = fileRoute{how: "type"} // never attach an OVA/OAD to the TV anchor
					break
				}
				switch {
				case !ok:
					out[i] = fileRoute{how: "type"}
				case w == f.anchor: // no related work of that type: the anchor (kind from the name)
					out[i] = fileRoute{w: w, sure: anchorSure}
				default:
					out[i] = fileRoute{w: w, how: "type"}
				}
				break
			}
			if h.Season > 0 {
				if onAnchorSeason(h.Season) {
					out[i] = fileRoute{w: f.anchor, how: "season", sure: anchorSure}
				} else {
					out[i] = fileRoute{w: f.seasonWork(h.Season, anchorSeason), how: "season"}
				}
				break
			}
			if w := f.matchName(h.Norm); w != nil {
				if w == f.anchor {
					out[i] = fileRoute{w: w, sure: anchorSure}
				} else {
					out[i] = fileRoute{w: w, how: "folder"}
				}
				break
			}
		}
	}
	return out
}

// otherWorkNamer returns a check: does the file's own name (title fragments of its file
// name and of each directory, the leading bracket = release group skipped) name a work,
// exactly by a name in the name index, that is neither the target work, nor one of its
// family (fam, as loaded), nor an entry sharing a name with it (same franchiseBase), nor a
// piece of one of its own names? Such a file is not mapped by rule ("[GM-Team][时光代理人
// 第3季]" holding "[灵境行者][Spirit Realm Walker][08]" files). A file without a work name
// ("03.mkv") or naming the work by any of its names / aliases passes. Lookups only (no DB
// queries); results per directory are cached.
func (s *Store) otherWorkNamer(ctx context.Context, fam *family) func(path string, w *Work) bool {
	idx, err := s.aliasIndex(ctx)
	if err != nil || idx == nil {
		return func(string, *Work) bool { return false }
	}
	type target struct {
		keys  map[string]bool // keys counting as the same work
		names []string        // normName of its own names
	}
	related := map[string]*target{}
	ours := func(w *Work) *target {
		if m, ok := related[w.Key]; ok {
			return m
		}
		m := map[string]bool{w.Key: true}
		var own []string
		for _, f := range fam.works {
			m[f.w.Key] = true
		}
		names := append([]string{w.Title, w.TitleCN}, w.Aliases...)
		for _, n := range names {
			for _, k := range idx.nativeBase[franchiseBase(n)] {
				m[k] = true
			}
			own = append(own, normName(n))
		}
		// Imported names can identify this work, but cannot grant a franchise exemption
		// to another work (generic synonyms such as "Bleach" span different series).
		for _, n := range idx.extra[w.Key] {
			own = append(own, normName(n))
		}
		tg := &target{keys: m, names: own}
		related[w.Key] = tg
		return tg
	}
	type partKey struct{ part, work string }
	cache := map[partKey]bool{}
	names := func(part string, tg *target) bool {
		// the leading bracket is the release group ("[Tsukigakirei]" is also an alias of 月がきれい)
		if t := strings.TrimSpace(part); strings.HasPrefix(t, "[") || strings.HasPrefix(t, "【") {
			if i := strings.IndexAny(t, "]】"); i > 0 {
				_, sz := utf8.DecodeRuneInString(t[i:])
				part = t[i+sz:]
			}
		}
		for _, nf := range nameFragments(part) {
			ks := idx.norm[nf]
			if len(ks) == 0 {
				continue
			}
			mine := false
			for _, k := range ks {
				mine = mine || tg.keys[k]
			}
			for _, n := range tg.names {
				// a piece of the work's own name ("Dragon Ball Super" of "Dragon Ball
				// Super: Beerus", split at the colon)
				mine = mine || strings.Contains(n, nf)
			}
			if !mine {
				return true
			}
		}
		return false
	}
	return func(p string, w *Work) bool {
		tg := ours(w)
		parts := strings.Split(strings.ReplaceAll(p, "\\", "/"), "/")
		parts[len(parts)-1] = stem(parts[len(parts)-1])
		for _, part := range parts {
			if strings.TrimSpace(part) == "" {
				continue
			}
			ck := partKey{part, w.Key}
			v, ok := cache[ck]
			if !ok {
				v = names(part, tg)
				cache[ck] = v
			}
			if v {
				return true
			}
		}
		return false
	}
}

// nameFragments: the normName of the parts of a name that may be a whole work name —
// titleFragments' split (brackets, separators, " - ") without its word-token check (the
// tokenizer is most of the cost; a part that isn't a name just finds nothing in the index).
func nameFragments(name string) []string {
	const seps = "[]【】()「」『』{}〔〕〖〗/|:+;,、"
	var out []string
	for _, part := range strings.FieldsFunc(normWidth(name), func(r rune) bool { return strings.ContainsRune(seps, r) }) {
		for _, f := range strings.Split(part, " - ") {
			n := normName(strings.Trim(f, " \t._-~!?★☆\u200b"))
			c := utf8.RuneCountInString(n)
			if c > 80 || c < 3 || (!hasNonASCII(n) && c < 5) {
				continue
			}
			out = append(out, n)
		}
	}
	return out
}

// fileResult is the mapping of one file. queue: a video the rules could not map with
// certainty (left unmapped for the decision model); hint: the rules' guess (work key).
type fileResult struct {
	kind   string
	work   any // nil or work key
	ep     *float64
	conf   float64
	method string
	queue  bool
	hint   string
}

// mapFiles classifies the files of a torrent and assigns each one its work and episode;
// anchor is the torrent-level match (nil: none, files are classified only), anchorSure
// whether it is certain. A file is mapped only when everything is certain: the work (sure
// route), the number (ParsedName.Sure) and the episode key, which must be in the work's
// official episode list as written (see validKey; no split-cour shift). Everything else is
// left unmapped with queue=true.
func (s *Store) mapFiles(ctx context.Context, paths []string, title string, anchor *Work, method string, wconf float64, anchorSure bool) []fileResult {
	out := make([]fileResult, len(paths))
	if anchor == nil {
		for i, p := range ClassifyFiles(paths, title) {
			wk, ep, conf, m := fileMapping(nil, method, wconf, p.Kind, p)
			out[i] = fileResult{kind: p.Kind, work: wk, ep: ep, conf: conf, method: m, queue: IsVideo(paths[i]) && p.Kind != "extra"}
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
	routes := fam.route(paths, title, method, anchorSure)
	groups := make([]string, len(paths))
	for i, r := range routes {
		if r.w != nil {
			groups[i] = r.w.w.Key
		} else {
			groups[i] = "\x00unmapped"
		}
	}
	ps := classifyFilesIn(paths, title, groups)
	refineRouted(fam, paths, routes, ps)
	valid := s.keyValidator()
	other := s.otherWorkNamer(ctx, fam)
	for i, p := range ps {
		r := routes[i]
		video := IsVideo(paths[i])
		sure := r.sure && r.w != nil
		if sure && video && MappableKind(p.Kind) {
			k, ok := KeyOfFile(p.Kind, p.Episode)
			sure = p.Sure && ok && valid(ctx, r.w.w, k)
		}
		if sure && video && other(paths[i], r.w.w) {
			sure = false // the file's own name names another work (a mislabelled pack)
		}
		if !sure {
			res := fileResult{kind: p.Kind, queue: video && p.Kind != "extra"}
			if MappableKind(p.Kind) {
				res.ep = p.Episode
			}
			if r.w != nil {
				res.hint = r.w.w.Key
			}
			out[i] = res
			continue
		}
		m, conf := method, wconf
		if r.how == "type" {
			m += "+type"
		}
		wk, ep, c, mm := fileMapping(r.w.w, m, conf, p.Kind, p)
		out[i] = fileResult{kind: p.Kind, work: wk, ep: ep, conf: c, method: mm}
	}
	return out
}

var reFileDate = regexp.MustCompile(`((?:19|20)\d\d)[-./](0?[1-9]|1[0-2])[-./](0?[1-9]|[12]\d|3[01])`)

// refineRouted applies the per-work rules after classification: files routed by a
// movie/OVA keyword become main episodes of that work (bgm lists OVA / movie parts as
// type-0 episodes), and a movie's only unnumbered main video is episode 1 (sure when the
// movie has at most one official main episode).
//
// Since the decision model (clef.go) the rules no longer guess beyond this: unnumbered
// episodes are not matched by official episode title / air date any more, and groups routed
// away from the anchor are not mapped (the old plausibility check against the official
// episode count is gone with them).
func refineRouted(fam *family, paths []string, routes []fileRoute, ps []ParsedName) {
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
						ps[i].Episode, ps[i].Sure = nil, false
					}
					ps[i].Kind = "episode"
					ps[i].Confidence = max(ps[i].Confidence, 0.5)
				}
			}
		}
		if k == "movie" || k == "ova" {
			var mains []int
			for _, i := range idx {
				if ps[i].Kind == "episode" || ps[i].Kind == "other" {
					mains = append(mains, i)
				}
			}
			if len(mains) == 1 && ps[mains[0]].Episode == nil {
				ps[mains[0]] = ParsedName{Kind: "episode", Episode: ptrF(1), Confidence: 0.7, Season: ps[mains[0]].Season, Sure: w.mainN <= 1}
			}
		}
	}
}

// keyValidator returns validKey with a per-call cache of official episode lists.
func (s *Store) keyValidator() func(ctx context.Context, w *Work, k EpisodeKey) bool {
	cache := map[string]*officialSet{}
	return func(ctx context.Context, w *Work, k EpisodeKey) bool {
		set, ok := cache[w.Key]
		if !ok {
			set = s.officialSet(ctx, w.Key)
			cache[w.Key] = set
		}
		return set.valid(k, w.Eps)
	}
}

// officialSet is the official episode list of a work, for key validation.
type officialSet struct {
	keys  map[EpisodeKey]bool
	kinds map[string]int // listed episodes per key kind
}

func (s *Store) officialSet(ctx context.Context, workKey string) *officialSet {
	set := &officialSet{keys: map[EpisodeKey]bool{}, kinds: map[string]int{}}
	eps, _ := s.OfficialEpisodes(ctx, workKey)
	for _, e := range eps {
		set.keys[e.Key] = true
		set.kinds[e.Key.Kind]++
	}
	return set
}

// valid: key k (as stored, before any split-cour shift) is an episode of the work —
//   - the work lists episodes of k's kind (bgm Archive): k must be one of them;
//   - no main episodes listed: 0 < n ≤ the planned count (infobox 话数, eps > 0; recaps
//     like 12.5 included);
//   - no specials of k's kind listed (bgm rarely lists OP/ED/PV/CM, often no SP): accepted
//     (nothing to check against).
func (o *officialSet) valid(k EpisodeKey, eps int) bool {
	if k.Kind == KeyMain && o.kinds[k.Kind] > 0 {
		return o.keys[k]
	}
	if k.Kind == KeyMain {
		return eps > 0 && k.Number > 0 && k.Number <= float64(eps)
	}
	return true
}

// ValidEpisode checks a decision-model answer: the canonical key (split-cour shift
// applied, see CanonicalKey) must be an episode of the work (officialSet.valid).
func (s *Store) ValidEpisode(ctx context.Context, workKey string, k EpisodeKey) (EpisodeKey, bool) {
	w, err := s.GetWork(ctx, workKey)
	if err != nil {
		return k, false
	}
	c := s.CanonicalKey(ctx, workKey, k)
	return c, s.officialSet(ctx, workKey).valid(c, w.Eps)
}
