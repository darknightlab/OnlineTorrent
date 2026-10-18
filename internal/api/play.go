package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/darknightlab/onlinetorrent/internal/catalog"
	"github.com/darknightlab/onlinetorrent/internal/httpx"
	"github.com/darknightlab/onlinetorrent/internal/magnet"
	"github.com/darknightlab/onlinetorrent/internal/meta"
	"github.com/darknightlab/onlinetorrent/internal/policy"
	"github.com/darknightlab/onlinetorrent/internal/store"
)

type playReq struct {
	Magnet    string `json:"magnet"`
	Infohash  string `json:"infohash"`
	TorrentID string `json:"torrent_id"`
	File      int    // ?file=N: the file about to be played (-1 = not given)
}

// fileParam reads the optional ?file= index (-1 if absent or invalid).
func fileParam(r *http.Request) int {
	if n, err := strconv.Atoi(r.URL.Query().Get("file")); err == nil && n >= 0 {
		return n
	}
	return -1
}

type playMeta struct {
	SubjectID  int64    `json:"subject_id"`
	SubjectKey string   `json:"subject_key"`
	BgmID      string   `json:"bgm_id,omitempty"`
	Title      string   `json:"title"`
	Episode    *float64 `json:"episode"`       // main episodes only (null for specials)
	Key        string   `json:"episode_key"`   // "12", "12.5", "sp1", "ncop1"…
	Label      string   `json:"episode_label"` // "第 12 话", "SP1", "NCOP1"…
	FileIndex  int      `json:"file_index"`    // the mapped file (watch/hash/{ih}/file/{file_index})
}

type playResp struct {
	Access   policy.Access `json:"access"`
	Infohash string        `json:"infohash"`
	Name     string        `json:"name"`
	Magnet   string        `json:"magnet"`
	// kept for compatibility: /play no longer pushes; see POST /torrents/{ih}/seed
	SeededBy []string  `json:"seeded_by"`
	Webseeds []string  `json:"webseeds"`
	Meta     *playMeta `json:"meta"`
}

func denyError(d policy.Decision, u *store.User) error {
	switch d.Action {
	case policy.Login:
		return httpx.E(401, "login_required", d.Reason)
	case policy.VIP:
		if u == nil {
			return httpx.E(401, "login_required", "该资源仅限大会员观看，请先登录")
		}
		return httpx.E(402, "vip_required", d.Reason)
	default:
		return httpx.E(451, "blocked", d.Reason)
	}
}

var reTorrentID = regexp.MustCompile(`^[A-Za-z0-9]{1,64}$`)

func (s *Server) maxPushBytes() int64 {
	return int64(s.St.Num(store.KeyMaxPushSize)) << 20
}

// POST /api/v1/torrents/hash/{ih}/play
func (s *Server) playHash(w http.ResponseWriter, r *http.Request) error {
	return s.play(w, r, playReq{Infohash: r.PathValue("ih"), File: fileParam(r)})
}

// POST /api/v1/torrents/moe/{id}/play — bangumi.moe catalogue id (trusted metadata)
func (s *Server) playMoe(w http.ResponseWriter, r *http.Request) error {
	return s.play(w, r, playReq{TorrentID: r.PathValue("id"), File: fileParam(r)})
}

