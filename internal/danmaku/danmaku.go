// Package danmaku collects danmaku (bullet comments) for an episode on demand and keeps
// them permanently (meta.db, table danmaku).
//
// Sources: Bilibili and Bahamut only. The comments are fetched by an unmodified
// huangxd-/danmu_api sidecar (setting danmaku.api_url; empty = off) from a platform episode
// we choose ourselves — never by title search:
//
//	bangumi subject → bangumi-data site ids (bd_sites: bilibili* media id, gamer acgDetail id)
//	→ the platform's episode list (Bilibili season sections / Bahamut anime page)
//	→ our official episode number (mapEpisode: only when certain)
//	→ danmu_api (Bilibili: GET /api/v2/comment?url=…; Bahamut: POST /api/v2/segmentcomment)
//
// Rules (owner): fetch only when someone opens the episode; once an episode has stored
// comments it is never fetched again; an empty or failed fetch is never stored (retried
// later with backoff); concurrent requests for one episode share one fetch.
package danmaku

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"golang.org/x/sync/singleflight"

	"github.com/darknightlab/onlinetorrent/internal/meta"
	"github.com/darknightlab/onlinetorrent/internal/store"
)

// Platforms.
const (
	Bilibili = "bilibili"
	Bahamut  = "bahamut"
)

// site families in bangumi-data (preference order: the mainland Bilibili listing first)
var familySites = map[string][]string{
	Bilibili: {"bilibili", "bilibili_hk_mo_tw", "bilibili_tw", "bilibili_hk_mo"},
	Bahamut:  {"gamer", "gamer_hk"},
}

var platforms = []string{Bilibili, Bahamut}

// Tunables (vars for tests).
var (
	waitFor        = 40 * time.Second // a request waits this long for a running fetch, then returns what is stored
	fetchTimeout   = 2 * time.Minute  // one fetch (all platforms of an episode)
	listTTL        = time.Hour        // platform episode lists
	listErrTTL     = 10 * time.Minute // failed platform list lookups
	backoffBase    = 10 * time.Minute // failed fetches: 10 min, 20, 40, … (max backoffMax)
	backoffMax     = 12 * time.Hour
	uncertainTTL   = 6 * time.Hour // no certain platform episode (the listing may change, e.g. a new upload)
	suspectRetries = 2             // a Bilibili result that looks cut off is accepted after this many retries
)

const (
	browserUA = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0 Safari/537.36"
	bahamutUA = "Anime/2.29.2 (7N5749MM3F.tw.com.gamer.anime; build:972; iOS 26.0.0) Alamofire/5.6.4"
	maxText   = 200 // runes per comment
)

// Service fetches and stores danmaku.
type Service struct {
	Meta *meta.Store
	St   *store.Store // setting danmaku.api_url
	Log  *slog.Logger
	// HTTP is the client for the platform metadata (episode lists); default: no proxy.
	HTTP func() *http.Client
	// Sidecar is the client for danmu_api (internal network: never a proxy).
	Sidecar *http.Client
	// Platform API bases (tests point them at fakes).
	BiliAPI, BahaAPI, BahaACG string
	// APIURL overrides the setting (tests).
	APIURL string
	Now    func() time.Time

	once  sync.Once
	group singleflight.Group
	sem   chan struct{} // concurrent sidecar fetches

	mu    sync.Mutex
	fails map[string]*failState // work|episode → backoff
	lists map[string]*listEntry // platform|ext id → episode list
}

type failState struct {
	attempts int
	next     time.Time
	reason   string
}

type listEntry struct {
	l   *listing
	err error
	exp time.Time
}

// listing is a platform's episode list for one site id.
type listing struct {
	Season string // Bilibili season id (in the play URL), "" for Bahamut
	Eps    []platEp
}

type platEp struct {
	Num float64 // the episode number the platform shows
	Ref string  // Bilibili ep id / Bahamut video sn
}

