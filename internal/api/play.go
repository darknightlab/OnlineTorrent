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
	"github.com/darknightlab/onlinetorrent/internal/qb"
	"github.com/darknightlab/onlinetorrent/internal/store"
	"github.com/darknightlab/onlinetorrent/internal/swarm"
	"github.com/darknightlab/onlinetorrent/internal/torrentfile"
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
	// Status: ready (decided; magnet when allowed) | pending (no metadata yet: no magnet,
	// listen on GET /torrents/hash/{ih}/resolve/events — resolve.go)
	Status   string        `json:"status"`
	Access   policy.Access `json:"access"`
	Infohash string        `json:"infohash"`
	Name     string        `json:"name"`
	Magnet   string        `json:"magnet"`
	Webseeds []string      `json:"webseeds"` // always empty (edge WebSeeds arrive over SSE)
	Meta     *playMeta     `json:"meta"`
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
		if ct.Status != "ready" || len(ct.Files) == 0 {
			return s.playPending(w, r, ct.Infohash)
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
		// only the infohash of a magnet is used: its name (dn=) and trackers (tr=) are the
		// submitter's and never reach the policy, the catalogue or qB
		ct := s.knownTorrent(ctx, m.Infohash)
		if ct == nil {
			return s.playPending(w, r, m.Infohash)
		}
		t = catalog.FromCatalog(ct)
	default:
		return httpx.E(400, "bad_request", "需要 magnet、infohash 或 torrent_id")
	}

	d, err := s.playDecision(ctx, u, t.Subject())
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
	// the title is real metadata: a crawl source's, or the qB metadata name of a user torrent
	_ = s.St.UpsertTorrent(ctx, store.TorrentRecord{Infohash: t.Infohash, Title: t.Title, Size: t.SizeBytes, Files: t.FileList, Team: team, Tags: tags,
		Source: t.Source, SourceID: t.ID, Trusted: t.Trusted})
	s.St.AddPlay(ctx, store.Play{Infohash: t.Infohash, Title: t.Title, IP: httpx.ClientIP(r), Action: policy.Allow}, uid(u))

	resp := playResp{
		Status: "ready", Access: d.Access(), Infohash: t.Infohash, Name: t.Title,
		Magnet: magnet.Build(t.Infohash, t.Title, s.St.List(store.KeyTrackers)), Webseeds: []string{},
	}
	// metadata over HTTP (BEP 9 xs=): webtorrent fetches it at start (same origin, with the
	// login cookie), so the file list shows up even when no WebRTC peer is reachable
	if s.allowed(roleOf(u), torrentFileKey) && s.metainfoObtainable(ctx, t.Infohash) {
		resp.Magnet += "&xs=" + url.QueryEscape(s.siteBase(r)+"/api/v1/torrents/hash/"+t.Infohash+"/torrent")
	}
	if tm, err := s.Meta.GetTorrentMap(ctx, t.Infohash); err == nil {
		tm.HideWorks(s.scopeOf(r)) // never point the player at a subject outside the scope
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
			s.checkSeeded(ctx, gen, ih, name, files, by, true)
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
	// evaluated with the catalogue's real metadata (nothing is playable before it is known)
	sub, _, known := s.catalogSubject(r.Context(), ih)
	if !known {
		return nil, policy.Subject{}, httpx.E(404, "not_found", "请先通过 /play 打开该种子")
	}
	d, err := s.playDecision(r.Context(), userOf(r), sub)
	if err != nil {
		return nil, sub, err
	}
	if !d.Allowed {
		return nil, sub, denyError(d, userOf(r))
	}
	return rec, sub, nil
}

