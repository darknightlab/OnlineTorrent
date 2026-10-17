package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/darknightlab/onlinetorrent/internal/catalog"
	"github.com/darknightlab/onlinetorrent/internal/httpx"
	"github.com/darknightlab/onlinetorrent/internal/magnet"
	"github.com/darknightlab/onlinetorrent/internal/policy"
	"github.com/darknightlab/onlinetorrent/internal/store"
)

type playReq struct {
	Magnet    string `json:"magnet"`
	Infohash  string `json:"infohash"`
	TorrentID string `json:"torrent_id"`
}

type playMeta struct {
	AnimeKey string   `json:"anime_key"`
	Title    string   `json:"title"`
	Episode  *float64 `json:"episode"`
}

type playResp struct {
	Access   policy.Access `json:"access"`
	Infohash string        `json:"infohash"`
	Name     string        `json:"name"`
	Magnet   string        `json:"magnet"`
	SeededBy []string      `json:"seeded_by"`
	Webseeds []string      `json:"webseeds"`
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

func (s *Server) play(w http.ResponseWriter, r *http.Request) error {
	if !s.limiter.allow("play:"+ipKey(r), 30, 6*time.Second) {
		return errRateLimited
	}
	var in playReq
	if err := httpx.DecodeSmall(r, &in); err != nil {
		return err
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
		var err error
		t, err = s.Cat.Detail(ctx, in.TorrentID)
		if err != nil {
			return httpx.E(404, "not_found", "找不到该种子")
		}
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
		// until qB has the real metadata the dn is all we know; the subject stays untrusted
		// so the user-magnet floor applies (see policy package doc)
		t = &catalog.Torrent{Infohash: m.Infohash, Title: userTitle, Source: "user"}
		if rec, err := s.St.GetTorrent(ctx, m.Infohash); err == nil && rec != nil {
			if rec.Title != "" {
				t.Title = rec.Title
			}
			t.FileList, t.Source, t.ID, t.SizeBytes = rec.Files, rec.Source, rec.SourceID, rec.Size
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
	if limit := s.maxPushBytes(); limit > 0 && t.SizeBytes > limit {
		return httpx.E(http.StatusRequestEntityTooLarge, "too_large", fmt.Sprintf("该种子过大（超过 %d MB），暂不支持在线播放", limit>>20))
	}

	var tags []string
	for _, g := range t.Tags {
		tags = append(tags, g.Name)
	}
	team := ""
	if t.Team != nil {
		team = t.Team.Name
	}
	rec := store.TorrentRecord{Infohash: t.Infohash, Size: t.SizeBytes, Files: t.FileList, Team: team, Tags: tags, Source: t.Source, SourceID: t.ID}
	if t.Source == "bangumi.moe" {
		rec.Title = t.Title
	} else {
		rec.UserTitle = userTitle
	}
	_ = s.St.UpsertTorrent(ctx, rec)

	trackers := s.St.List(store.KeyTrackers)
	timeout := time.Duration(s.St.Num(store.KeyQBAddTimeoutSec)) * time.Second
	if timeout <= 0 {
		timeout = 8 * time.Second
	}
	push := s.Pool.Push(ctx, t.Infohash, t.Title, trackers, timeout)
	s.St.AddPlay(ctx, store.Play{Infohash: t.Infohash, Title: t.Title, IP: httpx.ClientIP(r), Action: policy.Allow, SeededBy: strings.Join(push.SeededBy, ",")}, uid(u))
	if len(push.Errors) > 0 {
		s.Log.Warn("push errors", "ih", t.Infohash, "errors", push.Errors)
	}
	// user-submitted torrents: re-check once the file list is known
	if t.Source != "bangumi.moe" && len(t.FileList) == 0 && len(push.SeededBy) > 0 {
		s.startPostCheck(t.Infohash, uid(u))
	}

	resp := playResp{
		Access: d.Access(), Infohash: t.Infohash, Name: t.Title,
		Magnet: magnet.Build(t.Infohash, t.Title, trackers), SeededBy: push.SeededBy, Webseeds: []string{},
	}
	if tm, err := s.Meta.GetTorrentMap(ctx, t.Infohash); err == nil {
		for _, f := range tm.Files {
			if f.Work != nil && f.Kind == "episode" {
				title := f.Work.TitleCN
				if title == "" {
					title = f.Work.Title
				}
				resp.Meta = &playMeta{AnimeKey: f.Work.Key, Title: title, Episode: f.Episode}
				break
			}
		}
	}
	httpx.JSON(w, 200, resp)
	return nil
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
		Infohash string `json:"infohash"`
		Reason   string `json:"reason"`
	}
	if err := httpx.DecodeSmall(r, &in); err != nil {
		return err
	}
	ih, ok := magnet.NormalizeInfohash(in.Infohash)
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