func (s *Service) init() {
	s.once.Do(func() {
		s.sem = make(chan struct{}, 2)
		s.fails = map[string]*failState{}
		s.lists = map[string]*listEntry{}
		if s.Now == nil {
			s.Now = time.Now
		}
		if s.Log == nil {
			s.Log = slog.New(slog.DiscardHandler)
		}
		if s.BiliAPI == "" {
			s.BiliAPI = "https://api.bilibili.com"
		}
		if s.BahaAPI == "" {
			s.BahaAPI = "https://api.gamer.com.tw"
		}
		if s.BahaACG == "" {
			s.BahaACG = "https://acg.gamer.com.tw"
		}
		if s.Sidecar == nil {
			tr := http.DefaultTransport.(*http.Transport).Clone()
			tr.Proxy = nil
			s.Sidecar = &http.Client{Transport: tr, Timeout: 90 * time.Second}
		}
		if s.HTTP == nil {
			c := &http.Client{Timeout: 30 * time.Second}
			s.HTTP = func() *http.Client { return c }
		}
	})
}

func (s *Service) apiURL() string {
	if s.APIURL != "" {
		return strings.TrimRight(s.APIURL, "/")
	}
	if s.St == nil {
		return ""
	}
	return strings.TrimRight(strings.TrimSpace(s.St.Str(store.KeyDanmakuAPIURL)), "/")
}

// Get returns the danmaku of an episode (official main episodes only; empty when none is
// available). Stored comments are returned as they are. Otherwise — danmu_api configured,
// the episode not in backoff and mayFetch() allowing it (rate limit; only consulted when a
// new fetch would start) — the comments are fetched now; the call waits up to waitFor for
// the result (the fetch continues in the background with base).
func (s *Service) Get(ctx, base context.Context, w *meta.Work, key meta.EpisodeKey, mayFetch func() bool) ([]meta.Comment, error) {
	s.init()
	if key.Kind != meta.KeyMain {
		return nil, nil
	}
	sets, err := s.Meta.Danmaku(ctx, w.Key, key)
	if err != nil || len(sets) > 0 {
		return merge(sets), err
	}
	if s.apiURL() == "" {
		return nil, nil
	}
	fk := w.Key + "|" + key.String()
	if s.inBackoff(fk) {
		return nil, nil
	}
	ch := s.group.DoChan(fk, func() (any, error) {
		// the first caller pays for the fetch (rate limit); callers joining a running fetch don't
		if mayFetch != nil && !mayFetch() {
			return nil, errRateLimited
		}
		fctx, cancel := context.WithTimeout(base, fetchTimeout)
		defer cancel()
		return nil, s.collect(fctx, w, key, fk)
	})
	t := time.NewTimer(waitFor)
	defer t.Stop()
	select {
	case r := <-ch:
		if r.Err != nil && !errors.Is(r.Err, errRateLimited) && !errors.Is(r.Err, errUnavailable) {
			s.Log.Info("danmaku: fetch failed", "work", w.Key, "episode", key.String(), "err", r.Err)
		}
	case <-t.C:
		return nil, nil
	case <-ctx.Done():
		return nil, nil
	}
	sets, err = s.Meta.Danmaku(ctx, w.Key, key)
	return merge(sets), err
}

var (
	errRateLimited = errors.New("danmaku: rate limited")
	errUnavailable = errors.New("danmaku: no certain platform episode")
)

func (s *Service) inBackoff(fk string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	f := s.fails[fk]
	return f != nil && s.Now().Before(f.next)
}

func (s *Service) attempts(fk string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	if f := s.fails[fk]; f != nil {
		return f.attempts
	}
	return 0
}

// fail records a failed collection: retried after the backoff (uncertain mappings: a fixed
// delay). The map is pruned of expired entries when it grows.
func (s *Service) fail(fk, reason string, uncertain bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.Now()
	if len(s.fails) > 10000 {
		for k, f := range s.fails {
			if now.After(f.next) {
				delete(s.fails, k)
			}
		}
	}
	f := s.fails[fk]
	if f == nil {
		f = &failState{}
		s.fails[fk] = f
	}
	f.attempts++
	f.reason = reason
	d := uncertainTTL
	if !uncertain {
		d = time.Duration(float64(backoffBase) * math.Pow(2, float64(min(f.attempts-1, 10))))
		d = min(d, backoffMax)
	}
	f.next = now.Add(d)
}