func (s *Server) play(w http.ResponseWriter, r *http.Request, in playReq) error {
	if !s.limiter.allow("play:"+ipKey(r), 30, 6*time.Second) {
		return errRateLimited
	}
	ctx := r.Context()
	u := userOf(r)

	var t *catalog.Torrent
	userTitle := "" // dn= from a user magnet: untrusted, never stored as the title
	switch {
	case in.TorrentID != "":
		if !reTorrentID.MatchString(in.TorrentID) {
			return httpx.E(400, "bad_request", "torrent_id 无效")
		}
		// local catalogue only (filled by the bangumi.moe crawl source)
		ct, err := s.Meta.CatalogBySourceID(ctx, store.SourceKindBangumiMoe, in.TorrentID)
		if err != nil {
			return httpx.E(404, "not_found", "找不到该种子")
		}
		t = catalog.FromCatalog(ct)
	case in.Magnet != "" || in.Infohash != "":
		src := in.Magnet
		if src == "" {
			src = in.Infohash
		}
		m, err := magnet.Parse(src)
		if err != nil {
			return httpx.Bad(err)
		}
		// user-supplied trackers (tr=) are dropped entirely: we only announce to our own wss
		// trackers, so qB can't be pointed at arbitrary hosts.
		userTitle = store.Trunc(m.Name, store.MaxTitleBytes)
		// a crawled torrent: title, files and trust come from the catalogue
		if ct, err := s.Meta.CatalogByHash(ctx, m.Infohash); err == nil && ct.SourceKind != "user" && ct.Source != "user" {
			t = catalog.FromCatalog(ct)
			userTitle = ""
			break
		}
		// until qB has the real metadata the dn is all we know; the subject stays untrusted
		// so the user-magnet floor applies (see policy package doc)
		t = &catalog.Torrent{Infohash: m.Infohash, Title: userTitle, Source: "user"}
		if rec, err := s.St.GetTorrent(ctx, m.Infohash); err == nil && rec != nil {
			if rec.Title != "" {
				t.Title = rec.Title
			}
			t.FileList, t.Source, t.ID, t.SizeBytes, t.Trusted = rec.Files, rec.Source, rec.SourceID, rec.Size, rec.Trusted
			if rec.Team != "" {
				t.Team = &catalog.Ref{Name: rec.Team}
			}
			for _, g := range rec.Tags {
				t.Tags = append(t.Tags, catalog.Ref{Name: g})
			}
		}
	default:
		return httpx.E(400, "bad_request", "需要 magnet、infohash 或 torrent_id")
	}

	d, err := s.Policy.Decide(ctx, u, t.Subject())
	if err != nil {
		return err
	}
	if !d.Allowed {
		s.St.AddPlay(ctx, store.Play{Infohash: t.Infohash, Title: t.Title, IP: httpx.ClientIP(r), Action: d.Action}, uid(u))
		return denyError(d, u)
	}
	var tags []string
	for _, g := range t.Tags {
		tags = append(tags, g.Name)
	}
	team := ""
	if t.Team != nil {
		team = t.Team.Name
	}
	rec := store.TorrentRecord{Infohash: t.Infohash, Size: t.SizeBytes, Files: t.FileList, Team: team, Tags: tags, Source: t.Source, SourceID: t.ID, Trusted: t.Trusted}
	if t.Source != "user" {
		rec.Title = t.Title // catalogue title (crawl source)
	} else {
		rec.UserTitle = userTitle
	}
	_ = s.St.UpsertTorrent(ctx, rec)
	s.St.AddPlay(ctx, store.Play{Infohash: t.Infohash, Title: t.Title, IP: httpx.ClientIP(r), Action: policy.Allow}, uid(u))

	resp := playResp{
		Access: d.Access(), Infohash: t.Infohash, Name: t.Title,
		Magnet: magnet.Build(t.Infohash, t.Title, s.St.List(store.KeyTrackers)), SeededBy: []string{}, Webseeds: []string{},
	}
	// metadata over HTTP (BEP 9 xs=): webtorrent fetches it at start (same origin, with the
	// login cookie), so the file list shows up even when no WebRTC peer is reachable
	if s.allowed(roleOf(u), torrentFileKey) && s.metainfoObtainable(ctx, t.Infohash) {
		resp.Magnet += "&xs=" + url.QueryEscape(s.siteBase(r)+"/api/v1/torrents/hash/"+t.Infohash+"/torrent")
	}
	if tm, err := s.Meta.GetTorrentMap(ctx, t.Infohash); err == nil {
		resp.Meta = playMetaOf(tm, in.File)
	}
	httpx.JSON(w, 200, resp)
	return nil
}

// playMetaOf describes the mapped file being played: file (when given and mapped), else the
// first mapped main episode of the torrent, else its first mapped special (a torrent of only
// NCOP/NCED, SP, …). Subject and episode always come from that file's own mapping — a
// torrent can hold several subjects (multi-season batches, TV + movie).
func playMetaOf(tm *meta.TorrentMap, file int) *playMeta {
	var pick *meta.FileMap
	for i := range tm.Files {
		f := &tm.Files[i]
		if f.Work == nil || f.EpisodeKey == "" {
			continue
		}
		if f.Index == file {
			pick = f
			break
		}
		if pick == nil || (pick.Kind != "episode" && f.Kind == "episode") {
			pick = f
			if f.Kind == "episode" && file < 0 {
				break
			}
		}
	}
	if pick == nil {
		return nil
	}
	title := pick.Work.TitleCN
	if title == "" {
		title = pick.Work.Title
	}
	key, _ := meta.ParseEpisodeKey(pick.EpisodeKey)
	m := &playMeta{SubjectID: pick.Work.ID, SubjectKey: pick.Work.Key, BgmID: pick.Work.External["bgm"], Title: title, Key: key.String(), Label: key.Label(), FileIndex: pick.Index}
	if key.Kind == meta.KeyMain {
		m.Episode = pick.Episode
	}
	return m
}

// markSeeded remembers a seed request (GET /torrents/hash/{ih} reports "pending").
func (s *Server) markSeeded(ih string) {
	now := time.Now()
	s.seededAt.Range(func(k, v any) bool {
		if now.Sub(v.(time.Time)) > seedPendingFor {
			s.seededAt.Delete(k)
		}
		return true
	})
	s.seededAt.Store(ih, now)
}

// startPostCheck runs postCheck in the background, at most once per infohash at a time and
// at most 4 concurrently (beyond that the periodic re-check picks the torrent up).
func (s *Server) startPostCheck(ih string, by *int64) {
	if _, busy := s.postChecks.LoadOrStore(ih, struct{}{}); busy {
		return
	}
	select {
	case s.postSem <- struct{}{}:
	default:
		s.postChecks.Delete(ih)
		return
	}
	s.bg.Go(func() {
		defer func() {
			<-s.postSem
			s.postChecks.Delete(ih)
		}()
		s.postCheck(ih, by)
	})
}