// seed asks every online qB server to download and seed ONE file of the torrent — the one
// being played (route permission "POST /api/v1/torrents/{ih}/seed", default: members).
// Body {"file": n} (the torrent's file index, BEP 47 padding files counted, as in WebTorrent;
// optional for single-file torrents). qB downloads only the requested files: a torrent new to
// a server is added with every other file skipped; on a server that already has it the file
// is added to its selection (files already wanted are never dropped). The size limit
// (play.max_push_size_mb) applies to the sum of the wanted files after adding this one.
func (s *Server) seed(w http.ResponseWriter, r *http.Request) error {
	if !s.limiter.allow("seed:"+ipKey(r), 20, 10*time.Second) {
		return errRateLimited
	}
	var in struct {
		File *int `json:"file"`
	}
	if r.ContentLength != 0 {
		if err := httpx.DecodeSmall(r, &in); err != nil && r.ContentLength > 0 {
			return err
		}
	}
	rec, sub, err := s.recordedSubject(r)
	if err != nil {
		return err
	}
	ctx := r.Context()
	files, torrent := s.seedFiles(ctx, rec.Infohash)
	if len(files) == 0 {
		return httpx.E(http.StatusConflict, "no_metadata", "服务器还没有该种子的文件列表")
	}
	idx := -1
	switch {
	case in.File != nil:
		idx = *in.File
	default:
		real := -1
		for i, f := range files {
			if !f.Pad {
				if real >= 0 {
					return httpx.E(400, "bad_request", "请指定要播放的文件（file）")
				}
				real = i
			}
		}
		idx = real
	}
	if idx < 0 || idx >= len(files) || files[idx].Pad {
		return httpx.E(400, "bad_request", "文件序号无效")
	}
	want := files[idx].Path
	if limit := s.maxPushBytes(); limit > 0 {
		if total := wantedSize(files, s.Pool.Wanted(ctx, rec.Infohash), want); total > limit {
			return httpx.E(http.StatusRequestEntityTooLarge, "too_large", fmt.Sprintf("要下载的文件过大（超过 %d MB），服务器不参与做种", limit>>20))
		}
	}
	timeout := time.Duration(s.St.Num(store.KeyQBAddTimeoutSec)) * time.Second
	if timeout <= 0 {
		timeout = 8 * time.Second
	}
	// wss + configured BT trackers + the torrent's own announce list (we don't rely on qB's
	// "automatically add trackers"); adding with a file selection takes a few more calls
	push := s.Pool.Seed(ctx, qb.SeedRequest{Infohash: rec.Infohash, Name: sub.Title, Torrent: torrent, Want: []string{want},
		Trackers: swarm.PushTrackers(ctx, s.St, s.Meta, rec.Infohash, nil), Timeout: 2 * timeout})
	if len(push.Errors) > 0 {
		s.Log.Warn("push errors", "ih", rec.Infohash, "errors", push.Errors)
	}
	u := userOf(r)
	s.St.AddPlay(ctx, store.Play{Infohash: rec.Infohash, Title: sub.Title, IP: httpx.ClientIP(r), Action: "seed", SeededBy: strings.Join(push.SeededBy, ",")}, uid(u))
	// once qB has the metadata: re-check user torrents with the real file list and ingest
	// + scrape every torrent the catalogue doesn't fully know yet (watch page polls
	// GET /torrents/hash/{ih})
	if len(push.SeededBy) > 0 {
		status, known := s.Meta.KnownTorrent(ctx, rec.Infohash)
		if !known || status != "ready" {
			s.markSeeded(rec.Infohash)
			s.startPostCheck(rec.Infohash, uid(u))
		}
	}
	// which servers took it is never revealed (admins see it in the play log)
	httpx.JSON(w, 200, map[string]any{"seeded": len(push.SeededBy) > 0})
	return nil
}

// seedFile is a torrent file as the player numbers them (padding files included).
type seedFile struct {
	Path string
	Size int64
	Pad  bool
}

// seedFiles returns the torrent's files from its .torrent (also returned, for adding it to qB
// with metadata at once); without one, from the catalogue (no .torrent → nil).
func (s *Server) seedFiles(ctx context.Context, ih string) ([]seedFile, []byte) {
	if s.Crawl != nil {
		cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		b, err := s.Crawl.Metainfo(cctx, ih)
		cancel()
		if err == nil {
			if mi, err := torrentfile.ParseTorrent(b); err == nil && mi.Infohash == ih {
				out := make([]seedFile, len(mi.Files))
				for i, f := range mi.Files {
					out[i] = seedFile{f.Path, f.Size, f.Pad}
				}
				return out, b
			}
		}
	}
	ct := s.knownTorrent(ctx, ih)
	if ct == nil {
		return nil, nil
	}
	n := 0
	for _, f := range ct.Files {
		n = max(n, f.Index+1)
	}
	out := make([]seedFile, n)
	for _, f := range ct.Files {
		out[f.Index] = seedFile{Path: f.Path, Size: f.Size}
	}
	for i := range out {
		if out[i].Path == "" {
			out[i].Pad = true // not in the catalogue: treat as unrequestable
		}
	}
	return out, nil
}

// wantedSize sums the sizes of the wanted paths plus extra (paths matched like qB names).
func wantedSize(files []seedFile, wanted map[string]bool, extra ...string) int64 {
	set := map[string]bool{}
	for p := range wanted {
		set[strings.Trim(p, "/")] = true
	}
	for _, p := range extra {
		set[strings.Trim(p, "/")] = true
	}
	var total int64
	for _, f := range files {
		if f.Pad {
			continue
		}
		p := strings.Trim(f.Path, "/")
		hit := set[p]
		if !hit {
			for w := range set { // qB without the root folder (content layout NoSubfolder)
				if strings.HasSuffix(p, "/"+w) || strings.HasSuffix(w, "/"+p) {
					hit = true
					break
				}
			}
		}
		if hit {
			total += f.Size
		}
	}
	return total
}