func (s *Service) succeed(fk string) {
	s.mu.Lock()
	delete(s.fails, fk)
	s.mu.Unlock()
}

// target is a platform episode to fetch.
type target struct {
	Platform string
	Ref      string // ep id / video sn
	Season   string // Bilibili season id
}

// collect resolves the platform episodes and fetches them (all concurrently); every
// non-empty result is stored. Nothing stored → backoff.
func (s *Service) collect(ctx context.Context, w *meta.Work, key meta.EpisodeKey, fk string) error {
	if has, err := s.Meta.HasDanmaku(ctx, w.Key, key); err != nil || has {
		return err // stored meanwhile (another process / a finished flight)
	}
	targets, terr := s.Targets(ctx, w, key)
	if len(targets) == 0 {
		if terr != nil {
			s.fail(fk, terr.Error(), false)
			return terr
		}
		s.fail(fk, "no certain platform episode", true)
		return errUnavailable
	}
	select {
	case s.sem <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	defer func() { <-s.sem }()
	retries := s.attempts(fk)
	type result struct {
		t   target
		c   []meta.Comment
		err error
	}
	res := make([]result, len(targets))
	var wg sync.WaitGroup
	for i, t := range targets {
		wg.Go(func() {
			c, err := s.fetch(ctx, t, retries)
			res[i] = result{t, c, err}
		})
	}
	wg.Wait()
	stored := 0
	var errs []string
	for _, r := range res {
		if r.err != nil {
			errs = append(errs, r.t.Platform+": "+r.err.Error())
			continue
		}
		if _, err := s.Meta.SaveDanmaku(ctx, w.Key, key, r.t.Platform, r.t.Ref, r.c); err != nil {
			errs = append(errs, r.t.Platform+": "+err.Error())
			continue
		}
		stored++
		s.Log.Info("danmaku: stored", "work", w.Key, "episode", key.String(), "platform", r.t.Platform, "ref", r.t.Ref, "count", len(r.c))
	}
	if stored == 0 {
		msg := strings.Join(errs, "; ")
		s.fail(fk, msg, false)
		return errors.New(msg)
	}
	s.succeed(fk)
	return nil
}

// Targets maps an episode to platform episodes (only certain ones; see mapEpisode). An
// error is returned only when no target was found and a platform lookup failed.
func (s *Service) Targets(ctx context.Context, w *meta.Work, key meta.EpisodeKey) ([]target, error) {
	s.init()
	bgm := meta.BgmID(w)
	if bgm == 0 || key.Kind != meta.KeyMain {
		return nil, nil
	}
	sites, err := s.Meta.SiteIDs(ctx, bgm)
	if err != nil || len(sites) == 0 {
		return nil, err
	}
	ours, err := s.Meta.OfficialMainSorts(ctx, w.Key)
	if err != nil || len(ours) == 0 {
		return nil, err
	}
	var out []target
	var lookupErr error
	for _, p := range platforms {
		exts := extIDs(sites, p)
		for _, ext := range exts {
			shared, ok, err := s.sharedSorts(ctx, p, ext, bgm)
			if err != nil {
				return nil, err
			}
			if !ok {
				continue
			}
			l, err := s.listing(ctx, p, ext)
			if err != nil {
				lookupErr = err
				continue
			}
			// several listings for one subject (regional versions, dubs — or one listing per
			// cour, each numbered from 1): only a listing with every one of our episodes counts
			if len(exts) > 1 && !complete(ours, l.Eps) {
				continue
			}
			ref, ok := mapEpisode(ours, shared, l.Eps, key.Number)
			if !ok {
				continue
			}
			out = append(out, target{Platform: p, Ref: ref, Season: l.Season})
			break
		}
	}
	if len(out) == 0 {
		return nil, lookupErr
	}
	return out, nil
}

// complete: the platform lists every one of our episode numbers.
func complete(ours []float64, eps []platEp) bool {
	have := map[float64]bool{}
	for _, e := range eps {
		have[e.Num] = true
	}
	for _, n := range ours {
		if !have[n] {
			return false
		}
	}
	return true
}

// extIDs: the distinct site ids of a platform family, preferred site first.
func extIDs(sites []meta.SiteID, platform string) []string {
	var out []string
	for _, fs := range familySites[platform] {
		for _, x := range sites {
			if x.Site == fs && !contains(out, x.ExtID) {
				out = append(out, x.ExtID)
			}
		}
	}
	return out
}

func contains(l []string, s string) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}