// postCheck waits for metadata from qB, then evaluates the real name and files.
func (s *Server) postCheck(ih string, by *int64) {
	ctx, cancel := context.WithTimeout(s.BaseCtx, 3*time.Minute)
	defer cancel()
	gen := s.Policy.Generation()
	for i := 0; i < 36; i++ {
		select {
		case <-ctx.Done():
			return
		case <-time.After(5 * time.Second):
		}
		if name, files, ok := s.Pool.Metadata(ctx, ih); ok {
			s.checkSeeded(ctx, gen, ih, name, files, 0, by, true)
			return
		}
	}
}

func (s *Server) report(w http.ResponseWriter, r *http.Request) error {
	if err := s.rateLimit(r, "report"); err != nil {
		return err
	}
	var in struct {
		Reason string `json:"reason"`
	}
	if err := httpx.DecodeSmall(r, &in); err != nil {
		return err
	}
	ih, ok := magnet.NormalizeInfohash(r.PathValue("ih"))
	if !ok {
		return httpx.E(400, "bad_request", "infohash 无效")
	}
	reason := strings.TrimSpace(in.Reason)
	if len([]rune(reason)) > 500 {
		reason = string([]rune(reason)[:500])
	}
	// the title comes from our own records only, never from the client
	title := ""
	if rec, err := s.St.GetTorrent(r.Context(), ih); err == nil && rec != nil {
		title = rec.Title
		if title == "" && rec.UserTitle != "" {
			title = "（用户填写）" + rec.UserTitle
		}
	}
	if err := s.St.AddReview(r.Context(), ih, title, "report", reason, uid(userOf(r))); err != nil && !errors.Is(err, store.ErrNotFound) {
		return err
	}
	httpx.JSON(w, 200, map[string]any{})
	return nil
}

// recordedSubject loads a torrent previously resolved by /play and re-evaluates policy for
// the current user. Seed/webseed endpoints therefore never act on unknown torrents.
func (s *Server) recordedSubject(r *http.Request) (*store.TorrentRecord, policy.Subject, error) {
	ih, ok := magnet.NormalizeInfohash(r.PathValue("ih"))
	if !ok {
		return nil, policy.Subject{}, httpx.E(400, "bad_request", "infohash 无效")
	}
	rec, err := s.St.GetTorrent(r.Context(), ih)
	if err != nil {
		return nil, policy.Subject{}, err
	}
	if rec == nil {
		return nil, policy.Subject{}, httpx.E(404, "not_found", "请先通过 /play 打开该种子")
	}
	title := rec.Title
	if title == "" {
		title = rec.UserTitle
	}
	sub := policy.Subject{Infohash: ih, Title: title, Files: rec.Files, Team: rec.Team, Tags: rec.Tags, Trusted: rec.Trusted}
	d, err := s.Policy.Decide(r.Context(), userOf(r), sub)
	if err != nil {
		return nil, sub, err
	}
	if !d.Allowed {
		return nil, sub, denyError(d, userOf(r))
	}
	return rec, sub, nil
}

// seed asks every online qB server to download and seed the torrent (route permission
// "POST /api/v1/torrents/{ih}/seed", default: members).
func (s *Server) seed(w http.ResponseWriter, r *http.Request) error {
	if !s.limiter.allow("seed:"+ipKey(r), 20, 10*time.Second) {
		return errRateLimited
	}
	rec, sub, err := s.recordedSubject(r)
	if err != nil {
		return err
	}
	if limit := s.maxPushBytes(); limit > 0 && rec.Size > limit {
		return httpx.E(http.StatusRequestEntityTooLarge, "too_large", fmt.Sprintf("该种子过大（超过 %d MB），服务器不参与做种", limit>>20))
	}
	timeout := time.Duration(s.St.Num(store.KeyQBAddTimeoutSec)) * time.Second
	if timeout <= 0 {
		timeout = 8 * time.Second
	}
	push := s.Pool.Push(r.Context(), rec.Infohash, sub.Title, s.St.List(store.KeyTrackers), timeout)
	if len(push.Errors) > 0 {
		s.Log.Warn("push errors", "ih", rec.Infohash, "errors", push.Errors)
	}
	u := userOf(r)
	s.St.AddPlay(r.Context(), store.Play{Infohash: rec.Infohash, Title: sub.Title, IP: httpx.ClientIP(r), Action: "seed", SeededBy: strings.Join(push.SeededBy, ",")}, uid(u))
	// once qB has the metadata: re-check user torrents with the real file list and ingest
	// + scrape every torrent the catalogue doesn't fully know yet (watch page polls
	// GET /torrents/hash/{ih})
	if len(push.SeededBy) > 0 {
		status, known := s.Meta.KnownTorrent(r.Context(), rec.Infohash)
		if (!rec.Trusted && len(rec.Files) == 0) || !known || status != "ready" {
			s.markSeeded(rec.Infohash)
			s.startPostCheck(rec.Infohash, uid(u))
		}
	}
	httpx.JSON(w, 200, map[string]any{"seeded_by": push.SeededBy})
	return nil
}