// sharedSorts: the official main-episode numbers of the other bangumi subjects listed under
// the same platform id. ok=false when one of them has no official episode list (the shared
// listing can't be split with certainty).
func (s *Service) sharedSorts(ctx context.Context, platform, ext string, self int64) ([][]float64, bool, error) {
	ids, err := s.Meta.SubjectsWithSite(ctx, familySites[platform], ext)
	if err != nil {
		return nil, false, err
	}
	var out [][]float64
	for _, id := range ids {
		if id == self {
			continue
		}
		sorts, err := s.Meta.OfficialMainSorts(ctx, "bgm:"+strconv.FormatInt(id, 10))
		if err != nil {
			return nil, false, err
		}
		if len(sorts) == 0 {
			return nil, false, nil
		}
		out = append(out, sorts)
	}
	return out, true, nil
}

// mapEpisode finds the platform episode of our episode `want` (an official main-episode
// number of the work: ours). Certain only when the platform's numbering is one of:
//
//   - direct: every platform number is one of ours (the platform may list fewer, e.g. while
//     airing). When other subjects share the listing (shared), their official numbers must
//     not overlap ours or each other, and every platform number must belong to one of them;
//   - per season: our numbers (with those of the subjects sharing the listing) continue a
//     previous season (m … m+n−1, m > 1, integers, no gaps), the platform counts from 1
//     (1 … k, k ≤ n): platform episode i is number m+i−1.
//
// Platform numbers must be unique; a fractional platform number we don't have (a 12.5
// recap) is ignored. Anything else (an integer platform number we don't have, e.g. a recap
// counted as an episode, or another season on the same page) → not certain.
func mapEpisode(ours []float64, shared [][]float64, eps []platEp, want float64) (string, bool) {
	if len(eps) == 0 {
		return "", false
	}
	mine := map[float64]bool{}
	for _, n := range ours {
		mine[n] = true
	}
	if !mine[want] {
		return "", false
	}
	union := map[float64]bool{}
	for n := range mine {
		union[n] = true
	}
	for _, other := range shared {
		for _, n := range other {
			if union[n] {
				return "", false // overlapping numbering between the subjects of one listing
			}
		}
		for _, n := range other {
			union[n] = true
		}
	}
	plat := map[float64]string{}
	for _, e := range eps {
		if e.Num != math.Trunc(e.Num) && !union[e.Num] {
			continue // a platform-only recap (12.5) doesn't shift the integer numbering
		}
		if _, dup := plat[e.Num]; dup {
			return "", false
		}
		plat[e.Num] = e.Ref
	}
	if len(plat) == 0 {
		return "", false
	}
	direct := true
	for n := range plat {
		if !union[n] {
			direct = false
			break
		}
	}
	if direct {
		ref, ok := plat[want]
		return ref, ok
	}
	// per-season numbering: over all subjects of the listing (in order), e.g. both cours of
	// Re:Zero season 4 (bangumi 67–77 and 78–…) on one Bahamut page numbered 1–19
	sorted := make([]float64, 0, len(union))
	for n := range union {
		sorted = append(sorted, n)
	}
	sort.Float64s(sorted)
	m := sorted[0]
	if m <= 1 || m != math.Trunc(m) {
		return "", false
	}
	for i, n := range sorted {
		if n != m+float64(i) {
			return "", false
		}
	}
	if len(plat) > len(sorted) {
		return "", false
	}
	for i := 1; i <= len(plat); i++ {
		if _, ok := plat[float64(i)]; !ok {
			return "", false
		}
	}
	ref, ok := plat[want-m+1]
	return ref, ok
}

// ---- platform episode lists ----

func (s *Service) listing(ctx context.Context, platform, ext string) (*listing, error) {
	k := platform + "|" + ext
	now := s.Now()
	s.mu.Lock()
	if e := s.lists[k]; e != nil && now.Before(e.exp) {
		s.mu.Unlock()
		return e.l, e.err
	}
	s.mu.Unlock()
	var l *listing
	var err error
	switch platform {
	case Bilibili:
		l, err = s.bilibiliList(ctx, ext)
	case Bahamut:
		l, err = s.bahamutList(ctx, ext)
	default:
		err = fmt.Errorf("unknown platform %q", platform)
	}
	if ctx.Err() != nil {
		return nil, err // not cached: the caller gave up
	}
	ttl := listTTL
	if err != nil {
		ttl = listErrTTL
	}
	s.mu.Lock()
	if len(s.lists) > 2000 {
		clear(s.lists)
	}
	s.lists[k] = &listEntry{l: l, err: err, exp: now.Add(ttl)}
	s.mu.Unlock()
	return l, err
}

func (s *Service) getJSON(ctx context.Context, u, ua, referer string, out any) error {
	b, err := s.get(ctx, u, ua, referer)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(b, out); err != nil {
		return fmt.Errorf("%s: %w", redact(u), err)
	}
	return nil
}

func (s *Service) get(ctx context.Context, u, ua, referer string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", ua)
	if referer != "" {
		req.Header.Set("Referer", referer)
	}
	resp, err := s.HTTP().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("%s: HTTP %d", redact(u), resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 8<<20))
}

// redact drops the query (no secrets in logs; ids stay visible in the path).
func redact(u string) string {
	if i := strings.IndexByte(u, '?'); i >= 0 {
		return u[:i]
	}
	return u
}

var reNumber = regexp.MustCompile(`^\d{1,4}(?:\.\d{1,2})?$`)

// bilibiliList: media id (bangumi-data) → season id → the main section's episodes
// (pgc/web/season/section: also answers for Hong Kong / Macau / Taiwan-only seasons outside
// those regions, unlike pgc/view/web/season). Titles that aren't plain numbers (预告, SP, …)
// are skipped.
func (s *Service) bilibiliList(ctx context.Context, mediaID string) (*listing, error) {
	var rv struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Result  struct {
			Media struct {
				SeasonID int64 `json:"season_id"`
			} `json:"media"`
		} `json:"result"`
	}
	if err := s.getJSON(ctx, s.BiliAPI+"/pgc/review/user?media_id="+url.QueryEscape(mediaID), browserUA, "https://www.bilibili.com/", &rv); err != nil {
		return nil, err
	}
	if rv.Code != 0 || rv.Result.Media.SeasonID == 0 {
		return nil, fmt.Errorf("bilibili media %s: code %d %s", mediaID, rv.Code, rv.Message)
	}
	season := strconv.FormatInt(rv.Result.Media.SeasonID, 10)
	var sv struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Result  struct {
			Main struct {
				Episodes []struct {
					ID    int64  `json:"id"`
					Title string `json:"title"`
				} `json:"episodes"`
			} `json:"main_section"`
		} `json:"result"`
	}
	if err := s.getJSON(ctx, s.BiliAPI+"/pgc/web/season/section?season_id="+season, browserUA, "https://www.bilibili.com/", &sv); err != nil {
		return nil, err
	}
	if sv.Code != 0 {
		return nil, fmt.Errorf("bilibili season %s: code %d %s", season, sv.Code, sv.Message)
	}
	l := &listing{Season: season}
	for _, e := range sv.Result.Main.Episodes {
		t := strings.TrimSpace(e.Title)
		if e.ID == 0 || !reNumber.MatchString(t) {
			continue
		}
		n, _ := strconv.ParseFloat(t, 64)
		l.Eps = append(l.Eps, platEp{Num: n, Ref: strconv.FormatInt(e.ID, 10)})
	}
	return l, nil
}

var (
	reBahaVideo = regexp.MustCompile(`ani\.gamer\.com\.tw/animeVideo\.php\?sn=(\d+)"[^>]*>([^<]*)<`)
	reBahaList  = regexp.MustCompile(`(?s)動畫瘋線上看.*?<ul>(.*?)</ul>`)
)

// bahamutList: acgDetail id (bangumi-data) → its 動畫瘋 episodes. The acgDetail page lists the
// first 12 videos ("更多" after that); the full list comes from api video.php, accepted only
// when the anime belongs to this acgDetail id (acgSn). Only the main section ("0") is used;
// Bahamut numbers later seasons across seasons (S2 starts at 29), as bangumi usually does.
// Taiwan-only titles: video.php refuses other regions (the danmaku still works), so the
// page's own list is used — only when it is complete (no "更多").
func (s *Service) bahamutList(ctx context.Context, acg string) (*listing, error) {
	page, err := s.get(ctx, s.BahaACG+"/acgDetail.php?s="+url.QueryEscape(acg), browserUA, "")
	if err != nil {
		return nil, err
	}
	block := reBahaList.FindSubmatch(page)
	if block == nil {
		return &listing{}, nil // not on 動畫瘋
	}
	type link struct{ sn, label string }
	var links []link
	for _, m := range reBahaVideo.FindAllSubmatch(block[1], -1) {
		links = append(links, link{string(m[1]), strings.TrimSpace(string(m[2]))})
	}
	restricted := false
	for i, l := range links {
		if i == 3 {
			break
		}
		var v struct {
			Error *struct {
				Code int `json:"code"`
			} `json:"error"`
			Data struct {
				Anime struct {
					AcgSn    int64 `json:"acgSn"`
					Episodes map[string][]struct {
						Episode float64 `json:"episode"`
						VideoSn int64   `json:"videoSn"`
					} `json:"episodes"`
				} `json:"anime"`
			} `json:"data"`
		}
		if err := s.getJSON(ctx, s.BahaAPI+"/anime/v1/video.php?videoSn="+l.sn, bahamutUA, "", &v); err != nil {
			return nil, err
		}
		if v.Error != nil {
			restricted = restricted || v.Error.Code == 1011 // not available in this region
			continue
		}
		a := v.Data.Anime
		if strconv.FormatInt(a.AcgSn, 10) != acg {
			continue
		}
		out := &listing{}
		for _, e := range a.Episodes["0"] {
			if e.VideoSn > 0 && e.Episode >= 0 {
				out.Eps = append(out.Eps, platEp{Num: e.Episode, Ref: strconv.FormatInt(e.VideoSn, 10)})
			}
		}
		return out, nil
	}
	if !restricted {
		return &listing{}, nil
	}
	out := &listing{}
	for _, l := range links {
		if !reNumber.MatchString(l.label) {
			return &listing{}, nil // "更多": the page shows only part of the list
		}
		n, _ := strconv.ParseFloat(l.label, 64)
		out.Eps = append(out.Eps, platEp{Num: n, Ref: l.sn})
	}
	return out, nil
}

// ---- danmu_api ----

type apiComment struct {
	P string `json:"p"`
	M string `json:"m"`
}

type apiResponse struct {
	Success       *bool        `json:"success"`
	ErrorMessage  string       `json:"errorMessage"`
	Count         int          `json:"count"`
	Comments      []apiComment `json:"comments"`
	VideoDuration float64      `json:"videoDuration"`
}

// fetch gets one platform episode's comments from danmu_api. Empty = error. retries is the
// number of failed collections of this episode so far (a cut-off-looking Bilibili result
// is accepted only after suspectRetries).
func (s *Service) fetch(ctx context.Context, t target, retries int) ([]meta.Comment, error) {
	base := s.apiURL()
	if base == "" {
		return nil, errors.New("danmu_api not configured")
	}
	var req *http.Request
	var err error
	switch t.Platform {
	case Bilibili:
		play := "https://www.bilibili.com/bangumi/play/ep" + t.Ref
		if t.Season != "" {
			play += "?season_id=" + t.Season
		}
		q := url.Values{"url": {play}, "format": {"json"}, "duration": {"true"}}
		req, err = http.NewRequestWithContext(ctx, http.MethodGet, base+"/api/v2/comment?"+q.Encode(), nil)
	case Bahamut:
		body, _ := json.Marshal(map[string]any{"type": "bahamut", "segment_start": 0, "segment_end": 30000, "url": t.Ref})
		req, err = http.NewRequestWithContext(ctx, http.MethodPost, base+"/api/v2/segmentcomment?format=json", bytes.NewReader(body))
		if req != nil {
			req.Header.Set("Content-Type", "application/json")
		}
	default:
		return nil, fmt.Errorf("unknown platform %q", t.Platform)
	}
	if err != nil {
		return nil, err
	}
	resp, err := s.Sidecar.Do(req)
	if err != nil {
		return nil, errors.New("danmu_api request failed") // the URL holds the access token: not in the error
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("danmu_api: HTTP %d", resp.StatusCode)
	}
	var r apiResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<20)).Decode(&r); err != nil {
		return nil, fmt.Errorf("danmu_api: %w", err)
	}
	if r.Success != nil && !*r.Success {
		return nil, fmt.Errorf("danmu_api: %s", r.ErrorMessage)
	}
	c := convert(r.Comments)
	if len(c) == 0 {
		return nil, errors.New("no comments")
	}
	if t.Platform == Bilibili && truncated(c, r.VideoDuration) && retries < suspectRetries {
		// danmu_api stops at the first failed 6-minute segment (and returns what it has)
		return nil, fmt.Errorf("looks cut off (last comment %.0fs of %.0fs)", c[len(c)-1].Time, r.VideoDuration)
	}
	return c, nil
}

// truncated: no comment in the last 7 minutes of the video (a whole 6-minute segment missing).
func truncated(c []meta.Comment, duration float64) bool {
	return duration > 420 && len(c) > 0 && c[len(c)-1].Time < duration-420
}

// convert parses danmu_api's JSON ("p": "time,mode,colour,[source]"), keeping plain text
// comments (bilibili modes 1–6; 2/3/6 scroll variants → 1). Sorted by time.
func convert(in []apiComment) []meta.Comment {
	out := make([]meta.Comment, 0, len(in))
	for _, x := range in {
		f := strings.Split(x.P, ",")
		if len(f) < 3 {
			continue
		}
		t, err1 := strconv.ParseFloat(f[0], 64)
		mode, err2 := strconv.Atoi(strings.TrimSpace(f[1]))
		color, err3 := strconv.ParseInt(strings.TrimSpace(f[2]), 10, 64)
		if err1 != nil || err2 != nil || err3 != nil || t < 0 || math.IsNaN(t) || math.IsInf(t, 0) {
			continue
		}
		switch mode {
		case 1, 2, 3, 6:
			mode = 1
		case 4, 5:
		default:
			continue // 7 positioned / 8 code / 9 BAS: not plain text
		}
		text := strings.TrimSpace(x.M)
		if text == "" {
			continue
		}
		if utf8.RuneCountInString(text) > maxText {
			text = string([]rune(text)[:maxText])
		}
		out = append(out, meta.Comment{Time: math.Round(t*100) / 100, Mode: mode, Color: int(color & 0xFFFFFF), Text: text})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Time < out[j].Time })
	return out
}

// merge joins the stored sets of an episode, sorted by time.
func merge(sets []meta.DanmakuSet) []meta.Comment {
	n := 0
	for _, s := range sets {
		n += len(s.Comments)
	}
	out := make([]meta.Comment, 0, n)
	for _, s := range sets {
		out = append(out, s.Comments...)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Time < out[j].Time })
	return out
}
